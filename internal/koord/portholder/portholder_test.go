package portholder

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// PUT 404: tunneld kennt das Konto nicht (oder hat es nicht freigeschaltet)
// → ErrUnknownAccount. DELETE 404 heißt dagegen „schon geräumt“.
func TestPutNotFoundIsUnknownAccount(t *testing.T) {
	code := http.StatusNotFound
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer geheim" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(`{"error":"account not found"}`))
	}))
	defer ts.Close()
	c := New(ts.URL, "geheim")
	ctx := t.Context()

	err := c.Set(ctx, 15565, 7)
	if !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("Set bei 404: %v, erwartet ErrUnknownAccount", err)
	}
	if err := c.Clear(ctx, 15565); err != nil {
		t.Fatalf("Clear bei 404: %v, erwartet nil", err)
	}
	code = http.StatusServiceUnavailable
	if err := c.Set(ctx, 15565, 7); err == nil || errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("Set bei 503: %v, erwartet anderen Fehler", err)
	}
}
