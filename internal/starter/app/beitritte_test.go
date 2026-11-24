package app

import (
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
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// joinCoordinator ist ein Koordinator mit Beitrittsanfragen.
type joinCoordinator struct {
	mu       sync.Mutex
	requests []api.JoinRequestInfo
	posts    []string
	status   int // Antwort auf accept/decline (0 = 204)
}

func (j *joinCoordinator) handler(w http.ResponseWriter, r *http.Request) {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/join-requests":
		json.NewEncoder(w).Encode(j.requests)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/join-requests/"):
		j.posts = append(j.posts, strings.TrimPrefix(r.URL.Path, "/v1/join-requests/"))
		if j.status != 0 {
			w.WriteHeader(j.status)
			json.NewEncoder(w).Encode(map[string]string{"error": "Der Name ist inzwischen vergeben."})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "unbekannter Endpunkt"})
	}
}

func (j *joinCoordinator) gepostet() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.posts...)
}

func joinFenster(t *testing.T, admin bool) (*fensterTest, *joinCoordinator) {
	t.Helper()
	jc := &joinCoordinator{requests: []api.JoinRequestInfo{
		{Email: "tim@example.org", MinecraftName: "Tim_R", Requested: time.Now()}}}
	ts := httptest.NewServer(http.HandlerFunc(jc.handler))
	t.Cleanup(ts.Close)
	ft := startFenster(t, client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"}))
	ft.a.mu.Lock()
	ft.a.me = &api.Me{Email: "admin@example.org", Admin: admin}
	ft.a.mu.Unlock()
	return ft, jc
}

// Ansicht und Aktion: Annehmen/Ablehnen gehen an den Koordinator, nur für
// Admins; Fehler des Koordinators kommen mit Code und Text beim Fenster an.
func TestFensterBeitritt(t *testing.T) {
	probeAnzeige(t)
	ft, jc := joinFenster(t, true)
	got, err := ft.fc.Beitritte(t.Context())
	if err != nil || len(got) != 1 || got[0].MinecraftName != "Tim_R" {
		t.Fatalf("Beitritte = %+v, %v", got, err)
	}
	if code, _ := ft.mit(t, "POST", "/v1/beitritt", `{"email":"tim@example.org","annehmen":true}`); code != http.StatusOK {
		t.Fatalf("annehmen: %d", code)
	}
	if code, _ := ft.mit(t, "POST", "/v1/beitritt", `{"email":"tim@example.org","annehmen":false}`); code != http.StatusOK {
		t.Fatalf("ablehnen: %d", code)
	}
	if want := []string{"tim@example.org/accept", "tim@example.org/decline"}; strings.Join(jc.gepostet(), ",") != strings.Join(want, ",") {
		t.Fatalf("beim Koordinator: %q, erwartet %q", jc.gepostet(), want)
	}
	if code, body := ft.mit(t, "POST", "/v1/beitritt", `{"annehmen":true}`); code != http.StatusBadRequest {
		t.Fatalf("ohne E-Mail: %d %q", code, body)
	}
	jc.mu.Lock()
	jc.status = http.StatusConflict
	jc.mu.Unlock()
	code, body := ft.mit(t, "POST", "/v1/beitritt", `{"email":"tim@example.org","annehmen":true}`)
	if code != http.StatusConflict || !strings.Contains(fehlerText(t, body), "vergeben") {
		t.Fatalf("409: %d %q", code, body)
	}

	ft2, jc2 := joinFenster(t, false)
	if code, _ := ft2.mit(t, "POST", "/v1/beitritt", `{"email":"tim@example.org","annehmen":true}`); code != http.StatusForbidden {
		t.Fatalf("Nicht-Admin: %d, erwartet 403", code)
	}
	if len(jc2.gepostet()) != 0 {
		t.Fatalf("Nicht-Admin erreichte den Koordinator: %q", jc2.gepostet())
	}
}

// Der Tray-Hinweis kommt höchstens einmal je Anfrage, nennt nur den
// Minecraft-Namen und fällt nach Bearbeitung der Anfrage wieder weg.
func TestBeitrittsHinweis(t *testing.T) {
	got := countNotify(t)
	jc := &joinCoordinator{requests: []api.JoinRequestInfo{{Email: "tim@example.org", MinecraftName: "Tim_R"}}}
	ts := httptest.NewServer(http.HandlerFunc(jc.handler))
	t.Cleanup(ts.Close)
	c := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "admin@example.org"})
	a := busyApp(t, c)
	warte := func(n int) []string {
		t.Helper()
		ende := time.Now().Add(2 * time.Second)
		for len(got()) < n && time.Now().Before(ende) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond) // Überzählige sollen auffallen
		return got()
	}
	me := &api.Me{Email: "admin@example.org", Admin: true, PendingJoins: 1}

	a.beitrittsHinweis(t.Context(), c, me)
	a.beitrittsHinweis(t.Context(), c, me)
	if h := warte(1); len(h) != 1 || h[0] != "Neue Beitrittsanfrage von Tim_R" {
		t.Fatalf("Hinweise: %q", h)
	}
	// Eine zweite Anfrage: nur sie wird gemeldet.
	jc.mu.Lock()
	jc.requests = append(jc.requests, api.JoinRequestInfo{Email: "Eva@example.org", MinecraftName: "Eva_K"})
	jc.mu.Unlock()
	me.PendingJoins = 2
	a.beitrittsHinweis(t.Context(), c, me)
	a.beitrittsHinweis(t.Context(), c, me)
	h := warte(2)
	if len(h) != 2 || h[1] != "Neue Beitrittsanfrage von Eva_K" {
		t.Fatalf("Hinweise: %q", h)
	}
	for _, x := range h {
		if strings.Contains(x, "@") || strings.Contains(x, "example.org") {
			t.Errorf("Hinweis mit E-Mail: %q", x)
		}
	}
	// Alle bearbeitet; später meldet sich Tim erneut an: neuer Hinweis.
	me.PendingJoins = 0
	a.beitrittsHinweis(t.Context(), c, me)
	jc.mu.Lock()
	jc.requests = jc.requests[:1]
	jc.mu.Unlock()
	me.PendingJoins = 1
	a.beitrittsHinweis(t.Context(), c, me)
	if h := warte(3); len(h) != 3 {
		t.Fatalf("nach neuer Anfrage: %q", h)
	}
	// Kein Admin: nichts.
	n := len(got())
	a.beitrittsHinweis(t.Context(), c, &api.Me{Email: "x@example.org", PendingJoins: 1})
	if h := warte(n + 1); len(h) != n {
		t.Fatalf("Nicht-Admin bekam Hinweis: %q", h)
	}
}

// Ein Netzfehler bei accept trägt keine E-Mail-Adresse ins Log.
func TestBeitrittNetzfehlerOhneEmail(t *testing.T) {
	probeAnzeige(t)
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // niemand lauscht mehr
	ft := startFenster(t, client.New(&group.Group{Coordinator: url, DevIdentityEmail: "admin@example.org"}))
	ft.a.mu.Lock()
	ft.a.me = &api.Me{Email: "admin@example.org", Admin: true}
	ft.a.mu.Unlock()
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, _ := ft.mit(t, "POST", "/v1/beitritt", `{"email":"geheim.adresse@example.org","annehmen":true}`)
	if code < 400 {
		t.Fatalf("Code %d, erwartet Fehler", code)
	}
	if !strings.Contains(buf.String(), "Beitrittsanfrage") {
		t.Fatalf("Log ohne Fehlerzeile: %q", buf.String())
	}
	if strings.Contains(buf.String(), "geheim.adresse") {
		t.Fatalf("E-Mail im Log: %q", buf.String())
	}
}
