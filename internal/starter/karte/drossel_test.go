package karte

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrosselWartezeit(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1": time.Second, "3": 3 * time.Second, " 2 ": 2 * time.Second, "": time.Second, "0": time.Second,
		"-5": time.Second, "abc": time.Second, "Wed, 21 Oct 2026 07:28:00 GMT": time.Second, "9999": 60 * time.Second,
	} {
		if got := DrosselWartezeit(in); got != want {
			t.Errorf("DrosselWartezeit(%q) = %v, want %v", in, got, want)
		}
	}
	if !errors.Is(&Gedrosselt{Nach: time.Second}, ErrGedrosselt) {
		t.Error("Gedrosselt ist nicht ErrGedrosselt")
	}
}

func TestMitDrossel(t *testing.T) {
	alt := MaxDrosselWarte
	MaxDrosselWarte = 2 * time.Millisecond
	t.Cleanup(func() { MaxDrosselWarte = alt })
	n := 0
	v, err := MitDrossel(t.Context(), func() (int, error) {
		n++
		if n < 4 {
			return 0, &Gedrosselt{Nach: time.Second}
		}
		return 42, nil
	})
	if v != 42 || err != nil || n != 4 {
		t.Errorf("v=%d err=%v n=%d", v, err, n)
	}
	// Andere Fehler werden nie wiederholt.
	n = 0
	_, err = MitDrossel(t.Context(), func() (int, error) { n++; return 0, errors.New("kaputt") })
	if err == nil || n != 1 {
		t.Errorf("anderer Fehler: err=%v n=%d", err, n)
	}
	// Dauerhaft gedrosselt: nach MaxDrosselVersuche der Fehler.
	n = 0
	_, err = MitDrossel(t.Context(), func() (int, error) { n++; return 0, &Gedrosselt{Nach: time.Second} })
	if !errors.Is(err, ErrGedrosselt) || n != MaxDrosselVersuche {
		t.Errorf("dauerhaft: err=%v n=%d", err, n)
	}
	// Abbruch beendet das Warten.
	MaxDrosselWarte = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err = MitDrossel(ctx, func() (int, error) { return 0, &Gedrosselt{Nach: time.Minute} })
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Errorf("Abbruch: err=%v nach %v", err, time.Since(start))
	}
}
