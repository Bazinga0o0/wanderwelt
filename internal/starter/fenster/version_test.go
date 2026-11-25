package fenster

import (
	"errors"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// versionsDaten baut daten mit zählenden Attrappen für den Versionsabgleich.
func versionsDaten(eigene string, installiert string, starts *int) *daten {
	d := neueDaten(nil, nil)
	d.eigeneVersion = eigene
	d.versionTakt = 0
	d.installiertIst = func(v string) bool { return v == installiert }
	d.neustart = func() error { *starts++; return nil }
	return d
}

func TestVersionPruefen(t *testing.T) {
	z := func(v string) *fensterapi.Zustand { return &fensterapi.Zustand{Version: v} }

	t.Run("gleich: nichts", func(t *testing.T) {
		n := 0
		d := versionsDaten("1.2", "1.2", &n)
		d.versionPruefen(z("1.2"))
		if n != 0 || d.lesen().veraltet {
			t.Errorf("starts %d, veraltet %v", n, d.lesen().veraltet)
		}
	})
	t.Run("leer: nichts", func(t *testing.T) {
		n := 0
		d := versionsDaten("1.2", "1.3", &n)
		d.versionPruefen(z(""))
		d2 := versionsDaten("", "1.3", &n)
		d2.versionPruefen(z("1.3"))
		if n != 0 || d.lesen().veraltet || d2.lesen().veraltet {
			t.Errorf("starts %d", n)
		}
	})
	t.Run("anders: genau ein Neustart", func(t *testing.T) {
		n := 0
		d := versionsDaten("1.2", "1.3", &n)
		for i := 0; i < 5; i++ {
			d.versionPruefen(z("1.3"))
		}
		if n != 1 {
			t.Errorf("starts %d, want 1", n)
		}
	})
	t.Run("exe noch alt: nur Hinweis, später Neustart", func(t *testing.T) {
		n := 0
		inst := "1.2"
		d := versionsDaten("1.2", "", &n)
		d.installiertIst = func(v string) bool { return v == inst }
		d.versionPruefen(z("1.3"))
		if n != 0 || !d.lesen().veraltet {
			t.Fatalf("starts %d, veraltet %v", n, d.lesen().veraltet)
		}
		inst = "1.3"
		d.versionPruefen(z("1.3"))
		if n != 1 {
			t.Errorf("starts %d, want 1", n)
		}
	})
	t.Run("Neustart scheitert: Hinweis, kein zweiter Versuch", func(t *testing.T) {
		n := 0
		d := versionsDaten("1.2", "1.3", &n)
		d.neustart = func() error { n++; return errors.New("nein") }
		d.versionPruefen(z("1.3"))
		d.versionPruefen(z("1.3"))
		if n != 1 || !d.lesen().veraltet {
			t.Errorf("starts %d, veraltet %v", n, d.lesen().veraltet)
		}
	})
}
