package karte

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Der Koordinator begrenzt die Karten-Anfragen (etwa 50 je Sekunde im
// Mittel) und antwortet darüber mit 429 und „Retry-After“. Das ist keine
// Störung: Der Starter wartet kurz und fragt noch einmal.

// ErrGedrosselt: Der Koordinator hat die Anfrage gedrosselt (429).
var ErrGedrosselt = errors.New("der Koordinator drosselt die Anfragen")

// MaxDrosselWarte: So lange wartet der Starter höchstens auf einmal, auch
// wenn der Koordinator mehr verlangt (Variable, damit Tests sie verkürzen).
var MaxDrosselWarte = 5 * time.Second

// MaxDrosselVersuche: So oft wird eine gedrosselte Anfrage wiederholt, bevor
// sie als Fehler gilt.
const MaxDrosselVersuche = 6

// Gedrosselt ist der Fehler zu ErrGedrosselt mit der verlangten Wartezeit.
type Gedrosselt struct{ Nach time.Duration }

func (e *Gedrosselt) Error() string {
	return fmt.Sprintf("Der Koordinator drosselt die Anfragen (Wartezeit %.0f s).", e.Nach.Seconds())
}

// Is lässt errors.Is(err, ErrGedrosselt) gelten.
func (e *Gedrosselt) Is(ziel error) bool { return ziel == ErrGedrosselt }

// DrosselWartezeit liest den Kopf „Retry-After“ (ganze Sekunden); fehlt er
// oder ist er unbrauchbar, gilt eine Sekunde. Höchstens 60 Sekunden.
func DrosselWartezeit(retryAfter string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(retryAfter))
	if err != nil || s < 1 {
		s = 1
	}
	return time.Duration(min(s, 60)) * time.Second
}

// Warte wartet d (höchstens MaxDrosselWarte) oder bis ctx endet (dann dessen
// Fehler).
func Warte(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(min(d, MaxDrosselWarte))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MitDrossel ruft f auf und wiederholt es nach der verlangten Wartezeit, solange
// der Koordinator drosselt (höchstens MaxDrosselVersuche mal); jeder andere
// Fehler und der Erfolg gelten sofort.
func MitDrossel[T any](ctx context.Context, f func() (T, error)) (T, error) {
	for versuch := 1; ; versuch++ {
		v, err := f()
		g, ok := errors.AsType[*Gedrosselt](err)
		if !ok || versuch >= MaxDrosselVersuche {
			return v, err
		}
		if werr := Warte(ctx, g.Nach); werr != nil {
			return v, werr
		}
	}
}
