package fenster

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	for _, c := range []struct {
		vor  time.Duration
		want string
	}{
		{0, "gerade eben"},
		{59 * time.Second, "gerade eben"},
		{-5 * time.Second, "gerade eben"}, // Uhr des Koordinators geht vor
		{time.Minute, "vor 1 Min."},
		{90 * time.Second, "vor 1 Min."},
		{2 * time.Minute, "vor 2 Min."},
		{2*time.Minute + 59*time.Second, "vor 2 Min."},
		{59 * time.Minute, "vor 59 Min."},
		{3 * time.Hour, "vor 3 Std."},
		{23 * time.Hour, "vor 23 Std."},
		{16 * time.Hour, "vor 16 Std."},
		{30 * time.Hour, "gestern"},
		{4 * 24 * time.Hour, "vor 4 Tagen"},
		{60 * 24 * time.Hour, "24.09.2026"},
	} {
		if got := seit(jetzt.Add(-c.vor), jetzt); got != c.want {
			t.Errorf("seit(-%v) = %q, want %q", c.vor, got, c.want)
		}
	}

	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{-time.Minute, "gleich"},
		{30 * time.Second, "gleich"},
		{time.Minute, "in 1 Min."},
		{2*time.Minute + 30*time.Second, "in 3 Min."},
		{5 * time.Minute, "in 5 Min."},
		{2 * time.Hour, "in 2 Std."},
	} {
		if got := bis(jetzt.Add(c.in), jetzt); got != c.want {
			t.Errorf("bis(+%v) = %q, want %q", c.in, got, c.want)
		}
	}

	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 11, 23, 14, 5, 0, 0, time.Local), "heute 14:05"},
		{time.Date(2026, 11, 22, 21, 30, 0, 0, time.Local), "gestern 21:30"},
		{time.Date(2026, 11, 16, 9, 0, 0, 0, time.Local), "Mo., 16.11. 09:00"},
		{time.Date(2025, 11, 18, 21, 30, 0, 0, time.Local), "18.11.2025 21:30"},
	} {
		if got := zeitpunkt(c.t, jetzt); got != c.want {
			t.Errorf("zeitpunkt(%v) = %q, want %q", c.t, got, c.want)
		}
	}

	for _, c := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{900, "900 B"},
		{12 << 10, "12 KB"},
		{350 << 20, "350 MB"},
		{1288490188, "1,2 GB"}, // 1,2 GiB
		{1 << 30, "1,0 GB"},
		{20 << 30, "20 GB"},
		{1536 << 30, "1,5 TB"},
	} {
		if got := groesse(c.n); got != c.want {
			t.Errorf("groesse(%d) = %q, want %q", c.n, got, c.want)
		}
	}

	if got := gb(6144); got != "6 GB" {
		t.Errorf("gb(6144) = %q", got)
	}
	if got := gb(5632); got != "5,5 GB" {
		t.Errorf("gb(5632) = %q", got)
	}
	for _, c := range []struct {
		in string
		p  float32
		ok bool
	}{
		{"lädt die Welt: 45 %", 0.45, true},
		{"Download 100%", 1, true},
		{"startet den Server …", 0, false},
		{"45 % von 120 %", 0, false},
		{"%", 0, false},
	} {
		p, ok := prozent(c.in)
		if ok != c.ok || p != c.p {
			t.Errorf("prozent(%q) = %v, %v; want %v, %v", c.in, p, ok, c.p, c.ok)
		}
	}
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"20", 20 << 30, true},
		{"12,5", 12<<30 + 1<<29, true},
		{" 8 GB ", 8 << 30, true},
		{"0", 0, false},
		{"zwanzig", 0, false},
	} {
		n, err := gbEingabe(c.in)
		if (err == nil) != c.ok || n != c.want {
			t.Errorf("gbEingabe(%q) = %d, %v; want %d", c.in, n, err, c.want)
		}
	}
}
