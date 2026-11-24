package api

import (
	"errors"
	"testing"
)

// Ein Server darf eine Welt mit gleicher, älterer oder unbekannter Version
// laden; eine neuere oder nicht vergleichbare (Snapshot) nicht.
func TestCheckWorldVersion(t *testing.T) {
	for _, tc := range []struct {
		world, hosted string
		want          error
	}{
		{"26.2", "26.2", nil},
		{"", "26.2", nil},
		{"26.2", "", nil},
		{"26.1", "26.2", nil},
		{"26.2", "26.2.1", nil},
		{"26.2-rc1", "26.2", nil},
		{"1.21.4", "26.2", nil},
		{"26w14a", "26w14a", nil},
		{"26.2.0", "26.2", nil},
		{"26.3", "26.2", ErrWorldNewer},
		{"26.2.1", "26.2", ErrWorldNewer},
		{"26.2", "26.2-rc1", ErrWorldNewer},
		{"26w14a", "26.2", ErrVersionsIncomparable},
		{"26.2", "26w14a", ErrVersionsIncomparable},
	} {
		err := CheckWorldVersion(tc.world, tc.hosted)
		if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
			t.Errorf("CheckWorldVersion(%q, %q) = %v, want %v", tc.world, tc.hosted, err, tc.want)
		}
	}
}
