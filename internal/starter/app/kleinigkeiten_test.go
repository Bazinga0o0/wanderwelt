package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Nach einem Update läuft die Einrichtung still nach: keine Begrüßung.
// Bei einer frischen Einrichtung kommt sie mit den Hinweisen.
func TestAbschlussNachUpdateOhneBegruessung(t *testing.T) {
	var shown []string
	old := showInfo
	t.Cleanup(func() { showInfo = old })
	showInfo = func(s string) { shown = append(shown, s) }

	abschluss(true, "Lukas", []string{"Hinweis"})
	if len(shown) != 0 {
		t.Fatalf("nach dem Update gezeigt: %q", shown)
	}
	abschluss(false, "Lukas", []string{"Hinweis A"})
	if len(shown) != 1 || !strings.Contains(shown[0], "Fertig, Lukas!") || !strings.Contains(shown[0], "• Hinweis A") {
		t.Fatalf("frische Einrichtung: %q", shown)
	}
}

// Das Log (im Log-Tab des Fensters sichtbar) enthält keine E-Mail-Adresse
// anderer Mitglieder aus dem Pfad des Koordinator-Aufrufs.
func TestFensterLogOhneFremdeEmail(t *testing.T) {
	probeAnzeige(t)
	koord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"Nur Admins dürfen das"}`))
	}))
	defer koord.Close()
	ft := startFenster(t, client.New(&group.Group{Coordinator: koord.URL, DevIdentityEmail: "admin@example.org"}))

	var buf syncBuffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	err := ft.fc.Aktion(t.Context(), "rolle", map[string]any{"email": "bob@example.org", "admin": true})
	if err == nil {
		t.Fatal("rolle: kein Fehler")
	}
	out := buf.String()
	if !strings.Contains(out, "Fenster: Rolle: Koordinator: PUT 403: Nur Admins dürfen das") {
		t.Errorf("Log ohne Aktion, Status und Meldung:\n%s", out)
	}
	if strings.Contains(out, "bob@example.org") || strings.Contains(out, "/v1/members") {
		t.Errorf("Log enthält die E-Mail oder den Pfad:\n%s", out)
	}
	if got := logFehler(errors.New("netz weg")); got != "netz weg" {
		t.Errorf("logFehler(sonstiger Fehler) = %q", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Aus dem Fenster übernommen (FromWindow, ohne Spielerdatei bestätigt):
// keine weitere Rückfrage und kein zenity-Fortschrittsfenster. Von der
// Kommandozeile bzw. dem Tray bleibt das Fortschrittsfenster.
func TestImportAusFensterOhneDialoge(t *testing.T) {
	c := importCoordinator(t)
	for _, tc := range []struct {
		name       string
		fromWindow bool
		wantOpens  int32
	}{{"Fenster", true, 0}, {"Kommandozeile", false, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WANDERWELT_HOME", t.TempDir())
			p := probeAnzeige(t)
			var opens atomic.Int32
			openImportProgress = func() (zenity.ProgressDialog, error) {
				opens.Add(1)
				return nil, errors.New("kein Fenster im Test")
			}
			saves := t.TempDir()
			writeSave(t, saves, "Ohne Spieler", levelDat("26.2", false, testUUID), false)
			a := busyApp(t, c)
			res := a.importRequested(importRequest{Dir: filepath.Join(saves, "Ohne Spieler"),
				AllowMissingPlayer: true, FromWindow: tc.fromWindow})
			if res == nil || !strings.Contains(res.text, "ist jetzt die Gruppenwelt") {
				t.Fatalf("Ergebnis: %+v", res)
			}
			if _, confirms := p.got(); len(confirms) != 0 {
				t.Errorf("Rückfragen trotz bestätigter Spielerdatei: %q", confirms)
			}
			if got := opens.Load(); got != tc.wantOpens {
				t.Errorf("Fortschrittsfenster %d mal geöffnet, erwartet %d", got, tc.wantOpens)
			}
		})
	}
}

// fensterImport setzt FromWindow selbst; aus dem Körper lässt es sich nicht
// setzen.
func TestImportKoerperSetztFromWindowNicht(t *testing.T) {
	var r importRequest
	if err := json.Unmarshal([]byte(`{"dir":"x","FromWindow":true}`), &r); err != nil || r.FromWindow {
		t.Fatalf("FromWindow aus dem Körper: %+v, %v", r, err)
	}
}
