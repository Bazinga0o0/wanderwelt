package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

func writeAcc(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMinecraftName(t *testing.T) {
	two := `{"activeAccountLocalId":"b","accounts":{
	  "a":{"accessToken":"GEHEIM","minecraftProfile":{"id":"u-a","name":"Alpha"}},
	  "b":{"accessToken":"GEHEIM","minecraftProfile":{"id":"u-b","name":"Beta"}}}}`
	only := `{"accounts":{"x":{"minecraftProfile":{"id":"u-x","name":"Solo_1"}},"y":{}}}`
	ambiguous := `{"accounts":{"a":{"minecraftProfile":{"id":"1","name":"Alpha"}},"b":{"minecraftProfile":{"id":"2","name":"Beta"}}}}`
	cases := []struct {
		name, file, content string
		want, uuid          string
		ok                  bool
	}{
		{"aktives Konto", "launcher_accounts.json", two, "Beta", "u-b", true},
		{"Store-Datei", "launcher_accounts_microsoft_store.json", two, "Beta", "u-b", true},
		{"einziges Profil", "launcher_accounts.json", only, "Solo_1", "u-x", true},
		{"mehrdeutig", "launcher_accounts.json", ambiguous, "", "", false},
		{"Datei fehlt", "", "", "", "", false},
		{"kaputtes JSON", "launcher_accounts.json", "{kaputt", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.file != "" {
				writeAcc(t, dir, c.file, c.content)
			}
			name, uuid, ok := detectMinecraftName(dir)
			if name != c.want || uuid != c.uuid || ok != c.ok {
				t.Fatalf("got %q %q %v", name, uuid, ok)
			}
		})
	}
	// Kaputte Store-Datei: die andere Datei zählt.
	dir := t.TempDir()
	writeAcc(t, dir, "launcher_accounts_microsoft_store.json", "{")
	writeAcc(t, dir, "launcher_accounts.json", two)
	if n, _, ok := detectMinecraftName(dir); !ok || n != "Beta" {
		t.Fatalf("Rückfall: %q %v", n, ok)
	}
	// Keine Token-Felder in den eingelesenen Typen.
	var check func(reflect.Type)
	check = func(ty reflect.Type) {
		for ty.Kind() == reflect.Map || ty.Kind() == reflect.Ptr || ty.Kind() == reflect.Slice {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			if s := strings.ToLower(f.Name + f.Tag.Get("json")); strings.Contains(s, "token") {
				t.Errorf("Token-Feld %s", f.Name)
			}
			check(f.Type)
		}
	}
	check(reflect.TypeOf(launcherAccounts{}))
}

type nameCoordinator struct {
	mu     sync.Mutex
	posts  []string
	status []int // Antworten der Reihe nach (letzte wiederholt)
	name   string
}

func (n *nameCoordinator) handler(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch r.URL.Path {
	case "/v1/me":
		json.NewEncoder(w).Encode(api.Me{Email: "a@example.org", MinecraftName: n.name})
	case "/v1/me/minecraft":
		var req api.MinecraftNameRequest
		json.NewDecoder(r.Body).Decode(&req)
		n.posts = append(n.posts, req.MinecraftName)
		st := n.status[min(len(n.posts), len(n.status))-1]
		if st == 200 {
			n.name = req.MinecraftName
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(st)
		json.NewEncoder(w).Encode(map[string]string{"error": "Mojang kennt „" + req.MinecraftName + "“ nicht"})
	default:
		http.NotFound(w, r)
	}
}

func TestAskNameFlow(t *testing.T) {
	run := func(t *testing.T, status []int, detected bool, ja bool, entries ...string) (*nameCoordinator, int, int) {
		t.Helper()
		mc := t.TempDir()
		t.Setenv("WANDERWELT_MINECRAFT_DIR", mc)
		if detected {
			writeAcc(t, mc, "launcher_accounts.json", `{"activeAccountLocalId":"a","accounts":{"a":{"minecraftProfile":{"id":"u","name":"Erkannt_1"}}}}`)
		}
		nc := &nameCoordinator{status: status}
		ts := httptest.NewServer(http.HandlerFunc(nc.handler))
		t.Cleanup(ts.Close)
		a := busyApp(t, client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "a@example.org"}))
		p := probeDialogs(t, a)
		oldC, oldE := nameConfirm, nameEntry
		t.Cleanup(func() { nameConfirm, nameEntry = oldC, oldE })
		confirms, asks := 0, 0
		nameConfirm = func(string) bool { confirms++; return ja }
		nameEntry = func(string) (string, bool) {
			if asks >= len(entries) {
				return "", false
			}
			asks++
			return entries[asks-1], true
		}
		_ = p
		a.askMinecraftName()
		return nc, confirms, asks
	}
	t.Run("erkannt und Ja", func(t *testing.T) {
		nc, confirms, asks := run(t, []int{200}, true, true)
		if len(nc.posts) != 1 || nc.posts[0] != "Erkannt_1" || confirms != 1 || asks != 0 {
			t.Fatalf("posts %v confirms %d asks %d", nc.posts, confirms, asks)
		}
	})
	t.Run("409 ohne Dialogschleife", func(t *testing.T) {
		nc, _, asks := run(t, []int{409}, true, true)
		if len(nc.posts) != 1 || asks != 0 {
			t.Fatalf("posts %v asks %d", nc.posts, asks)
		}
	})
	t.Run("400, dann gültiger Name", func(t *testing.T) {
		nc, _, asks := run(t, []int{400, 200}, false, false, "Falsch_x", "Richtig_2")
		if len(nc.posts) != 2 || nc.posts[1] != "Richtig_2" || asks != 2 {
			t.Fatalf("posts %v asks %d", nc.posts, asks)
		}
	})
}
