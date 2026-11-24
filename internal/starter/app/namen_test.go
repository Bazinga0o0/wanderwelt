package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Fremde E-Mail-Adressen stehen weder in Hinweisen noch im Log noch im
// Tooltip-Text, wenn das Mitglied keinen Minecraft-Namen hat.
func TestKeineFremdeEmailInHinweisen(t *testing.T) {
	const fremd = "bob@example.org"
	notes := countNotify(t)
	got := fakeHostRun(t)
	a := takeoverApp(t)
	var buf syncBuffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	a.takeOver(&api.HandoverOffer{HandoverID: 7, LeaseID: 41, WorldID: "w-1", HeadGeneration: 12, Port: 15566,
		From: api.HostInfo{Email: fremd}, MinecraftAddress: "mc.example.org"})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("keine Host-Sitzung gestartet")
	}
	waitFor(t, "Hinweis", func() bool { return len(notes()) > 0 })
	waitFor(t, "Aktion frei", func() bool { return busyOf(a) == "" })
	a.endHosting("Test")
	waitFor(t, "Sitzung zu", func() bool { return !a.isHosting() && strings.Contains(strings.Join(notes(), "|"), "Hosten beendet") })

	alles := strings.Join(notes(), "|") + "\n" + buf.String()
	if strings.Contains(alles, fremd) {
		t.Errorf("fremde E-Mail in Hinweisen oder Log:\n%s", alles)
	}
	if !strings.Contains(alles, "Du übernimmst das Hosten von dem bisherigen Host") {
		t.Errorf("kein Ersatztext:\n%s", alles)
	}
	h := &api.HostInfo{Email: fremd}
	if s := hostAnzeige(h); strings.Contains(s, "@") || !strings.Contains(s, "ohne Minecraft-Namen") {
		t.Errorf("hostAnzeige = %q", s)
	}
	if s := fensterapi.UebergabeText(&api.HandoverState{State: api.HandoverReady, To: api.HostInfo{Email: fremd}}); strings.Contains(s, "@") {
		t.Errorf("UebergabeText = %q", s)
	}
}

func TestExplainDeutsch(t *testing.T) {
	for _, err := range []error{
		context.DeadlineExceeded,
		&client.StatusError{StatusCode: 500, Method: "GET", Message: "boom"},
	} {
		s := explain(err)
		if strings.Contains(s, "deadline") || strings.Contains(s, "GET") || strings.Contains(s, "Internal") {
			t.Errorf("explain(%v) = %q", err, s)
		}
	}
	if got := mitPunkt(""); got != "." {
		t.Errorf("mitPunkt = %q", got)
	}
	if got := mitKomma(""); got != "" {
		t.Errorf("mitKomma = %q", got)
	}
}

// memberNameCoordinator beantwortet /v1/me und merkt sich Namensänderungen.
type memberNameCoordinator struct {
	mu    sync.Mutex
	calls []string
	name  string
}

func (m *memberNameCoordinator) handler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(api.Me{Email: "a@example.org", MinecraftName: m.name})
		case r.URL.Path == "/v1/me/minecraft":
			var req api.MinecraftNameRequest
			json.NewDecoder(r.Body).Decode(&req)
			m.calls = append(m.calls, "POST "+req.MinecraftName)
			if status != 200 {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]string{"error": "kennt Mojang nicht"})
				return
			}
			m.name = req.MinecraftName
		case strings.HasPrefix(r.URL.Path, "/v1/members/") && strings.HasSuffix(r.URL.Path, "/minecraft"):
			var req api.MinecraftNameRequest
			json.NewDecoder(r.Body).Decode(&req)
			m.calls = append(m.calls, r.Method+" "+r.URL.Path+" "+req.MinecraftName)
			if status != 200 {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]string{"error": "Mojang kennt den Namen nicht"})
			}
		default:
			http.NotFound(w, r)
		}
	}
}

func (m *memberNameCoordinator) gesehen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

type dialoge struct{ confirms, asks int }

func fakeNameDialogs(t *testing.T, ja bool, entries ...string) *dialoge {
	t.Helper()
	d := &dialoge{}
	oldC, oldE := nameConfirm, nameEntry
	t.Cleanup(func() { nameConfirm, nameEntry = oldC, oldE })
	nameConfirm = func(string) bool { d.confirms++; return ja }
	nameEntry = func(string) (string, bool) {
		if d.asks >= len(entries) {
			return "", false
		}
		d.asks++
		return entries[d.asks-1], true
	}
	return d
}

func nameClient(url string) *client.Client {
	return client.New(&group.Group{Coordinator: url, DevIdentityEmail: "a@example.org"})
}

// Die Einrichtung fragt nach dem Namen (askNameFor, von install aufgerufen)
// und sagt dem Tray, dass er beim selben Start nicht noch einmal fragt.
func TestInstallAsksForName(t *testing.T) {
	mc := t.TempDir()
	t.Setenv("WANDERWELT_MINECRAFT_DIR", mc)
	writeAcc(t, mc, "launcher_accounts.json", `{"activeAccountLocalId":"a","accounts":{"a":{"minecraftProfile":{"id":"u","name":"Erkannt_1"}}}}`)
	t.Run("erkannt, Nein, dann Eingabe mit Zeichenprüfung", func(t *testing.T) {
		m := &memberNameCoordinator{}
		ts := httptest.NewServer(m.handler(200))
		defer ts.Close()
		a := busyApp(t, nil)
		probeDialogs(t, a)
		d := fakeNameDialogs(t, false, "ab", "Zu Lang!!", "Richtig_2")
		me := a.askNameFor(t.Context(), nameClient(ts.URL))
		if me == nil || me.MinecraftName != "Richtig_2" {
			t.Fatalf("Me danach: %+v", me)
		}
		if g := m.gesehen(); d.confirms != 1 || d.asks != 3 || len(g) != 1 || g[0] != "POST Richtig_2" {
			t.Fatalf("confirms %d asks %d posts %v", d.confirms, d.asks, g)
		}
	})
	t.Run("drei Fehlversuche, dann Schluss", func(t *testing.T) {
		m := &memberNameCoordinator{}
		ts := httptest.NewServer(m.handler(400))
		defer ts.Close()
		a := busyApp(t, nil)
		p := probeDialogs(t, a)
		d := fakeNameDialogs(t, false, "Falsch_a", "Falsch_b", "Falsch_c", "Falsch_d")
		if me := a.askNameFor(t.Context(), nameClient(ts.URL)); me != nil {
			t.Fatalf("Me = %+v", me)
		}
		if d.asks != nameTries {
			t.Fatalf("asks %d", d.asks)
		}
		p.check(t, "ließ sich nicht eintragen")
	})
	t.Run("Tray fragt danach nicht noch einmal", func(t *testing.T) {
		m := &memberNameCoordinator{}
		ts := httptest.NewServer(m.handler(200))
		defer ts.Close()
		a := busyApp(t, nameClient(ts.URL))
		probeDialogs(t, a)
		d := fakeNameDialogs(t, true)
		a.nameAsked.Store(true)
		a.askMinecraftName()
		if d.confirms != 0 || d.asks != 0 || len(m.gesehen()) != 0 {
			t.Fatalf("Tray fragte trotz Einrichtung: %+v %v", d, m.gesehen())
		}
		a.askMinecraftName() // der nächste Start fragt wieder
		if d.confirms != 1 {
			t.Fatalf("zweiter Lauf: %+v", d)
		}
	})
}

// Beim Start fragt wanderwelt, wenn /v1/me keinen Namen liefert, und sonst nicht.
func TestStartAsksIfNameMissing(t *testing.T) {
	mc := t.TempDir()
	t.Setenv("WANDERWELT_MINECRAFT_DIR", mc)
	writeAcc(t, mc, "launcher_accounts.json", `{"activeAccountLocalId":"a","accounts":{"a":{"minecraftProfile":{"id":"u","name":"Erkannt_1"}}}}`)
	for _, c := range []struct {
		name  string
		hat   string
		fragt int
	}{{"leer", "", 1}, {"gesetzt", "Schon_da", 0}} {
		t.Run(c.name, func(t *testing.T) {
			m := &memberNameCoordinator{name: c.hat}
			ts := httptest.NewServer(m.handler(200))
			defer ts.Close()
			a := busyApp(t, nameClient(ts.URL))
			probeDialogs(t, a)
			d := fakeNameDialogs(t, true)
			a.askMinecraftName()
			if d.confirms != c.fragt {
				t.Fatalf("Fragen: %d, erwartet %d", d.confirms, c.fragt)
			}
			if c.fragt == 1 && (len(m.gesehen()) != 1 || m.gesehen()[0] != "POST Erkannt_1") {
				t.Fatalf("Aufrufe: %v", m.gesehen())
			}
		})
	}
}

// Admin ändert den Namen eines Mitglieds über die lokale Steuerung.
func TestFensterMcNameAendern(t *testing.T) {
	probeAnzeige(t)
	m := &memberNameCoordinator{}
	ts := httptest.NewServer(m.handler(200))
	defer ts.Close()
	ft := startFenster(t, client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"}))
	ctx := t.Context()
	body := map[string]any{"email": "bob@example.org", "name": "Bob_42"}

	ft.a.mu.Lock()
	ft.a.me = &api.Me{Email: "admin@example.org"}
	ft.a.mu.Unlock()
	if err := ft.fc.Aktion(ctx, "mcname", body); err == nil || !strings.Contains(err.Error(), "nur Admins") {
		t.Fatalf("Nicht-Admin: %v", err)
	}
	ft.a.mu.Lock()
	ft.a.me.Admin = true
	ft.a.mu.Unlock()
	if err := ft.fc.Aktion(ctx, "mcname", map[string]any{"email": "bob@example.org", "name": "x y"}); err == nil || !strings.Contains(err.Error(), "3 bis 16") {
		t.Fatalf("ungültiger Name: %v", err)
	}
	if len(m.gesehen()) != 0 {
		t.Fatalf("Koordinator trotz Fehler gefragt: %v", m.gesehen())
	}
	if err := ft.fc.Aktion(ctx, "mcname", body); err != nil {
		t.Fatal(err)
	}
	if g := m.gesehen(); len(g) != 1 || g[0] != "PUT /v1/members/bob@example.org/minecraft Bob_42" {
		t.Fatalf("Aufrufe: %q", g)
	}
	// Der Koordinator lehnt ab: sein Text kommt im Fenster an.
	m2 := &memberNameCoordinator{}
	ts2 := httptest.NewServer(m2.handler(400))
	defer ts2.Close()
	ft.a.mu.Lock()
	ft.a.c = client.New(&group.Group{Coordinator: ts2.URL, DevIdentityEmail: "admin@example.org"})
	ft.a.mu.Unlock()
	if err := ft.fc.Aktion(ctx, "mcname", body); err == nil || err.Error() != "Mojang kennt den Namen nicht" {
		t.Fatalf("Koordinator-Fehler: %v", err)
	}
}
