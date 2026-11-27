package lease

import (
	"path/filepath"
	"testing"
	"time"
)

// Info.LastBeat ist die Zeit der Vergabe und danach des letzten
// Lebenszeichens (für die Gesundheit der Admins).
func TestInfoLastBeat(t *testing.T) {
	clk := newFakeClock()
	m, err := New(filepath.Join(t.TempDir(), "lease.json"), clk)
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.Acquire(Candidate{Email: "a@example.org", MachineID: "pc-a", AccountID: 1})
	if err != nil {
		t.Fatal(err)
	}
	start := clk.Now()
	if info, ok := m.Info(); !ok || !info.LastBeat.Equal(start) {
		t.Fatalf("nach der Vergabe: %+v ok=%v, want LastBeat %v", info, ok, start)
	}
	clk.Advance(30 * time.Second)
	if info, _ := m.Info(); !info.LastBeat.Equal(start) {
		t.Errorf("ohne Lebenszeichen wandert LastBeat: %v", info.LastBeat)
	}
	if err := m.Heartbeat(g.LeaseID, "pc-a"); err != nil {
		t.Fatal(err)
	}
	if info, _ := m.Info(); !info.LastBeat.Equal(clk.Now()) {
		t.Errorf("nach dem Lebenszeichen: %v, want %v", info.LastBeat, clk.Now())
	}
}
