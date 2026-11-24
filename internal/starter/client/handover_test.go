package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// 409 auf HandoverStart heißt „keine Übergabe“, mit dem Grund aus der
// Antwort; jeder andere Fehler bleibt ein *StatusError.
func TestHandoverStartConflict(t *testing.T) {
	status, body := http.StatusConflict, `{"error":"kein Kandidat bereit"}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/handover/start" {
			t.Errorf("Anfrage %s %s", r.Method, r.URL.Path)
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL})
	_, err := c.HandoverStart(t.Context(), 1, "pc")
	if !errors.Is(err, ErrNoHandover) || !strings.Contains(err.Error(), "kein Kandidat bereit") {
		t.Fatalf("Fehler %v, erwartet ErrNoHandover mit Grund", err)
	}
	status, body = http.StatusInternalServerError, `{"error":"kaputt"}`
	_, err = c.HandoverStart(t.Context(), 1, "pc")
	if errors.Is(err, ErrNoHandover) {
		t.Fatalf("500 ist keine ErrNoHandover: %v", err)
	}
	status, body = http.StatusOK, `{"handover_id":3,"to":{"minecraft_name":"Neu"}}`
	resp, err := c.HandoverStart(t.Context(), 1, "pc")
	if err != nil || resp.HandoverID != 3 || resp.To.MinecraftName != "Neu" {
		t.Fatalf("Antwort %+v, %v", resp, err)
	}
}
