package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
)

// Hinweise von Recover (der Stand eines verwaisten Servers wurde gesichert)
// erscheinen erst mit dem Ergebnis und in derselben Benachrichtigung davor:
// nie in zufälliger Reihenfolge zum Hinweis „verworfen“.
func TestHandleRecoverCombinesNotes(t *testing.T) {
	got := make(chan string, 4)
	old := notify
	t.Cleanup(func() { notify = old })
	notify = func(s string) { got <- s }

	de := &host.DiscardedError{Path: `C:\wanderwelt\verworfen-1`, Reason: "jemand hat inzwischen weitergespielt"}
	orphan := recoverNotes{host.OrphanSavedNotice}
	tests := []struct {
		name      string
		err       error
		pending   bool
		notes     recoverNotes
		want      string
		wantRetry bool
	}{
		{name: "verworfen", err: de, notes: orphan, want: host.OrphanSavedNotice + " " + discardText(de)},
		{name: "hochgeladen", notes: orphan, want: host.OrphanSavedNotice + " Der Stand ist hochgeladen."},
		{
			name: "später", err: fmt.Errorf("Upload-Zuschlag: %w", host.ErrPendingLater), pending: true, notes: orphan,
			want: host.OrphanSavedNotice + " Der Stand wird nachgereicht, sobald es geht.", wantRetry: true,
		},
		{name: "verworfen ohne Hinweis", err: de, want: discardText(de)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if retry := (&App{}).handleRecover(tt.err, tt.pending, tt.notes); retry != tt.wantRetry {
				t.Errorf("retry = %v, want %v", retry, tt.wantRetry)
			}
			select {
			case s := <-got:
				if s != tt.want {
					t.Errorf("Benachrichtigung %q, want %q", s, tt.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("keine Benachrichtigung")
			}
			select {
			case s := <-got:
				t.Errorf("zweite Benachrichtigung %q", s)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}

	// Ohne Hinweise und ohne etwas zu melden: keine Benachrichtigung.
	if (&App{}).handleRecover(nil, false, nil) {
		t.Error("retry ohne Vormerkung")
	}
	select {
	case s := <-got:
		t.Errorf("Benachrichtigung ohne Anlass: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}
