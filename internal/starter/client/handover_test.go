package client

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
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
	status, body = http.StatusConflict, `{"error":"Es läuft schon eine Host-Übergabe"}`
	_, err = c.HandoverStart(t.Context(), 1, "pc")
	if !errors.Is(err, ErrHandoverBusy) || errors.Is(err, ErrNoHandover) {
		t.Fatalf("Fehler %v, erwartet ErrHandoverBusy (nicht ErrNoHandover)", err)
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

// HandoverAbort: 204 ist Erfolg, 404 (älterer Koordinator) wird übergangen,
// jeder andere Fehler bleibt einer.
func TestHandoverAbort(t *testing.T) {
	status := http.StatusNoContent
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/handover/abort" {
			t.Errorf("Anfrage %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(status)
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL})
	req := api.HandoverAbortRequest{HandoverID: 3, LeaseID: 4, MachineID: "pc"}
	if err := c.HandoverAbort(t.Context(), req); err != nil {
		t.Fatalf("204: %v", err)
	}
	if !strings.Contains(got, `"handover_id":3`) || !strings.Contains(got, `"lease_id":4`) {
		t.Fatalf("Körper %q", got)
	}
	status = http.StatusNotFound
	if err := c.HandoverAbort(t.Context(), req); err != nil {
		t.Fatalf("404 sollte übergangen werden: %v", err)
	}
	status = http.StatusConflict
	if err := c.HandoverAbort(t.Context(), req); err == nil {
		t.Fatal("409 sollte ein Fehler sein")
	}
}
