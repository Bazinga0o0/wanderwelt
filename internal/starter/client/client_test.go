package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Lehnt der Koordinator einen Blob mit 409 ab (derselbe Blob wird gerade
// schon hochgeladen, etwa vom eben abgebrochenen Versuch), beginnt PutBlob
// nach kurzer Pause von vorn – höchstens len(blobBusyRetry)-mal; andere
// Fehler wiederholt es nicht.
func TestPutBlobRetriesBusyBlob(t *testing.T) {
	old := blobBusyRetry
	blobBusyRetry = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { blobBusyRetry = old })

	var busy, calls atomic.Int32 // busy: so viele PUTs bekommen noch 409
	code := atomic.Int32{}       // Antwort danach
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPut {
			t.Errorf("unerwartet %s %s", r.Method, r.URL.Path)
		}
		if busy.Add(-1) >= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"Konflikt mit dem aktuellen Stand: Blob wird gerade schon hochgeladen"}`))
			return
		}
		w.WriteHeader(int(code.Load()))
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: "a@example.org"})
	data := []byte("blob")
	put := func() error { return c.PutBlob(t.Context(), "ab", bytes.NewReader(data), int64(len(data))) }

	for _, tc := range []struct {
		name      string
		busy      int32
		code      int
		wantErr   bool
		wantCalls int32
	}{
		{"frei", 0, http.StatusCreated, false, 1},
		{"zweimal belegt", 2, http.StatusCreated, false, 3},
		{"fünfmal belegt", 5, http.StatusCreated, false, 6},
		{"immer belegt", 100, http.StatusCreated, true, 6},
		{"anderer Fehler", 0, http.StatusInternalServerError, true, 1},
	} {
		busy.Store(tc.busy)
		code.Store(int32(tc.code))
		calls.Store(0)
		err := put()
		if (err != nil) != tc.wantErr || calls.Load() != tc.wantCalls {
			t.Errorf("%s: %d PUTs, Fehler %v; erwartet %d PUTs, Fehler %v", tc.name, calls.Load(), err, tc.wantCalls, tc.wantErr)
		}
		if tc.name == "immer belegt" && !blobBusy(err) {
			t.Errorf("%s: Fehler %v, erwartet den 409", tc.name, err)
		}
	}

	// Endet ctx während der Pause, kommt der 409 gleich zurück.
	blobBusyRetry = []time.Duration{time.Hour}
	busy.Store(100)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.PutBlob(ctx, "ab", bytes.NewReader(data), int64(len(data)))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("PutBlob wartet trotz Abbruch %v", d)
	}
	if !blobBusy(err) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nach Abbruch: %v, erwartet den 409", err)
	}
}
