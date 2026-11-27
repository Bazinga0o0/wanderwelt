package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Der Reiter „Zustand“ holt die Gesundheit über die lokale Steuerung vom
// Koordinator (GET /v1/health): mit Token, ohne Token 403, ein Koordinator
// ohne den Endpunkt ist „zu alt“, ein Nicht-Admin bekommt die Ablehnung des
// Koordinators mit seinem Text.
func TestFensterGesundheit(t *testing.T) {
	probeAnzeige(t)
	mode := "ok"
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch mode {
		case "ok":
			w.Write([]byte(`{"version":"0.1.5","started":"2026-11-26T10:00:00Z","uptime_s":7200,` +
				`"disk":{"free_bytes":1000,"total_bytes":2000},"store_bytes":5,"store_known":true,` +
				`"world_id":"w1","head_generation":9,"open_joins":1,"backup":{"known":false},"status":"warnung",` +
				`"warnings":[{"level":"warnung","text":"Letzte Sicherung unbekannt"}]}`))
		case "admin":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"Die Gesundheit des Koordinators sehen nur Admins"}`))
		case "alt":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"unbekannter Endpunkt GET /v1/health"}`))
		}
	}))
	defer coord.Close()
	ft := startFenster(t, client.New(&group.Group{Coordinator: coord.URL, DevIdentityEmail: "admin@example.org"}))

	// Ohne Token oder mit falschem Host: 403, ohne den Koordinator zu fragen.
	if code, _, body := ft.roh(t, "GET", "/v1/gesundheit", ft.host, "", ""); code != http.StatusForbidden {
		t.Errorf("ohne Token: %d %q", code, body)
	}
	if code, _, body := ft.roh(t, "GET", "/v1/gesundheit", "localhost:1", "Bearer "+ft.token, ""); code != http.StatusForbidden {
		t.Errorf("falscher Host: %d %q", code, body)
	}

	ctx := t.Context()
	h, err := ft.fc.Gesundheit(ctx)
	if err != nil {
		t.Fatalf("Gesundheit: %v", err)
	}
	if h.Version != "0.1.5" || h.UptimeS != 7200 || h.Disk == nil || h.Disk.FreeBytes != 1000 || h.HeadGeneration != 9 ||
		h.Status != api.HealthWarnung || len(h.Warnings) != 1 || h.Warnings[0].Level != api.WarnWarnung {
		t.Errorf("Gesundheit: %+v", h)
	}

	mode = "admin"
	if _, err := ft.fc.Gesundheit(ctx); err == nil || !strings.Contains(err.Error(), "nur Admins") {
		t.Errorf("Nicht-Admin: %v", err)
	}
	mode = "alt"
	if _, err := ft.fc.Gesundheit(ctx); err == nil || !strings.Contains(err.Error(), "zu alt") {
		t.Errorf("alter Koordinator: %v", err)
	}
}

// Die Starter-Version geht mit Lebenszeichen und Bereitschaft an den
// Koordinator; ein vom Aufrufer gesetzter Wert bleibt, ohne gesetzte Version
// bleibt das Feld weg (ältere Koordinatoren kennen es nicht, ignorieren es
// aber ohnehin).
func TestStarterVersionImLebenszeichen(t *testing.T) {
	old := client.StarterVersion()
	t.Cleanup(func() { client.SetStarterVersion(old) })
	var bodies []string
	coord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(strings.Builder)
		buf := make([]byte, 512)
		for {
			n, err := r.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		bodies = append(bodies, r.URL.Path+" "+b.String())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer coord.Close()
	c := client.New(&group.Group{Coordinator: coord.URL})

	client.SetStarterVersion("0.1.5")
	c.Heartbeat(t.Context(), api.HeartbeatRequest{LeaseID: 1, MachineID: "pc", Players: []string{}})
	c.Presence(t.Context(), api.PresenceRequest{MachineID: "pc"})
	c.Heartbeat(t.Context(), api.HeartbeatRequest{LeaseID: 1, MachineID: "pc", StarterVersion: "9.9.9"})
	client.SetStarterVersion("")
	c.Presence(t.Context(), api.PresenceRequest{MachineID: "pc"})

	want := []string{`"starter_version":"0.1.5"`, `"starter_version":"0.1.5"`, `"starter_version":"9.9.9"`, ""}
	if len(bodies) != 4 {
		t.Fatalf("Anfragen: %q", bodies)
	}
	for i, w := range want {
		if w == "" {
			if strings.Contains(bodies[i], "starter_version") {
				t.Errorf("Anfrage %d ohne gesetzte Version trägt sie doch: %s", i, bodies[i])
			}
			continue
		}
		if !strings.Contains(bodies[i], w) {
			t.Errorf("Anfrage %d: %s, want %s", i, bodies[i], w)
		}
	}
}
