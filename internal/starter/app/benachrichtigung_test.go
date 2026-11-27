package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// statusKoordinator liefert einen von Hand gestellten Status und /v1/me.
type statusKoordinator struct {
	mu     sync.Mutex
	status *api.Status // nil: Fehler 500
	me     api.Me
}

func (k *statusKoordinator) handler(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	defer k.mu.Unlock()
	switch r.URL.Path {
	case "/v1/status":
		if k.status == nil {
			http.Error(w, `{"error":"kaputt"}`, http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(k.status)
	case "/v1/me":
		json.NewEncoder(w).Encode(k.me)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (k *statusKoordinator) setze(st *api.Status) { k.mu.Lock(); k.status = st; k.mu.Unlock() }

func benachApp(t *testing.T, me api.Me) (*App, *statusKoordinator, func(n int) []string) {
	t.Helper()
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	got := countNotify(t)
	k := &statusKoordinator{me: me}
	ts := httptest.NewServer(http.HandlerFunc(k.handler))
	t.Cleanup(ts.Close)
	a := busyApp(t, client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: me.Email}))
	a.mu.Lock()
	a.me = &me
	a.mu.Unlock()
	warte := func(n int) []string {
		t.Helper()
		ende := time.Now().Add(2 * time.Second)
		for len(got()) < n && time.Now().Before(ende) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond) // Überzählige sollen auffallen
		return got()
	}
	return a, k, warte
}

func benachStatus(email, name string, bereit bool, spieler ...string) *api.Status {
	return &api.Status{Host: &api.HostInfo{Email: email, MinecraftName: name}, HostReady: bereit, Players: spieler}
}

// Der erste Statusabruf ist nur der Vergleichsstand: Wer das Programm
// startet, während schon jemand hostet, bekommt keinen Hinweis.
func TestBenachrichtigungKeinHinweisBeimStart(t *testing.T) {
	a, k, warte := benachApp(t, api.Me{Email: "ich@example.org", MinecraftName: "Ich_Selbst"})
	k.setze(benachStatus("anna@example.org", "Anna", true, "Anna", "Ben"))
	a.pollStatus(false)
	a.pollStatus(false)
	if h := warte(1); len(h) != 0 {
		t.Fatalf("Hinweise ohne Änderung: %q", h)
	}
}

func TestBenachrichtigungHinweiseNachStandardschaltern(t *testing.T) {
	a, k, warte := benachApp(t, api.Me{Email: "ich@example.org", MinecraftName: "Ich_Selbst"})
	k.setze(&api.Status{})
	a.pollStatus(false)

	// Anna startet: „hostet jetzt“ und „online“ (beides standardmäßig an).
	k.setze(benachStatus("anna@example.org", "Anna", true, "Anna"))
	a.pollStatus(false)
	h := warte(2)
	if len(h) != 2 || h[0] != "Anna hostet jetzt – die Welt ist gleich bereit." || h[1] != "Die Welt ist online (Host Anna)." {
		t.Fatalf("Hinweise: %q", h)
	}
	// Spieler sind standardmäßig aus.
	k.setze(benachStatus("anna@example.org", "Anna", true, "Anna", "Ben"))
	a.pollStatus(false)
	if h := warte(3); len(h) != 2 {
		t.Fatalf("Spieler-Hinweis trotz Standard aus: %q", h)
	}
	// Eingeschaltet: Beitritte und Abgänge eines Abrufs in einer Meldung; der eigene Name zählt nicht.
	if err := a.setBenach(ereignis.Spieler, true); err != nil {
		t.Fatal(err)
	}
	k.setze(benachStatus("anna@example.org", "Anna", true, "Anna", "Cora", "Dirk", "Ich_Selbst"))
	a.pollStatus(false)
	h = warte(3)
	if len(h) != 3 || h[2] != "Cora und Dirk sind beigetreten, Ben hat die Welt verlassen." {
		t.Fatalf("Hinweise: %q", h)
	}
	// „Niemand hostet mehr“ ist aus; Wartung an.
	k.setze(&api.Status{})
	a.pollStatus(false)
	if h := warte(4); len(h) != 3 {
		t.Fatalf("Niemand-Hinweis trotz Standard aus: %q", h)
	}
	k.setze(&api.Status{Maintenance: &api.Maintenance{Reason: "Update"}})
	a.pollStatus(false)
	h = warte(4)
	if len(h) != 4 || !strings.HasPrefix(h[3], "Wartung am Server angekündigt (Update).") {
		t.Fatalf("Hinweise: %q", h)
	}
}

// Was man selbst ausgelöst hat (eigenes Hosten, eigener Beitritt), zeigt der
// Starter schon anders: kein doppelter Hinweis.
func TestBenachrichtigungNichtDoppeltBeiEigenem(t *testing.T) {
	a, k, warte := benachApp(t, api.Me{Email: "Ich@Example.org", MinecraftName: "Ich_Selbst"})
	if err := a.setBenach(ereignis.Spieler, true); err != nil {
		t.Fatal(err)
	}
	k.setze(&api.Status{})
	a.pollStatus(false)
	k.setze(benachStatus("ich@example.org", "Ich_Selbst", false)) // ich hoste (E-Mail ohne Rücksicht auf Groß/Klein)
	a.pollStatus(false)
	k.setze(benachStatus("ich@example.org", "Ich_Selbst", true, "ich_selbst"))
	a.pollStatus(false)
	if h := warte(1); len(h) != 0 {
		t.Fatalf("Hinweise zum eigenen Hosten: %q", h)
	}
	// Ein anderer tritt bei: das schon.
	k.setze(benachStatus("ich@example.org", "Ich_Selbst", true, "ich_selbst", "Gast"))
	a.pollStatus(false)
	if h := warte(1); len(h) != 1 || h[0] != "Gast ist beigetreten." {
		t.Fatalf("Hinweise: %q", h)
	}
}

// Nach einem Ausfall des Koordinators gibt es keinen Vorgänger: keine Flut
// alter Ereignisse, wenn er zurück ist.
func TestBenachrichtigungKeineFlutNachAusfall(t *testing.T) {
	a, k, warte := benachApp(t, api.Me{Email: "ich@example.org"})
	k.setze(&api.Status{})
	a.pollStatus(false)
	k.setze(nil) // Koordinator kaputt
	a.pollStatus(false)
	k.setze(benachStatus("anna@example.org", "Anna", true, "Anna"))
	a.pollStatus(false)
	if h := warte(1); len(h) != 0 {
		t.Fatalf("Hinweise nach dem Ausfall: %q", h)
	}
}

func TestBenachrichtigungSchalterBleibenImGruppenordner(t *testing.T) {
	a, _, _ := benachApp(t, api.Me{Email: "ich@example.org"})
	for _, art := range []ereignis.Art{ereignis.HostStartet, ereignis.WeltOnline, ereignis.Wartung, ereignis.Beitrittsanfrage} {
		if !a.benachAn(art) {
			t.Errorf("%s ist standardmäßig an", art)
		}
	}
	for _, art := range []ereignis.Art{ereignis.Spieler, ereignis.NiemandHostet, ereignis.Uebergabe} {
		if a.benachAn(art) {
			t.Errorf("%s ist standardmäßig aus", art)
		}
	}
	if err := a.setBenach(ereignis.Spieler, true); err != nil {
		t.Fatal(err)
	}
	if err := a.setBenach(ereignis.HostStartet, false); err != nil {
		t.Fatal(err)
	}
	if a.benachAn(ereignis.HostStartet) || !a.benachAn(ereignis.Spieler) {
		t.Error("Umstellung nicht wirksam")
	}
	data, err := os.ReadFile(benachPfad())
	if err != nil {
		t.Fatal(err)
	}
	var e benachEinstellung
	if err := json.Unmarshal(data, &e); err != nil || len(e.Arten) != 2 {
		t.Fatalf("Datei: %s (%v)", data, err)
	}
	// Zurück auf den Standard: Der Eintrag verschwindet.
	if err := a.setBenach(ereignis.Spieler, false); err != nil {
		t.Fatal(err)
	}
	if err := a.setBenach(ereignis.HostStartet, true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(benachPfad())
	if strings.Contains(string(data), "arten") {
		t.Errorf("Standardwerte stehen noch in der Datei: %s", data)
	}
	if err := a.setBenach("gibtsnicht", true); err == nil {
		t.Error("unbekannter Hinweis wurde angenommen")
	}
	// Eine unlesbare Datei bedeutet Standard und lässt sich überschreiben.
	os.WriteFile(benachPfad(), []byte("{kaputt"), 0o600)
	if !a.benachAn(ereignis.HostStartet) {
		t.Error("kaputte Datei: Standard gilt")
	}
	if err := a.setBenach(ereignis.Spieler, true); err != nil || !a.benachAn(ereignis.Spieler) {
		t.Errorf("kaputte Datei überschreiben: %v", err)
	}
}

// Das Fenster sieht je Mitglied die passenden Schalter: den für Beitrittsanfragen nur Admins.
func TestBenachrichtigungZustandFuerAdminUndMitglied(t *testing.T) {
	a, _, _ := benachApp(t, api.Me{Email: "ich@example.org"})
	arten := func(me *api.Me) []string {
		var out []string
		for _, s := range a.benachZustand(me) {
			out = append(out, s.Art)
		}
		return out
	}
	mitglied := arten(&api.Me{Email: "x@example.org"})
	admin := arten(&api.Me{Email: "x@example.org", Admin: true})
	if len(admin) != len(mitglied)+1 || admin[len(admin)-1] != string(ereignis.Beitrittsanfrage) {
		t.Errorf("Mitglied %v, Admin %v", mitglied, admin)
	}
	for _, s := range mitglied {
		if s == string(ereignis.Beitrittsanfrage) {
			t.Error("Mitglied sieht den Admin-Schalter")
		}
	}
}

func TestBenachrichtigungEigenesFiltern(t *testing.T) {
	me := &api.Me{Email: "Ich@example.org", MinecraftName: "Ich_Selbst"}
	_, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: "ich@example.org"}, me)
	if !skip {
		t.Error("eigenes Hosten wurde nicht gefiltert")
	}
	_, skip = eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: "anna@example.org"}, me)
	if skip {
		t.Error("fremdes Hosten wurde gefiltert")
	}
	_, skip = eigenesFiltern(ereignis.Ereignis{Art: ereignis.Uebergabe, WerKey: "anna@example.org", NachKey: "ich@example.org"}, me)
	if !skip {
		t.Error("Übergabe an mich wurde nicht gefiltert")
	}
	e, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.Spieler, Neu: []string{"ich_selbst", "Anna"}, Weg: []string{"ICH_SELBST"}}, me)
	if skip || len(e.Neu) != 1 || e.Neu[0] != "Anna" || len(e.Weg) != 0 {
		t.Errorf("Spieler: %+v %v", e, skip)
	}
	if _, skip = eigenesFiltern(ereignis.Ereignis{Art: ereignis.Spieler, Neu: []string{"Ich_Selbst"}}, me); !skip {
		t.Error("nur der eigene Beitritt blieb übrig")
	}
	// Ohne bekannte Identität wird nichts gefiltert.
	if _, skip = eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: "ich@example.org"}, nil); skip {
		t.Error("ohne Identität gefiltert")
	}
}
