package api

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Gründe, aus denen ein Server eine Welt nicht laden darf (CheckWorldVersion).
var (
	// ErrWorldNewer: Die Welt ist mit einer neueren Minecraft-Version
	// gespeichert; ein älterer Server würde sie beschädigen.
	ErrWorldNewer = errors.New("Welt mit neuerer Minecraft-Version gespeichert")
	// ErrVersionsIncomparable: Die Versionen lassen sich nicht vergleichen
	// (etwa Snapshots wie "26w14a").
	ErrVersionsIncomparable = errors.New("Minecraft-Versionen nicht vergleichbar")
)

// CheckWorldVersion meldet, ob ein Server der Version hosted eine Welt laden
// darf, die mit der Version world gespeichert wurde: ja bei gleicher,
// älterer (der Server hebt sie an) oder unbekannter ("") Version;
// ErrWorldNewer bei einer neueren, ErrVersionsIncomparable, wenn sich die
// Versionen nicht vergleichen lassen. Dieselbe Regel gilt beim Hosten und
// beim Zurückholen eines Stands.
func CheckWorldVersion(world, hosted string) error {
	if world == "" || hosted == "" || world == hosted {
		return nil
	}
	c, err := CompareVersions(world, hosted)
	if err != nil {
		return ErrVersionsIncomparable
	}
	if c > 0 {
		return ErrWorldNewer
	}
	return nil
}

var versionRE = regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?(-[0-9A-Za-z.\-]+)?$`)

// CompareVersions vergleicht zwei Minecraft-Versionen der Form "26.2",
// "26.1.1", "1.21.4" oder mit Vorabzusatz "26.2-rc1" (vor "26.2"): -1, 0
// oder 1. Snapshots ("26w14a") sind nicht vergleichbar (Fehler).
func CompareVersions(a, b string) (int, error) {
	va, pa, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, pb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range va {
		if va[i] != vb[i] {
			if va[i] < vb[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case pa == pb:
		return 0, nil
	case pa: // Vorabversion vor der Vollversion
		return -1, nil
	default:
		return 1, nil
	}
}

func parseVersion(s string) (v [3]int, pre bool, err error) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return v, false, fmt.Errorf("Version %q nicht erkannt", s)
	}
	for i := range 3 {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return v, false, fmt.Errorf("Version %q nicht erkannt", s)
		}
		v[i] = n
	}
	return v, m[4] != "", nil
}

// ReleaseVersion zerlegt eine Release-Version des Starters
// "MAJOR.MINOR.PATCH" (ein "v" davor ist erlaubt, Tag v0.5.0 → 0.5.0). Nur
// Ziffern ohne führende Null; Vorabversionen ("0.5.0-rc.1"), Snapshots
// ("0.0.0-SNAPSHOT-<commit>"), "dev" und Commit-Kurzformen sind keine
// Release-Version (ok false).
func ReleaseVersion(s string) (v [3]int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		for j := 0; j < len(p); j++ {
			if p[j] < '0' || p[j] > '9' {
				return v, false
			}
		}
		v[i], _ = strconv.Atoi(p)
	}
	return v, true
}

// StarterSHA256 liefert den SHA-256 aus SHA256 als kleingeschriebenes Hex
// (64 Zeichen), mit oder ohne "sha256:" davor; ok false, wenn er keiner ist.
func (r StarterRelease) StarterSHA256() (hexSum string, ok bool) {
	h := strings.ToLower(strings.TrimSpace(r.SHA256))
	h = strings.TrimPrefix(h, "sha256:")
	if len(h) != 64 {
		return "", false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return "", false
		}
	}
	return h, true
}
