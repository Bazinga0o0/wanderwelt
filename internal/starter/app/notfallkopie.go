package app

// Notfallkopie (Reiter Hosten, Haken „Notfallkopie behalten“, Standard an):
// Nach einer eigenen Host-Sitzung liegt der zuletzt eingereichte Stand als
// normaler Weltordner in <Dokumente>\wanderwelt-Notfallkopie, falls der
// Koordinator ausfällt. Es gibt genau eine Kopie.
//
// Quelle ist der Bereitstellungsordner der Sitzung, den sie über
// host.Deps.OnCommitted meldet, solange er genau den eingereichten Stand
// enthält (bei einer Übergabe der Übergabe-Stand, nicht der lokale Weltordner,
// der danach Spectator-Spielerdaten hat). Der Haken belegt host.HoldSync,
// damit keine folgende Sitzung den Ordner neu bildet, und kopiert im
// Hintergrund: Zuschlag-Rückgabe und Übergabe warten nicht darauf.
//
// Die Kopie läuft mit Abbruch (Context von App.ctx, Frist notfallFrist). Braucht
// eine Sitzung, Recover oder „Welt übernehmen“ den gemeinsamen Platz
// (host.SetSyncContention), wird sie abgebrochen statt zu warten; die alte
// Kopie bleibt. Beim Programmende wartet der Starter höchstens notfallEndWait
// auf eine laufende Kopie (waitNotfall). Scheitert der Schluss-Upload, wird
// der bereitgestellte, nur lokale Stand trotzdem kopiert (LIESMICH.txt sagt
// es), ebenso ein nachgereichter Stand (host.SetRecoveredHook).
//
// Liegt „Dokumente“ unter OneDrive, nimmt die Kopie %USERPROFILE% (die
// Weltdaten sollen nicht in die Cloud).
//
// Die neue Kopie entsteht neben der alten (…Notfallkopie.neu) und ersetzt sie
// erst, wenn sie vollständig ist (alte → .alt, neue → Name, .alt löschen); es
// gibt nie einen Zustand ohne Kopie, wenn die neue scheitert. Fehler stehen
// im Log, einmal je Start des Starters zusätzlich als Hinweis.
//
// Der Haken (und die Angaben zur letzten Kopie) stehen lokal in
// Dir()/notfallkopie.json; der Koordinator erfährt nichts davon.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

const (
	// notfallName ist der Ordner der Notfallkopie in den Dokumenten.
	notfallName = "wanderwelt-Notfallkopie"
	// notfallDatei: Einstellung und Angaben zur letzten Kopie in Dir() (Gruppenordner).
	notfallDatei = "notfallkopie.json"
	// notfallLiesmich: Datei mit Erklärung im Ordner der Kopie.
	notfallLiesmich = "LIESMICH.txt"
	// notfallDokumenteEnv überschreibt den Dokumente-Ordner (Tests, Entwicklung).
	notfallDokumenteEnv = "WANDERWELT_DOKUMENTE"
	// notfallHoldWait: So lange wartet der Haken am Ende der Sitzung auf
	// HoldSync, ehe er auf die Kopie verzichtet.
	notfallHoldWait = 3 * time.Second
	// notfallFrist: So lange darf eine Kopie höchstens dauern.
	notfallFrist = 10 * time.Minute
	// notfallEndWait: So lange wartet das Programmende auf eine Kopie.
	notfallEndWait = 2 * time.Minute
)

// notfallRenameWaits sind die Pausen von renameRetry (zusammen etwa 10 s);
// im Test austauschbar.
var notfallRenameWaits = []time.Duration{
	100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond,
	1600 * time.Millisecond, 3200 * time.Millisecond, 3700 * time.Millisecond,
}

// notfallFree liefert den freien Platz am Ort path (Fehler: unbekannt, dann
// keine Vorprüfung); im Test austauschbar.
var notfallFree = winsys.FreeSpace

// notfallRoot ist der Ordner, in dem wanderwelt-Notfallkopie liegt: Dokumente,
// bei OneDrive-Umleitung %USERPROFILE%.
func notfallRoot() (string, error) {
	d, err := dokumenteDir()
	if err != nil {
		return "", err
	}
	if os.Getenv(notfallDokumenteEnv) == "" && imOneDrive(d) {
		home := os.Getenv("USERPROFILE")
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		if home != "" {
			return home, nil
		}
	}
	return d, nil
}

// imOneDrive: path liegt unter einem der OneDrive-Ordner aus der Umgebung.
func imOneDrive(path string) bool {
	p := strings.ToLower(filepath.Clean(path))
	for _, env := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		b := strings.ToLower(filepath.Clean(base))
		if p == b || strings.HasPrefix(p, b+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// dokumenteDir liefert den Dokumente-Ordner: WANDERWELT_DOKUMENTE, sonst der
// Windows-Known-Folder, sonst (nicht Windows) ~/Documents. Im Test
// austauschbar.
var dokumenteDir = func() (string, error) {
	if d := os.Getenv(notfallDokumenteEnv); d != "" {
		return d, nil
	}
	d, err := winsys.DocumentsDir()
	if err == nil && d != "" {
		return d, nil
	}
	if err != nil && !errors.Is(err, winsys.ErrUnsupported) {
		return "", err
	}
	h, herr := os.UserHomeDir()
	if herr != nil {
		return "", herr
	}
	return filepath.Join(h, "Documents"), nil
}

// notfallHold belegt den Platz, den Download und Stände teilen; im Test
// austauschbar.
var notfallHold = host.HoldSyncQuiet

// notfallCopyFile kopiert eine Datei der Kopie; im Test austauschbar (Fehler
// einspielen).
var notfallCopyFile = copyRegular

// notfallJetzt ist die Uhr der Kopie; im Test austauschbar.
var notfallJetzt = time.Now

// notfallState gehört zu App.notfall.
type notfallState struct {
	// mu schützt cancel, notified und notfallkopie.json; nie gehalten, während
	// kopiert wird.
	mu       sync.Mutex
	cancels  map[int]context.CancelFunc // laufende und wartende Kopien
	nextID   int
	notified bool           // der Hinweis zu einem Fehler kam schon
	wg       sync.WaitGroup // laufende Kopien
}

// notfallEinstellung ist der Inhalt von Dir()/notfallkopie.json.
type notfallEinstellung struct {
	Aus bool `json:"aus,omitempty"` // Haken aus (fehlt: an)
	// Ordner: Name des Ordners der Notfallkopie in den Dokumenten (leer: je
	// Gruppe wanderwelt-Notfallkopie-<id>; die aus dem alten Einzel-Layout
	// migrierte Gruppe behält wanderwelt-Notfallkopie).
	Ordner     string    `json:"ordner,omitempty"`
	WorldID    string    `json:"world_id,omitempty"`
	Generation int64     `json:"generation,omitempty"`
	Zeit       time.Time `json:"zeit,omitzero"`
	Groesse    int64     `json:"groesse,omitempty"`
	// NichtHochgeladen: Die Kopie enthält einen Stand, der nicht eingereicht
	// werden konnte.
	NichtHochgeladen bool `json:"nicht_hochgeladen,omitempty"`
	// Welten: die Angaben zur letzten Kopie jeder weiteren Welt (Schlüssel:
	// Kennung); die der ersten Welt stehen oben wie bisher.
	Welten map[string]notfallLetzte `json:"welten,omitempty"`
}

// notfallLetzte sind die Angaben zur letzten Kopie einer weiteren Welt.
type notfallLetzte struct {
	WorldID          string    `json:"world_id,omitempty"`
	Generation       int64     `json:"generation,omitempty"`
	Zeit             time.Time `json:"zeit,omitzero"`
	Groesse          int64     `json:"groesse,omitempty"`
	NichtHochgeladen bool      `json:"nicht_hochgeladen,omitempty"`
}

// letzte liefert die Angaben zur letzten Kopie der Welt world.
func (e notfallEinstellung) letzte(world string) notfallLetzte {
	if group.DefaultWorld(world) {
		return notfallLetzte{WorldID: e.WorldID, Generation: e.Generation, Zeit: e.Zeit, Groesse: e.Groesse, NichtHochgeladen: e.NichtHochgeladen}
	}
	return e.Welten[world]
}

// setzeLetzte merkt die Angaben zur letzten Kopie der Welt world.
func (e *notfallEinstellung) setzeLetzte(world string, l notfallLetzte) {
	if group.DefaultWorld(world) {
		e.WorldID, e.Generation, e.Zeit, e.Groesse, e.NichtHochgeladen = l.WorldID, l.Generation, l.Zeit, l.Groesse, l.NichtHochgeladen
		return
	}
	if e.Welten == nil {
		e.Welten = map[string]notfallLetzte{}
	}
	e.Welten[world] = l
}

func notfallPfad() string { return filepath.Join(group.Dir(), notfallDatei) }

func ladeNotfall() (notfallEinstellung, error) {
	var e notfallEinstellung
	data, err := os.ReadFile(notfallPfad())
	if errors.Is(err, fs.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return notfallEinstellung{}, fmt.Errorf("%s: %w", notfallPfad(), err)
	}
	return e, nil
}

func speichereNotfall(e notfallEinstellung) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(group.Dir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(group.Dir(), notfallDatei+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if err := os.Rename(name, notfallPfad()); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// notfallAn: Der Haken ist gesetzt (Standard; auch bei unlesbarer Datei).
func (a *App) notfallAn() bool {
	e, err := ladeNotfall()
	return err != nil || !e.Aus
}

// setNotfall schaltet den Haken.
func (a *App) setNotfall(an bool) error {
	a.notfall.mu.Lock()
	defer a.notfall.mu.Unlock()
	e, err := ladeNotfall()
	if err != nil {
		e = notfallEinstellung{} // verdorben: neu anfangen
	}
	e.Aus = !an
	return speichereNotfall(e)
}

// notfallZustand füllt den Teil des Fenster-Zustands.
func (a *App) notfallZustand() fensterapi.Notfallkopie {
	a.notfall.mu.Lock()
	e, err := ladeNotfall()
	a.notfall.mu.Unlock()
	n := fensterapi.Notfallkopie{Behalten: err != nil || !e.Aus}
	root, rerr := notfallRoot()
	if rerr != nil {
		return n
	}
	// Gezeigt wird die Kopie der Welt, die „Spielen“ gerade nimmt.
	world := a.weltZustand().Aktiv
	n.Ordner = filepath.Join(root, notfallOrdnerNameFor(world))
	if fi, err := os.Stat(n.Ordner); err == nil && fi.IsDir() {
		n.Vorhanden = true
		l := e.letzte(world)
		n.Zeit, n.Groesse, n.NichtHochgeladen = l.Zeit, l.Groesse, l.NichtHochgeladen
		n.WorldID, n.Generation = l.WorldID, l.Generation
	}
	return n
}

// abortNotfall bricht eine laufende Kopie ab (host.SetSyncContention).
func (a *App) abortNotfall() {
	a.notfall.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(a.notfall.cancels))
	for _, c := range a.notfall.cancels {
		cs = append(cs, c)
	}
	a.notfall.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

// waitNotfall wartet höchstens max auf laufende Kopien (Programmende); danach
// bricht es sie ab (die alte Kopie bleibt).
func (a *App) waitNotfall(max time.Duration) {
	done := make(chan struct{})
	go func() { a.notfall.wg.Wait(); close(done) }()
	select {
	case <-done:
		return
	case <-time.After(max):
	}
	log.Printf("Notfallkopie: nach %v nicht fertig – abgebrochen, die alte Kopie bleibt", max)
	a.abortNotfall()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// onCommitted ist host.Deps.OnCommitted (und host.SetRecoveredHook): Ist der
// Haken an, belegt es den Platz und kopiert im Hintergrund.
func (a *App) onCommitted(c host.Committed) {
	if !a.notfallAn() {
		log.Printf("Notfallkopie: aus, keine Kopie von %s/%d", c.WorldID, c.Generation)
		return
	}
	// Die Abbruchfunktion steht vor dem Belegen des Platzes bereit, damit der
	// Haken von SetSyncContention sie immer findet; ältere Kopien weichen.
	cctx, ccancel := context.WithTimeout(a.ctx, notfallFrist)
	a.notfall.mu.Lock()
	for _, old := range a.notfall.cancels {
		old()
	}
	if a.notfall.cancels == nil {
		a.notfall.cancels = map[int]context.CancelFunc{}
	}
	a.notfall.nextID++
	id := a.notfall.nextID
	a.notfall.cancels[id] = ccancel
	a.notfall.mu.Unlock()
	done := func() {
		a.notfall.mu.Lock()
		delete(a.notfall.cancels, id)
		a.notfall.mu.Unlock()
		ccancel()
	}
	hctx, hcancel := context.WithTimeout(cctx, notfallHoldWait)
	unlock, err := notfallHold(hctx)
	hcancel()
	if err != nil {
		done()
		if errors.Is(err, context.Canceled) {
			log.Printf("Notfallkopie abgebrochen, bevor sie begann (%s/%d)", c.WorldID, c.Generation)
			return
		}
		log.Printf("Notfallkopie: Platz nicht frei, keine Kopie von %s/%d: %v", c.WorldID, c.Generation, err)
		a.notfallFehler(errors.New("der Platz für Welt-Dateien war belegt, diesmal gibt es keine neue Kopie"))
		return
	}
	a.notfall.wg.Add(1)
	go func() {
		defer a.notfall.wg.Done()
		defer unlock()
		defer done()
		err := a.notfallKopieren(cctx, c)
		switch {
		case err == nil:
		case errors.Is(err, context.DeadlineExceeded):
			log.Printf("Notfallkopie: %v", err)
			a.notfallFehler(fmt.Errorf("das Kopieren dauerte länger als %v", notfallFrist))
		case errors.Is(err, context.Canceled):
			log.Printf("Notfallkopie abgebrochen (Platz gebraucht oder Programmende), die alte bleibt: %v", err)
		default:
			log.Printf("Notfallkopie: %v", err)
			a.notfallFehler(err)
		}
	}()
}

// notfallFehler zeigt einmal je Start einen Hinweis.
func (a *App) notfallFehler(err error) {
	a.notfall.mu.Lock()
	first := !a.notfall.notified
	a.notfall.notified = true
	a.notfall.mu.Unlock()
	if first {
		go notify("Die Notfallkopie der Welt ließ sich nicht anlegen: " + err.Error() + " (Details im Log; eine ältere Kopie bleibt erhalten.)")
	}
}

// notfallKopieren legt die Kopie von c an und merkt sie in der Einstellung.
func (a *App) notfallKopieren(ctx context.Context, c host.Committed) error {
	root, err := notfallRoot()
	if err != nil {
		return fmt.Errorf("Dokumente-Ordner unbekannt: %w", err)
	}
	began := time.Now()
	size, err := schreibeNotfallkopie(ctx, root, c, notfallJetzt())
	if err != nil {
		return err
	}
	a.notfall.mu.Lock()
	defer a.notfall.mu.Unlock()
	e, lerr := ladeNotfall()
	if lerr != nil {
		e = notfallEinstellung{}
	}
	e.setzeLetzte(c.GroupWorld, notfallLetzte{WorldID: c.WorldID, Generation: c.Generation, Zeit: notfallJetzt(), Groesse: size, NichtHochgeladen: c.NotUploaded})
	if err := speichereNotfall(e); err != nil {
		log.Printf("Notfallkopie: Angaben nicht gespeichert: %v", err)
	}
	log.Printf("Notfallkopie: Stand %s/%d (%s, hochgeladen: %v) in %s, %v", c.WorldID, c.Generation, humanSize(size), !c.NotUploaded, filepath.Join(root, notfallOrdnerNameFor(c.GroupWorld)), time.Since(began).Round(time.Millisecond))
	return nil
}

// schreibeNotfallkopie kopiert c.Dir nach root\wanderwelt-Notfallkopie und
// ersetzt dabei eine vorhandene Kopie so, dass bei einem Fehler die alte
// erhalten bleibt. Liefert die Größe der Dateien.
func schreibeNotfallkopie(ctx context.Context, root string, c host.Committed, now time.Time) (int64, error) {
	if fi, err := os.Stat(c.Dir); err != nil || !fi.IsDir() {
		return 0, fmt.Errorf("Stand %s nicht lesbar", c.Dir)
	}
	final := filepath.Join(root, notfallOrdnerNameFor(c.GroupWorld))
	neu, alt := final+".neu", final+".alt"
	if err := os.MkdirAll(root, 0o755); err != nil {
		return 0, err
	}
	// Platz vorher prüfen: Größe des Stands plus 10 %.
	need, err := weltGroesse(c.Dir)
	if err != nil {
		return 0, err
	}
	need += need / 10
	if free, ferr := notfallFree(root); ferr == nil && free < need {
		return 0, fmt.Errorf("zu wenig freier Platz in %s (nötig %s, frei %s)", root, humanSize(need), humanSize(free))
	}
	// Reste eines abgebrochenen Tauschs: Fehlt die Kopie, aber die alte
	// liegt noch da, ist sie die Kopie.
	if _, err := os.Lstat(final); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Lstat(alt); err == nil {
			if err := os.Rename(alt, final); err != nil {
				return 0, fmt.Errorf("alte Kopie nicht wiederhergestellt: %w", err)
			}
		}
	}
	if err := os.RemoveAll(neu); err != nil {
		return 0, fmt.Errorf("Rest %s nicht gelöscht: %w", neu, err)
	}
	if err := os.RemoveAll(alt); err != nil {
		return 0, fmt.Errorf("Rest %s nicht gelöscht: %w", alt, err)
	}
	size, err := kopiereWelt(ctx, c.Dir, neu)
	if err == nil {
		err = os.WriteFile(filepath.Join(neu, notfallLiesmich), []byte(liesmichText(c, now)), 0o644)
	}
	if err != nil {
		os.RemoveAll(neu)
		return 0, err
	}
	hadOld := false
	if _, err := os.Lstat(final); err == nil {
		hadOld = true
		if err := renameRetry(final, alt); err != nil {
			os.RemoveAll(neu)
			return 0, fmt.Errorf("alte Kopie nicht beiseite gelegt: %w", err)
		}
	}
	if err := renameRetry(neu, final); err != nil {
		if hadOld {
			if rerr := renameRetry(alt, final); rerr != nil {
				log.Printf("Notfallkopie: alte Kopie nicht zurückgelegt (liegt in %s): %v", alt, rerr)
			}
		}
		os.RemoveAll(neu)
		return 0, fmt.Errorf("neue Kopie nicht an ihren Platz gelegt: %w", err)
	}
	if hadOld {
		if err := os.RemoveAll(alt); err != nil {
			log.Printf("Notfallkopie: alte Kopie %s nicht gelöscht: %v", alt, err)
		}
	}
	return size, nil
}

// renameRetry benennt um und versucht es bei kurzen Sperren (Virenscanner,
// Explorer) ein paar Mal.
func renameRetry(from, to string) error {
	err := os.Rename(from, to)
	for _, w := range notfallRenameWaits {
		if err == nil {
			return nil
		}
		time.Sleep(w)
		err = os.Rename(from, to)
	}
	return err
}

// zuKopieren: Eine Datei gehört in die Kopie (nicht session.lock und
// Temp-Dateien von wanderwelt).
func zuKopieren(name string) bool {
	return name != "session.lock" && !(strings.HasPrefix(name, ".wanderwelt-") && strings.HasSuffix(name, ".tmp"))
}

// weltGroesse ist die Summe der Dateigrößen, die kopiereWelt kopieren würde.
func weltGroesse(src string) (int64, error) {
	var total int64
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && zuKopieren(d.Name()) {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

// kopiereWelt kopiert die Dateien aus src nach dst; Ergebnis: die Summe der
// Dateigrößen. Endet ctx, bricht es ab (vor jeder Datei und beim Lesen).
func kopiereWelt(ctx context.Context, src, dst string) (int64, error) {
	var total int64
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			if !zuKopieren(d.Name()) {
				return nil
			}
			n, err := notfallCopyFile(ctx, path, target)
			total += n
			return err
		}
		return nil // Links u. Ä. gehören nicht in eine Welt
	})
	return total, err
}

// ctxReader liest nur, solange ctx gilt.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if c.ctx.Err() != nil {
		return 0, context.Cause(c.ctx)
	}
	return c.r.Read(p)
}

// copyRegular kopiert eine Datei samt Änderungszeit (mit Sync, abbrechbar).
func copyRegular(ctx context.Context, src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, ctxReader{ctx, in})
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("%s kopieren: %w", src, err)
	}
	os.Chtimes(dst, time.Time{}, fi.ModTime())
	return n, nil
}

// liesmichText ist der Inhalt von LIESMICH.txt.
func liesmichText(c host.Committed, now time.Time) string {
	art := "Stand dieses PCs"
	if c.Handover {
		art = "Übergabe-Stand (der Server ging danach an einen anderen PC)"
	}
	gen := fmt.Sprintf("Generation:        %d", c.Generation)
	var warn []string
	if c.NotUploaded {
		art = "NICHT hochgeladen (eventuell nur hier vorhanden)"
		gen = fmt.Sprintf("Basis-Generation:   %d (darauf baut dieser Stand auf)", c.Generation)
		warn = []string{
			"ACHTUNG: Dieser Stand wurde NICHT hochgeladen und ist eventuell nur hier vorhanden.",
			"Er ist neuer als der letzte Stand beim Koordinator (Basis siehe unten).",
			"",
		}
	}
	v := c.MinecraftVersion
	if v == "" {
		v = "unbekannt"
	}
	lines := []string{
		"wanderwelt – Notfallkopie der Welt",
		"",
		"Das ist der neueste Stand der Welt von diesem PC, für den Fall, dass der Koordinator ausfällt.",
		"",
		"Welt-ID:           " + cmp.Or(c.WorldID, "neue Welt (noch ohne ID)"),
		gen,
		"Art:               " + art,
		"Minecraft-Version: " + v,
		"Kopiert am:        " + now.Local().Format("02.01.2006 15:04"),
		"",
		"So nutzt du sie:",
		"1. Einzelspieler: Kopiere diesen Ordner nach %APPDATA%\\.minecraft\\saves (am besten unter",
		"   neuem Namen) und öffne ihn in Minecraft in der Version oben.",
		"2. Neuer Koordinator: Kopiere den Ordner nach saves und lade ihn in wanderwelt über",
		"   „Welt übernehmen“ in den neuen Koordinator.",
		"",
		"wanderwelt ersetzt diesen Ordner nach der nächsten eigenen Host-Sitzung durch den dann",
		"neuesten Stand. Willst du ihn behalten, kopiere ihn vorher an einen anderen Ort.",
		"",
	}
	if len(warn) > 0 {
		lines = slices.Insert(lines, 2, warn...)
	}
	return strings.Join(lines, "\r\n")
}
