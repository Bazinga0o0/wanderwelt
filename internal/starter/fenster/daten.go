package fenster

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Datenschicht des Fensters: Eine Goroutine fragt die laufende Instanz ab,
// solange das Fenster offen (und nicht minimiert) ist – den Zustand im
// festen Takt, alles andere nur für den sichtbaren Reiter. Die Oberfläche
// liest Schnappschüsse und schickt Aktionen; deren Ergebnisse kommen als
// Aufträge zurück, die die Oberfläche in ihrer eigenen Goroutine ausführt.

const (
	// zustandTakt: So oft holt das Fenster den Zustand der Instanz.
	zustandTakt = 1500 * time.Millisecond
	// staendeTakt: Stände-Liste (und Speicherstände) höchstens so oft, und
	// nur bei sichtbarem Reiter „Welt“.
	staendeTakt = 30 * time.Second
	// mitgliederTakt: Mitgliederliste bei sichtbarem Reiter „Mitglieder“.
	mitgliederTakt = 15 * time.Second
	// logTakt: Log bei sichtbarem Reiter „Log“.
	logTakt = 3 * time.Second
	// startWarten: So lange wartet das Fenster nach dem Starten auf die
	// Instanz, bevor es „wanderwelt läuft nicht“ zeigt.
	startWarten = 15 * time.Second
	// bildFrist: siehe daten.bildFrist.
	bildFrist = 30 * time.Second
	// logZeilen: so viele Zeilen zeigt der Reiter „Log“.
	logZeilen = 300
	// anfrageFrist: Obergrenze je Abfrage; Aktionen dürfen länger.
	anfrageFrist  = 10 * time.Second
	aktionsFrist  = 30 * time.Second
	ruhigNeuMalen = 15 * time.Second // auch ohne Änderung, für „vor X Min.“
)

// reiter ist ein Reiter des Fensters.
type reiter int

const (
	reiterStart reiter = iota
	reiterWelt
	reiterHosten
	reiterMitglieder
	reiterLog
	anzahlReiter
)

// verbindung: Spricht das Fenster mit einer Instanz?
type verbindung int

const (
	verbindet   verbindung = iota // noch keine Antwort
	startet                       // Instanz gestartet, wartet auf sie
	verbunden                     // Zustand liegt vor
	laeuftNicht                   // keine Instanz
)

// quelle ist, was das Fenster von der Instanz braucht (*fensterapi.Client).
type quelle interface {
	Zustand(ctx context.Context) (*fensterapi.Zustand, error)
	Staende(ctx context.Context) (*api.GenerationsResponse, error)
	Speicherstaende(ctx context.Context) ([]fensterapi.Speicherstand, error)
	Log(ctx context.Context, zeilen int) (string, error)
	Mitglieder(ctx context.Context) ([]api.MemberInfo, error)
	Beitritte(ctx context.Context) ([]api.JoinRequestInfo, error)
	Aktion(ctx context.Context, name string, body any) error
}

// schnappschuss ist der Stand, den die Oberfläche zeichnet. Er wird nur als
// Ganzes ersetzt; die Oberfläche verändert ihn nicht.
type schnappschuss struct {
	verb      verbindung
	startFehl string // Starten der Instanz gescheitert
	// veraltet: Die Instanz hat eine andere Version als dieses Fenster,
	// und ein automatischer Neustart ging nicht (oder ist schon gelaufen).
	veraltet bool
	runde    uint64 // zählt Verbindungen; neue Instanz → neue Runde

	z    *fensterapi.Zustand
	zErr string // Zustand nicht lesbar, Instanz antwortet aber

	staende    *api.GenerationsResponse
	staendeErr string
	staendeAb  time.Time // wann geholt (Null: noch nie)

	saves    []fensterapi.Speicherstand
	savesErr string
	savesAb  time.Time

	log    string
	logErr string
	logAb  time.Time

	mitglieder    []api.MemberInfo
	mitgliederErr string
	mitgliederAb  time.Time

	// Offene Beitrittsanfragen (kommen mit den Mitgliedern).
	anfragen    []api.JoinRequestInfo
	anfragenErr string
}

// daten hält Verbindung und Schnappschuss.
type daten struct {
	// staendeNeu zählt die Wünsche „Stände neu laden“ (unter mu).
	staendeNeu int
	// Takte (Tests verkürzen sie).
	takt, staendeTakt, mitgliederTakt, logTakt, startWarten time.Duration
	// bildFrist: Kommt so lange nach einem Neuzeichnen-Wunsch kein Bild,
	// ruht die Abfrage (Oberfläche hängt).
	bildFrist time.Duration

	verbinde    func() (quelle, error)
	starte      func() error // Instanz losgelöst starten
	neuZeichnen func()       // darf aus jeder Goroutine kommen
	jetzt       func() time.Time
	// minimiert (darf nil sein): Solange das Fenster minimiert ist, ruht
	// die Abfrage – die Instanz zeigt ihre Hinweise dann wieder selbst.
	minimiert func() bool

	wach chan struct{}

	// Versionsabgleich (Update bei offenem Fenster): eigeneVersion ist die
	// Version dieses Prozesses; installiertIst sagt, ob die installierte
	// exe die übergebene Version hat; neustart startet das Fenster neu
	// (und beendet diesen Prozess). Alle drei dürfen fehlen (dann nichts).
	eigeneVersion  string
	installiertIst func(version string) bool
	neustart       func() error
	versionTakt    time.Duration // Mindestabstand der Prüfung der installierten exe

	neustartGetan   bool      // der eine Neustart dieses Prozesses ist verbraucht (unter mu)
	versionGeprueft time.Time // letzte Prüfung der installierten exe (unter mu)

	mu          sync.Mutex
	s           schnappschuss
	c           quelle
	reiter      reiter
	pausiert    bool      // letzte Runde fiel aus (minimiert)
	savesWunsch bool      // „Welt übernehmen“ aufgeklappt
	autoStart   bool      // beim ersten Fehlschlag Instanz starten
	startBis    time.Time // Instanz gestartet, warten bis
	auftraege   []func()
	gemalt      time.Time
	wunschSeit  time.Time // erster Neuzeichnen-Wunsch ohne folgendes Bild
}

func neueDaten(starte func() error, neuZeichnen func()) *daten {
	return &daten{
		takt:           zustandTakt,
		staendeTakt:    staendeTakt,
		mitgliederTakt: mitgliederTakt,
		logTakt:        logTakt,
		startWarten:    startWarten,
		bildFrist:      bildFrist,
		verbinde: func() (quelle, error) {
			c, err := fensterapi.Verbinden()
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		starte:      starte,
		neuZeichnen: neuZeichnen,
		jetzt:       time.Now,
		wach:        make(chan struct{}, 1),
		versionTakt: 30 * time.Second,
	}
}

// lesen liefert den aktuellen Schnappschuss.
func (d *daten) lesen() schnappschuss {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.s
}

// lauf fragt ab, bis ctx endet.
func (d *daten) lauf(ctx context.Context) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.wach:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		d.runde(ctx)
		t.Reset(d.takt)
	}
}

// wecken: sofort eine Runde (Reiterwechsel, nach einer Aktion).
func (d *daten) wecken() {
	select {
	case d.wach <- struct{}{}:
	default:
	}
}

// setzeReiter meldet den sichtbaren Reiter.
func (d *daten) setzeReiter(r reiter) {
	d.mu.Lock()
	alt := d.reiter
	d.reiter = r
	d.mu.Unlock()
	if alt != r {
		d.wecken()
	}
}

// gezeichnet: Das Fenster hat ein Bild gezeichnet (ist also nicht
// minimiert); ruhte die Abfrage, geht sie sofort weiter.
func (d *daten) gezeichnet() {
	d.mu.Lock()
	p := d.pausiert
	d.pausiert, d.wunschSeit = false, time.Time{}
	d.mu.Unlock()
	if p {
		d.wecken()
	}
}

// setzeSavesWunsch: „Welt übernehmen“ auf- oder zugeklappt.
func (d *daten) setzeSavesWunsch(v bool) {
	d.mu.Lock()
	alt := d.savesWunsch
	d.savesWunsch = v
	d.mu.Unlock()
	if v && !alt {
		d.wecken()
	}
}

// neuLaden vergisst den Zeitpunkt einer Liste, damit die nächste Runde sie
// holt (nach einer Aktion, auf Wunsch).
func (d *daten) neuLaden(staende, saves, mitglieder bool) {
	d.mu.Lock()
	if staende {
		d.staendeNeu++
		d.s.staendeAb = time.Time{}
	}
	if saves {
		d.s.savesAb = time.Time{}
	}
	if mitglieder {
		d.s.mitgliederAb = time.Time{}
	}
	d.mu.Unlock()
	d.wecken()
}

// starten startet die Instanz („Starten“, oder beim Öffnen ohne Instanz).
// Der Zustand „startet“ steht sofort (ein zweiter Klick tut nichts mehr);
// der Prozess startet danach in einer eigenen Goroutine.
func (d *daten) starten() {
	d.mu.Lock()
	d.autoStart = false
	if d.s.verb == startet && d.jetzt().Before(d.startBis) {
		d.mu.Unlock()
		return // startet schon
	}
	d.s.verb, d.s.startFehl = startet, ""
	d.startBis = d.jetzt().Add(d.startWarten)
	d.mu.Unlock()
	d.malen(true)
	go func() {
		if err := d.starte(); err != nil {
			d.mu.Lock()
			d.s.verb, d.s.startFehl = laeuftNicht, "wanderwelt ließ sich nicht starten: "+err.Error()
			d.startBis = time.Time{}
			d.mu.Unlock()
			d.malen(true)
		}
		d.wecken()
	}()
}

// runde: eine Abfrage-Runde.
func (d *daten) runde(ctx context.Context) {
	d.mu.Lock()
	c, r, minimiert := d.c, d.reiter, d.minimiert
	// Seit bildFrist kein Bild nach einem Neuzeichnen-Wunsch: Die Oberfläche
	// hängt (oder zeichnet nicht). Dann nicht weiter abfragen, damit die
	// Instanz ihre Hinweise wieder selbst zeigt.
	ohneBild := !d.wunschSeit.IsZero() && d.jetzt().Sub(d.wunschSeit) > d.bildFrist
	d.mu.Unlock()
	if ohneBild || (minimiert != nil && minimiert()) {
		d.mu.Lock()
		d.pausiert = true
		d.mu.Unlock()
		return
	}
	if c == nil {
		var err error
		if c, err = d.verbinde(); err != nil {
			d.ohneInstanz()
			return
		}
		d.mu.Lock()
		d.c = c
		d.startBis = time.Time{}
		d.autoStart = false
		// Neue Instanz: Listen neu holen, Hinweise neu zählen.
		d.s = schnappschuss{verb: verbindet, runde: d.s.runde + 1}
		d.mu.Unlock()
	}

	actx, cancel := context.WithTimeout(ctx, anfrageFrist)
	z, err := c.Zustand(actx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, fensterapi.ErrLaeuftNicht) {
		d.mu.Lock()
		if d.c == c {
			d.c = nil
		}
		d.mu.Unlock()
		d.ohneInstanz()
		return
	}

	d.mu.Lock()
	alt := d.s
	n := d.s
	n.verb = verbunden
	n.startFehl = ""
	if err != nil {
		n.zErr = err.Error()
	} else {
		n.z, n.zErr = z, ""
	}
	admin := n.z != nil && n.z.Me != nil && n.z.Me.Admin
	jetzt := d.jetzt()
	holeStaende := r == reiterWelt && faellig(n.staendeAb, jetzt, d.staendeTakt)
	holeSaves := r == reiterWelt && admin && d.savesWunsch && faellig(n.savesAb, jetzt, d.staendeTakt)
	holeLog := r == reiterLog && faellig(n.logAb, jetzt, d.logTakt)
	holeMitglieder := r == reiterMitglieder && admin && faellig(n.mitgliederAb, jetzt, d.mitgliederTakt)
	d.s = n
	d.mu.Unlock()
	if err == nil {
		d.versionPruefen(z)
	}

	// Listen nacheinander (die Instanz ist lokal; keine Parallelität nötig).
	if holeStaende {
		d.mu.Lock()
		neu := d.staendeNeu
		d.mu.Unlock()
		actx, cancel := context.WithTimeout(ctx, anfrageFrist)
		g, err := c.Staende(actx)
		cancel()
		d.mu.Lock()
		if d.staendeNeu == neu { // sonst wurde währenddessen neu verlangt: gleich noch einmal holen
			d.s.staendeAb = d.jetzt()
		}
		if err != nil {
			d.s.staendeErr = err.Error()
		} else {
			d.s.staende, d.s.staendeErr = g, ""
		}
		d.mu.Unlock()
	}
	if holeSaves {
		actx, cancel := context.WithTimeout(ctx, anfrageFrist)
		sv, err := c.Speicherstaende(actx)
		cancel()
		d.mu.Lock()
		d.s.savesAb = d.jetzt()
		if err != nil {
			d.s.savesErr = err.Error()
		} else {
			d.s.saves, d.s.savesErr = sv, ""
		}
		d.mu.Unlock()
	}
	if holeLog {
		actx, cancel := context.WithTimeout(ctx, anfrageFrist)
		l, err := c.Log(actx, logZeilen)
		cancel()
		d.mu.Lock()
		d.s.logAb = d.jetzt()
		if err != nil {
			d.s.logErr = err.Error()
		} else {
			d.s.log, d.s.logErr = l, ""
		}
		d.mu.Unlock()
	}
	if holeMitglieder {
		actx, cancel := context.WithTimeout(ctx, anfrageFrist)
		m, err := c.Mitglieder(actx)
		cancel()
		d.mu.Lock()
		d.s.mitgliederAb = d.jetzt()
		if err != nil {
			d.s.mitgliederErr = err.Error()
		} else {
			d.s.mitglieder, d.s.mitgliederErr = m, ""
		}
		d.mu.Unlock()

		actx, cancel = context.WithTimeout(ctx, anfrageFrist)
		an, err := c.Beitritte(actx)
		cancel()
		d.mu.Lock()
		if err != nil {
			d.s.anfragenErr = err.Error()
		} else {
			d.s.anfragen, d.s.anfragenErr = an, ""
		}
		d.mu.Unlock()
	}

	d.mu.Lock()
	geaendert := !gleich(alt, d.s)
	d.mu.Unlock()
	d.malen(geaendert)
}

// ohneInstanz: Niemand antwortet. Kurz nach dem Starten wird gewartet;
// beim ersten Mal (Fenster ohne Instanz geöffnet) wird gestartet.
func (d *daten) ohneInstanz() {
	d.mu.Lock()
	auto := d.autoStart
	alt := d.s.verb
	switch {
	case auto:
	case !d.startBis.IsZero() && d.jetzt().Before(d.startBis):
		d.s.verb = startet
	default:
		if !d.startBis.IsZero() {
			d.s.startFehl = "wanderwelt hat sich nach dem Starten nicht gemeldet."
		}
		d.startBis = time.Time{}
		if d.s.verb != laeuftNicht {
			// Alte Daten nicht weiter zeigen.
			d.s = schnappschuss{verb: laeuftNicht, startFehl: d.s.startFehl, runde: d.s.runde}
		}
	}
	neu := d.s.verb
	d.mu.Unlock()
	if auto {
		d.starten()
		return
	}
	d.malen(alt != neu)
}

// faellig: Liste nie geholt oder älter als takt.
func faellig(ab, jetzt time.Time, takt time.Duration) bool {
	return ab.IsZero() || jetzt.Sub(ab) >= takt
}

// gleich vergleicht zwei Schnappschüsse ohne die Abholzeiten.
func gleich(a, b schnappschuss) bool {
	a.staendeAb, b.staendeAb = time.Time{}, time.Time{}
	a.savesAb, b.savesAb = time.Time{}, time.Time{}
	a.logAb, b.logAb = time.Time{}, time.Time{}
	a.mitgliederAb, b.mitgliederAb = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// malen lässt das Fenster neu zeichnen: bei Änderungen sofort, sonst
// höchstens alle ruhigNeuMalen (relative Zeiten wie „vor 3 Min.“).
func (d *daten) malen(geaendert bool) {
	d.mu.Lock()
	jetzt := d.jetzt()
	if !geaendert && jetzt.Sub(d.gemalt) < ruhigNeuMalen {
		d.mu.Unlock()
		return
	}
	d.gemalt = jetzt
	nz := d.neuZeichnen
	if nz != nil && d.wunschSeit.IsZero() {
		d.wunschSeit = jetzt
	}
	d.mu.Unlock()
	if nz != nil {
		nz()
	}
}

// auftrag reiht f für die Oberfläche ein (läuft dort beim nächsten Bild).
func (d *daten) auftrag(f func()) {
	d.mu.Lock()
	d.auftraege = append(d.auftraege, f)
	d.mu.Unlock()
	d.malen(true)
}

// abarbeiten führt die eingereihten Aufträge aus (nur aus der Oberfläche).
func (d *daten) abarbeiten() {
	d.mu.Lock()
	a := d.auftraege
	d.auftraege = nil
	d.mu.Unlock()
	for _, f := range a {
		f()
	}
}

// aktion schickt eine Aktion an die Instanz; fertig läuft danach in der
// Oberfläche (err nil: angenommen).
func (d *daten) aktion(name string, body any, fertig func(error)) {
	d.mu.Lock()
	c := d.c
	d.mu.Unlock()
	if c == nil {
		d.auftrag(func() { fertig(fensterapi.ErrLaeuftNicht) })
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), aktionsFrist)
		err := c.Aktion(ctx, name, body)
		cancel()
		if fertig != nil {
			d.auftrag(func() { fertig(err) })
		}
		d.wecken()
	}()
}

// versionPruefen vergleicht die Version der Instanz mit der eigenen. Bei
// einem Unterschied (beide nicht leer) startet es das Fenster neu, aber
// nur einmal je Prozess und nur, wenn die installierte exe schon die Version
// der Instanz hat; sonst bleibt der Hinweis „veraltet“.
func (d *daten) versionPruefen(z *fensterapi.Zustand) {
	if z == nil || z.Version == "" || d.eigeneVersion == "" || z.Version == d.eigeneVersion {
		d.setzeVeraltet(false)
		return
	}
	d.mu.Lock()
	jetzt := d.jetzt()
	if d.neustartGetan || (!d.versionGeprueft.IsZero() && jetzt.Sub(d.versionGeprueft) < d.versionTakt) {
		d.mu.Unlock()
		d.setzeVeraltet(true)
		return
	}
	d.versionGeprueft = jetzt
	d.mu.Unlock()
	if d.installiertIst == nil || d.neustart == nil || !d.installiertIst(z.Version) {
		d.setzeVeraltet(true)
		return
	}
	d.mu.Lock()
	d.neustartGetan = true
	d.mu.Unlock()
	if err := d.neustart(); err != nil {
		d.setzeVeraltet(true)
	}
}

func (d *daten) setzeVeraltet(v bool) {
	d.mu.Lock()
	ch := d.s.veraltet != v
	d.s.veraltet = v
	d.mu.Unlock()
	if ch && d.neuZeichnen != nil {
		d.neuZeichnen()
	}
}
