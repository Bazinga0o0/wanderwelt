package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func (e *restoreEnv) hostChat(as who) *api.ChatSettings {
	e.t.Helper()
	var hs api.HostSettings
	if code := call(e.t, e.base, as, http.MethodGet, "/v1/host/settings", nil, &hs); code != http.StatusOK {
		e.t.Fatalf("Host-Einstellungen: %d", code)
	}
	return hs.Chat
}

// Chat-Verlauf je Welt: Standard (an, 5 MiB) ohne neue JSON-Felder, nur Admins
// ändern, Werte werden geprüft, der Host bekommt die Einstellung der Welt, die er
// hostet.
func TestWeltChatEinstellung(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)

	// Standard: Die Felder fehlen im JSON (ältere Starter sehen nichts Neues),
	// der Host bekommt "an, 5 MiB".
	code, raw := e.raw(userB, http.MethodGet, "/v1/worlds", nil)
	if code != http.StatusOK || strings.Contains(string(raw), "chat_") {
		t.Fatalf("Weltliste enthält Chat-Felder im Standard: %d %s", code, raw)
	}
	l := e.host(userA, "pc-a")
	if c := e.hostChat(userA); c == nil || c.Off || c.MiB != 5 {
		t.Fatalf("Standard für den Host: %+v", c)
	}
	e.release(userA, "pc-a", l)

	off, mib := true, 12
	// Mitglieder und Fremde dürfen nicht ändern; nichts ändert sich.
	for _, u := range []who{userB, stranger} {
		if code := call(t, e.base, u, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{ChatOff: &off, ChatMiB: &mib}, nil); code != http.StatusForbidden {
			t.Fatalf("%s ändert Chat-Einstellung: %d", u.email, code)
		}
	}
	if w := e.worlds(userB).Worlds[0]; w.ChatOff || w.ChatMiB != 0 {
		t.Fatalf("abgelehnte Änderung wirkt: %+v", w)
	}

	// Admin ändert; Mitglieder lesen es.
	var w api.GroupWorld
	if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{ChatOff: &off, ChatMiB: &mib}, &w); code != http.StatusOK || !w.ChatOff || w.ChatMiB != 12 {
		t.Fatalf("Admin ändert Chat: %d %+v", code, w)
	}
	if got := e.worlds(userB).Worlds[0]; !got.ChatOff || got.ChatMiB != 12 {
		t.Fatalf("Mitglied sieht: %+v", got)
	}

	// Eine Änderung nur am Namen lässt den Chat unberührt.
	name := "Survival"
	if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{Name: &name}, nil); code != http.StatusOK {
		t.Fatalf("Umbenennen: %d", code)
	}
	if got := e.worlds(userB).Worlds[0]; !got.ChatOff || got.ChatMiB != 12 || got.Name != "Survival" {
		t.Fatalf("Umbenennen änderte den Chat: %+v", got)
	}

	// Ungültige Größen: 400, nichts ändert sich. 0 = Standard ist erlaubt.
	for _, bad := range []int{-1, 51, 100, 1 << 20} {
		bad := bad
		if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{ChatMiB: &bad}, nil); code != http.StatusBadRequest {
			t.Fatalf("Größe %d: %d", bad, code)
		}
	}
	for _, ok := range []int{1, 50} {
		ok := ok
		if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{ChatMiB: &ok}, nil); code != http.StatusOK {
			t.Fatalf("Größe %d: %d", ok, code)
		}
	}
	if got := e.worlds(userB).Worlds[0]; got.ChatMiB != 50 || !got.ChatOff {
		t.Fatalf("nach Grenzwerten: %+v", got)
	}

	// Der Host bekommt die Einstellung der Welt, die er hostet.
	l = e.host(userA, "pc-a")
	if c := e.hostChat(userA); c == nil || !c.Off || c.MiB != 50 {
		t.Fatalf("Host-Einstellung: %+v", c)
	}
	e.release(userA, "pc-a", l)

	// Zurück auf Standard: an, 0 = Standardgröße (Felder verschwinden wieder).
	on, zero := false, 0
	if code := call(t, e.base, userA, http.MethodPatch, "/v1/worlds/w1", api.WorldPatch{ChatOff: &on, ChatMiB: &zero}, nil); code != http.StatusOK {
		t.Fatalf("Zurücksetzen: %d", code)
	}
	if _, raw := e.raw(userB, http.MethodGet, "/v1/worlds", nil); strings.Contains(string(raw), "chat_") {
		t.Fatalf("Chat-Felder nach dem Zurücksetzen: %s", raw)
	}
}

// Beim Anlegen kann die Chat-Einstellung gleich mitkommen; jede Welt hat ihre
// eigene, und der Host bekommt die der Welt des Zuschlags.
func TestWeltChatJeWelt(t *testing.T) {
	e := newRestoreEnv(t)
	e.seed(1)
	code, w2 := e.addWorld(userA, api.WorldCreate{Name: "Creative", ChatOff: true, ChatMiB: 3})
	if code != http.StatusCreated || !w2.ChatOff || w2.ChatMiB != 3 {
		t.Fatalf("Anlegen: %d %+v", code, w2)
	}
	if code, _ := e.addWorld(userA, api.WorldCreate{Name: "Zu groß", ChatMiB: 51}); code != http.StatusBadRequest {
		t.Fatalf("Anlegen mit Größe 51: %d", code)
	}
	if code, _ := e.addWorld(userB, api.WorldCreate{Name: "Heimlich", ChatOff: true}); code != http.StatusForbidden {
		t.Fatalf("Mitglied legt Welt mit Chat an: %d", code)
	}
	if w := e.worlds(userA).Worlds[0]; w.ChatOff || w.ChatMiB != 0 {
		t.Fatalf("erste Welt: %+v", w)
	}

	// Erste Welt hosten: Standard. (Die zweite ist leer; nur Admins hosten sie.)
	l := e.host(userA, "pc-a")
	if c := e.hostChat(userA); c == nil || c.Off || c.MiB != 5 {
		t.Fatalf("Welt 1: %+v", c)
	}
	e.release(userA, "pc-a", l)

	r := e.playWorld(userA, "pc-a", w2.ID)
	if r.status != http.StatusOK || r.resp.Role != api.RoleHost {
		t.Fatalf("Welt 2 hosten: %d %+v", r.status, r.resp)
	}
	if c := e.hostChat(userA); c == nil || !c.Off || c.MiB != 3 {
		t.Fatalf("Welt 2: %+v", c)
	}
}

// Abwärtskompatibilität des JSON: ältere Koordinatoren schicken keine Chat-Felder,
// neue Typen lesen das als Standard (an, 5 MiB); ein HostSettings ohne "chat"
// bleibt nil (der Host fasst die Konfig-Datei dann nicht an).
func TestChatFelderAbwaertskompatibel(t *testing.T) {
	var l api.WorldList
	if err := json.Unmarshal([]byte(`{"worlds":[{"id":"w1","name":"Welt","head_generation":3}]}`), &l); err != nil {
		t.Fatal(err)
	}
	if w := l.Worlds[0]; w.ChatOff || api.ChatMiBOrDefault(w.ChatMiB) != 5 {
		t.Fatalf("Welt ohne Chat-Felder: %+v", w)
	}
	var hs api.HostSettings
	if err := json.Unmarshal([]byte(`{"properties":{},"whitelist":[],"ops":[]}`), &hs); err != nil || hs.Chat != nil {
		t.Fatalf("HostSettings ohne chat: %v %+v", err, hs.Chat)
	}
	// Neue Felder werden nur gesendet, wenn sie vom Standard abweichen.
	b, _ := json.Marshal(api.GroupWorld{ID: "w1", Name: "Welt"})
	if strings.Contains(string(b), "chat") {
		t.Fatalf("Standard-Welt als JSON: %s", b)
	}
}
