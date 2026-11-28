package fenster

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/karte"
)

// Reiter „Karte“ (nur Mitglieder, nur wenn der Status der gewählten Welt
// `karte` nennt): die Oberwelt als Draufsicht. Solange der Reiter offen ist,
// läuft eine Sitzung: ein Lader holt die sichtbaren Kacheln, eine Goroutine
// prüft das Manifest alle 60 s. Wird der Reiter verlassen oder das Fenster
// geschlossen, bricht die Sitzung alles ab und wartet auf das Ende ihrer
// Goroutinen. Das Fenster spricht nur mit der eigenen Instanz (fensterapi);
// die holt Manifest und Kacheln mit dem Token der Gruppe vom Koordinator.

const (
	// karteManifestFrist: Obergrenze eines Manifest-Abrufs.
	karteManifestFrist = 50 * time.Second
	// karteStatusAbstand: Abstand zwischen Karte und Statusleiste (dp).
	karteStatusAbstand = 8
)

// Takte der Manifest-Prüfung (Variablen, damit Tests sie verkürzen):
// karteManifestTakt, solange der Reiter offen ist; karteErsterVersuch, solange
// es noch kein Manifest gibt.
var (
	karteManifestTakt  = 60 * time.Second
	karteErsterVersuch = 15 * time.Second
)

// karteQuelle ist, was die Karte von der Instanz braucht (*fensterapi.Client).
type karteQuelle interface {
	KarteManifest(ctx context.Context, welt string) (*karte.Manifest, error)
	KarteKachel(ctx context.Context, welt string, k karte.Kachel, etag string) (karte.Antwort, error)
}

var _ karteQuelle = (*fensterapi.Client)(nil)

// karteQuelle: die Quelle der aktuellen Verbindung (nil: keine oder eine
// ohne Karte).
func (d *daten) karteQuelle() karteQuelle {
	d.mu.Lock()
	c := d.c
	d.mu.Unlock()
	q, _ := c.(karteQuelle)
	return q
}

// karteInfoFuer: die gewählte Welt und der Kartenstand aus dem Status; nil,
// wenn es keinen Reiter geben soll (kein Mitglied, keine Welt, Status ohne
// `karte`).
func karteInfoFuer(z *fensterapi.Zustand) (welt string, info *api.KarteInfo) {
	if z == nil || z.Me == nil || z.Status == nil || z.Welt.Aktiv == "" {
		return "", nil
	}
	for _, w := range z.Status.Worlds {
		if w.ID == z.Welt.Aktiv && w.Karte != nil {
			return w.ID, w.Karte
		}
	}
	return "", nil
}

// aktiveGruppe: Kennung der aktiven Gruppe (für den Ordner des Zwischenspeichers).
func aktiveGruppe(z *fensterapi.Zustand) string {
	if z != nil {
		for _, g := range z.Gruppen {
			if g.Aktiv {
				return g.ID
			}
		}
	}
	return "gruppe"
}

// reiterZulaessig: Gibt es den Reiter r für diesen Zustand? (Reiter nur für
// Admins, Reiter Karte nur mit Kartenstand der gewählten Welt.)
func reiterZulaessig(r reiter, admin bool, z *fensterapi.Zustand) bool {
	switch {
	case nurAdmin(r):
		return admin
	case r == reiterKarte:
		_, info := karteInfoFuer(z)
		return info != nil
	}
	return true
}

// karteAnsicht ist der Zustand des Reiters. Alles darin gehört der
// Fenster-Goroutine; die Sitzung hat ihren eigenen Schutz.
type karteAnsicht struct {
	// wurzel: Kontext des Fensters (nil: Background); Run setzt ihn.
	wurzel context.Context

	sitz *karteSitzung
	// merk: die zuletzt gesehene Ansicht je Gruppe/Welt (Reiterwechsel).
	merk map[string]karte.Ansicht

	bild kartenBild

	plus, minus, mitte, neu widget.Clickable
}

// karteSitzung gilt, solange der Reiter für eine Gruppe, Welt und Verbindung
// offen ist.
type karteSitzung struct {
	schluessel string
	quelle     karteQuelle
	welt       string
	lader      *karte.Lader[kartenTextur]
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	wecker     chan struct{} // „Neu laden“
	minimiert  func() bool
	malen      func()
	jetzt      func() time.Time

	mu       sync.Mutex
	manifest *karte.Manifest
	fehler   error // letzter Fehler beim Manifest (nil: ok)
	laedt    bool  // ein Abruf läuft
	geprueft time.Time
}

// holerAdapter lässt den Lader über die Quelle abrufen.
type holerAdapter struct {
	q    karteQuelle
	welt string
}

func (h holerAdapter) Kachel(ctx context.Context, k karte.Kachel, etag string) (karte.Antwort, error) {
	return h.q.KarteKachel(ctx, h.welt, k, etag)
}

// platteFuer öffnet den Zwischenspeicher auf der Platte; ohne (Fehler) läuft
// die Karte nur mit dem Arbeitsspeicher.
func platteFuer(gruppe, welt string) *karte.Platte {
	p, err := karte.NeuePlatte(karte.OrdnerFuer(group.Home(), gruppe, welt), karte.PlatteMax)
	if err != nil {
		return nil
	}
	return p
}

func (v *karteAnsicht) kontext() context.Context {
	if v.wurzel != nil {
		return v.wurzel
	}
	return context.Background()
}

// abgleich öffnet oder schließt die Sitzung je nach sichtbarem Reiter.
func (v *karteAnsicht) abgleich(u *oberflaeche, s schnappschuss) {
	welt, info := karteInfoFuer(s.z)
	var q karteQuelle
	if u.reiter == reiterKarte && info != nil && s.verb == verbunden {
		q = u.d.karteQuelle()
	}
	if q == nil {
		v.schliessen()
		return
	}
	gruppe := aktiveGruppe(s.z)
	schluessel := gruppe + "\x00" + welt
	if v.sitz != nil {
		if v.sitz.schluessel == schluessel && v.sitz.quelle == q {
			return
		}
		v.schliessen()
	}
	v.oeffnen(u, gruppe, welt, schluessel, q)
}

func (v *karteAnsicht) oeffnen(u *oberflaeche, gruppe, welt, schluessel string, q karteQuelle) {
	if v.merk == nil {
		v.merk = map[string]karte.Ansicht{}
	}
	if a, ok := v.merk[schluessel]; ok {
		v.bild.sicht, v.bild.hatSicht = a, true
	} else {
		v.bild.hatSicht = false
	}
	ctx, cancel := context.WithCancel(v.kontext())
	sz := &karteSitzung{
		schluessel: schluessel, quelle: q, welt: welt, cancel: cancel,
		wecker: make(chan struct{}, 1), minimiert: u.d.minimiert, jetzt: u.d.jetzt,
		malen: func() { u.d.malen(true) },
	}
	sz.lader = karte.NeuerLader(ctx, holerAdapter{q, welt}, platteFuer(gruppe, welt), neueTextur, sz.malen)
	v.sitz = sz
	sz.wg.Add(1)
	go sz.lauf(ctx)
}

// schliessen beendet die Sitzung: Abrufe abbrechen, Goroutinen abwarten.
func (v *karteAnsicht) schliessen() {
	sz := v.sitz
	if sz == nil {
		return
	}
	v.sitz = nil
	if v.merk != nil && v.bild.hatSicht {
		v.merk[sz.schluessel] = v.bild.sicht
	}
	sz.stoppen()
}

func (sz *karteSitzung) stoppen() {
	sz.cancel()
	sz.lader.Stopp()
	sz.wg.Wait()
}

// beenden: Das Fenster geht zu.
func (u *oberflaeche) beenden() { u.karte.schliessen() }

// lauf prüft das Manifest: gleich beim Öffnen, dann alle 60 s (solange das
// Fenster nicht minimiert ist) und auf Wunsch („Neu laden“).
func (sz *karteSitzung) lauf(ctx context.Context) {
	defer sz.wg.Done()
	for {
		if sz.minimiert == nil || !sz.minimiert() {
			sz.pruefen(ctx)
		}
		warte := karteManifestTakt
		sz.mu.Lock()
		if sz.manifest == nil {
			warte = karteErsterVersuch
		}
		sz.mu.Unlock()
		t := time.NewTimer(warte)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-sz.wecker:
			t.Stop()
		}
	}
}

// pruefen holt das Manifest; ein anderes ersetzt das alte im Lader (nur
// geänderte Hashes laden Kacheln nach).
func (sz *karteSitzung) pruefen(ctx context.Context) {
	sz.mu.Lock()
	sz.laedt = true
	sz.mu.Unlock()
	sz.malen()
	actx, cancel := context.WithTimeout(ctx, karteManifestFrist)
	m, err := karte.MitDrossel(actx, func() (*karte.Manifest, error) { return sz.quelle.KarteManifest(actx, sz.welt) })
	cancel()
	if ctx.Err() != nil {
		return
	}
	sz.mu.Lock()
	sz.laedt = false
	sz.geprueft = sz.jetzt()
	sz.fehler = err
	neu := err == nil && !m.Gleich(sz.manifest)
	if neu {
		sz.manifest = m
	}
	sz.mu.Unlock()
	if neu {
		sz.lader.Setzen(m)
	}
	sz.malen()
}

// stand ist ein Schnappschuss der Sitzung fürs Zeichnen.
type karteStand struct {
	manifest *karte.Manifest
	fehler   error
	laedt    bool
}

func (sz *karteSitzung) stand() karteStand {
	sz.mu.Lock()
	defer sz.mu.Unlock()
	return karteStand{manifest: sz.manifest, fehler: sz.fehler, laedt: sz.laedt}
}

// neuLaden: Manifest sofort neu holen, gescheiterte Kacheln erneut versuchen.
func (sz *karteSitzung) neuLaden() {
	sz.lader.FehlerVergessen()
	select {
	case sz.wecker <- struct{}{}:
	default:
	}
}

// fehlerText macht aus einem Fehler der Karte eine Meldung (Titel, Text).
func karteFehlerText(err error) (titel, text string) {
	switch {
	case errors.Is(err, karte.ErrNochNichtErzeugt):
		return "Die Karte ist noch nicht erzeugt", "Der Koordinator hat für diese Welt noch keine Karte gerendert. Sie erscheint nach dem nächsten Stand; das Fenster prüft von selbst, ob sie da ist."
	case errors.Is(err, karte.ErrManifestUngueltig):
		return "Die Karte ist ungültig", "Das Manifest der Karte lässt sich nicht lesen (" + strings.TrimPrefix(err.Error(), karte.ErrManifestUngueltig.Error()+": ") + "). Ein anderer Stand der Karte oder ein Update des Koordinators behebt das."
	case errors.Is(err, fensterapi.ErrLaeuftNicht):
		return "wanderwelt antwortet nicht", "Die laufende wanderwelt ist nicht erreichbar. „Neu laden“ versucht es noch einmal."
	}
	return "Der Koordinator ist nicht erreichbar", err.Error()
}

// layout zeichnet den Reiter.
func (v *karteAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	sz := v.sitz
	if sz == nil {
		// Nur für einen Augenblick (die Sitzung öffnet im nächsten Bild) oder
		// ohne Verbindung zur Instanz.
		return u.mittig(gtx, t.leise("Die Karte wird vorbereitet …"))
	}
	st := sz.stand()
	if v.plus.Clicked(gtx) {
		v.bild.zoomMitte(knopfSchritt, st.manifest)
	}
	if v.minus.Clicked(gtx) {
		v.bild.zoomMitte(1/knopfSchritt, st.manifest)
	}
	if v.mitte.Clicked(gtx) && st.manifest != nil {
		v.bild.zurMitte(st.manifest)
	}
	if v.neu.Clicked(gtx) {
		sz.neuLaden()
	}

	return layout.Inset{Top: 12, Bottom: 14, Left: 20, Right: 20}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				// Fehler, solange schon eine Karte da ist: kleine Warnung über der Karte.
				if st.fehler == nil || st.manifest == nil {
					return layout.Dimensions{}
				}
				titel, text := karteFehlerText(st.fehler)
				return layout.Inset{Bottom: karteStatusAbstand}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return t.meldung(gtx, meldungWarn, "Karte nicht aktualisiert – "+titel, text)
				})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = gtx.Constraints.Max
				return v.flaeche(gtx, t, st, sz)
			}),
			layout.Rigid(layout.Spacer{Height: karteStatusAbstand}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return v.statusleiste(gtx, t, st, sz) }),
		)
	})
}

// flaeche: die Karte mit Rand, oder die Meldung, solange es keine gibt.
func (v *karteAnsicht) flaeche(gtx layout.Context, t *thema, st karteStand, sz *karteSitzung) layout.Dimensions {
	size := gtx.Constraints.Max
	if st.manifest == nil {
		return layout.Stack{Alignment: layout.Center}.Layout(gtx,
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				rechteck(gtx, size, t.f.flaeche2, gtx.Dp(10))
				return layout.Dimensions{Size: size}
			}),
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(40), gtx.Dp(520))
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				if st.fehler != nil {
					titel, text := karteFehlerText(st.fehler)
					art := meldungFehler
					if errors.Is(st.fehler, karte.ErrNochNichtErzeugt) {
						art = meldungInfo
					}
					return t.meldung(gtx, art, titel, text)
				}
				return t.leise("Die Karte wird geladen …")(gtx)
			}),
		)
	}
	return hinterlegt(gtx, t.f.rand, t.f.rand, 10, func(gtx layout.Context) layout.Dimensions {
		inner := layout.UniformInset(1)
		return inner.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return v.bild.layout(gtx, t, st.manifest, sz.lader)
		})
	})
}

// statusleiste: Stand, Zoom, Mauskoordinate links; Knöpfe rechts.
func (v *karteAnsicht) statusleiste(gtx layout.Context, t *thema, st karteStand, sz *karteSitzung) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: 13, farbe: t.f.leise, zeilen: 1}, v.statusText(gtx.Now, st, sz))
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, &v.minus, "−", knopfNeben, st.manifest != nil)
		}),
		layout.Rigid(layout.Spacer{Width: 6}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, &v.plus, "+", knopfNeben, st.manifest != nil)
		}),
		layout.Rigid(layout.Spacer{Width: 10}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, &v.mitte, "Zur Mitte", knopfNeben, st.manifest != nil)
		}),
		layout.Rigid(layout.Spacer{Width: 10}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			txt := "Neu laden"
			if st.laedt {
				txt = "Lädt …"
			}
			return t.knopf(gtx, &v.neu, txt, knopfNeben, !st.laedt)
		}),
	)
}

// statusText: „Stand: vor 5 Min. · Zoom 25 % (Stufe 2) · x 120, z −45 · lädt 3 Kacheln“.
func (v *karteAnsicht) statusText(jetzt time.Time, st karteStand, sz *karteSitzung) string {
	if st.manifest == nil {
		return "Keine Karte"
	}
	var teile []string
	if alt, ok := st.manifest.Alter(jetzt); ok {
		teile = append(teile, "Stand: "+karte.AlterText(alt))
	} else {
		teile = append(teile, "Stand unbekannt")
	}
	teile = append(teile, "Zoom "+zoomText(v.bild.sicht.Zoom)+" (Stufe "+strconv.Itoa(v.bild.stufe)+")")
	if bx, bz, ok := v.bild.mausBlock(); ok {
		teile = append(teile, fmt.Sprintf("x %s, z %s", minusText(bx), minusText(bz)))
	}
	if n := sz.lader.Ausstehend(); n > 0 {
		teile = append(teile, "lädt "+strconv.Itoa(n)+" Kacheln …")
	}
	if n, _ := sz.lader.Fehler(); n > 0 {
		teile = append(teile, strconv.Itoa(n)+" Kacheln nicht ladbar")
	}
	return strings.Join(teile, " · ")
}

// zoomText: der Zoomfaktor als Prozent („100 %“, „1,6 %“, „800 %“).
func zoomText(z float64) string {
	p := karte.KlemmeZoom(z) * 100
	s := strconv.FormatFloat(p, 'f', -1, 64)
	switch {
	case p >= 10:
		s = strconv.FormatFloat(p, 'f', 0, 64)
	case p >= 1:
		s = strings.Replace(strconv.FormatFloat(p, 'f', 1, 64), ".0", "", 1)
	default:
		s = strconv.FormatFloat(p, 'f', 2, 64)
	}
	return strings.Replace(s, ".", ",", 1) + " %"
}

// minusText: Zahl mit echtem Minuszeichen.
func minusText(n int) string { return strings.Replace(strconv.Itoa(n), "-", "−", 1) }
