package bench

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Scheitert die Upload-Messung, geht Spielen weiter: erst mit 0, danach mit
// dem letzten gemessenen Wert. Ein Abbruch durch den Aufrufer bleibt ein
// Fehler.
func TestMeasureSurvivesFailedSpeedTest(t *testing.T) {
	old := measureCPU
	t.Cleanup(func() { measureCPU = old; lastUpload.set(0) })
	measureCPU = func(context.Context) (float64, error) { return 500, nil }
	lastUpload.set(0)

	var fail atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c := client.New(&group.Group{Coordinator: ts.URL})
	ctx := t.Context()

	fail.Store(true)
	s, err := Measure(ctx, c, 4096)
	if err != nil || s.UploadMbps != 0 || s.CPUScore != 500 || s.XmxMB != 4096 || s.RAMTotalMB <= 0 {
		t.Fatalf("gescheiterte Messung ohne früheren Wert: %+v, %v; erwartet Upload 0 ohne Fehler", s, err)
	}
	fail.Store(false)
	s, err = Measure(ctx, c, 4096)
	if err != nil || s.UploadMbps <= 0 {
		t.Fatalf("Messung: %+v, %v", s, err)
	}
	measured := s.UploadMbps
	fail.Store(true)
	s, err = Measure(ctx, c, 4096)
	if err != nil || s.UploadMbps != measured {
		t.Fatalf("gescheiterte Messung: %+v, %v; erwartet den letzten Wert %.1f", s, err, measured)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	measureCPU = func(context.Context) (float64, error) { return 500, nil }
	if _, err := Measure(cctx, c, 4096); err == nil {
		t.Fatal("abgebrochene Messung ohne Fehler")
	}
}

// Messung des Pings: Kennt der Koordinator /v1/ping, steht der Median in
// Score.PingMs (mindestens 0,1); ein älterer Koordinator (404) oder eine
// gescheiterte Messung lässt das Feld weg (0 = fehlt, damit der Starter
// kein Feld sendet, das ein älterer Koordinator ablehnt).
func TestMeasurePing(t *testing.T) {
	old := measureCPU
	t.Cleanup(func() { measureCPU = old; lastUpload.set(0) })
	measureCPU = func(context.Context) (float64, error) { return 500, nil }

	var mode atomic.Int32 // 0 neu, 1 alt (404), 2 kaputt (502)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		switch {
		case r.URL.Path == "/v1/ping" && mode.Load() == 1:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/v1/ping" && mode.Load() == 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer ts.Close()
	c := client.New(&group.Group{Coordinator: ts.URL})

	s, err := Measure(t.Context(), c, 4096)
	if err != nil || s.PingMs < 0.1 || s.PingMs > 500 {
		t.Fatalf("Messung mit Ping: %+v, %v", s, err)
	}
	mode.Store(1)
	if s, err = Measure(t.Context(), c, 4096); err != nil || s.PingMs != 0 {
		t.Fatalf("älterer Koordinator: %+v, %v; erwartet PingMs 0", s, err)
	}
	mode.Store(2)
	if s, err = Measure(t.Context(), c, 4096); err != nil || s.PingMs != 0 || s.CPUScore != 500 {
		t.Fatalf("gescheiterte Ping-Messung: %+v, %v; erwartet PingMs 0 ohne Fehler", s, err)
	}
}

func TestPingToMsNeverZero(t *testing.T) {
	if got := pingToMs(0); got != 0.1 {
		t.Fatalf("pingToMs(0) = %v, erwartet 0,1 (0 hieße „fehlt“)", got)
	}
	if got := pingToMs(23500 * time.Microsecond); got < 23.49 || got > 23.51 {
		t.Fatalf("pingToMs(23,5ms) = %v", got)
	}
}
