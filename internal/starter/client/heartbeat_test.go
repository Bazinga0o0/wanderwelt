package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Ein älterer Koordinator beantwortet das Lebenszeichen mit 204 ohne Körper,
// ein neuer mit 200 und api.HeartbeatResponse. Ohne Körper (auch 200 mit
// leerem Körper) ist das Ergebnis nil, nil; eine Antwort, die sich nicht
// lesen lässt, ist kein Fehler – das Lebenszeichen ist angekommen, ein
// Fehler hier führte sonst zum Fencing. 409 bleibt ErrLeaseLost.
func TestHeartbeat204(t *testing.T) {
	created := time.Date(2026, 11, 23, 13, 5, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   *api.HeartbeatResponse
		lost   bool
	}{
		{"alter Koordinator: 204", http.StatusNoContent, "", nil, false},
		{"200 mit leerem Körper", http.StatusOK, "", nil, false},
		{"200 nur Leerzeichen", http.StatusOK, " \n", nil, false},
		{"200 nicht lesbar", http.StatusOK, "<html>", nil, false},
		{"neuer Koordinator ohne Angaben", http.StatusOK, `{}`, &api.HeartbeatResponse{}, false},
		{"neuer Koordinator mit Zurückholen", http.StatusOK,
			`{"restore":{"by":"Admin","target":{"world_id":"w1","number":3},"target_created":"2026-11-23T13:05:00Z","requested_at":"2026-11-23T14:00:00Z","delay_s":30},"members_version":7}`,
			&api.HeartbeatResponse{
				Restore: &api.RestoreNotice{
					By: "Admin", Target: api.GenRef{WorldID: "w1", Number: 3},
					TargetCreated: created, RequestedAt: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC), DelayS: 30,
				},
				MembersVersion: 7,
			}, false},
		{"Zuschlag verloren", http.StatusConflict, `{"error":"lease lost"}`, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/lease/heartbeat" {
					t.Errorf("Anfrage %s %s", r.Method, r.URL.Path)
				}
				io.Copy(io.Discard, r.Body)
				if tt.body != "" {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer ts.Close()
			c := New(&group.Group{Coordinator: ts.URL})
			got, err := c.Heartbeat(t.Context(), api.HeartbeatRequest{LeaseID: 1, MachineID: "pc", Players: []string{}})
			if tt.lost {
				if !errors.Is(err, ErrLeaseLost) || got != nil {
					t.Fatalf("Heartbeat = %+v, %v; erwartet nil, ErrLeaseLost", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Heartbeat: %v", err)
			}
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("Heartbeat = %+v, erwartet nil (alter Koordinator)", got)
			case tt.want != nil && got == nil:
				t.Fatalf("Heartbeat = nil, erwartet %+v", tt.want)
			case tt.want != nil:
				if got.MembersVersion != tt.want.MembersVersion || (got.Restore == nil) != (tt.want.Restore == nil) {
					t.Fatalf("Heartbeat = %+v, erwartet %+v", got, tt.want)
				}
				if tt.want.Restore != nil {
					g, w := *got.Restore, *tt.want.Restore
					if g.By != w.By || g.Target != w.Target || !g.TargetCreated.Equal(w.TargetCreated) ||
						!g.RequestedAt.Equal(w.RequestedAt) || g.DelayS != w.DelayS {
						t.Fatalf("Notice = %+v, erwartet %+v", g, w)
					}
				}
			}
		})
	}
}
