package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// fakeReplayClock ersetzt die Uhr des neuen Fragens nach dem Zurückholen:
// fire lässt sie ablaufen.
func fakeReplayClock(t *testing.T) (fire func()) {
	t.Helper()
	ch := make(chan time.Time, 1)
	var waited atomic.Int64
	old := restoreReplayAfter
	restoreReplayAfter = func(d time.Duration) <-chan time.Time {
		waited.Store(int64(d))
		return ch
	}
	t.Cleanup(func() { restoreReplayAfter = old })
	return func() {
		t.Helper()
		waitFor(t, "Warten auf das neue Fragen", func() bool { return waited.Load() != 0 })
		if d := time.Duration(waited.Load()); d != 3*time.Second {
			t.Fatalf("wartet %v, erwartet 3 s", d)
		}
		ch <- time.Now()
	}
}

// replayCoord ist ein Koordinator, bei dem niemand hostet und das Manifest
// nicht zu haben ist („Spielen“ scheitert gleich mit einem Hinweis).
func replayCoord(t *testing.T) *client.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/status", "/v1/me":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"error":"kaputt"}`, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(ts.Close)
	return client.New(&group.Group{Coordinator: ts.URL})
}

func restoreErr() error {
	return &host.RestoreError{Notice: api.RestoreNotice{
		By:            "Chef",
		TargetCreated: time.Date(2026, 11, 23, 11, 5, 0, 0, time.UTC),
		DelayS:        30,
	}}
}

// Endet die Host-Sitzung wegen Zurückholen, kommt ein Hinweis, und nach
// 3 s fragt wanderwelt von selbst neu: wie „Spielen“, oder – kam die
// Sitzung vom Dauerbetrieb – wie sein Takt. Läuft dann schon ein „Spielen“
// (etwa das des Dauerbetriebs), kommt kein zweites.
func TestRestoreEndReplays(t *testing.T) {
	want := "Chef hat den Stand von " + time.Date(2026, 11, 23, 11, 5, 0, 0, time.UTC).In(time.Local).Format("15:04") +
		" zurückgeholt – wanderwelt startet die Welt neu."

	t.Run("wie Spielen", func(t *testing.T) {
		notes := countNotify(t)
		fire := fakeReplayClock(t)
		f := installFakeDauerPlay(t, nil)
		a := &App{ctx: t.Context(), c: replayCoord(t)}
		a.hostEnded(restoreErr(), false, false, false)
		waitFor(t, "Hinweis", func() bool { return len(notes()) == 1 })
		if n := notes(); n[0] != want {
			t.Fatalf("Hinweis %q, erwartet %q", n[0], want)
		}
		if b := busyOf(a); b != "" {
			t.Fatalf("vor den 3 s läuft schon %q", b)
		}
		fire()
		// „Spielen“ läuft und scheitert hier am Manifest – mit dem Hinweis von
		// „Spielen“.
		waitFor(t, "Spielen", func() bool { return len(notes()) == 2 })
		if n := notes(); !strings.HasPrefix(n[1], "Spielen: ") {
			t.Fatalf("Hinweis %q, erwartet den von „Spielen“", n[1])
		}
		if c := f.calls.Load(); c != 0 {
			t.Fatalf("%d× Dauerbetrieb-Play, erwartet keins", c)
		}
	})

	t.Run("Dauerbetrieb: wie sein Takt", func(t *testing.T) {
		notes := countNotify(t)
		fire := fakeReplayClock(t)
		f := installFakeDauerPlay(t, nil)
		old := onACPower
		onACPower = func() bool { return true }
		t.Cleanup(func() { onACPower = old })
		a := &App{ctx: t.Context(), c: replayCoord(t)}
		a.dauer.on.Store(true)
		a.hostEnded(restoreErr(), false, false, true)
		fire()
		waitFor(t, "Dauerbetrieb-Play", func() bool { return f.calls.Load() == 1 })
		if b := busyOf(a); b != dauerLabel {
			t.Fatalf("laufende Aktion %q, erwartet %q", b, dauerLabel)
		}
		f.release()
		waitFor(t, "Ende des Plays", func() bool { return busyOf(a) == "" })
		if n := notes(); len(n) != 1 || n[0] != want {
			t.Fatalf("Hinweise %q, erwartet nur %q", n, want)
		}
	})

	for _, dauer := range []bool{false, true} {
		name := "Spielen"
		if dauer {
			name = "Dauerbetrieb"
		}
		t.Run(name+": Takt des Dauerbetriebs war schneller", func(t *testing.T) {
			notes := countNotify(t)
			fire := fakeReplayClock(t)
			f := installFakeDauerPlay(t, nil)
			old := onACPower
			onACPower = func() bool { return true }
			t.Cleanup(func() { onACPower = old })
			a := &App{ctx: t.Context(), c: replayCoord(t), status: freeStatus}
			a.dauer.on.Store(true)
			a.hostEnded(restoreErr(), false, false, dauer)
			if !a.autoPlay() {
				t.Fatal("Takt fordert nicht an")
			}
			waitFor(t, "Dauerbetrieb-Play", func() bool { return f.calls.Load() == 1 })
			fire()
			time.Sleep(50 * time.Millisecond)
			if c := f.calls.Load(); c != 1 {
				t.Fatalf("%d× Dauerbetrieb-Play, erwartet 1", c)
			}
			if a.dauer.manual.Load() {
				t.Fatal("das laufende Play wurde zum gewöhnlichen „Spielen“")
			}
			if n := notes(); len(n) != 1 || n[0] != want {
				t.Fatalf("Hinweise %q, erwartet nur %q", n, want)
			}
		})
	}
}
