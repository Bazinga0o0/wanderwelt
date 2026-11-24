package fensterapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const testToken = "0123456789abcdef"

// writeControl legt control.json wie die laufende Instanz ab.
func writeControl(t *testing.T, home string, port int, token string) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"port": port, "token": token, "pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(home, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFensterClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WANDERWELT_HOME", home)

	// Ohne control.json läuft nichts.
	if c, err := Verbinden(); !errors.Is(err, ErrLaeuftNicht) || c != nil {
		t.Fatalf("Verbinden ohne control.json = %v, %v; want ErrLaeuftNicht", c, err)
	}
	// control.json von einem beendeten Lauf: niemand antwortet.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	writeControl(t, home, deadPort, testToken)
	if c, err := Verbinden(); !errors.Is(err, ErrLaeuftNicht) || c != nil {
		t.Fatalf("Verbinden ohne laufende Instanz = %v, %v; want ErrLaeuftNicht", c, err)
	}

	zustand := Zustand{
		Version: "1.4.0", Phase: "Hostet", Detail: "lädt die Welt: 45 %", Hosting: true,
		HostAddr: "wanderwelt.example:25565",
		Status:   &api.Status{Players: []string{"Lukas"}, HeadGeneration: 7, WorldID: "w1"},
		Me:       &api.Me{MinecraftName: "Lukas", Admin: true},
		Profil:   api.Profile{XmxMB: 6144, ViewDistance: 10},
		Hinweise: []Hinweis{{Seq: 3, Zeit: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), Art: "fehler", Text: "kaputt"}},
	}
	staende := api.GenerationsResponse{Worlds: []api.WorldInfo{{WorldID: "w1", Active: true,
		Generations: []api.GenerationInfo{{WorldID: "w1", Number: 7, Size: 123, RestoredFrom: &api.GenRef{WorldID: "w1", Number: 3}}}}},
		Storage: &api.StorageInfo{UsedBytes: 5, BudgetBytes: 10}}
	saves := []Speicherstand{{Folder: "Neue Welt", LevelName: "Neue Welt", Version: "26.2", Size: 42, PlayerFile: true}}
	members := []api.MemberInfo{{Email: "lukas@x.de", MinecraftName: "Lukas", Admin: true, Online: true}}

	var mu sync.Mutex
	var seen []string // Methode, Pfad, Authorization, Host, Körper
	var srvHost string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, fmt.Sprintf("%s %s %s %s %s", r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Host, body))
		host := srvHost
		mu.Unlock()
		if r.Host != host || r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(v)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/zustand":
			writeJSON(200, zustand)
		case "GET /v1/staende":
			writeJSON(200, staende)
		case "GET /v1/speicherstaende":
			writeJSON(200, saves)
		case "GET /v1/mitglieder":
			writeJSON(200, members)
		case "GET /v1/log":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "Zeilen %s\nzweite Zeile\n", r.URL.Query().Get("zeilen"))
		case "POST /v1/play":
			w.WriteHeader(http.StatusAccepted)
		case "POST /v1/autostart":
			writeJSON(200, map[string]any{})
		case "POST /v1/zurueckholen":
			writeJSON(http.StatusConflict, map[string]string{"fehler": "x"})
		case "POST /v1/speicher":
			http.Error(w, "kaputt", http.StatusInternalServerError)
		default:
			http.Error(w, "unbekannte Aktion", http.StatusNotFound)
		}
	}))
	srv.Start()
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	mu.Lock()
	srvHost = fmt.Sprintf("127.0.0.1:%d", port)
	mu.Unlock()
	writeControl(t, home, port, testToken)

	c, err := Verbinden()
	if err != nil {
		t.Fatalf("Verbinden: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	z, err := c.Zustand(ctx)
	if err != nil || !reflect.DeepEqual(*z, zustand) {
		t.Fatalf("Zustand = %+v, %v; want %+v", z, err, zustand)
	}
	st, err := c.Staende(ctx)
	if err != nil || !reflect.DeepEqual(*st, staende) {
		t.Fatalf("Staende = %+v, %v", st, err)
	}
	sv, err := c.Speicherstaende(ctx)
	if err != nil || !reflect.DeepEqual(sv, saves) {
		t.Fatalf("Speicherstaende = %+v, %v", sv, err)
	}
	ms, err := c.Mitglieder(ctx)
	if err != nil || !reflect.DeepEqual(ms, members) {
		t.Fatalf("Mitglieder = %+v, %v", ms, err)
	}
	lg, err := c.Log(ctx, 50)
	if err != nil || lg != "Zeilen 50\nzweite Zeile\n" {
		t.Fatalf("Log = %q, %v", lg, err)
	}
	if err := c.Aktion(ctx, "play", nil); err != nil {
		t.Fatalf("Aktion play: %v", err)
	}
	if err := c.Aktion(ctx, "autostart", map[string]bool{"an": true}); err != nil {
		t.Fatalf("Aktion autostart: %v", err)
	}
	// Fehler tragen den Text für den Nutzer.
	err = c.Aktion(ctx, "zurueckholen", api.RestoreRequest{WorldID: "w1", Number: 3})
	if err == nil || err.Error() != "x" {
		t.Fatalf("Aktion zurueckholen = %v, want Fehlertext \"x\"", err)
	}
	if err := c.Aktion(ctx, "speicher", api.BudgetRequest{BudgetBytes: 1}); err == nil || errors.Is(err, ErrLaeuftNicht) {
		t.Fatalf("Aktion speicher mit 500 ohne JSON = %v", err)
	}
	if err := c.Aktion(ctx, "gibtsnicht", nil); err == nil {
		t.Fatal("unbekannte Aktion ohne Fehler")
	}

	mu.Lock()
	got := copyOf(seen)
	mu.Unlock()
	want := []string{
		"GET /v1/zustand Bearer " + testToken + " " + srvHost + " ",
		"GET /v1/staende Bearer " + testToken + " " + srvHost + " ",
		"GET /v1/speicherstaende Bearer " + testToken + " " + srvHost + " ",
		"GET /v1/mitglieder Bearer " + testToken + " " + srvHost + " ",
		"GET /v1/log?zeilen=50 Bearer " + testToken + " " + srvHost + " ",
		"POST /v1/play Bearer " + testToken + " " + srvHost + " ",
		"POST /v1/autostart Bearer " + testToken + " " + srvHost + " {\"an\":true}",
		"POST /v1/zurueckholen Bearer " + testToken + " " + srvHost + " {\"world_id\":\"w1\",\"number\":3}",
		"POST /v1/speicher Bearer " + testToken + " " + srvHost + " {\"budget_bytes\":1}",
		"POST /v1/gibtsnicht Bearer " + testToken + " " + srvHost + " ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Anfragen =\n%q\nwant\n%q", got, want)
	}

	// Die Instanz endet bei offenem Fenster: wieder ErrLaeuftNicht.
	srv.Close()
	if _, err := c.Zustand(ctx); !errors.Is(err, ErrLaeuftNicht) {
		t.Fatalf("Zustand nach Ende der Instanz = %v, want ErrLaeuftNicht", err)
	}
	// Ein abgebrochener Kontext ist kein "läuft nicht".
	done, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.Zustand(done); errors.Is(err, ErrLaeuftNicht) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Zustand mit abgebrochenem Kontext = %v", err)
	}
}

func copyOf(in []string) []string { return append([]string(nil), in...) }

// Wer hostet, „beendet das Spiel für sich“; wer nur mitspielt, spielt „nicht
// mehr mit“ (Tray und Fenster nehmen dieselben Texte).
func TestBeendenText(t *testing.T) {
	label, hinweis := BeendenText(true)
	if label != "Spiel für mich beenden" || !strings.Contains(hinweis, "Server stoppen") {
		t.Errorf("Host: %q / %q", label, hinweis)
	}
	label, hinweis = BeendenText(false)
	if label != "Nicht mehr mitspielen" || hinweis != "Minecraft schließt du selbst; wanderwelt übernimmt dann nicht mehr automatisch" {
		t.Errorf("Mitspieler: %q / %q", label, hinweis)
	}
}
