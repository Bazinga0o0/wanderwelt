// Package app ist das Starter-Programm wanderwelt.exe: Installation beim
// ersten Start, danach ein Tray-Programm, das beim Koordinator „Spielen“
// anfragt, als Host die Host-Sitzung betreibt und im Hintergrund weiterläuft.
//
// Ablauf von Run:
//
//	Sperre (eine Instanz pro Home()) → Log → ggf. nach Home() kopieren und
//	dort neu starten → Steuerung (control.json) → ggf. Installation →
//	Gruppe/Zustand laden → host.Recover → Tray
//
// Ein zweiter Start findet die Sperre belegt, schickt seinen Wunsch („play“
// oder „show“) über die Steuerung an die laufende Instanz und endet. Ist er
// eine andere wanderwelt.exe als die installierte (andere Bytes), ist er ein
// Update: Er schickt „update“ (die laufende Instanz beendet das Hosten
// sauber und endet), wartet auf die Sperre und ersetzt dann die
// installierte Kopie.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/discord"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

const (
	// ExeName ist der Name der installierten Kopie in Home().
	ExeName = "wanderwelt.exe"
	// HandoffFlag startet die Kopie in Home(), nachdem sich das Programm
	// dorthin kopiert hat; sie wartet dann kurz auf die Sperre.
	HandoffFlag = "--uebergabe"
	// UpdatedFlag (nach HandoffFlag): Die Kopie in Home() ist gerade durch
	// eine neue Version ersetzt worden; sie zeigt das an.
	UpdatedFlag = "--aktualisiert"

	handoffWait = 15 * time.Second
	// updateWait: So lange wartet eine neue Version darauf, dass die
	// laufende Instanz das Hosten beendet und die Sperre freigibt.
	updateWait = 15 * time.Minute
	// replaceWait: So lange wird versucht, die installierte Kopie zu
	// ersetzen (die alte Instanz kann gerade noch enden).
	replaceWait = 30 * time.Second
	// recoverRetry: So oft wird ein vorgemerkter Stand erneut nachgereicht.
	recoverRetry = 2 * time.Minute
	// recoverWait: So lange wartet „Spielen“ auf ein laufendes Nachreichen.
	recoverWait = 5 * time.Minute
	// statusInterval: So oft holt der Tray Host und Spieler. Nicht kürzer
	// als blobStall des Koordinators (internal/koord/server): Der erste
	// gelingende Status nach einem Verbindungsverlust stößt das Nachreichen
	// an (kickRecoverLocked); bis dahin muss der Koordinator den Blob des
	// abgerissenen Uploads freigegeben haben.
	statusInterval = 30 * time.Second
	// Mitspielen (watchJoin): Status alle joinPoll; nach der Bereitschaft
	// des Hosts noch joinWatch lang beobachten, ob er wieder verschwindet;
	// höchstens joinReadyMax auf die Bereitschaft warten; verschwindet er,
	// höchstens maxAutoPlay-mal von selbst neu fragen.
	joinPoll     = 5 * time.Second
	joinWatch    = 5 * time.Minute
	joinReadyMax = 30 * time.Minute
	maxAutoPlay  = 2
	// hostEndWait: So lange wartet das Programm beim Ende (Tray zu) auf das
	// saubere Ende einer Host-Sitzung (Stop höchstens 3 min, Schluss-Stand
	// samt Wiederholen).
	hostEndWait = 6 * time.Minute
	// unauthorizedRetry: Pause vor dem einen Wiederholen nach 401 oder 503.
	unauthorizedRetry = 2 * time.Second
	// notifyTitle steht über allen Hinweisen.
	notifyTitle = "wanderwelt"
)

// Options steuern Run.
type Options struct {
	// Version des Starters (für state.json).
	Version string
	// Play: „wanderwelt.exe play“ – nach dem Start sofort „Spielen“.
	Play bool
	// Handoff: von einer anderen Kopie nach dem Kopieren gestartet.
	Handoff bool
	// Updated: Diese Kopie hat gerade eine ältere Version ersetzt.
	Updated bool
	// JoinLink: Start über wanderwelt://beitreten (Beitreten-Seite); das
	// Argument des Links trägt nichts und wird nie ausgewertet.
	JoinLink bool
	// Invite: die beim Bauen eingesetzten Gruppen-Adressen (Base64-YAML,
	// siehe group.ParseInvite); leer: Einrichtung mit Gruppendatei.
	Invite string
}

type phase int

const (
	phReady     phase = iota // bereit
	phStarting               // „Spielen“ läuft: Messung, Zuschlag, Welt laden, Server startet
	phHosting                // hostet
	phJoined                 // spielt mit
	phUploading              // lädt hoch (Schluss-Stand, Nachreichen, Welt übernehmen)
	phError                  // Fehler
)

func (p phase) String() string {
	switch p {
	case phReady:
		return "bereit"
	case phStarting:
		return "startet"
	case phHosting:
		return "hostet"
	case phJoined:
		return "spielt mit"
	case phUploading:
		return "lädt hoch"
	case phError:
		return "Fehler"
	}
	return "?"
}

// App ist die laufende Instanz.
type App struct {
	version string
	invite  *group.Invite   // eingesetzte Gruppen-Adressen; nil: Einrichtung mit Gruppendatei
	updated bool            // gestartet mit --aktualisiert: die erneute Einrichtung läuft still
	ctx     context.Context // endet beim Beenden
	cancel  context.CancelFunc
	hc      *worldsync.HashCache

	// world: belegt, solange jemand Home()/server benutzt (Host-Sitzung,
	// Vorladen, Nachreichen). Kapazität 1.
	world chan struct{}

	// nameAsked: Die Einrichtung hat die Namensfrage gestellt; der Tray fragt
	// beim selben Start nicht noch einmal.
	nameAsked atomic.Bool

	// presetToken: Token aus „Entdecken“ der Einrichtung (nur während der
	// Einrichtung, nie im Log).
	presetToken string
	// zeigeEntdeckenBis: bis wann das Fenster zum Reiter „Entdecken“ springen
	// soll (wanderwelt://beitreten bei mehreren Gruppen; unter mu).
	zeigeEntdeckenBis time.Time

	mu        sync.Mutex
	g         *group.Group
	c         *client.Client
	st        *group.State // gehört während einer Host-Sitzung host.Run
	machineID string
	me        *api.Me
	status    *api.Status
	statusErr error
	phase     phase
	detail    string // Zusatz zum Zustand, etwa „lädt die Welt: 45 %“
	lastErr   string
	busy      string // laufende Aktion („Spielen“, „Welt übernehmen“, …)
	quitting  bool
	ending    bool // die Windows-Sitzung endet
	trayUp    bool // systray läuft (onTrayReady kam)

	// trayMu reiht updateTray und systray.Quit (quitNow): Nach Quit ist das
	// Symbol gelöscht, jede Änderung scheiterte dann.
	trayMu sync.Mutex

	worldHolder    string
	prefetchCancel context.CancelFunc
	recoverLoop    bool
	recoverKick    chan struct{} // solange recoverLoop: sofort neu versuchen (Koordinator wieder da)

	hostCancel context.CancelFunc
	hostDone   chan struct{}
	hostAddr   string

	joinCancel context.CancelFunc // beobachtet als Mitspieler den Host (watchJoin)
	joinSeq    int

	dauer dauerState // Dauerbetrieb (dauerbetrieb.go)

	uebergabe uebergabeState // Host-Übergabe (uebergabe.go)

	su selfUpdateState // Selbst-Update (selfupdate.go)

	hinweise hinweisPuffer // die letzten Hinweise, fürs Fenster

	// joinGemeldet: offene Beitrittsanfragen (E-Mail klein), für die der Admin
	// schon einen Hinweis bekam (beitritte.go; unter mu).
	joinGemeldet map[string]bool
	ent          entdeckenState    // Reiter Entdecken (entdecken.go)
	titel        map[string]string // Titel der Gruppen nach Kennung (nur lokal; unter mu)
	joinMu       sync.Mutex        // serialisiert beitrittsHinweis
	notfall      notfallState      // Notfallkopie (notfallkopie.go)

	// presence: Discord-Anzeige (anzeige.go); nil: aus (Tests).
	presence discord.Presence
	// lastJoinLink: wann zuletzt ein Beitritts-Link das Fenster öffnete (unter mu).
	lastJoinLink time.Time

	refreshCh chan struct{}
	playCh    chan struct{}
	quitCh    chan struct{}
}

// Run ist der Einstieg ohne Unterbefehl bzw. mit „play“. Ergebnis ist der
// Exit-Code.
func Run(opts Options) int {
	release, ok, err := lockInstance(opts.Handoff)
	if err != nil {
		showError("wanderwelt kann nicht starten: " + err.Error())
		return 1
	}
	if !ok {
		newer, err := differsFromInstalled()
		if err != nil {
			log.Printf("Vergleich mit der installierten Kopie: %v", err)
		}
		if !newer {
			action := actionShow
			if opts.Play {
				action = actionPlay
			}
			if opts.JoinLink {
				action = actionJoin
			}
			if err := sendToRunning(action); err != nil {
				showError(err.Error())
				return 1
			}
			return 0
		}
		// Neue Version: die laufende Instanz beenden lassen, dann ersetzen.
		if release, err = updateRunning(); err != nil {
			showError(err.Error())
			return 1
		}
	}
	defer release()

	logc, err := setupLog(false)
	if err != nil {
		log.Printf("Log nicht geöffnet: %v", err)
	} else {
		defer logc.Close()
	}
	log.Printf("wanderwelt %s startet (Home %s, PID %d)", opts.Version, group.Home(), os.Getpid())

	// Nicht aus Home() gestartet: dorthin kopieren und die Kopie übernehmen
	// lassen.
	if handed, err := handOffToInstalled(opts, release); err != nil {
		log.Printf("Kopie nach Home(): %v", err)
		showError("wanderwelt konnte sich nicht nach " + group.Home() + " kopieren:\n" + err.Error())
		return 1
	} else if handed {
		return 0
	}
	removeOldCopies()
	migrateLayout()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inv, err := group.ParseInvite(opts.Invite)
	if err != nil {
		// Ein kaputter Gruppen-Build verhält sich wie ein Build ohne Adressen.
		log.Printf("%v", err)
	}
	a := &App{
		version:   opts.Version,
		invite:    inv,
		updated:   opts.Updated,
		ctx:       ctx,
		cancel:    cancel,
		world:     make(chan struct{}, 1),
		refreshCh: make(chan struct{}, 1),
		playCh:    make(chan struct{}, 1),
		quitCh:    make(chan struct{}),
	}
	// Discord-Anzeige (aus/Noop, solange keine Client-ID steht).
	a.presence = discord.New(discord.Options{ClientID: discord.ClientID, Logf: log.Printf})
	defer a.discordClose()

	// Notfallkopie: bricht ab, wenn jemand den Platz braucht; Kopie auch nach
	// nachgereichten Ständen.
	host.SetSyncContention(a.abortNotfall)
	host.SetRecoveredHook(a.onCommitted)
	defer host.SetSyncContention(nil)
	defer host.SetRecoveredHook(nil)

	ctl, err := startControl(a.onControl, a)
	if err != nil {
		log.Printf("%v", err)
		showError("wanderwelt kann nicht starten: " + err.Error())
		return 1
	}
	defer ctl.Close()
	if opts.Play {
		a.requestPlay()
	}
	if opts.Updated {
		log.Printf("aktualisiert auf %s", opts.Version)
		go notify("wanderwelt wurde aktualisiert (Version " + opts.Version + ").")
	}

	freshInstall := false
	joinLink := opts.JoinLink
	if !installed() {
		joinLink = false // die Einrichtung fragt selbst nach dem Gruppencode
		if err := a.install(ctx); err != nil {
			if errors.Is(err, errLater) {
				log.Printf("Einrichtung wartet auf die Freigabe; sie geht beim nächsten Start weiter")
				return 0
			}
			if errors.Is(err, errCanceled) {
				log.Printf("Installation abgebrochen")
				return 1
			}
			log.Printf("Installation gescheitert: %v", err)
			showError("Einrichtung gescheitert:\n" + explain(err))
			return 1
		}
		freshInstall = true
	}

	if err := a.load(); err != nil {
		log.Printf("Start: %v", err)
		showError("wanderwelt kann nicht starten:\n" + explain(err))
		return 1
	}
	// Ab jetzt zeigt ein offenes Fenster die Meldungen statt der Dialoge;
	// Einrichtung und Startfehler (danach endet das Programm) bleiben Dialoge.
	fensterInstanz.Store(a)
	// Eine Hash-Cache-Instanz für alles in diesem Prozess: Recover (auch
	// das spätere Nachreichen), Vorladen, Host-Sitzung, Welt übernehmen.
	hc := worldsync.LoadHashCache()
	a.mu.Lock()
	a.hc = hc
	a.mu.Unlock()
	a.recoverAtStart()

	if err := winsys.OnShutdown(winsys.ShutdownHooks{
		Hosting: a.shutdownHosting,
		Finish:  a.finishForShutdown,
		Ended:   a.sessionEnded,
	}); err != nil {
		log.Printf("Herunterfahren abfangen nicht möglich: %v", err)
	}

	if joinLink {
		go a.onJoinLink()
	}
	go a.statusLoop()
	go a.dauerbetriebLoop()
	go a.presenceLoop()
	a.registerJoinProtocol()
	go a.discordLoop()
	go a.selfUpdateLoop()
	if freshInstall {
		a.prefetch()
	}
	if !a.isQuitting() { // „update“ kann schon vor dem Tray gekommen sein
		a.runTray()
	}
	// Endete der Tray nicht über „Beenden“ (Windows schließt sein Fenster),
	// kann noch gehostet werden: erst sauber beenden.
	a.finishHosting(hostEndWait)
	// Eine laufende Notfallkopie nicht abwürgen (höchstens notfallEndWait).
	a.waitNotfall(notfallEndWait)
	a.discordClose()
	a.shutdownBackground()
	log.Printf("wanderwelt beendet")
	return 0
}

// lockInstance belegt die Sperre dieses Home(). Nach einer Übergabe wird
// kurz gewartet, bis die vorige Kopie sie freigegeben hat.
func lockInstance(handoff bool) (release func(), ok bool, err error) {
	wait := time.Duration(0)
	if handoff {
		wait = handoffWait
	}
	return lockInstanceWait(wait)
}

// lockInstanceWait belegt die Sperre dieses Home() und wartet dabei
// höchstens wait, falls sie belegt ist.
func lockInstanceWait(wait time.Duration) (release func(), ok bool, err error) {
	name := `Local\wanderwelt-` + group.HomeKey()
	deadline := time.Now().Add(wait)
	for {
		release, ok, err = winsys.SingleInstance(name)
		if err != nil || ok || time.Now().After(deadline) {
			return release, ok, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// differsFromInstalled meldet, ob dieses Programm nicht die installierte
// Kopie Home()/wanderwelt.exe ist und andere Bytes hat (eine andere
// Version). Fehlt die installierte Kopie, zählt das als anders.
func differsFromInstalled() (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	target := installedExe()
	if samePath(self, target) {
		return false, nil
	}
	if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	same, err := sameContent(self, target)
	return !same && err == nil, err
}

// sameContent vergleicht zwei Dateien über ihren SHA-256.
func sameContent(a, b string) (bool, error) {
	ha, err := fileSHA256(a)
	if err != nil {
		return false, err
	}
	hb, err := fileSHA256(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// updateRunning bittet die laufende Instanz um „update“ (Hosten sauber
// beenden, dann enden) und wartet höchstens updateWait auf ihre Sperre.
func updateRunning() (release func(), err error) {
	if err := sendToRunning(actionUpdate); err != nil {
		if errors.Is(err, errUnknownAction) {
			return nil, errors.New("Die laufende wanderwelt-Version kann sich nicht selbst ersetzen. " +
				"Bitte wanderwelt über das Symbol im Infobereich beenden („Beenden“) und die neue Version dann noch einmal starten.")
		}
		return nil, err
	}
	release, ok, err := lockInstanceWait(updateWait)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("Die laufende wanderwelt-Instanz hat sich nach %v nicht beendet (hostet sie noch?). "+
			"Bitte die neue Version später noch einmal starten.", updateWait)
	}
	return release, nil
}

// installedExe ist der Ort der installierten Kopie: Home()/wanderwelt.exe.
func installedExe() string { return filepath.Join(group.Home(), ExeName) }

// handOffToInstalled kopiert das laufende Programm nach Home(), wenn es
// nicht schon von dort läuft, gibt die Sperre frei und startet die Kopie
// mit denselben Argumenten. handed: Dieser Prozess soll jetzt enden. Hatte
// die installierte Kopie andere Bytes, ist das ein Update; die Kopie zeigt
// es dann an.
func handOffToInstalled(opts Options, release func()) (handed bool, err error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	target := installedExe()
	if samePath(self, target) {
		return false, nil
	}
	updated := false
	if _, statErr := os.Stat(target); statErr == nil {
		same, err := sameContent(self, target)
		updated = err == nil && !same
	}
	if err := copySelf(self, target); err != nil {
		if _, statErr := os.Stat(target); statErr != nil {
			return false, err
		}
		// Die vorhandene Kopie ist vielleicht älter, aber lauffähig.
		log.Printf("Kopie nach %s gescheitert, die vorhandene übernimmt: %v", target, err)
		if updated {
			showError("Die neue Version ließ sich nicht nach " + target + " kopieren; die bisherige startet wieder.\n\n" + err.Error())
		}
		updated = false
	}
	args := []string{HandoffFlag}
	if updated {
		args = append(args, UpdatedFlag)
	}
	if opts.Play {
		args = append(args, "play")
	}
	if opts.JoinLink {
		args = append(args, JoinLinkArg)
	}
	cmd := exec.Command(target, args...)
	cmd.Dir = group.Home()
	release()
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("%s starten: %w", target, err)
	}
	log.Printf("an %s übergeben (PID %d)", target, cmd.Process.Pid)
	cmd.Process.Release()
	return true, nil
}

func samePath(a, b string) bool {
	ia, err1 := os.Stat(a)
	ib, err2 := os.Stat(b)
	if err1 == nil && err2 == nil {
		return os.SameFile(ia, ib)
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func copySelf(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ExeName+".*.tmp")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	// Ersetzen ist atomar (Rename). Die alte Kopie kann noch laufen (ein
	// offenes Fenster, eine gerade endende Instanz) oder von einem
	// Virenscanner offen sein. Eine laufende exe lässt sich unter Windows
	// nicht ersetzen, wohl aber umbenennen: dann beiseiteschieben
	// (wanderwelt.exe.alt-…, removeOldCopies räumt sie beim nächsten Start
	// weg). Wiederholen, höchstens replaceWait lang.
	deadline := time.Now().Add(replaceWait)
	for i := 1; ; i++ {
		if err = os.Rename(tmp.Name(), dst); err == nil {
			return nil
		}
		aside := dst + oldCopyMark + strconv.FormatInt(time.Now().UnixNano(), 36)
		if os.Rename(dst, aside) == nil {
			if err = os.Rename(tmp.Name(), dst); err == nil {
				log.Printf("%s lief noch; die alte Kopie liegt jetzt unter %s", dst, filepath.Base(aside))
				return nil
			}
			os.Rename(aside, dst) // wenigstens die alte Kopie behalten
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(min(time.Duration(i)*50*time.Millisecond, 500*time.Millisecond))
	}
	os.Remove(tmp.Name())
	return err
}

// oldCopyMark: Endung der beiseitegeschobenen alten Kopien
// (wanderwelt.exe.alt-<Zeit>).
const oldCopyMark = ".alt-"

// removeOldCopies löscht beiseitegeschobene alte Kopien in Home(). Läuft
// eine noch (ein Fenster der alten Version), bleibt sie bis zum nächsten
// Start liegen.
func removeOldCopies() {
	olds, _ := filepath.Glob(filepath.Join(group.Home(), ExeName+oldCopyMark+"*"))
	for _, f := range olds {
		if err := os.Remove(f); err == nil {
			log.Printf("alte Kopie %s gelöscht", filepath.Base(f))
		}
	}
}

// installed: Gruppendatei, PC-Kennung und abgeschlossene Installation.
func installed() bool {
	if _, err := group.LoadGroup(group.GroupPath()); err != nil {
		return false
	}
	st, err := group.LoadState()
	return err == nil && st.MachineID != "" && st.InstalledVersion != ""
}

// load liest Gruppendatei und Zustand.
func (a *App) load() error {
	g, err := group.LoadGroup(group.GroupPath())
	if err != nil {
		return err
	}
	st, err := group.LoadState()
	if err != nil {
		return err
	}
	if st.MachineID == "" {
		return errors.New("keine PC-Kennung in state.json")
	}
	if st.InstalledVersion != a.version && a.version != "" {
		st.InstalledVersion = a.version
		if err := st.Save(); err != nil {
			log.Printf("Zustand nicht gespeichert: %v", err)
		}
	}
	a.mu.Lock()
	a.g, a.c, a.st, a.machineID = g, client.New(g), st, st.MachineID
	a.mu.Unlock()
	return nil
}

// ---- Steuerung ----

func (a *App) onControl(action string, body []byte) {
	switch action {
	case actionPlay:
		a.requestPlay()
	case actionJoin:
		a.onJoinLink()
	case actionShow:
		go notify("wanderwelt läuft schon – das Symbol ist im Infobereich der Taskleiste.")
	case actionUpdate:
		go a.quitForUpdate()
	case actionImportCLI:
		var req importRequest
		if err := json.Unmarshal(body, &req); err != nil || req.Dir == "" {
			log.Printf("Steuerung: import ohne gültigen Ordner: %v", err)
			return
		}
		a.mu.Lock()
		ready := a.st != nil && a.hc != nil
		a.mu.Unlock()
		if !ready {
			go notify("Welt übernehmen von der Kommandozeile: wanderwelt startet noch – bitte gleich noch einmal.")
			return
		}
		go a.importFromControl(req)
	}
}

// requestPlay merkt „Spielen“ vor; der Tray führt es aus, sobald er läuft.
func (a *App) requestPlay() {
	select {
	case a.playCh <- struct{}{}:
	default:
	}
}

// ---- Zustand für den Tray ----

func (a *App) setPhase(p phase, detail string) {
	a.mu.Lock()
	a.phase, a.detail = p, detail
	if p != phError {
		a.lastErr = ""
	}
	a.mu.Unlock()
	a.refresh()
}

func (a *App) setDetail(detail string) {
	a.mu.Lock()
	a.detail = detail
	a.mu.Unlock()
	a.refresh()
}

// fail zeigt einen Fehler: Zustand Fehler (bis zur nächsten Aktion) und
// Hinweis.
func (a *App) fail(prefix string, err error) {
	msg := explain(err)
	if prefix != "" {
		msg = prefix + ": " + msg
	}
	a.showFailed(msg, err)
	go notify(msg)
}

// showFailed setzt den Zustand Fehler (ohne Hinweis; den zeigt der
// Aufrufer, etwa als Dialog).
func (a *App) showFailed(msg string, err error) {
	log.Printf("Fehler: %s (%v)", msg, err)
	a.mu.Lock()
	a.phase, a.detail, a.lastErr = phError, "", msg
	a.mu.Unlock()
	a.refresh()
}

func (a *App) refresh() {
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
}

// begin belegt die einzige laufende Aktion; false: es läuft schon eine.
func (a *App) begin(what string) (ok bool, running string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.quitting {
		return false, "Beenden"
	}
	if a.busy != "" {
		return false, a.busy
	}
	a.busy = what
	return true, ""
}

func (a *App) end() {
	a.mu.Lock()
	a.busy = ""
	a.mu.Unlock()
	a.refresh()
}

// endWith gibt die laufende Aktion frei und zeigt danach ihr Ergebnis
// (nil: keins). Ergebnis- und Fehlerfenster sind modal und bleiben offen,
// bis jemand sie wegklickt; solange soll schon „Spielen“ gehen.
func (a *App) endWith(r *resultDialog) {
	a.end()
	r.show()
}

// resultDialog ist das Ergebnis- oder Fehlerfenster am Ende einer Aktion
// (begin … endWith). Fragen, die über den Ablauf entscheiden (confirm,
// Auswahl), stellt die Aktion selbst, solange sie läuft.
type resultDialog struct {
	text  string
	isErr bool
}

func infoDialog(text string) *resultDialog  { return &resultDialog{text: text} }
func errorDialog(text string) *resultDialog { return &resultDialog{text: text, isErr: true} }

func (r *resultDialog) show() {
	switch {
	case r == nil:
	case r.isErr:
		showError(r.text)
	default:
		showInfo(r.text)
	}
}

func (a *App) client() *client.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.c
}

// ---- Welt-Ordner ----

func (a *App) tryWorld(holder string) bool {
	select {
	case a.world <- struct{}{}:
		a.mu.Lock()
		a.worldHolder = holder
		a.mu.Unlock()
		return true
	default:
		return false
	}
}

func (a *App) waitWorld(ctx context.Context, holder string) error {
	select {
	case a.world <- struct{}{}:
		a.mu.Lock()
		a.worldHolder = holder
		a.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) releaseWorld() {
	a.mu.Lock()
	a.worldHolder = ""
	a.mu.Unlock()
	<-a.world
}

// ---- Nachreichen (host.Recover) ----

// recoverAtStart läuft vor dem Tray: Reste eines Absturzes aufräumen und
// einen vorgemerkten Stand nachreichen. Gibt es etwas zu tun (Vormerkung
// oder Markierung), läuft es im Hintergrund, damit der Tray gleich da ist;
// der Welt-Ordner bleibt solange belegt (Spielen wartet darauf).
func (a *App) recoverAtStart() {
	if err := a.waitWorld(a.ctx, "recover"); err != nil {
		return
	}
	a.mu.Lock()
	st, c := a.st, a.c
	a.mu.Unlock()
	run := func() {
		var notes recoverNotes
		err := recoverWorld(a.ctx, c, st, "", a.hc, notes.add)
		pending := st.PendingUpload
		a.releaseWorld()
		if a.handleRecover(err, pending, notes) {
			a.startRecoverLoop()
		}
	}
	_, markerErr := os.Lstat(filepath.Join(host.DefaultDir(), host.MarkerName))
	if !st.PendingUpload && errors.Is(markerErr, fs.ErrNotExist) {
		run() // nichts nachzureichen oder aufzuräumen: sofort fertig
		return
	}
	if st.PendingUpload {
		go notify("Der letzte Stand dieses PCs wird nachgereicht …")
	}
	go run()
}

// handleRecover wertet das Ergebnis von host.Recover aus; retry: später
// noch einmal versuchen. notes sind die Hinweise, die Recover unterwegs
// gegeben hat (etwa host.OrphanSavedNotice); sie erscheinen erst jetzt, in
// einer Benachrichtigung vor dem Hinweis zum Ergebnis – so nie in
// zufälliger Reihenfolge und nie „wird hochgeladen“ nach „verworfen“.
func (a *App) handleRecover(err error, pending bool, notes recoverNotes) (retry bool) {
	return a.reportRecover(err, pending, notes, false)
}

// reportRecover ist handleRecover; later: ein späterer Versuch
// (startRecoverLoop). Gelingt der, gibt es immer einen Hinweis – zuletzt
// hieß es ja „wird nachgereicht“.
func (a *App) reportRecover(err error, pending bool, notes recoverNotes, later bool) (retry bool) {
	var de *host.DiscardedError
	var outcome string
	switch {
	case err == nil && later && !pending:
		outcome = "Der vorgemerkte Stand ist jetzt hochgeladen."
	case err == nil:
		// Ohne Hinweise von Recover gibt es dazu nichts zu sagen.
		if len(notes) > 0 {
			outcome = "Der Stand ist hochgeladen."
			if pending {
				outcome = "Der Stand wird nachgereicht, sobald es geht."
			}
		}
		retry = pending
	case errors.As(err, &de):
		log.Printf("Recover: %v", de)
		outcome = discardText(de)
	case errors.Is(err, host.ErrMaybeRunning):
		// Erst ein Neustart des PCs (oder das Beenden von Java) klärt das.
		log.Printf("Recover: %v", err)
		outcome = explain(err)
	case errors.Is(err, host.ErrPendingLater):
		log.Printf("Recover: %v – neuer Versuch in %v", err, recoverRetry)
		if len(notes) > 0 {
			outcome = "Der Stand wird nachgereicht, sobald es geht."
		}
		retry = true
	default:
		log.Printf("Recover: %v", err)
		if pending {
			outcome = fmt.Sprintf("Der letzte Stand konnte noch nicht nachgereicht werden; neuer Versuch in %v. (%s)", recoverRetry, explain(err))
		}
		retry = pending
	}
	if text := strings.Join(append(notes, outcome), " "); strings.TrimSpace(text) != "" {
		go notify(strings.TrimSpace(text))
	}
	return retry
}

// recoverNotes sammelt die Hinweise von host.Recover, die kein Fehler sind
// (etwa: der Stand eines weiterlaufenden Servers wurde gesichert), bis das
// Ergebnis feststeht (handleRecover). Recover ruft add in der Goroutine
// des Aufrufers auf.
type recoverNotes []string

func (n *recoverNotes) add(text string) { *n = append(*n, text) }

// discardText ist der Hinweis zu einem verworfenen lokalen Stand.
func discardText(de *host.DiscardedError) string {
	return fmt.Sprintf("Der lokale Stand wurde nicht hochgeladen (%s). Er liegt zur Sicherheit unter %s; aufbewahrt werden die zwei neuesten.", de.Reason, de.Path)
}

// recoverWorld ist host.RecoverWithCache; im Test austauschbar.
var recoverWorld = host.RecoverWithCache

// startRecoverLoop versucht alle recoverRetry erneut nachzureichen, solange
// der Starter läuft und noch ein Stand vorgemerkt ist – und sofort, wenn der
// Koordinator nach Fehlern wieder antwortet (kickRecoverLocked).
func (a *App) startRecoverLoop() {
	a.mu.Lock()
	if a.recoverLoop {
		a.mu.Unlock()
		return
	}
	a.recoverLoop = true
	kick := make(chan struct{}, 1)
	a.recoverKick = kick
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			a.recoverLoop, a.recoverKick = false, nil
			a.mu.Unlock()
		}()
		t := time.NewTicker(recoverRetry)
		defer t.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
			case <-kick:
				log.Printf("Nachreichen: Der Koordinator antwortet wieder – neuer Versuch")
				t.Reset(recoverRetry)
			}
			if !a.tryWorld("recover") {
				continue // hostet oder lädt gerade
			}
			a.mu.Lock()
			st, c := a.st, a.c
			a.mu.Unlock()
			if !st.PendingUpload {
				a.releaseWorld()
				return
			}
			var notes recoverNotes
			err := recoverWorld(a.ctx, c, st, "", a.hc, notes.add)
			pending := st.PendingUpload
			a.releaseWorld()
			if !a.reportRecover(err, pending, notes, true) {
				return
			}
		}
	}()
}

// kickRecoverLocked: Der Koordinator antwortet nach Fehlern wieder. Wartet
// ein vorgemerkter Stand aufs Nachreichen, versucht startRecoverLoop es
// gleich statt erst im nächsten Takt. Aufruf mit gehaltenem a.mu.
func (a *App) kickRecoverLocked() {
	if a.recoverKick == nil {
		return
	}
	select {
	case a.recoverKick <- struct{}{}:
	default:
	}
}

// ---- Vorladen ----

// prefetch lädt nach der Installation den aktuellen Stand im Hintergrund
// nach Home()/server/world, damit der erste Start als Host schnell geht.
func (a *App) prefetch() {
	if !a.tryWorld("prefetch") {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.prefetchCancel = cancel
	st, c := a.st, a.c
	a.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			a.mu.Lock()
			a.prefetchCancel = nil
			a.mu.Unlock()
			a.setDetail("")
			a.releaseWorld()
		}()
		dir := host.DefaultDir()
		if st.PendingUpload {
			log.Printf("Vorladen: ein Stand wartet aufs Nachreichen – nicht vorladen")
			return
		}
		if _, err := os.Stat(filepath.Join(dir, host.MarkerName)); err == nil {
			log.Printf("Vorladen: Markierungsdatei vorhanden – nicht vorladen")
			return
		}
		head, err := retry401(ctx, func() (*api.Generation, error) { return c.Head(ctx) })
		if err != nil {
			log.Printf("Vorladen: %v", err)
			return
		}
		if head == nil {
			log.Printf("Vorladen: es gibt noch keine Welt")
			return
		}
		log.Printf("Vorladen: Stand %s/%d (%d Dateien)", head.WorldID, head.Number, len(head.Files))
		// Derselbe Platz wie Download und Stände der Host-Sitzung und von
		// Recover; dieselbe Hash-Cache-Instanz wie die Host-Sitzung.
		unlock, err := host.HoldSync(ctx)
		if err != nil {
			log.Printf("Vorladen: abgebrochen")
			return
		}
		defer unlock()
		pr := newPercent(func(p int) { a.setDetail(fmt.Sprintf("lädt die Welt vor: %d %%", p)) })
		err = worldsync.Download(ctx, c, head, filepath.Join(dir, host.WorldName), a.hc, pr.report)
		switch {
		case err == nil:
			log.Printf("Vorladen: fertig")
		case ctx.Err() != nil:
			log.Printf("Vorladen: abgebrochen")
		default:
			log.Printf("Vorladen: %v", err)
		}
	}()
}

// percent meldet ganze Prozente nur bei Änderung.
type percent struct {
	mu   sync.Mutex
	last int
	fn   func(int)
}

func newPercent(fn func(int)) *percent { return &percent{last: -1, fn: fn} }

func (p *percent) report(done, total int64) {
	if total <= 0 {
		return
	}
	v := int(done * 100 / total)
	p.mu.Lock()
	changed := v != p.last
	p.last = v
	p.mu.Unlock()
	if changed {
		p.fn(v)
	}
}

// ---- Ende ----

// finishHosting: Das Programm endet gleich. Läuft noch eine Host-Sitzung,
// wird sie sauber beendet (Stop, Schluss-Stand, Zuschlag zurück); gewartet
// wird höchstens wait.
func (a *App) finishHosting(wait time.Duration) {
	a.mu.Lock()
	done := a.hostDone
	a.mu.Unlock()
	if done == nil {
		return
	}
	a.endHosting("Programm endet")
	select {
	case <-done:
	case <-time.After(wait):
		log.Printf("Hosten nach %v nicht beendet – das Programm endet trotzdem", wait)
	}
}

// shutdownBackground: Der Tray ist zu. Laufende Hintergrundarbeit abbrechen.
func (a *App) shutdownBackground() {
	a.cancel()
}

// ---- Hilfen ----

// retry401 wiederholt f einmal nach 2 s, wenn der Koordinator (über
// tunneld) mit 401 oder 503 antwortet (tunneld kurz ohne Verbindung zum
// Koordinator); bleibt es bei 401, ist das errTokenInvalid.
func retry401[T any](ctx context.Context, f func() (T, error)) (T, error) {
	v, err := f()
	if !isStatus(err, 401) && !isStatus(err, 503) {
		return v, err
	}
	log.Printf("Koordinator: %v – neuer Versuch in %v", err, unauthorizedRetry)
	select {
	case <-time.After(unauthorizedRetry):
	case <-ctx.Done():
		return v, err
	}
	v, err = f()
	if isStatus(err, 401) {
		err = fmt.Errorf("%w: %w", errTokenInvalid, err)
	}
	return v, err
}

var (
	errCanceled     = errors.New("abgebrochen")
	errTokenInvalid = errors.New("der Token in der Gruppendatei wird abgelehnt")
)

func isStatus(err error, code int) bool {
	se, ok := errors.AsType[*client.StatusError](err)
	return ok && se.StatusCode == code
}

// hostAnzeige ist der Name des Hosts für Tooltip und Hinweise. Hat er noch
// keinen Minecraft-Namen, steht eine Umschreibung da: fremde E-Mail-Adressen
// gehören nur in den Reiter Mitglieder (Admins), nicht in Log und Hinweise.
func hostAnzeige(h *api.HostInfo) string {
	if h == nil || h.MinecraftName == "" {
		return "ein Mitglied (ohne Minecraft-Namen)"
	}
	return h.MinecraftName
}

// explain macht aus einem Fehler einen Satz für Menschen.
func explain(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errTokenInvalid):
		return "Der Koordinator lehnt deinen persönlichen Token ab (401). Ist dein tunnel-Konto schon freigeschaltet und der Token in der Gruppendatei noch gültig? Frag sonst den Admin oder lade über das Tray-Menü eine neue Gruppendatei."
	case isStatus(err, 403):
		se, _ := errors.AsType[*client.StatusError](err)
		if strings.Contains(se.Message, "gehört nicht zur Gruppe") {
			return "Dein tunnel-Konto ist nicht Mitglied dieser wanderwelt-Gruppe (403). Bitte den Admin, dich in die Mitgliederliste aufzunehmen."
		}
		return "Der Koordinator verweigert das (403): " + se.Message
	case errors.Is(err, context.Canceled):
		return "abgebrochen"
	case errors.Is(err, context.DeadlineExceeded):
		return "Der Koordinator antwortet nicht rechtzeitig – bitte gleich noch einmal versuchen."
	}
	if se, ok := errors.AsType[*client.StatusError](err); ok && se.StatusCode >= 500 {
		return fmt.Sprintf("Der Koordinator meldet einen Fehler (%d) – bitte später noch einmal versuchen.", se.StatusCode)
	}
	if _, ok := errors.AsType[*net.OpError](err); ok {
		return "Der Koordinator ist nicht erreichbar – ist der PC online? (" + err.Error() + ")"
	}
	return err.Error()
}

// newMachineID ist eine zufällige UUID (Version 4).
func newMachineID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- Meldungen: als Dialog oder im Fenster ----

// notify zeigt eine Benachrichtigung (blockiert, bis sie angezeigt ist;
// deshalb meist mit go aufgerufen). Im Test austauschbar.
var notify = func(text string) { melden(hinweisInfo, text, toast) }

// showError und showInfo zeigen ein modales Fenster und kehren erst zurück,
// wenn jemand es wegklickt. Im Test austauschbar.
var showError = func(text string) { melden(hinweisFehler, text, errorBox) }

var showInfo = func(text string) { melden(hinweisInfo, text, infoBox) }

// toast, errorBox und infoBox zeigen wirklich an (zenity); im Test
// austauschbar.
var toast = func(text string) {
	log.Printf("Hinweis: %s", text)
	if err := zenity.Notify(text, zenity.Title(notifyTitle), notifyIcon()); err != nil {
		log.Printf("Hinweis nicht angezeigt: %v", err)
	}
}

var errorBox = func(text string) {
	log.Printf("Fehlermeldung: %s", text)
	zenity.Error(text, zenity.Title(notifyTitle), windowIcon())
}

var infoBox = func(text string) {
	log.Printf("Meldung: %s", text)
	zenity.Info(text, zenity.Title(notifyTitle), windowIcon())
}

// confirm stellt eine Ja/Nein-Frage – immer als Dialog, auch bei offenem
// Fenster. Im Test austauschbar.
var confirm = func(text, yes, no string) bool {
	err := zenity.Question(text, zenity.Title(notifyTitle), windowIcon(), zenity.OKLabel(yes), zenity.CancelLabel(no))
	return err == nil
}

const (
	// maxHinweise: so viele Hinweise hält die Instanz für das Fenster.
	maxHinweise = 20
	// fensterFrist: So lange nach seiner letzten Abfrage des Zustands gilt
	// das Fenster als offen (es fragt alle 1,5 s).
	fensterFrist = 5 * time.Second

	hinweisInfo   = "info"
	hinweisFehler = "fehler"
)

// fensterInstanz ist die laufende Instanz, deren Fenster Meldungen statt
// der Dialoge zeigt; gesetzt, sobald sie geladen ist. nil: immer Dialoge.
var fensterInstanz atomic.Pointer[App]

// fensterNow ist die Uhr für Hinweise und die Frist des Fensters; im Test
// austauschbar.
var fensterNow = time.Now

// melden macht text zum Hinweis der Instanz und zeigt ihn mit show an –
// außer das Fenster ist offen (hat in den letzten fensterFrist den Zustand
// abgefragt): Dann erscheint er nur dort. Eine Benachrichtigung, die den
// aktuellen Fehler wiederholt (fail, Dauerbetrieb), ist ein Hinweis der Art
// „fehler“.
func melden(art, text string, show func(string)) {
	if a := fensterInstanz.Load(); a != nil {
		if art == hinweisInfo && a.repeatsError(text) {
			art = hinweisFehler
		}
		if a.hinweise.add(art, text, fensterNow()) {
			log.Printf("Hinweis im Fenster (%s): %s", art, text)
			return
		}
	}
	show(text)
}

// repeatsError: text beginnt mit dem Fehler, den der Zustand gerade zeigt.
func (a *App) repeatsError(text string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr != "" && strings.HasPrefix(text, a.lastErr)
}

// hinweisPuffer hält die letzten maxHinweise Hinweise und die Zeit der
// letzten Abfrage durch das Fenster.
type hinweisPuffer struct {
	mu        sync.Mutex
	seq       int64
	list      []fensterapi.Hinweis // Seq aufsteigend
	abgefragt time.Time            // letzte Abfrage des Zustands (Null: nie)
}

// add hängt einen Hinweis an; fensterOffen: Das Fenster zeigt ihn.
func (h *hinweisPuffer) add(art, text string, now time.Time) (fensterOffen bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	h.list = append(h.list, fensterapi.Hinweis{Seq: h.seq, Zeit: now, Art: art, Text: text})
	if len(h.list) > maxHinweise {
		h.list = slices.Clone(h.list[len(h.list)-maxHinweise:])
	}
	return h.offenLocked(now)
}

// abfrage: Das Fenster holt den Zustand – es gilt ab jetzt fensterFrist
// lang als offen. Ergebnis sind die Hinweise.
func (h *hinweisPuffer) abfrage(now time.Time) []fensterapi.Hinweis {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.abgefragt = now
	return slices.Clone(h.list)
}

// fensterOffen meldet, ob das Fenster zuletzt vor weniger als fensterFrist
// abgefragt hat.
func (h *hinweisPuffer) fensterOffen(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.offenLocked(now)
}

func (h *hinweisPuffer) offenLocked(now time.Time) bool {
	return !h.abgefragt.IsZero() && now.Sub(h.abgefragt) < fensterFrist
}
