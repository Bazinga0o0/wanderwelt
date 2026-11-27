package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Die Statistik geht vom Koordinator durch die Steuerung ans Fenster, auch
// für Mitglieder ohne Admin-Recht; ein älterer Koordinator ist „zu alt“.
func TestFensterStatistik(t *testing.T) {
	probeAnzeige(t)
	var alt bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if alt || r.Method != "GET" || r.URL.Path != "/v1/stats" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "unbekannter Endpunkt GET " + r.URL.Path})
			return
		}
		json.NewEncoder(w).Encode(api.Stats{Total: api.StatsPeriod{HostTotalS: 90, Members: []api.MemberStats{
			{MinecraftName: "Alice", PlayS: 120, HostS: 90, HostSessions: 2, HostShare: 1}}}})
	}))
	t.Cleanup(ts.Close)
	ft := startFenster(t, client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "bob@example.org"}))
	ft.a.mu.Lock()
	ft.a.me = &api.Me{Email: "bob@example.org"} // kein Admin
	ft.a.mu.Unlock()

	st, err := ft.fc.Statistik(t.Context())
	if err != nil || len(st.Total.Members) != 1 || st.Total.Members[0].HostS != 90 || st.Total.Members[0].MinecraftName != "Alice" {
		t.Fatalf("Statistik = %+v, %v", st, err)
	}
	alt = true
	if _, err := ft.fc.Statistik(t.Context()); err == nil || !strings.Contains(err.Error(), "zu alt") {
		t.Fatalf("älterer Koordinator: %v, erwartet „zu alt“", err)
	}
}
