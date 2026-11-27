package benachrichtigung

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
)

// Standardwerte des Dienstes.
const (
	// Anlauf: So lange nach dem Start wird der Stand nur nachgeführt. Nach
	// einem Neustart des Koordinators sind Spielerliste und „bereit“ erst mit
	// dem nächsten Lebenszeichen des Hosts (alle 10 s) wieder da; ohne die
	// Anlaufzeit würden sie als Ereignisse gemeldet.
	Anlauf = 30 * time.Second
	// Sammeln: So lange werden Beitritte und Abgänge zu einer Nachricht
	// zusammengefasst.
	Sammeln = 10 * time.Second
	// OnlineSperre: „Die Welt ist online“ für denselben Host höchstens so
	// oft (ein wackelndes Lebenszeichen soll keine Flut machen).
	OnlineSperre = 15 * time.Minute

	maxGruppe = 80
	maxWelt   = 40
	maxName   = 40
	maxGrund  = 120
)

// Optionen konfigurieren den Dienst.
type Optionen struct {
	Webhook Webhook
	// Gruppe: Titel der Gruppe für den Anfang jeder Nachricht (leer:
	// „wanderwelt“).
	Gruppe string
	// Arten stellt Schalter gegenüber dem Standard für Discord um.
	Arten map[ereignis.Art]bool
	// Client, Log: siehe SenderOptionen.
	Client Doer
	Log    *slog.Logger
	// Senden ersetzt den Sender (Tests): bekommt die fertigen Nachrichten.
	Senden func(text string)
	// Sender: Einstellungen des Senders (Webhook, Client und Log füllt der
	// Dienst selbst ein).
	Sender SenderOptionen
	// Anlauf und Sammeln überschreiben die Standardwerte (Tests).
	Anlauf, Sammeln time.Duration
}

// Dienst leitet aus den Status des Koordinators Nachrichten für Discord ab.
// Beobachte darf aus mehreren Goroutinen kommen und blockiert nie.
type Dienst struct {
	gruppe  string
	arten   map[ereignis.Art]bool
	senden  func(string)
	sender  *Sender
	anlauf  time.Duration
	sammeln time.Duration

	mu      sync.Mutex
	start   time.Time // Zeit der ersten Beobachtung
	letzter *api.Status
	// Gesammelte Beitritte und Abgänge und der Beginn des Sammelns.
	neu, weg []string
	welt     string
	seit     time.Time
	// Letzter Hinweis „online“ (gegen Wiederholungen).
	onlineHost string
	onlineZeit time.Time
}

// Neu baut den Dienst. Ohne o.Senden schickt er über einen Sender, den Lauf
// abarbeitet.
func Neu(o Optionen) *Dienst {
	d := &Dienst{
		gruppe:  strings.TrimSpace(o.Gruppe),
		arten:   o.Arten,
		senden:  o.Senden,
		anlauf:  o.Anlauf,
		sammeln: o.Sammeln,
	}
	if d.anlauf <= 0 {
		d.anlauf = Anlauf
	}
	if d.sammeln <= 0 {
		d.sammeln = Sammeln
	}
	if d.senden == nil {
		so := o.Sender
		so.Webhook, so.Client, so.Log = o.Webhook, o.Client, o.Log
		d.sender = NeuerSender(so)
		d.senden = d.sender.Senden
	}
	return d
}

// Lauf stellt die Nachrichten zu, bis ctx endet (blockiert; in eigener
// Goroutine aufrufen). Ohne Sender (Tests) kehrt er sofort zurück.
func (d *Dienst) Lauf(ctx context.Context) {
	if d.sender != nil {
		d.sender.Lauf(ctx)
	}
}

// an: Ist der Schalter art auf Discord an?
func (d *Dienst) an(art ereignis.Art) bool {
	if ereignis.NurWindows(art) {
		return false
	}
	if v, ok := d.arten[art]; ok {
		return v
	}
	return ereignis.Standard(ereignis.Discord, art)
}

// Beobachte bekommt den Status jetzt (im Takt des Koordinators). Die ersten
// Beobachtungen und alle in der Anlaufzeit führen nur den Vergleichsstand
// nach; danach werden Änderungen zu Nachrichten.
func (d *Dienst) Beobachte(st api.Status, jetzt time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.start.IsZero() {
		d.start = jetzt
	}
	prev := d.letzter
	d.letzter = &st
	if prev == nil || jetzt.Sub(d.start) < d.anlauf {
		return
	}
	for _, e := range ereignis.Aus(prev, &st) {
		if !d.an(e.Art) {
			continue
		}
		if e.Art == ereignis.Spieler {
			d.sammle(e, jetzt)
			continue
		}
		d.leereLocked() // Reihenfolge wahren: erst, was sich vorher ergeben hat
		d.sendeLocked(e, jetzt)
	}
	if (len(d.neu) > 0 || len(d.weg) > 0) && jetzt.Sub(d.seit) >= d.sammeln {
		d.leereLocked()
	}
}

// sammle merkt Beitritte und Abgänge; wer innerhalb der Sammelzeit kommt und
// geht (oder umgekehrt), fällt ganz weg.
func (d *Dienst) sammle(e ereignis.Ereignis, jetzt time.Time) {
	if len(d.neu) == 0 && len(d.weg) == 0 {
		d.seit = jetzt
	}
	d.welt = e.Welt
	gleich := func(a string) func(string) bool {
		return func(b string) bool { return strings.EqualFold(a, b) }
	}
	for _, n := range e.Neu {
		if i := slices.IndexFunc(d.weg, gleich(n)); i >= 0 {
			d.weg = slices.Delete(d.weg, i, i+1)
		} else if !slices.ContainsFunc(d.neu, gleich(n)) {
			d.neu = append(d.neu, n)
		}
	}
	for _, n := range e.Weg {
		if i := slices.IndexFunc(d.neu, gleich(n)); i >= 0 {
			d.neu = slices.Delete(d.neu, i, i+1)
		} else if !slices.ContainsFunc(d.weg, gleich(n)) {
			d.weg = append(d.weg, n)
		}
	}
}

// leereLocked macht aus dem Gesammelten eine Nachricht.
func (d *Dienst) leereLocked() {
	if len(d.neu) == 0 && len(d.weg) == 0 {
		return
	}
	e := ereignis.Ereignis{Art: ereignis.Spieler, Neu: d.neu, Weg: d.weg, Welt: d.welt}
	d.neu, d.weg, d.welt = nil, nil, ""
	d.senden(d.nachricht(e))
}

// sendeLocked schickt ein einzelnes Ereignis.
func (d *Dienst) sendeLocked(e ereignis.Ereignis, jetzt time.Time) {
	if e.Art == ereignis.WeltOnline {
		if e.WerKey != "" && e.WerKey == d.onlineHost && jetzt.Sub(d.onlineZeit) < OnlineSperre {
			return
		}
		d.onlineHost, d.onlineZeit = e.WerKey, jetzt
	}
	d.senden(d.nachricht(e))
}

// nachricht ist der fertige Text: „Gruppe · Welt: Meldung“.
func (d *Dienst) nachricht(e ereignis.Ereignis) string {
	kopf := Saeubern(d.gruppe, maxGruppe)
	if kopf == "" {
		kopf = "wanderwelt"
	}
	if w := Saeubern(e.Welt, maxWelt); w != "" {
		kopf += " · " + w
	}
	san := func(s string) string { return Saeubern(s, maxName) }
	if e.Art == ereignis.Wartung {
		san = func(s string) string { return Saeubern(s, maxGrund) }
	}
	return kuerze(kopf + ": " + ereignis.Text(e, san))
}
