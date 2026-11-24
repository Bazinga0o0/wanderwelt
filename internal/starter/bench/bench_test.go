package bench

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
