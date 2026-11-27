package client

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
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

// Median: ungerade Zahl = mittlerer Wert, gerade = Mittel der beiden
// mittleren; Ausreißer zählen nicht; die Eingabe bleibt unverändert.
func TestMedian(t *testing.T) {
	ms := func(v ...int) []time.Duration {
		var ds []time.Duration
		for _, x := range v {
			ds = append(ds, time.Duration(x)*time.Millisecond)
		}
		return ds
	}
	if got := Median(ms(50, 20, 900, 25, 22, 30, 21)); got != 25*time.Millisecond {
		t.Fatalf("Median mit Ausreißer = %v, erwartet 25ms", got)
	}
	in := ms(40, 10, 30, 20)
	if got := Median(in); got != 25*time.Millisecond {
		t.Fatalf("Median gerade = %v, erwartet 25ms", got)
	}
	if in[0] != 40*time.Millisecond {
		t.Fatalf("Median hat die Eingabe sortiert: %v", in)
	}
	if got := Median(nil); got != 0 {
		t.Fatalf("Median(nil) = %v", got)
	}
}

// Ping: ein ungezählter Probe-Aufruf plus PingProbes gezählte, alle auf
// GET /v1/ping über dieselbe Verbindung; der Wert ist der Median.
func TestPingMedianOverKeepAlive(t *testing.T) {
	var calls atomic.Int32
	conns := map[string]bool{}
	var mu sync.Mutex
	delays := []time.Duration{0, 5, 300, 5, 5, 5, 5, 5} // Probe 1 (ungezählt) = 0, dann 5 ms mit einem Ausreißer
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/ping" {
			t.Errorf("unerwartet %s %s", r.Method, r.URL.Path)
		}
		n := int(calls.Add(1)) - 1
		mu.Lock()
		conns[r.RemoteAddr] = true
		mu.Unlock()
		time.Sleep(delays[n] * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL})
	res, err := c.Ping(t.Context())
	if err != nil || res.Estimated {
		t.Fatalf("Ping = %+v, %v", res, err)
	}
	if got := calls.Load(); got != 1+PingProbes {
		t.Fatalf("%d Aufrufe, erwartet %d", got, 1+PingProbes)
	}
	if len(conns) != 1 {
		t.Fatalf("%d Verbindungen, erwartet 1 (Keep-Alive)", len(conns))
	}
	// Der Ausreißer (300 ms) darf den Median nicht heben: 5 ms plus Spielraum.
	if res.Median < 5*time.Millisecond || res.Median > 40*time.Millisecond {
		t.Fatalf("Median %v, erwartet etwa 5ms (Ausreißer 300ms außen vor)", res.Median)
	}
}

// Ein älterer Koordinator (404 auf /v1/ping): Ersatz über GET /v1/me, das
// Ergebnis gilt als geschätzt; andere Fehler sind Fehler.
func TestPingFallsBackOnOldCoordinator(t *testing.T) {
	var ping, me atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ping":
			ping.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"nicht gefunden"}`))
		case "/v1/me":
			me.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		}
	}))
	defer ts.Close()
	c := New(&group.Group{Coordinator: ts.URL})
	res, err := c.Ping(t.Context())
	if err != nil || !res.Estimated || res.Median < 0 { // Loopback kann unter Windows 0 messen
		t.Fatalf("Ping = %+v, %v; erwartet geschätzt", res, err)
	}
	if ping.Load() != 1 || me.Load() != 1+PingProbes {
		t.Fatalf("/v1/ping %d×, /v1/me %d×; erwartet 1 und %d", ping.Load(), me.Load(), 1+PingProbes)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	if _, err := New(&group.Group{Coordinator: bad.URL}).Ping(t.Context()); err == nil {
		t.Fatal("Ping gegen 502 ohne Fehler")
	}
}
