package karte

import (
	"sync"
	"time"
)

// Ratenbegrenzung der Karten-Endpunkte je Mitglied (Eimer mit Marken): so
// viele Anfragen je Sekunde im Mittel, kurzzeitig bis zum Fassungsvermögen.
// Großzügig, damit eine Karte aufbaubar ist (ein Bildschirm hat einige
// Dutzend Kacheln), und doch eine Grenze für den kleinen Server.
const (
	AnfragenProSekunde = 50.0
	AnfragenSpitze     = 150.0
)

type eimer struct {
	marken float64
	stand  time.Time
}

type begrenzer struct {
	jetzt func() time.Time
	mu    sync.Mutex
	e     map[string]*eimer
}

func newBegrenzer(jetzt func() time.Time) *begrenzer {
	return &begrenzer{jetzt: jetzt, e: map[string]*eimer{}}
}

// erlaube entnimmt eine Marke für key; false, wenn der Eimer leer ist.
func (b *begrenzer) erlaube(key string) bool {
	now := b.jetzt()
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.e[key]
	if e == nil {
		// Schlüssel sind nur Mitglieder (vor dem Aufruf geprüft); trotzdem
		// begrenzen, damit die Tabelle nie unbegrenzt wächst.
		if len(b.e) >= 4096 {
			for k, o := range b.e {
				if now.Sub(o.stand) > time.Minute {
					delete(b.e, k)
				}
			}
			if len(b.e) >= 4096 {
				return false
			}
		}
		e = &eimer{marken: AnfragenSpitze, stand: now}
		b.e[key] = e
	}
	if dt := now.Sub(e.stand).Seconds(); dt > 0 {
		e.marken = min(AnfragenSpitze, e.marken+dt*AnfragenProSekunde)
		e.stand = now
	}
	if e.marken < 1 {
		return false
	}
	e.marken--
	return true
}

// Erlaube meldet, ob das Mitglied key jetzt eine Karten-Anfrage stellen
// darf (sonst 429).
func (d *Dienst) Erlaube(key string) bool { return d.lim.erlaube(key) }
