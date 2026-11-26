// Package host ist die Host-Sitzung des Starters: Sie lädt den neuesten
// Stand, startet den Minecraft-Server, gibt ihn über tunnel frei, hält den
// Zuschlag mit Lebenszeichen, reicht regelmäßig einen Zwischenstand ein
// (Abstand vom Koordinator, Standard 5 Minuten) und beendet alles sauber –
// nach der Leerlauf-Frist ohne Spieler (vom Koordinator, Standard 10
// Minuten; im Dauerbetrieb nicht), wenn der Aufrufer es verlangt oder ein
// Admin einen älteren Stand zurückholt (Ansage im Chat, 30 s später) – oder
// stoppt sofort, wenn der Zuschlag nicht mehr gesichert ist (Fencing).
// Jeder Stand nennt die Spieler, die seit dem vorigen online waren; ändern
// sich Mitglieder oder Rollen, gleicht die Sitzung Whitelist und Ops des
// laufenden Servers an.
//
// Sicherheitskern:
//   - Zwei Server laufen nie auf derselben Welt weiter: Kann der Host 60 s
//     lang kein Lebenszeichen absetzen oder lehnt der Koordinator
//     Lebenszeichen oder Commit als „Zuschlag verloren“ ab, beendet er
//     sofort tunnel und stoppt den Server (höchstens 25 s zum Speichern,
//     dann hart) und lädt nichts mehr hoch. Der Koordinator vergibt den
//     Zuschlag frühestens nach 90 s neu.
//   - Nie wird etwas halb Geschriebenes hochgeladen: Zwischenstände werden
//     zwischen save-off/save-all flush und save-on bereitgestellt, der
//     Schluss-Stand nach sauberem Stop. Die Markierungsdatei
//     <Dir>/.wanderwelt-running steht, solange der Server die Welt
//     verändern kann; nur ein sauberer Stop entfernt sie. Findet Recover sie
//     beim nächsten Start, wird die Welt verworfen statt hochgeladen.
//
// Aufbau von Run: Eine Schleife in einer Goroutine hält den ganzen Zustand
// und trifft jede Zeitentscheidung über die Uhr aus Deps. Alles, was
// blockieren kann (Koordinator, Server-Befehle, Dateien), läuft in eigenen
// Goroutinen und meldet sein Ergebnis an die Schleife zurück. So kommt das
// Fencing auch dann pünktlich, wenn gerade ein Aufruf hängt.
package host

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/mcserver"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// Zeiten der Sitzung (Global Constraints).
const (
	HeartbeatInterval = 10 * time.Second // Lebenszeichen
	FenceAfter        = 60 * time.Second // so lange ohne erfolgreiches Lebenszeichen → Fencing
	PlayersInterval   = 15 * time.Second // Spielerliste abfragen
	SnapshotInterval  = 5 * time.Minute  // Zwischenstand, wenn der Koordinator keinen gültigen Abstand liefert
	// DefaultEmptyTimeout: so lange ohne Spieler → sauberes Ende, wenn der
	// Koordinator keine gültige Leerlauf-Frist liefert.
	DefaultEmptyTimeout = 10 * time.Minute
)

// Namen im Server-Ordner.
const (
	// MarkerName: steht, solange der Server die Welt verändern kann.
	MarkerName = ".wanderwelt-running"
	// StagingName: Bereitstellungsordner für Zwischen- und Schluss-Stände.
	StagingName = ".wanderwelt-staging"
	// WorldName: der Weltordner (level-name).
	WorldName = "world"
)

// Ports: lokaler Minecraft-Server und öffentlicher Port bei tunneld.
const (
	MinecraftPort = 25565
	PublicPort    = 15565
)

const (
	// heartbeatTimeout: Ein Lebenszeichen darf nicht länger hängen als bis
	// zum nächsten.
	heartbeatTimeout = HeartbeatInterval
	playersTimeout   = 15 * time.Second
	saveOnTimeout    = 30 * time.Second
	releaseTimeout   = 15 * time.Second
	// stopTimeout ist die eigene Frist für Stop (Stop selbst wartet
	// höchstens 2 Minuten, dann hart).
	stopTimeout = 3 * time.Minute
	// fenceStopGrace: So lange darf der Server nach dem Fencing (gerechnet
	// ab dem Fencing) zum sauberen Stoppen brauchen; danach wird er hart
	// beendet und sein Stand nicht vorgemerkt. tunnel endet beim Fencing
	// sofort, niemand spielt mehr auf ihm – die Frist dient nur dem
	// Speichern. Das Fencing kommt spätestens FenceAfter (60 s) nach dem
	// letzten erfolgreichen Lebenszeichen, der Koordinator vergibt den
	// Zuschlag frühestens 90 s danach neu (lease.LeaseTTL): Ein schon
	// laufender Stop endet also spätestens nach 85 s, vor dem Ablauf des
	// Zuschlags. Später aus sein kann der Server, wenn Stop noch wartet:
	// auf das release eines Zwischenstands bis zu saveOnTimeout (30 s), auf
	// einen abgebrochenen Start bis zu dessen eigener Frist in mcserver
	// (2 min). Sicher bleibt es auch dann: Nach der Frist wird nur noch hart
	// beendet und nichts vorgemerkt, und ohne tunnel erreicht den Server
	// niemand mehr.
	fenceStopGrace = 25 * time.Second
	// tunnelKillWait: so lange auf das Ende eines beendeten tunnels warten.
	tunnelKillWait = 10 * time.Second
	// tunnelStable: Lief tunnel so lange, beginnt die Pause wieder bei 2 s.
	tunnelStable = time.Minute
	// pidPoll: So oft wird während Start nach der PID des Servers gesehen.
	pidPoll = 200 * time.Millisecond
	// maxWait: Länger wartet die Schleife nie am Stück. Unter Windows zählt
	// die Wartezeit eines Timers den Ruhezustand nicht mit; so wird die Uhr
	// nach dem Aufwachen spätestens nach einer Sekunde neu gelesen.
	maxWait = time.Second
	// tunnelSettle: So lange muss tunnel laufen, bevor der Server als
	// erreichbar gilt (ready im Lebenszeichen). tunnel meldet seine
	// Verbindung nicht; in dieser Zeit steht sie üblicherweise.
	tunnelSettle = 3 * time.Second
	// finalRetryFor: So lange wird ein gescheiterter Schluss-Stand in der
	// Sitzung wiederholt (Zuschlag und Lebenszeichen bleiben), bevor er
	// vorgemerkt und der Zuschlag freigegeben wird; finalRetryPause ist die
	// Pause zwischen den Versuchen.
	finalRetryFor   = 60 * time.Second
	finalRetryPause = 10 * time.Second
	// restoreDelay: So lange nach der Ansage endet die Sitzung beim
	// Zurückholen, wenn die Notice keine (sinnvolle) Frist nennt;
	// restoreDelayMax ist die längste angenommene. restoreWarn: so lange vor
	// dem Ende kommt die zweite Ansage.
	restoreDelay    = 30 * time.Second
	restoreDelayMax = 5 * time.Minute
	restoreWarn     = 10 * time.Second
	// maintMinDelay: Auch bei Frist 0 vergeht so viel Zeit zwischen Ansage
	// und Stopp, damit die Ansage noch ankommt (weitere Ansagen: maintWarnAt).
	maintMinDelay = 3 * time.Second
	// sayTimeout begrenzt eine Ansage im Chat.
	sayTimeout = 15 * time.Second
	// membersTimeout begrenzt einen Abgleich von Whitelist und Ops
	// (Host-Einstellungen holen und alle Befehle).
	membersTimeout = 2 * time.Minute
)

// kickReason steht beim Hinauswurf eines Spielers, der kein Mitglied mehr
// ist.
const kickReason = "Du bist nicht mehr Mitglied der Gruppe."

// syncSlot: Auf einem PC laufen Download und ein Stand (Bereitstellen,
// Manifest, Hochladen, Einreichen) nie gleichzeitig. Beide teilen sich
// Rezeptablage und Hash-Cache: Download räumt lokale Rezepte auf, die ein
// laufender Upload noch lesen will, und schreibt die Welt, die Stage gerade
// kopiert. Eine Sitzung hält das schon durch ihren Ablauf ein (Download nur
// beim Start, Zwischenstände erst danach, der Schluss-Stand erst nach dem
// letzten Zwischenstand); der Platz sichert es zusätzlich gegen Recover
// oder eine zweite Sitzung im selben Prozess.
var syncSlot = make(chan struct{}, 1)

// syncContention: Haken, den holdSync ruft, wenn der Platz belegt ist und es
// warten muss (siehe SetSyncContention).
var syncContention atomic.Pointer[func()]

// SetSyncContention setzt den Haken, den jeder Aufruf von HoldSync (und jeder
// Download, Stand und Recover) ruft, wenn der Platz belegt ist und er warten
// muss; nil entfernt ihn. Er muss schnell zurückkehren und darf den Platz
// nicht selbst anfordern. So kann ein langer Halter (Notfallkopie) abbrechen.
func SetSyncContention(f func()) {
	if f == nil {
		syncContention.Store(nil)
		return
	}
	syncContention.Store(&f)
}

// holdSync belegt syncSlot, bis unlock aufgerufen wird. Endet ctx vorher,
// kommt context.Cause(ctx).
func holdSync(ctx context.Context) (unlock func(), err error) { return holdSyncOpt(ctx, true) }

// holdSyncOpt: notify false meldet das Warten nicht über syncContention (für
// den Halter selbst, die Notfallkopie).
func holdSyncOpt(ctx context.Context, notify bool) (unlock func(), err error) {
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	select {
	case syncSlot <- struct{}{}:
		return func() { <-syncSlot }, nil
	default:
	}
	// Belegt: Wer den Platz hält, erfährt, dass jemand wartet (die
	// Notfallkopie bricht dann ab, statt zu blockieren).
	if f := syncContention.Load(); f != nil && notify {
		(*f)()
	}
	select {
	case syncSlot <- struct{}{}:
		return func() { <-syncSlot }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// HoldSync belegt denselben Platz für Aufrufer außerhalb der Sitzung, die
// auf diesem PC herunterladen oder einen Stand bilden (Vorladen der Welt
// nach der Installation, Welt übernehmen): Sie laufen dann nie zugleich mit
// Download oder Stand einer Sitzung oder von Recover. Endet ctx vorher,
// kommt context.Cause(ctx). unlock gibt den Platz frei.
func HoldSync(ctx context.Context) (unlock func(), err error) { return holdSync(ctx) }

// HoldSyncQuiet ist HoldSync, ruft aber beim Warten nicht den Haken von
// SetSyncContention: für den Halter, den dieser Haken abbrechen soll.
func HoldSyncQuiet(ctx context.Context) (unlock func(), err error) { return holdSyncOpt(ctx, false) }

// tunnelBackoff sind die Pausen vor einem Neustart von tunnel.
var tunnelBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}

// ErrFenced: Run hat den Server gestoppt, weil der Zuschlag nicht mehr
// gesichert war (60 s ohne Lebenszeichen oder vom Koordinator abgelehnt).
// Der Stand ist, wenn der Stopp sauber war, als PendingUpload vorgemerkt.
var ErrFenced = errors.New("Server gestoppt, weil der Zuschlag nicht mehr gesichert ist")

// ErrRestore: Run hat die Sitzung sauber beendet (Schluss-Stand
// eingereicht, Zuschlag zurück), weil ein Admin einen älteren Stand
// zurückholt. Der Koordinator legt ihn nach der Freigabe als Head an; der
// Aufrufer fragt danach neu. Run liefert dann *RestoreError (errors.Is mit
// ErrRestore trifft).
var ErrRestore = errors.New("ein Admin holt einen älteren Stand zurück")

// RestoreError ist das Ergebnis von Run nach einem Ende wegen Zurückholen;
// Notice ist die Ansage des Koordinators (wer, welcher Stand).
type RestoreError struct {
	Notice api.RestoreNotice
}

func (e *RestoreError) Error() string {
	return fmt.Sprintf("%s holt den Stand von %s zurück", cmp.Or(e.Notice.By, "ein Admin"), RestoreTime(e.Notice))
}

func (e *RestoreError) Unwrap() error { return ErrRestore }

// RestoreTime ist die Zeit des zurückgeholten Stands für Ansage und
// Hinweis: Ortszeit, Stunde und Minute („14:05“).
func RestoreTime(n api.RestoreNotice) string {
	return n.TargetCreated.In(time.Local).Format("15:04")
}

// ErrMaintenance: Run hat die Sitzung sauber beendet (Schluss-Stand
// eingereicht, Zuschlag zurück), weil der Koordinator in Wartung geht. Es
// folgt keine Übergabe und kein neuer Start: Spielen wird bis zum Ende der
// Wartung abgelehnt. Run liefert dann *MaintenanceError.
var ErrMaintenance = errors.New("Wartung am Server")

// MaintenanceError ist das Ergebnis von Run nach einem Ende wegen Wartung;
// Maintenance ist die Ansage des Koordinators (Grund).
type MaintenanceError struct {
	Maintenance api.Maintenance
}

func (e *MaintenanceError) Error() string {
	return "Wartung am Server: " + cmp.Or(e.Maintenance.Reason, "ohne Angabe")
}

func (e *MaintenanceError) Unwrap() error { return ErrMaintenance }

// maintWarnAt sind die Zeiten vor dem Stopp, zu denen weitere Ansagen
// kommen (absteigend; nur wenn die Frist länger ist).
var maintWarnAt = []time.Duration{time.Minute, 10 * time.Second}

// Clock ist die Uhr der Sitzung (im Test eine Fake-Uhr). After liefert
// einen Kanal, der nach d die Zeit liefert.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock ist die echte Uhr.
type SystemClock struct{}

// Now liefert die aktuelle Zeit.
func (SystemClock) Now() time.Time { return time.Now() }

// After feuert nach d.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// DefaultDir ist der Server-Ordner: Dir()/server (Welt in server/world).
func DefaultDir() string { return filepath.Join(group.Dir(), "server") }

// Deps ist alles, was eine Sitzung braucht. Run wird direkt nach der
// Antwort "host" auf Play aufgerufen.
type Deps struct {
	Client *client.Client
	Server mcserver.Server
	Paths  *deps.Paths
	// LeaseID und MachineID: der Zuschlag aus Play.
	LeaseID   int64
	MachineID string
	// Base und BaseWorldID: Head aus der Antwort auf Play (HeadGeneration,
	// WorldID). Weicht der Head beim Laden davon ab, bricht Run ab. 0 bzw.
	// "" heißt: nicht prüfen.
	Base        int64
	BaseWorldID string
	// Clock: nil heißt SystemClock.
	Clock Clock
	// TunnelCmd baut den tunnel-Prozess; er muss enden, wenn ctx endet
	// (exec.CommandContext). nil: Paths.Tunnel mit
	// "tcp 25565 --port 15565 --config Dir()/tunnel.yaml", aber nur, wenn
	// Group.TunnelServer gesetzt ist (sonst ohne tunnel, Dev-Modus).
	TunnelCmd func(ctx context.Context) *exec.Cmd

	// Dir ist der Server-Ordner (Welt in Dir/world); leer: DefaultDir().
	Dir string
	// Group: tunnel_server und Token für Dir()/tunnel.yaml.
	Group *group.Group
	// State wird fortgeschrieben (LastUploaded…, PendingUpload) und
	// gespeichert; Run gehört er bis zum Ende.
	State *group.State
	// Profile: Leistungsprofil für Prepare.
	Profile api.Profile
	// MinecraftVersion: die Manifest-Version, die gehostet wird (für Commits).
	MinecraftVersion string
	// HashCache: nil heißt worldsync.LoadHashCache().
	HashCache *worldsync.HashCache
	// DownloadProgress (darf nil sein) bekommt den Fortschritt beim Laden
	// der Welt.
	DownloadProgress func(done, total int64)
	// OnReady (darf nil sein) läuft einmal, sobald der Server läuft und über
	// tunnel erreichbar ist (wie ready im Lebenszeichen). Läuft in der
	// Schleife der Sitzung und darf nicht blockieren.
	OnReady func()
	// Discarded (darf nil sein) bekommt jeden lokalen Stand, den die Sitzung
	// verwirft (nach Dir()/verworfen-<Zeit> verschoben statt hochgeladen).
	// Läuft in der Sitzung und darf nicht blockieren.
	Discarded func(*DiscardedError)
	// Dauerbetrieb (darf nil sein, heißt aus) meldet, ob dieser PC gerade im
	// Dauerbetrieb hostet (im Profil an und am Netz). Die Sitzung fragt bei
	// jeder Entscheidung neu: Solange es an ist, endet sie nicht mangels
	// Spielern, und ein Zwischenstand kommt nur, wenn seit dem letzten Stand
	// jemand online war. Geht es aus, gilt wieder die Leerlauf-Frist, gezählt
	// ab dem letzten Spieler oder ab dem Moment, in dem die Sitzung das
	// Abschalten bemerkt (nächste Spielerabfrage), je nachdem, was später
	// war. Läuft in der Schleife der Sitzung und darf nicht blockieren.
	Dauerbetrieb func() bool
	// OnEmptyTimeout (darf nil sein) bekommt die Leerlauf-Frist der Sitzung,
	// sobald sie feststeht (für Hinweise). Läuft in der Schleife der Sitzung
	// und darf nicht blockieren.
	OnEmptyTimeout func(time.Duration)
	// OwnName: Minecraft-Name des Hosts (leer: unbekannt). Gleicht die
	// Sitzung Whitelist und Ops an, wird er nie entfernt oder hinausgeworfen.
	OwnName string

	// Handover (darf nil sein: nie): Kommt hier ein Wert an (oder wird der
	// Kanal geschlossen), versucht die Sitzung, die laufende Welt an einen
	// anderen PC zu übergeben statt sie zu beenden (handover.go). Gelingt es,
	// endet Run mit nil, ohne Schluss-Stand (der Übergabe-Stand ist schon
	// eingereicht); gibt es keinen Kandidaten oder scheitert es, endet die
	// Sitzung wie bei Ende von ctx. Ein Wert genügt; weitere werden
	// ignoriert.
	Handover <-chan struct{}
	// OnHandover (darf nil sein) meldet den Fortgang einer Übergabe, für
	// Hinweise: state ist "vorbereitung", "start" (neuer Host startet),
	// "bereit", "gescheitert" oder "keiner da"; to der Minecraft-Name des
	// neuen Hosts (leer, wo unbekannt). Läuft in der Schleife der Sitzung und
	// darf nicht blockieren.
	OnHandover func(state, to string)
	// TunnelPort (0: Haupt-Port, wie bisher): Port bei tunneld, auf dem der
	// erste tunnel-Prozess den Server freigibt. Ein neuer Host, der eine
	// Übergabe annimmt, setzt hier den Ersatz-Port aus dem Angebot. Meldet
	// der Koordinator später im Lebenszeichen MainPort, startet die Sitzung
	// zusätzlich einen tunnel-Prozess auf dem Haupt-Port. Bis dahin ist der
	// Zuschlag der wartende: Ein abgelehnter Zwischenstand (409) wird still
	// ausgelassen statt das Fencing auszulösen. Mit einem eigenen TunnelCmd
	// bestimmt dieser den Port selbst (er wird für jeden tunnel aufgerufen).
	TunnelPort int

	// OnCommitted (darf nil sein) bekommt am Ende von Run, nach dem Zurückgeben
	// des Zuschlags, den zuletzt eingereichten Stand, sofern der
	// Bereitstellungsordner noch genau ihn enthält (Committed). Der Haken
	// läuft in der Goroutine von Run und muss schnell zurückkehren: Soll er
	// den Ordner länger lesen (Notfallkopie), belegt er vor der Rückkehr
	// HoldSync und gibt den Platz erst nach dem Lesen frei; so bildet keine
	// folgende Sitzung den Ordner neu, solange er gelesen wird.
	OnCommitted func(Committed)

	// idle (nur Tests) wird aufgerufen, bevor die Schleife wartet: next ist
	// der Zeitpunkt, zu dem sie spätestens wieder aufwacht, inflight die Zahl
	// der Schritte, deren Ergebnis noch aussteht. Bei inflight 0 wartet sie
	// nur noch auf die Uhr.
	idle func(next time.Time, inflight int)
	// stage (nur Tests) ersetzt worldsync.StageCachedContext.
	stage func(ctx context.Context, worldDir, stagingDir string, hc *worldsync.HashCache) error
	// onMarker (nur Tests) läuft beim Start direkt nach dem Schreiben der
	// Markierung, vor der letzten Prüfung von ctx und vor Start.
	onMarker func(ctx context.Context)
	// onJob (nur Tests) bekommt das Job-Objekt der tunnel-Prozesse.
	onJob func(*procJob)
}

// Committed beschreibt den zuletzt erfolgreich eingereichten Stand der
// Sitzung (Deps.OnCommitted).
type Committed struct {
	// Dir ist der Bereitstellungsordner: eine Kopie genau dieses Stands (ein
	// normaler Weltordner). Nur gültig, solange niemand ihn neu bildet (siehe
	// Deps.OnCommitted).
	Dir              string
	WorldID          string
	Generation       int64
	MinecraftVersion string
	// Handover: Es war der Übergabe-Stand (nicht ein Zwischen- oder
	// Schluss-Stand).
	Handover bool
	// At: Zeitpunkt, zu dem der Stand eingereicht wurde.
	At time.Time
	// NotUploaded: Der Stand ist bereitgestellt, aber nicht eingereicht (der
	// Upload scheiterte; er wird später nachgereicht). Er ist dann der
	// neueste Stand und nur auf diesem PC vorhanden; WorldID und Generation
	// nennen die Basis, auf der er aufbaut.
	NotUploaded bool
}

type phase int

const (
	phStarting phase = iota // Welt laden, Server starten
	phHosting               // Server läuft
	phStopping              // Stop läuft
	phFinal                 // Schluss-Stand wird hochgeladen
	phDone                  // nichts mehr zu tun
)

// Session ist der Zustand einer laufenden Host-Sitzung. Alle Felder gehören
// der Schleife in Run; Goroutinen melden ihre Ergebnisse über events.
type Session struct {
	d   Deps
	clk Clock
	st  *group.State
	hc  *worldsync.HashCache

	dir, world, staging, marker string
	tunnelOn                    bool
	// stage stellt bereit: immer mit Abbruch und mit hc, demselben Cache,
	// den Download beim Start bekommen hat. Download hinterlegt darin Hash
	// und Rezept jeder Datei, stage gibt sie an die Bereitstellung weiter;
	// sonst zerlegte der erste Zwischenstand nach einer Übergabe die ganze
	// Welt neu.
	stage func(ctx context.Context, worldDir, stagingDir string, hc *worldsync.HashCache) error
	// job hält die tunnel-Prozesse: Endet der Starter (auch durch Absturz),
	// schließt Windows den Handle und beendet tunnel mit. Java gehört nicht
	// hinein – hart beendet wäre die Welt beschädigt.
	job *procJob

	// mark ist der Inhalt der Markierungsdatei; während des Starts gehört er
	// dem Start-Schritt, danach der Schleife.
	mark       marker
	markerKept bool // Markierung trotz sauberem Stop behalten (Zustand nicht gespeichert)

	ctx         context.Context         // Run-Kontext: endet → sauberes Ende
	work        context.Context         // Netz und Server-Befehle; endet beim Fencing
	stopWork    context.CancelCauseFunc // beendet work (Fencing, Absturz, Ende)
	startCancel context.CancelFunc      // bricht den Start ab

	events   chan func()
	quit     chan struct{}
	wg       sync.WaitGroup
	inflight int // laufende Schritte, deren Ergebnis noch aussteht

	phase   phase
	ending  bool  // sauberes Ende verlangt
	fenced  error // Grund des Fencings
	result  error // Ergebnis von Run (ohne Fencing)
	release bool  // am Ende Zuschlag zurückgeben

	// committed: der zuletzt eingereichte Stand, solange staging noch genau
	// ihn enthält (restage setzt es zurück); Goroutinen der Stände und die
	// Schleife greifen zu, daher emMu.
	emMu      sync.Mutex
	committed *Committed

	baseWorld     string
	baseGen       int64
	baseUncertain bool // ein Commit ist vielleicht angekommen, ohne dass wir es wissen

	exited <-chan error // Ende des Servers, solange er unerwartet enden könnte

	lastOK  time.Time // Sendezeit des letzten erfolgreichen Lebenszeichens
	nextHB  time.Time
	hbBusy  bool
	hbFails int
	// hbReady: ready im zuletzt gesendeten Lebenszeichen; readyNotified:
	// OnReady ist gelaufen.
	hbReady       bool
	readyNotified bool

	players     []string
	nextPlayers time.Time
	plBusy      bool
	emptySince  time.Time // seit wann niemand online ist (Null: jemand oder unbekannt)
	// emptyAfter: Leerlauf-Frist (aus den Host-Einstellungen).
	emptyAfter time.Duration
	// played: Seit dem Beginn des letzten Zwischenstands (oder dem Start)
	// war jemand online; im Dauerbetrieb gibt es nur dann einen neuen.
	played bool
	// dauer: zuletzt gesehener Wert von Deps.Dauerbetrieb (fürs Log beim
	// Wechsel).
	dauer bool
	// seen: wer seit dem Beginn des letzten Stands online war (Players im
	// Commit).
	seen map[string]bool
	// coordNew: Das letzte Lebenszeichen kam mit JSON-Antwort – der
	// Koordinator kennt Players im Commit (ein älterer lehnt das Feld ab).
	// playersRefused: Er hat es trotzdem abgelehnt (400); in dieser Sitzung
	// gehen Stände ohne Spielerliste.
	coordNew       bool
	playersRefused bool

	// Zurückholen: restore ist die erste Notice (nil: keine; jede weitere
	// wird übergangen), restoreWarnAt die zweite Ansage, restoreAt das
	// saubere Ende (Null: keins geplant); restoreEnd: Das Ende kam vom
	// Zurückholen (Run liefert dann *RestoreError).
	restore       *api.RestoreNotice
	restoreWarnAt time.Time
	restoreAt     time.Time
	restoreEnd    bool

	// Wartung (onMaintenanceNotice): maint ist die zuletzt angenommene
	// Notice (nil: keine), maintAt der Stopp (Null: keiner geplant),
	// maintWarns die noch ausstehenden Ansagen (aufsteigend), maintEnd: Das
	// Ende kam von der Wartung (Run liefert dann *MaintenanceError).
	maint      *api.Maintenance
	maintAt    time.Time
	maintWarns []time.Time
	maintEnd   bool

	// members: Whitelist und Ops, wie der laufende Server sie hat (aus den
	// Host-Einstellungen des Starts und jedem Abgleich); membersSeen: die
	// MembersVersion, auf die abgeglichen ist (0: noch keine);
	// membersBusy: ein Abgleich läuft.
	members     memberLists
	membersSeen int64
	membersBusy bool

	nextSnap     time.Time
	snapBusy     bool // Zwischenstand wird bereitgestellt oder hochgeladen
	snapHolding  bool // Zwischenstand steht zwischen SaveBarrier und release
	snapCancel   context.CancelCauseFunc
	stopWaiting  bool               // Stop wartet auf release des Zwischenstands
	stopCancel   context.CancelFunc // bricht den laufenden Stop ab (der Server wird dann hart beendet)
	stopBy       time.Time          // Frist für den sauberen Stop nach dem Fencing (Null: keine)
	stopLate     bool               // diese Frist ist abgelaufen
	finalStarted bool
	finalFirst   time.Time // erster Versuch des Schluss-Stands
	finalRetryAt time.Time // nächster Versuch nach einem Fehlschlag (Null: keiner geplant)
	// snapEvery: Abstand der Zwischenstände (aus den Host-Einstellungen).
	snapEvery time.Duration
	// stopBegan, finalStop: Beginn und Dauer von Stop (Wanduhr), Messwerte
	// des Schluss-Stands.
	stopBegan time.Time
	finalStop time.Duration

	// tunnels: tunnels[0] ist der erste tunnel-Prozess (Haupt-Port oder
	// Ersatz-Port, Deps.TunnelPort); tunnels[1] kommt hinzu, wenn der
	// Koordinator MainPort meldet.
	tunnels []*tunnelSlot
	// mainPort: Dieser Host darf den Haupt-Port haben (immer, außer als
	// wartender Zuschlag einer Übergabe vor der Beförderung).
	mainPort bool

	// ho: laufende Übergabe an einen anderen PC (handover.go); handoverCh:
	// Deps.Handover, solange noch nichts empfangen wurde. hoPending: Die
	// Übergabe ist verlangt, beginnt aber erst, wenn kein Zwischenstand mehr
	// läuft. handedOver: Die Übergabe ist abgeschlossen, der Server stoppt
	// ohne weiteren Stand.
	ho         *handover
	modeDebt   map[string]string // Spielmodi, die nach dem Rückbau noch zu setzen sind (Spieler war offline)
	handoverCh <-chan struct{}
	hoPending  bool
	handedOver bool
}

// tunnelSlot ist ein tunnel-Prozess samt Port und Neustart-Überwachung.
type tunnelSlot struct {
	port  int
	index int
	proc  *tunnelProc
	at    time.Time // geplanter Neustart (Null: keiner)
	fails int
}

// tunnel liefert den ersten tunnel-Prozess (nil: läuft keiner).
func (s *Session) tunnel() *tunnelProc { return s.tunnels[0].proc }

type tunnelProc struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{} // geschlossen nach cmd.Wait
	err     error         // gültig nach done
	started time.Time
}

// Run hostet die Welt, bis niemand mehr spielt (Leerlauf-Frist laut
// Koordinator, Standard 10 Minuten; im Dauerbetrieb nie), ctx endet
// (Herunterfahren, „Spiel für mich beenden“) oder der Zuschlag nicht mehr
// gesichert ist:
//
//	Head laden → HostSettings → Prepare → Markierung → Start → tunnel
//	→ Lebenszeichen 10 s, Spieler 15 s, Zwischenstand laut Koordinator (5 min)
//
// Sauberes Ende (Ergebnis nil): tunnel beenden, Stop, Markierung löschen,
// Schluss-Stand bereitstellen, hochladen und einreichen, Zuschlag
// zurückgeben. Scheitert der Upload, bleibt der Stand als PendingUpload
// vorgemerkt.
//
// Fencing (Ergebnis wrappt ErrFenced): sofort tunnel beenden und Server
// stoppen, nichts mehr hochladen; war der Stopp sauber (innerhalb von
// fenceStopGrace ab dem Fencing, sonst wird hart beendet), Markierung
// löschen und PendingUpload mit der letzten eigenen Basis vormerken.
//
// Scheitert das Laden der Welt oder der Start, wird nichts gestartet bzw.
// der Server wieder gestoppt und der Zuschlag zurückgegeben.
func Run(ctx context.Context, d Deps) error {
	if err := d.check(); err != nil {
		if d.Client != nil && d.LeaseID != 0 {
			releaseLease(ctx, d.Client, d.LeaseID, d.MachineID)
		}
		return err
	}
	s := newSession(ctx, d)
	if err := s.prepare(); err != nil {
		releaseLease(ctx, d.Client, d.LeaseID, d.MachineID)
		return fmt.Errorf("Hosten abgebrochen: %w", err)
	}
	err := s.loop()
	if s.release {
		releaseLease(ctx, d.Client, d.LeaseID, d.MachineID)
	}
	if d.OnCommitted != nil {
		if c := s.lastCommitted(); c != nil {
			d.OnCommitted(*c)
		}
	}
	return err
}

// restage bildet den Bereitstellungsordner neu. Er enthält danach (auch bei
// Fehler) nicht mehr den zuletzt eingereichten Stand.
func (s *Session) restage(ctx context.Context) error {
	s.emMu.Lock()
	s.committed = nil
	s.emMu.Unlock()
	return s.stage(ctx, s.world, s.staging, s.hc)
}

// noteStaged merkt den bereitgestellten, noch nicht eingereichten Stand
// (Basis b); gelingt das Einreichen, ersetzt noteCommitted ihn.
func (s *Session) noteStaged(b baseRef) {
	s.emMu.Lock()
	s.committed = &Committed{
		Dir: s.staging, WorldID: b.world, Generation: b.gen,
		MinecraftVersion: s.d.MinecraftVersion, At: s.clk.Now(), NotUploaded: true,
	}
	s.emMu.Unlock()
}

// noteCommitted merkt den eben eingereichten Stand; der Bereitstellungsordner
// gehört bis zum nächsten restage diesem Stand.
func (s *Session) noteCommitted(g *api.Generation, handover bool) {
	s.emMu.Lock()
	s.committed = &Committed{
		Dir: s.staging, WorldID: g.WorldID, Generation: g.Number,
		MinecraftVersion: s.d.MinecraftVersion, Handover: handover, At: s.clk.Now(),
	}
	s.emMu.Unlock()
}

func (s *Session) lastCommitted() *Committed {
	s.emMu.Lock()
	defer s.emMu.Unlock()
	return s.committed
}

func (d *Deps) check() error {
	switch {
	case d.Client == nil:
		return errors.New("host: kein Client")
	case d.Server == nil:
		return errors.New("host: kein Server")
	case d.Paths == nil:
		return errors.New("host: keine Pfade (deps.Ensure)")
	case d.State == nil:
		return errors.New("host: kein Zustand")
	case d.LeaseID == 0 || d.MachineID == "":
		return errors.New("host: kein Zuschlag")
	case d.TunnelCmd == nil && d.Group != nil && d.Group.TunnelServer != "" && d.Paths.Tunnel == "":
		return errors.New("host: tunnel.exe fehlt (deps.Ensure)")
	}
	return nil
}

func newSession(ctx context.Context, d Deps) *Session {
	s := &Session{
		d:       d,
		clk:     d.Clock,
		st:      d.State,
		hc:      d.HashCache,
		dir:     d.Dir,
		ctx:     ctx,
		events:  make(chan func()),
		quit:    make(chan struct{}),
		players: []string{},
		seen:    map[string]bool{},

		handoverCh: d.Handover,
		mainPort:   d.TunnelPort == 0 || d.TunnelPort == PublicPort,
	}
	first := PublicPort
	if d.TunnelPort != 0 {
		first = d.TunnelPort
	}
	s.tunnels = []*tunnelSlot{{port: first}}
	s.snapEvery = SnapshotInterval
	s.emptyAfter = DefaultEmptyTimeout
	if s.clk == nil {
		s.clk = SystemClock{}
	}
	if s.hc == nil {
		s.hc = worldsync.LoadHashCache()
	}
	s.stage = d.stage
	if s.stage == nil {
		s.stage = worldsync.StageCachedContext
	}
	if s.dir == "" {
		s.dir = DefaultDir()
	}
	if abs, err := filepath.Abs(s.dir); err == nil {
		s.dir = abs
	}
	s.world = filepath.Join(s.dir, WorldName)
	s.staging = filepath.Join(s.dir, StagingName)
	s.marker = filepath.Join(s.dir, MarkerName)
	s.tunnelOn = d.TunnelCmd != nil || (d.Group != nil && d.Group.TunnelServer != "")
	// Die Arbeit überlebt das Ende von ctx (das saubere Ende braucht sie)
	// und endet nur beim Fencing.
	s.work, s.stopWork = context.WithCancelCause(context.WithoutCancel(ctx))
	return s
}

// prepare läuft vor der Schleife: Server-Ordner, Reste eines Absturzes,
// tunnel-Konfiguration.
func (s *Session) prepare() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	// Eine Markierung hier heißt: Recover lief nicht, oder ein früherer
	// Start in diesem Prozess ist gescheitert. Wie Recover: einen
	// verwaisten Server beenden und den Stand verwerfen (Download baut die
	// Welt gleich neu auf).
	if present, err := markerPresent(s.marker); err != nil {
		return err
	} else if present {
		de, err := discardDirty(s.dir, s.st, "der Server wurde nicht sauber gestoppt")
		if err != nil {
			return err
		}
		s.discarded(de)
	}
	if s.tunnelOn && s.d.Group != nil && s.d.Group.TunnelServer != "" {
		if err := writeTunnelConfig(s.d.Group); err != nil {
			return fmt.Errorf("tunnel-Konfiguration: %w", err)
		}
	}
	if !s.tunnelOn {
		log.Printf("host: kein tunnel_server in der Gruppendatei – Server nur lokal erreichbar")
		return nil
	}
	job, err := newProcJob()
	if err != nil {
		// tunnel läuft dann ohne Job; nach einem Absturz beendet ihn Recover
		// über die Markierung.
		log.Printf("host: Job-Objekt für tunnel nicht angelegt: %v", err)
		return nil
	}
	s.job = job
	if s.d.onJob != nil {
		s.d.onJob(job)
	}
	return nil
}

// ---- Schleife ----

func (s *Session) loop() error {
	defer func() {
		close(s.quit)
		s.stopWork(errors.New("Sitzung beendet"))
		s.stopTunnel()
		s.wg.Wait()
		if s.job != nil {
			s.job.close()
		}
	}()

	now := s.clk.Now()
	// Der Zuschlag ist gerade erteilt; das erste Lebenszeichen geht sofort.
	s.lastOK, s.nextHB = now, now
	s.startup()

	ctxDone := s.ctx.Done()
	for {
		now := s.clk.Now()
		s.step(now)
		if s.phase == phDone && s.inflight == 0 {
			break
		}
		wait := maxWait
		if wake := s.nextWake(); !wake.IsZero() && wake.Sub(now) < wait {
			wait = wake.Sub(now)
		}
		timer := s.clk.After(wait)
		if s.d.idle != nil {
			s.d.idle(now.Add(wait), s.inflight)
		}
		select {
		case f := <-s.events:
			f()
		case <-timer:
		case <-ctxDone:
			ctxDone = nil
			s.requestEnd("Hosten beendet")
		case err := <-s.exited:
			s.exited = nil
			s.onServerExit(err)
		case <-s.handoverCh:
			s.handoverCh = nil
			s.requestHandover()
		}
	}
	if s.fenced != nil {
		return fmt.Errorf("%w: %w", ErrFenced, s.fenced)
	}
	// Nur ein sauberes Ende wegen Zurückholen (Schluss-Stand eingereicht);
	// sonst gilt der Fehler.
	if s.result == nil && s.restoreEnd {
		return &RestoreError{Notice: *s.restore}
	}
	if s.result == nil && s.maintEnd {
		return &MaintenanceError{Maintenance: *s.maint}
	}
	return s.result
}

// step erledigt alles, was zur Zeit now fällig ist. Danach liegt jeder
// Termin aus nextWake nach now.
func (s *Session) step(now time.Time) {
	if !s.stopBy.IsZero() && !now.Before(s.stopBy) {
		s.stopBy = time.Time{}
		s.expireStop()
	}
	if !s.heartbeating() {
		return
	}
	// Was dieser Schritt auch entscheidet: Fällt die Bereitschaft weg, geht
	// das gleich hinaus.
	defer s.reportNotReady(now)
	if !now.Before(s.lastOK.Add(FenceAfter)) {
		s.fence(fmt.Errorf("%v ohne erfolgreiches Lebenszeichen", FenceAfter))
		return
	}
	if !s.hbBusy && !now.Before(s.nextHB) {
		s.sendHeartbeat(now)
	}
	if s.ready(now) {
		if !s.readyNotified {
			s.readyNotified = true
			if s.tunnelOn {
				log.Printf("host: Server bereit und über tunnel erreichbar")
			} else {
				log.Printf("host: Server bereit (ohne tunnel, nur lokal erreichbar)")
			}
			if s.d.OnReady != nil {
				s.d.OnReady()
			}
		}
		// Bereitschaft sofort melden, nicht erst mit dem nächsten
		// Lebenszeichen im Takt (dessen Zeitplan bleibt).
		if !s.hbBusy && !s.hbReady {
			s.heartbeat(now)
		}
	}
	if s.phase == phFinal && !s.finalRetryAt.IsZero() && !now.Before(s.finalRetryAt) {
		s.finalRetryAt = time.Time{}
		s.maybeStartFinal()
	}
	if !s.active() {
		return
	}
	if !s.restoreAt.IsZero() && !now.Before(s.restoreAt) {
		s.restoreAt, s.restoreWarnAt = time.Time{}, time.Time{}
		s.restoreEnd = true
		s.requestEnd(fmt.Sprintf("%s holt einen älteren Stand zurück", cmp.Or(s.restore.By, "ein Admin")))
		return
	}
	if !s.maintAt.IsZero() && !now.Before(s.maintAt) {
		s.maintAt, s.maintWarns = time.Time{}, nil
		s.maintEnd = true
		s.requestEnd("Wartung am Server: " + s.maint.Reason)
		return
	}
	for len(s.maintWarns) > 0 && !now.Before(s.maintWarns[0]) {
		left := max(s.maintAt.Sub(now).Round(time.Second), time.Second)
		s.maintWarns = s.maintWarns[1:]
		s.say("Wartung: Server stoppt in " + maintDuration(left))
	}
	if !s.restoreWarnAt.IsZero() && !now.Before(s.restoreWarnAt) {
		s.restoreWarnAt = time.Time{}
		left := max(s.restoreAt.Sub(now).Round(time.Second), time.Second)
		s.say(fmt.Sprintf("Neustart in %d Sekunden", left/time.Second))
	}
	s.hoStep(now)
	if !s.plBusy && !now.Before(s.nextPlayers) {
		s.queryPlayers(now)
	}
	if !now.Before(s.nextSnap) {
		for !s.nextSnap.After(now) {
			s.nextSnap = s.nextSnap.Add(s.snapEvery)
		}
		switch {
		case s.ho != nil || s.hoPending:
			// Der Stand der Übergabe ersetzt den Zwischenstand (und der
			// Server steht ohnehin eingefroren).
			log.Printf("host: Übergabe läuft, Zwischenstand übersprungen")
		case s.snapBusy:
			log.Printf("host: voriger Zwischenstand läuft noch, dieser wird übersprungen")
		case !s.played && s.dauerbetrieb():
			// Leere Stände verdrängten sonst ältere aus der Aufbewahrung.
			log.Printf("host: Dauerbetrieb – seit dem letzten Stand war niemand online, Zwischenstand übersprungen")
		default:
			s.startSnapshot()
		}
	}
	for _, t := range s.tunnels {
		if !t.at.IsZero() && !now.Before(t.at) {
			t.at = time.Time{}
			s.startSlot(t, now)
		}
	}
}

// reportNotReady: Das zuletzt gesendete Lebenszeichen meldete „bereit“, und
// die Sitzung endet (Stop beginnt) – dann sofort ein Lebenszeichen mit
// ready=false, nicht erst im Takt (dessen Zeitplan bleibt). Der Koordinator
// entzieht einem Host, der nach einer Zurückhol-Anfrage zu lange „bereit“
// meldet, den Zuschlag; ein Host, der kurz vor dieser Frist zu stoppen
// beginnt, verlöre sonst seinen Schluss-Stand. Läuft gerade ein
// Lebenszeichen, kommt der nächste Schritt mit dessen Ergebnis. Ein
// abgerissener tunnel bei laufender Sitzung meldet sich weiter im Takt.
func (s *Session) reportNotReady(now time.Time) {
	if s.heartbeating() && !s.hbBusy && s.hbReady && !s.active() {
		s.heartbeat(now)
	}
}

// nextWake ist der früheste offene Termin (Null: keiner).
func (s *Session) nextWake() time.Time {
	var w time.Time
	add := func(t time.Time) {
		if !t.IsZero() && (w.IsZero() || t.Before(w)) {
			w = t
		}
	}
	add(s.stopBy)
	if s.heartbeating() {
		add(s.lastOK.Add(FenceAfter))
		if !s.hbBusy {
			add(s.nextHB)
		}
	}
	if s.active() {
		if !s.plBusy {
			add(s.nextPlayers)
		}
		add(s.nextSnap)
		for _, t := range s.tunnels {
			add(t.at)
		}
		add(s.restoreWarnAt)
		add(s.restoreAt)
		add(s.maintAt)
		if len(s.maintWarns) > 0 {
			add(s.maintWarns[0])
		}
		s.hoWake(add)
		// Bereitschaft melden, sobald tunnel steht; läuft gerade ein
		// Lebenszeichen, weckt dessen Ergebnis die Schleife (sonst stünde
		// hier ein vergangener Termin und die Schleife liefe leer).
		if t := s.tunnel(); t != nil && !s.hbReady && !s.hbBusy {
			add(t.started.Add(tunnelSettle))
		}
	}
	if s.phase == phFinal {
		add(s.finalRetryAt)
	}
	return w
}

// ready: Der Server läuft und soll weiterlaufen, und er ist erreichbar –
// über tunnel, sobald der tunnelSettle lang läuft (ohne tunnel sofort).
func (s *Session) ready(now time.Time) bool {
	if !s.active() {
		return false
	}
	if !s.tunnelOn {
		return true
	}
	t := s.tunnel()
	return t != nil && !now.Before(t.started.Add(tunnelSettle))
}

// heartbeating: Der Zuschlag wird noch gebraucht (auch beim sauberen Ende,
// bis der Schluss-Stand eingereicht ist).
func (s *Session) heartbeating() bool { return s.fenced == nil && s.phase != phDone }

// active: Der Server läuft und soll weiterlaufen.
func (s *Session) active() bool { return s.phase == phHosting && !s.ending && s.fenced == nil }

// launch führt op in einer eigenen Goroutine aus. Das Ergebnis von op (eine
// Funktion) läuft danach in der Schleife.
func (s *Session) launch(op func() func()) {
	s.inflight++
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		apply := op()
		s.events <- func() {
			s.inflight--
			apply()
		}
	}()
}

// post schickt f an die Schleife; nach ihrem Ende wird f verworfen.
func (s *Session) post(f func()) {
	select {
	case s.events <- f:
	case <-s.quit:
	}
}

// ---- Start ----

type startResult struct {
	head             *api.Generation
	discardedPending string // Pfad, wohin ein veralteter PendingUpload-Stand verschoben wurde
	discardReason    string
	err              error
	snapEvery        time.Duration // Abstand der Zwischenstände aus den Host-Einstellungen
	emptyAfter       time.Duration // Leerlauf-Frist aus den Host-Einstellungen
	members          memberLists   // Whitelist und Ops, mit denen der Server startet
}

// discarded meldet einen verworfenen lokalen Stand an Deps.Discarded.
func (s *Session) discarded(de *DiscardedError) {
	if de != nil && s.d.Discarded != nil {
		s.d.Discarded(de)
	}
}

func (s *Session) startup() {
	ctx, cancel := context.WithCancel(s.work)
	s.startCancel = cancel
	s.launch(func() func() {
		r := s.startupOp(ctx)
		return func() {
			cancel()
			s.onStarted(r)
		}
	})
}

// startupOp: Head laden, Welt aufbauen, Server vorbereiten und starten.
// Läuft in einer eigenen Goroutine; der Zustand (st) wird hier nur gelesen.
func (s *Session) startupOp(ctx context.Context) (r startResult) {
	head, err := s.d.Client.Head(ctx)
	if err != nil {
		r.err = fmt.Errorf("Stand der Welt abfragen: %w", err)
		return r
	}
	if head == nil {
		r.err = errors.New("auf dem Koordinator gibt es noch keine Welt (erst „Welt übernehmen …“)")
		return r
	}
	if (s.d.Base != 0 && head.Number != s.d.Base) || (s.d.BaseWorldID != "" && head.WorldID != s.d.BaseWorldID) {
		r.err = fmt.Errorf("Stand hat sich seit dem Zuschlag geändert (%s/%d statt %s/%d)",
			head.WorldID, head.Number, s.d.BaseWorldID, s.d.Base)
		return r
	}
	if err := checkWorldVersion(head.MinecraftVersion, s.d.MinecraftVersion); err != nil {
		r.err = err
		return r
	}

	// Ein sauber gestoppter, noch nicht eingereichter Stand: Baut er auf
	// dem Head auf, wird auf ihm weitergespielt (der erste Commit reicht ihn
	// mit ein). Sonst ist er überholt und wird beiseitegelegt.
	// Ein Weltordner mit abgebrochenem Download ist nie ein solcher Stand.
	keepLocal := false
	if s.st.PendingUpload {
		onBase := head.WorldID == s.st.LastUploadedWorldID && head.Number == s.st.LastUploadedGeneration
		incomplete := worldsync.DownloadIncomplete(s.world)
		if (onBase || ownCommitOn(head, s.d.MachineID, s.st)) && worldPresent(s.world) && !incomplete {
			keepLocal = true
			log.Printf("host: nachzureichender Stand baut auf %s/%d auf – es wird auf ihm weitergespielt", head.WorldID, head.Number)
		} else {
			dest, err := discardWorld(s.world)
			if err != nil {
				r.err = err
				return r
			}
			r.discardedPending = dest
			if incomplete {
				r.discardReason = "ein Download in den Weltordner wurde nicht abgeschlossen"
				log.Printf("host: im vorgemerkten Stand wurde ein Download nicht abgeschlossen, verschoben nach %s", dest)
			} else {
				r.discardReason = fmt.Sprintf("seitdem wurde weitergespielt (Stand %s/%d)", head.WorldID, head.Number)
				log.Printf("host: nachzureichender Stand ist überholt (Head %s/%d), verschoben nach %s", head.WorldID, head.Number, dest)
			}
		}
	}
	if !keepLocal {
		// Download nie gleichzeitig mit einem Stand (syncSlot). Hier läuft
		// noch kein Zwischenstand dieser Sitzung: Die starten erst, wenn der
		// Server läuft.
		unlock, err := holdSync(ctx)
		if err != nil {
			r.err = fmt.Errorf("Welt laden: %w", err)
			return r
		}
		// Scheitert das, ist der Weltordner gemischt: nicht starten.
		err = worldsync.Download(ctx, s.d.Client, head, s.world, s.hc, s.d.DownloadProgress)
		unlock()
		if err != nil {
			r.err = fmt.Errorf("Welt laden: %w", err)
			return r
		}
	}
	// Nie auf einer Welt mit abgebrochenem Download starten: Sie ist kein
	// gültiger Stand, und jeder Zwischenstand daraus wäre gemischt.
	if worldsync.DownloadIncomplete(s.world) {
		r.err = fmt.Errorf("Welt laden: %w: %s", worldsync.ErrDownloadIncomplete, s.world)
		return r
	}

	hs, err := s.d.Client.HostSettings(ctx)
	if err != nil {
		r.err = fmt.Errorf("Welt-Einstellungen holen: %w", err)
		return r
	}
	r.snapEvery = snapshotEvery(hs)
	r.emptyAfter = emptyTimeout(hs)
	r.members = listsFrom(hs)
	err = s.d.Server.Prepare(ctx, mcserver.PrepareInput{
		Dir:          s.dir,
		WorldDirName: WorldName,
		Paths:        s.d.Paths,
		Settings:     *hs,
		Profile:      s.d.Profile,
	})
	if err != nil {
		r.err = fmt.Errorf("Server vorbereiten: %w", err)
		return r
	}
	// Nach einem Fencing (oder Ende) während des Starts darf Java nicht mehr
	// starten, auch wenn ein Schritt davor den Abbruch nicht bemerkt hat.
	if ctx.Err() != nil {
		r.err = fmt.Errorf("Start abgebrochen: %w", context.Cause(ctx))
		return r
	}
	// Ab hier kann der Server die Welt verändern.
	s.mark = marker{Java: s.d.Paths.Java, Written: time.Now().UTC()}
	if err := writeMarker(s.marker, s.mark); err != nil {
		r.err = fmt.Errorf("Markierungsdatei: %w", err)
		return r
	}
	if s.d.onMarker != nil {
		s.d.onMarker(ctx)
	}
	if ctx.Err() != nil {
		// Java hat die Welt nie berührt: Die Markierung muss weg, sonst
		// verwürfe der nächste Start einen gültigen Stand.
		if err := removeMarker(s.marker); err != nil {
			log.Printf("host: Markierungsdatei nach Start-Abbruch nicht gelöscht: %v", err)
		}
		r.err = fmt.Errorf("Start abgebrochen: %w", context.Cause(ctx))
		return r
	}
	if javaSeen, err := s.startServer(ctx); err != nil {
		if javaSeen {
			// Start hat den Server selbst gestoppt (vielleicht hart): Die
			// Markierung bleibt, der nächste Start verwirft die Welt.
			r.err = fmt.Errorf("Server starten: %w", err)
			return r
		}
		// Java lief nie (oder kürzer als ein Abfragetakt – zu kurz, um die
		// Welt zu laden): Die Welt ist unberührt. Die Markierung muss weg,
		// sonst verwürfe der nächste Start einen gültigen, vielleicht noch
		// nicht hochgeladenen Stand.
		if rerr := removeMarker(s.marker); rerr != nil {
			log.Printf("host: Markierungsdatei nach gescheitertem Start nicht gelöscht: %v", rerr)
		}
		r.err = fmt.Errorf("Server starten: %w", err)
		return r
	}
	r.head = head
	return r
}

// startServer startet den Server und trägt seine PID samt Programm und
// Startzeit in die Markierung ein, sobald sie bekannt ist (schon während
// Start), falls der Server sie preisgibt. javaSeen: Es lief (vielleicht)
// ein Server-Prozess – ohne PID-Angabe des Servers immer true.
func (s *Session) startServer(ctx context.Context) (javaSeen bool, err error) {
	ps, ok := s.d.Server.(interface{ PID() int })
	if !ok {
		return true, s.d.Server.Start(ctx)
	}
	done := make(chan error, 1)
	go func() { done <- s.d.Server.Start(ctx) }()
	tick := time.NewTicker(pidPoll)
	defer tick.Stop()
	recorded := false
	for {
		select {
		case err := <-done:
			pid := ps.PID()
			if pid > 0 {
				javaSeen = true
			}
			if err == nil && !recorded {
				s.recordPID(pid)
			}
			return javaSeen, err
		case <-tick.C:
			if pid := ps.PID(); pid > 0 {
				javaSeen = true
				if !recorded {
					recorded = s.recordPID(pid)
				}
			}
		}
	}
}

// recordPID schreibt die Markierung mit PID, Programmpfad und Startzeit des
// Servers neu.
func (s *Session) recordPID(pid int) bool {
	if pid <= 0 {
		return false
	}
	ref := identify(pid)
	s.mark.PID, s.mark.Exe, s.mark.Created = ref.PID, ref.Exe, ref.Created
	s.mark.Written = time.Now().UTC()
	if err := writeMarker(s.marker, s.mark); err != nil {
		log.Printf("host: PID %d nicht in die Markierung geschrieben: %v", pid, err)
		return false
	}
	return true
}

// identify liefert PID, Programmpfad und Startzeit eines Prozesses (soweit
// lesbar).
func identify(pid int) procRef {
	ref := procRef{PID: pid}
	if p, err := openProcess(pid); err == nil {
		if exe, err := p.exe(); err == nil {
			ref.Exe = exe
		}
		if c, err := p.created(); err == nil {
			ref.Created = c
		}
		p.close()
	}
	return ref
}

// ownCommitOn meldet, ob head ein Stand dieses PCs direkt auf der Basis des
// vorgemerkten Stands ist: ein Commit, der beim Fencing oder Stop noch
// unterwegs war und doch angekommen ist. Der lokale Stand ist dann nicht
// überholt, sondern baut auf head auf.
func ownCommitOn(head *api.Generation, machine string, st *group.State) bool {
	return head != nil && machine != "" && head.MachineID == machine &&
		head.WorldID == st.LastUploadedWorldID &&
		head.Parent == st.LastUploadedGeneration && head.Number == st.LastUploadedGeneration+1
}

// checkWorldVersion: Eine Welt, die mit einer neueren Minecraft-Version
// gespeichert wurde als der, die gehostet wird, lädt ein älterer Server nie
// (er würde sie beschädigen). Lassen sich die Versionen nicht vergleichen
// (Snapshot), wird nur bei Gleichheit gehostet. Fehlt eine Angabe, gibt es
// nichts zu prüfen. Die Regel steht in api.CheckWorldVersion; der
// Koordinator prüft beim Zurückholen eines Stands dieselbe.
func checkWorldVersion(world, hosted string) error {
	switch err := api.CheckWorldVersion(world, hosted); {
	case errors.Is(err, api.ErrVersionsIncomparable):
		return fmt.Errorf("die Welt ist mit Minecraft %s gespeichert, gehostet wird %s – die Versionen lassen sich nicht vergleichen, deshalb wird nicht gehostet (Versions-Manifest prüfen)", world, hosted)
	case err != nil:
		return fmt.Errorf("die Welt ist mit Minecraft %s gespeichert, gehostet wird nur %s – ein älterer Server würde sie beschädigen. Der Admin muss das Versions-Manifest auf %s oder neuer stellen", world, hosted, world)
	}
	return nil
}

func (s *Session) onStarted(r startResult) {
	if r.discardedPending != "" {
		s.st.PendingUpload = false
		s.saveState()
		s.discarded(&DiscardedError{Path: r.discardedPending, Reason: r.discardReason})
	}
	if r.err != nil {
		if s.fenced == nil {
			s.result = fmt.Errorf("Hosten abgebrochen: %w", r.err)
		}
		log.Printf("host: Hosten abgebrochen: %v", r.err)
		s.release = true
		s.phase = phDone
		return
	}
	s.baseWorld, s.baseGen = r.head.WorldID, r.head.Number
	// Die Basis gleich vermerken (siehe group.State): Stürzt der Starter ab
	// und läuft der Server weiter, sichert Recover dessen Stand auf ihr –
	// auch vor dem ersten eigenen Commit.
	if s.st.LastUploadedWorldID != s.baseWorld || s.st.LastUploadedGeneration != s.baseGen {
		s.st.LastUploadedWorldID, s.st.LastUploadedGeneration = s.baseWorld, s.baseGen
		s.saveState()
	}
	s.exited = s.d.Server.Exited()
	s.members = r.members
	log.Printf("host: Server läuft auf %s/%d", s.baseWorld, s.baseGen)
	if s.fenced != nil || s.ending {
		s.beginStop()
		return
	}
	if s.restore != nil {
		// Während des Starts angekündigt: Noch spielt niemand (tunnel steht
		// nicht), also gleich wieder sauber beenden – ohne Ansage.
		s.restoreEnd, s.ending = true, true
		log.Printf("host: Ende: %s holt einen älteren Stand zurück – der Server wird gleich nach dem Start wieder gestoppt",
			cmp.Or(s.restore.By, "ein Admin"))
		s.beginStop()
		return
	}
	if s.maint != nil {
		// Wie beim Zurückholen: Noch spielt niemand, also gleich sauber
		// beenden.
		s.maintEnd, s.ending = true, true
		log.Printf("host: Ende: Wartung am Server (%s) – der Server wird gleich nach dem Start wieder gestoppt", s.maint.Reason)
		s.beginStop()
		return
	}
	s.phase = phHosting
	if r.snapEvery > 0 {
		s.snapEvery = r.snapEvery
	}
	if r.emptyAfter > 0 {
		s.emptyAfter = r.emptyAfter
	}
	log.Printf("host: Zwischenstand alle %v, Ende nach %v ohne Spieler", s.snapEvery, s.emptyAfter)
	if s.d.OnEmptyTimeout != nil {
		s.d.OnEmptyTimeout(s.emptyAfter)
	}
	s.dauerbetrieb() // ein eingeschalteter Dauerbetrieb steht gleich im Log
	now := s.clk.Now()
	s.nextPlayers = now
	s.nextSnap = now.Add(s.snapEvery)
	// Einmal auftauen schadet nie: Ein übernommener oder zuletzt während
	// einer Übergabe gespeicherter Stand kann einen eingefrorenen Tick haben.
	s.command("tick unfreeze")
	s.startTunnel(now)
}

// snapshotEvery ist der Abstand der Zwischenstände aus den
// Host-Einstellungen. Fehlt er (älterer Koordinator) oder liegt er außerhalb
// von 60–1800 s, gilt SnapshotInterval.
func snapshotEvery(hs *api.HostSettings) time.Duration {
	n := hs.SnapshotIntervalS
	switch {
	case n == 0:
		return SnapshotInterval
	case n < api.MinSnapshotIntervalS || n > api.MaxSnapshotIntervalS:
		log.Printf("host: Abstand der Zwischenstände vom Koordinator (%d s) liegt außerhalb von %d–%d s, es gelten %v",
			n, api.MinSnapshotIntervalS, api.MaxSnapshotIntervalS, SnapshotInterval)
		return SnapshotInterval
	}
	return time.Duration(n) * time.Second
}

// emptyTimeout ist die Leerlauf-Frist aus den Host-Einstellungen. Fehlt
// sie (älterer Koordinator) oder liegt sie außerhalb von 60–3600 s, gilt
// DefaultEmptyTimeout.
func emptyTimeout(hs *api.HostSettings) time.Duration {
	n := hs.EmptyTimeoutS
	switch {
	case n == 0:
		return DefaultEmptyTimeout
	case n < api.MinEmptyTimeoutS || n > api.MaxEmptyTimeoutS:
		log.Printf("host: Leerlauf-Frist vom Koordinator (%d s) liegt außerhalb von %d–%d s, es gelten %v",
			n, api.MinEmptyTimeoutS, api.MaxEmptyTimeoutS, DefaultEmptyTimeout)
		return DefaultEmptyTimeout
	}
	return time.Duration(n) * time.Second
}

// dauerbetrieb fragt Deps.Dauerbetrieb (nil: aus) und schreibt einen
// Wechsel ins Log. Geht er aus, während niemand online ist, beginnt die
// Leerlauf-Frist jetzt (ein kurz abgezogenes Notebook beendet die Sitzung
// nicht).
func (s *Session) dauerbetrieb() bool {
	on := s.d.Dauerbetrieb != nil && s.d.Dauerbetrieb()
	if on != s.dauer {
		s.dauer = on
		switch {
		case on:
			log.Printf("host: Dauerbetrieb an – die Sitzung endet nicht mangels Spielern, Zwischenstände nur, wenn jemand online war")
		case !s.emptySince.IsZero():
			s.emptySince = s.clk.Now()
			log.Printf("host: Dauerbetrieb aus – niemand online, Ende nach %v ohne Spieler (ab jetzt)", s.emptyAfter)
		default:
			log.Printf("host: Dauerbetrieb aus – Ende nach %v ohne Spieler", s.emptyAfter)
		}
	}
	return on
}

// ---- Lebenszeichen und Spieler ----

// sendHeartbeat schickt das Lebenszeichen im Takt.
func (s *Session) sendHeartbeat(now time.Time) {
	s.nextHB = now.Add(HeartbeatInterval)
	s.heartbeat(now)
}

// heartbeat schickt ein Lebenszeichen mit Spielerliste und Bereitschaft.
func (s *Session) heartbeat(now time.Time) {
	s.hbBusy = true
	players := []string{}
	if s.active() {
		players = slices.Clone(s.players)
	}
	s.hbReady = s.ready(now)
	req := api.HeartbeatRequest{LeaseID: s.d.LeaseID, MachineID: s.d.MachineID, Players: players, Ready: s.hbReady}
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, heartbeatTimeout)
		resp, err := s.d.Client.Heartbeat(ctx, req)
		cancel()
		return func() { s.onHeartbeat(now, resp, err) }
	})
}

// onHeartbeat wertet ein Lebenszeichen aus. resp ist nil bei einem älteren
// Koordinator (204).
func (s *Session) onHeartbeat(sentAt time.Time, resp *api.HeartbeatResponse, err error) {
	s.hbBusy = false
	switch {
	case err == nil:
		if sentAt.After(s.lastOK) {
			s.lastOK = sentAt
		}
		if s.hbFails > 0 {
			log.Printf("host: Lebenszeichen geht wieder durch (nach %d Fehlversuchen)", s.hbFails)
		}
		s.hbFails = 0
		s.coordNew = resp != nil
		if resp != nil && resp.Restore != nil {
			s.onRestoreNotice(*resp.Restore)
		}
		if resp != nil && resp.Maintenance != nil {
			s.onMaintenanceNotice(*resp.Maintenance)
		}
		if resp != nil {
			if resp.MainPort {
				s.openMainPort()
			}
			if resp.Handover != nil {
				s.onHandoverState(*resp.Handover)
			}
			s.syncMembers(resp.MembersVersion)
		}
	case !s.heartbeating():
	case errors.Is(err, client.ErrLeaseLost):
		s.fence(fmt.Errorf("Koordinator lehnt das Lebenszeichen ab: %w", err))
	default:
		s.hbFails++
		log.Printf("host: Lebenszeichen gescheitert (%d. Mal, letztes erfolgreiches vor %v): %v",
			s.hbFails, s.clk.Now().Sub(s.lastOK).Round(time.Second), err)
	}
}

func (s *Session) queryPlayers(now time.Time) {
	s.plBusy = true
	s.nextPlayers = now.Add(s.playersEvery())
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, playersTimeout)
		names, err := s.d.Server.Players(ctx)
		cancel()
		return func() { s.onPlayers(names, err) }
	})
}

func (s *Session) onPlayers(names []string, err error) {
	s.plBusy = false
	if !s.active() {
		return
	}
	if err != nil {
		log.Printf("host: Spielerliste: %v", err)
		return
	}
	if names == nil {
		names = []string{}
	}
	s.players = names
	for _, n := range names {
		s.seen[n] = true
	}
	s.applyModeDebt(names)
	if s.ho != nil {
		// Während einer Übergabe zählt keine Leerlauf-Frist.
		s.hoPlayers(names)
		return
	}
	// Beendet wird nur auf eine frische Abfrage hin, die nach der
	// Leerlauf-Frist immer noch niemanden zeigt – wer gerade wiederkommt,
	// wird nicht hinausgeworfen. Im Dauerbetrieb beendet die Frist nicht;
	// geht er aus, beginnt sie neu (dauerbetrieb), falls sie schon lief.
	now := s.clk.Now()
	dauer := s.dauerbetrieb()
	switch {
	case len(names) > 0:
		s.emptySince = time.Time{}
		s.played = true
	case s.emptySince.IsZero():
		s.emptySince = now
	case !now.Before(s.emptySince.Add(s.emptyAfter)) && !dauer:
		s.requestEnd(fmt.Sprintf("%v ohne Spieler", s.emptyAfter))
	}
}

// ---- Zurückholen ----

// onRestoreNotice: Ein Lebenszeichen meldet ein ausstehendes Zurückholen.
// Der Koordinator schickt die Notice mit jedem Lebenszeichen, bis der
// Zuschlag zurück ist; nur die erste zählt. War der Server schon bereit
// (Spieler können verbunden sein), kommt sofort die Ansage, kurz vor dem
// Ende eine zweite, und nach der Frist der Notice (30 s) endet die Sitzung
// sauber. Ist er noch nicht bereit, endet sie sofort ohne Ansage, während
// des Starts gleich nach dem Start (onStarted) – nie meldet sie danach noch
// „bereit“: Der Koordinator entzieht einem Host, der nach der Anfrage zu
// lange „bereit“ meldet, den Zuschlag. Endet die Sitzung ohnehin schon,
// ändert sich nichts – der Koordinator holt den Stand nach der Freigabe
// zurück.
func (s *Session) onRestoreNotice(n api.RestoreNotice) {
	if s.restore != nil || s.maint != nil {
		return
	}
	s.restore = &n
	log.Printf("host: %s holt den Stand %s/%d von %s zurück", cmp.Or(n.By, "ein Admin"),
		n.Target.WorldID, n.Target.Number, n.TargetCreated.In(time.Local).Format("02.01.2006 15:04"))
	switch {
	case s.active() && !s.readyNotified:
		// Der Server läuft, ist aber noch nicht erreichbar: Niemand kann
		// verbunden sein.
		s.restoreEnd = true
		s.requestEnd(fmt.Sprintf("%s holt einen älteren Stand zurück – der Server war noch nicht bereit", cmp.Or(n.By, "ein Admin")))
	case s.active():
		delay := noticeDelay(n)
		now := s.clk.Now()
		s.restoreAt = now.Add(delay)
		if delay > restoreWarn {
			s.restoreWarnAt = s.restoreAt.Add(-restoreWarn)
		}
		s.say(restoreAnnouncement(n, delay))
		log.Printf("host: Ende in %v", delay)
	case s.phase == phStarting && !s.ending && s.fenced == nil:
		log.Printf("host: Der Server startet noch – er wird gleich nach dem Start wieder gestoppt")
	default:
		log.Printf("host: Die Sitzung endet bereits")
	}
}

// ---- Wartung ----

// onMaintenanceNotice: Ein Lebenszeichen meldet eine Wartung am Server. Der
// Koordinator schickt sie mit jedem Lebenszeichen; zählt eine Notice mit
// neuem StopAt, ersetzt sie die bisherige (der Admin hat die Frist
// geändert). War der Server schon bereit, kommt sofort die Ansage („Wartung:
// Server stoppt in N Sekunden – Grund“), vor dem Stopp bei 60 und 10 s
// weitere, und zu StopAt endet die Sitzung sauber – ohne Übergabe, mit
// Schluss-Stand und Rückgabe des Zuschlags. Eine Frist von 0 gibt trotzdem
// maintMinDelay Zeit für die Ansage. Läuft der Server noch nicht (Start,
// nicht bereit), endet die Sitzung sofort ohne Ansage; endet sie ohnehin
// schon oder läuft ein Zurückholen, ändert sich nichts. Die Frist rechnet
// der Host aus RemainingS (Uhr des Koordinators) von seiner eigenen Uhr aus.
func (s *Session) onMaintenanceNotice(n api.Maintenance) {
	if s.restore != nil || s.maintEnd || (s.maint != nil && s.maint.StopAt.Equal(n.StopAt)) {
		return
	}
	first := s.maint == nil
	s.maint = &n
	log.Printf("host: Wartung am Server (%s): Stopp in %d s", n.Reason, n.RemainingS)
	switch {
	case s.active() && !s.readyNotified:
		s.maintEnd = true
		s.requestEnd("Wartung am Server (" + n.Reason + ") – der Server war noch nicht bereit")
	case s.active():
		delay := max(time.Duration(n.RemainingS)*time.Second, maintMinDelay)
		now := s.clk.Now()
		s.maintAt = now.Add(delay)
		s.maintWarns = nil
		for _, w := range maintWarnAt {
			if delay > w {
				s.maintWarns = append(s.maintWarns, s.maintAt.Add(-w))
			}
		}
		s.say(maintAnnouncement(n, delay, first))
		log.Printf("host: Ende in %v", delay)
	case s.phase == phStarting && !s.ending && s.fenced == nil:
		log.Printf("host: Der Server startet noch – er wird gleich nach dem Start wieder gestoppt")
	default:
		log.Printf("host: Die Sitzung endet bereits")
	}
}

// maintDuration ist die Frist für die Ansage: Sekunden, ab zwei Minuten
// Minuten.
func maintDuration(d time.Duration) string {
	if d >= 2*time.Minute {
		return fmt.Sprintf("%d Minuten", int((d+30*time.Second)/time.Minute))
	}
	return fmt.Sprintf("%d Sekunden", d/time.Second)
}

// maintAnnouncement ist die Ansage im Chat, etwa „Wartung: Server stoppt in
// 60 Sekunden – Update des Koordinators. Danach ist Spielen gleich wieder
// möglich.“ Eine geänderte Frist heißt „Frist geändert“.
func maintAnnouncement(n api.Maintenance, delay time.Duration, first bool) string {
	text := "Wartung: Server stoppt in " + maintDuration(delay)
	if !first {
		text = "Wartung (Frist geändert): Server stoppt in " + maintDuration(delay)
	}
	if n.Reason != "" {
		text += " – " + n.Reason
	}
	return text + ". Danach ist Spielen gleich wieder möglich."
}

// noticeDelay ist die Frist bis zum Ende: DelayS der Notice, ohne
// sinnvolle Angabe (nicht 1–300 s) 30 s.
func noticeDelay(n api.RestoreNotice) time.Duration {
	if n.DelayS <= 0 || time.Duration(n.DelayS)*time.Second > restoreDelayMax {
		return restoreDelay
	}
	return time.Duration(n.DelayS) * time.Second
}

// restoreAnnouncement ist die erste Ansage, etwa „Admin Chef holt den Stand
// von 14:05 zurück – Neustart in 30 Sekunden“.
func restoreAnnouncement(n api.RestoreNotice, delay time.Duration) string {
	who := "Ein Admin"
	if n.By != "" {
		who = "Admin " + n.By
	}
	return fmt.Sprintf("%s holt den Stand von %s zurück – Neustart in %d Sekunden", who, RestoreTime(n), delay/time.Second)
}

// say sagt text im Chat an (in einer eigenen Goroutine; ein Fehler steht
// nur im Log).
func (s *Session) say(text string) {
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, sayTimeout)
		err := s.d.Server.Say(ctx, text)
		cancel()
		return func() {
			if err != nil {
				log.Printf("host: Ansage %q nicht gesendet: %v", text, err)
				return
			}
			log.Printf("host: Ansage: %s", text)
		}
	})
}

// ---- Whitelist und Ops ----

// memberLists sind Whitelist und Ops: UUID (klein) → Minecraft-Name.
type memberLists struct {
	whitelist, ops map[string]string
}

// listsFrom liest Whitelist und Ops aus den Host-Einstellungen.
func listsFrom(hs *api.HostSettings) memberLists {
	toMap := func(ps []api.Player) map[string]string {
		m := make(map[string]string, len(ps))
		for _, p := range ps {
			if id := strings.ToLower(strings.TrimSpace(p.UUID)); id != "" {
				m[id] = strings.TrimSpace(p.Name)
			}
		}
		return m
	}
	return memberLists{whitelist: toMap(hs.Whitelist), ops: toMap(hs.Ops)}
}

// memberCommands sind die Befehle, die einen Server mit old auf next
// bringen, verglichen über die UUID, je Gruppe nach Namen sortiert: neue
// Mitglieder auf die Whitelist, neue Ops, weggefallene Ops (auch entfernter
// Mitglieder – Ops kämen sonst trotz Whitelist herein), entfernte Mitglieder
// von der Whitelist und hinaus – nie aber own, den Spieler des Hosts selbst
// (leer: unbekannt). Ein Name, der kein gültiger Minecraft-Name ist, bekommt
// keinen Befehl (er könnte weitere Argumente einschleusen).
func memberCommands(old, next memberLists, own string) []string {
	var cmds []string
	added := func(from, to map[string]string) []string {
		var names []string
		for id, name := range to {
			if _, ok := from[id]; ok {
				continue
			}
			if !commandName(name) {
				log.Printf("host: %q ist kein gültiger Minecraft-Name – kein Befehl", name)
				continue
			}
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}
	for _, n := range added(old.whitelist, next.whitelist) {
		cmds = append(cmds, "whitelist add "+n)
	}
	for _, n := range added(old.ops, next.ops) {
		cmds = append(cmds, "op "+n)
	}
	for _, n := range added(next.ops, old.ops) {
		cmds = append(cmds, "deop "+n)
	}
	for _, n := range added(next.whitelist, old.whitelist) {
		if own != "" && strings.EqualFold(n, own) {
			log.Printf("host: %s fehlt in der Whitelist, ist aber der Host selbst – bleibt drauf", n)
			continue
		}
		cmds = append(cmds, "whitelist remove "+n, "kick "+n+" "+kickReason)
	}
	return cmds
}

// commandName: ein Minecraft-Name, wie ihn der Koordinator annimmt
// (1–25 Zeichen aus Buchstaben, Ziffern und _).
func commandName(s string) bool {
	if s == "" || len(s) > 25 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_') {
			return false
		}
	}
	return true
}

// syncMembers: Das Lebenszeichen nennt version als MembersVersion. Weicht
// sie von der ab, auf die der Server abgeglichen ist, holt die Sitzung die
// Host-Einstellungen neu und gleicht Whitelist und Ops des laufenden
// Servers an. Die erste Version nach dem Start gleicht mit dem Stand des
// Starts ab (meist ohne Befehle): Ob die Mitglieder sich seit dem Abruf
// beim Start geändert haben, sagt die Version allein nicht. 0 (unbekannt,
// älterer Koordinator) gleicht nie ab. Scheitert der Abgleich, kommt der
// nächste Versuch mit dem nächsten Lebenszeichen; die Befehle schaden auch
// doppelt nicht.
func (s *Session) syncMembers(version int64) {
	if version == 0 || version == s.membersSeen || s.membersBusy || !s.active() {
		return
	}
	s.membersBusy = true
	old := s.members
	s.launch(func() func() {
		next, err := s.syncMembersOp(old)
		return func() {
			s.membersBusy = false
			if err != nil {
				log.Printf("host: Whitelist und Ops nicht angeglichen (neuer Versuch mit dem nächsten Lebenszeichen): %v", err)
				return
			}
			s.members, s.membersSeen = next, version
		}
	})
}

// syncMembersOp holt die Host-Einstellungen und schickt die Befehle von old
// zum neuen Stand. Läuft in einer eigenen Goroutine; old wird nur gelesen.
func (s *Session) syncMembersOp(old memberLists) (memberLists, error) {
	ctx, cancel := context.WithTimeout(s.work, membersTimeout)
	defer cancel()
	hs, err := s.d.Client.HostSettings(ctx)
	if err != nil {
		return old, fmt.Errorf("Host-Einstellungen holen: %w", err)
	}
	next := listsFrom(hs)
	if len(next.whitelist) == 0 && len(old.whitelist) > 0 {
		// Eher ein Fehler beim Koordinator (etwa keine UUID auflösbar) als
		// eine Gruppe ohne Mitglieder: nicht alle hinauswerfen.
		return old, errors.New("die Host-Einstellungen nennen eine leere Whitelist – übersprungen")
	}
	for _, cmd := range memberCommands(old, next, s.d.OwnName) {
		resp, err := s.d.Server.Command(ctx, cmd)
		if err != nil {
			return old, err
		}
		log.Printf("host: Mitglieder angeglichen: %s → %s", cmd, strings.TrimSpace(resp))
	}
	return next, nil
}

// ---- Zwischenstände und Schluss-Stand ----

type baseRef struct {
	world     string
	gen       int64
	uncertain bool
}

type uploadResult struct {
	gen       *api.Generation
	base      baseRef // Basis nach einem Abgleich mit dem Head
	adopted   bool    // base kommt aus dem Abgleich
	uncertain bool    // der Commit kam vielleicht an
	err       error
	stats     snapStats
	// players: wer seit dem vorigen Stand online war (auch wenn es nicht
	// mitging); refused: Der Koordinator hat Players abgelehnt, eingereicht
	// wurde ohne.
	players []string
	refused bool
}

// snapStats sind die Messwerte eines Stands fürs Log (Wanduhr, nicht die
// Uhr der Sitzung).
type snapStats struct {
	save   time.Duration // Zwischenstand: SaveBarrier (save-off, save-all flush); Schluss-Stand: Stop
	stage  time.Duration // Bereitstellen
	hash   time.Duration // Manifest bilden (hashen, Regionsdateien zerlegen)
	upload time.Duration // fehlende Blobs hochladen und einreichen
	sent   int64         // hochgeladene Bytes (nur, was dem Koordinator fehlte)
	total  time.Duration // Zwischenstand: ab dem Beginn; Schluss-Stand: ab dem Beginn von Stop
}

// logStats schreibt die Messwerte des eingereichten Stands gen in eine
// Zeile, etwa „host: Stand 7: speichern 1,2 s, bereitstellen 3,4 s, hashen
// 0,5 s, hochgeladen 12,5 MB in 2,1 s, gesamt 7,3 s“. Beim Schluss-Stand
// steht statt speichern die Dauer von Stop (dabei speichert der Server).
func logStats(gen int64, final bool, st snapStats) {
	what, save := fmt.Sprintf("Stand %d", gen), "speichern"
	if final {
		what, save = fmt.Sprintf("Stand %d (Schluss-Stand)", gen), "stoppen"
	}
	log.Printf("host: %s: %s %s, bereitstellen %s, hashen %s, hochgeladen %s in %s, gesamt %s",
		what, save, seconds(st.save), seconds(st.stage), seconds(st.hash),
		byteSize(st.sent), seconds(st.upload), seconds(st.total))
}

// seconds: „1,2 s“.
func seconds(d time.Duration) string {
	return strings.Replace(fmt.Sprintf("%.1f s", d.Seconds()), ".", ",", 1)
}

// byteSize: „12,5 MB“ (KB unter 1 MB, GB ab 1 GB).
func byteSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MB", float64(n)/(1<<20)), ".", ",", 1)
	default:
		return strings.Replace(fmt.Sprintf("%.1f KB", float64(n)/(1<<10)), ".", ",", 1)
	}
}

func (s *Session) base() baseRef {
	return baseRef{world: s.baseWorld, gen: s.baseGen, uncertain: s.baseUncertain}
}

func (s *Session) startSnapshot() {
	s.snapBusy, s.snapHolding = true, true
	// Wer jetzt online ist, spielt nach diesem Stand weiter.
	s.played = len(s.players) > 0
	players := s.takeSeen()
	send := s.commitPlayers(players)
	// Eigener Abbruch für Barriere und Stage: Ende oder Fencing brechen das
	// Bereitstellen ab, damit Stop nie in einen Zwischenstand fällt.
	ctx, cancel := context.WithCancelCause(s.work)
	s.snapCancel = cancel
	b := s.base()
	s.launch(func() func() {
		r := s.snapshotOp(ctx, b, send)
		r.players = players
		return func() {
			cancel(nil)
			s.onUploaded(r, false)
		}
	})
}

// takeSeen liefert, wer seit dem Beginn des letzten Stands online war
// (sortiert), und beginnt die Liste für den nächsten Stand mit denen, die
// jetzt online sind (sie spielen danach weiter).
func (s *Session) takeSeen() []string {
	out := slices.Sorted(maps.Keys(s.seen))
	s.seen = map[string]bool{}
	for _, n := range s.players {
		s.seen[n] = true
	}
	return out
}

// commitPlayers ist die Spielerliste für den Commit: players, wenn der
// Koordinator das Feld kennt (Lebenszeichen mit JSON-Antwort, nicht
// abgelehnt), sonst nil – ein älterer Koordinator lehnt jedes unbekannte
// Feld ab.
func (s *Session) commitPlayers(players []string) []string {
	if len(players) == 0 || !s.coordNew || s.playersRefused {
		return nil
	}
	return players
}

// snapshotOp: speichern und anhalten, bereitstellen, sofort wieder
// freigeben und das der Schleife melden; danach hashen, hochladen und
// einreichen (mit players, falls nicht nil). Wurde ctx dabei beendet (Ende,
// Fencing), ist der Bereitstellungsordner kein stimmiger Stand: Er wird nie
// eingereicht.
func (s *Session) snapshotOp(ctx context.Context, b baseRef, players []string) uploadResult {
	began := time.Now()
	released := func() { s.events <- s.onSnapReleased } // die Schleife wartet auf diesen Schritt
	// Ein Stand läuft nie gleichzeitig mit Download (syncSlot): belegt vor
	// SaveBarrier, damit der Server nie auf den Platz wartet, freigegeben
	// erst nach dem Einreichen.
	unlock, err := holdSync(ctx)
	if err != nil {
		released()
		return uploadResult{err: fmt.Errorf("Zwischenstand: %w", err)}
	}
	defer unlock()
	t := time.Now()
	release, err := s.d.Server.SaveBarrier(ctx)
	if err != nil {
		released() // SaveBarrier schaltet das Speichern bei Fehlern selbst wieder ein
		return uploadResult{err: fmt.Errorf("Speichern für den Zwischenstand: %w", err)}
	}
	saveTook := time.Since(t)
	t = time.Now()
	stageErr := s.restage(ctx)
	stageTook := time.Since(t)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveOnTimeout)
	if err := release(rctx); err != nil {
		log.Printf("host: Speichern nicht wieder eingeschaltet: %v", err)
	}
	cancel()
	released()
	if stageErr == nil && ctx.Err() != nil {
		stageErr = context.Cause(ctx)
	}
	if stageErr != nil {
		return uploadResult{err: fmt.Errorf("Zwischenstand bereitstellen: %w", stageErr)}
	}
	// Ab hier ist der Stand vollständig bereitgestellt; er darf auch dann
	// noch eingereicht werden, wenn inzwischen das Ende verlangt wurde.
	r := s.uploadStaged(s.work, b, players)
	r.stats.save, r.stats.stage, r.stats.total = saveTook, stageTook, time.Since(began)
	return r
}

// onSnapReleased: Der Zwischenstand hat release hinter sich; ein wartender
// Stop darf jetzt kommen.
func (s *Session) onSnapReleased() {
	s.snapHolding = false
	if s.stopWaiting {
		s.stopWaiting = false
		s.launchStop()
	}
}

// maybeStartFinal startet den Schluss-Upload, sobald der Server sauber
// gestoppt ist und kein Zwischenstand mehr läuft (der Bereitstellungsordner
// gehört bis dahin ihm).
func (s *Session) maybeStartFinal() {
	if s.phase != phFinal || s.snapBusy || s.finalStarted || !s.finalRetryAt.IsZero() {
		return
	}
	s.finalStarted = true
	if s.finalFirst.IsZero() {
		s.finalFirst = s.clk.Now()
	}
	b := s.base()
	stopBegan, stopTook := s.stopBegan, s.finalStop
	// Spieler seit dem letzten Zwischenstand (nach Stop fragt niemand mehr).
	send := s.commitPlayers(slices.Sorted(maps.Keys(s.seen)))
	s.launch(func() func() {
		var r uploadResult
		// Nie gleichzeitig mit Download (syncSlot).
		if unlock, err := holdSync(s.work); err != nil {
			r.err = fmt.Errorf("Schluss-Stand: %w", err)
		} else {
			t := time.Now()
			if err := s.restage(s.work); err != nil {
				r.err = fmt.Errorf("Schluss-Stand bereitstellen: %w", err)
			} else {
				stageTook := time.Since(t)
				r = s.uploadStaged(s.work, b, send)
				r.stats.save, r.stats.stage = stopTook, stageTook
			}
			unlock()
		}
		if !stopBegan.IsZero() {
			r.stats.total = time.Since(stopBegan)
		}
		return func() { s.onUploaded(r, true) }
	})
}

// uploadStaged hasht, lädt hoch und reicht den bereitgestellten Stand auf
// der Basis b ein, mit der Spielerliste players (nil: ohne). Lehnt der
// Koordinator die Spielerliste ab (400 wegen "players": älterer
// Koordinator), reicht es denselben Stand einmal ohne sie ein – abgelehnt
// heißt, nichts wurde angelegt.
func (s *Session) uploadStaged(ctx context.Context, b baseRef, players []string) (r uploadResult) {
	if err := ctx.Err(); err != nil {
		r.err = err
		return r
	}
	s.noteStaged(b)
	if b.uncertain {
		nb, err := s.resolveBase(ctx, b)
		if err != nil {
			r.err = fmt.Errorf("Basis prüfen: %w", err)
			r.uncertain = true
			return r
		}
		r.base, r.adopted, b = nb, true, nb
	}
	t := time.Now()
	files, err := worldsync.BuildManifest(s.staging, s.hc)
	if err != nil {
		r.err = err
		return r
	}
	r.stats.hash = time.Since(t)
	t = time.Now()
	req := api.CommitRequest{
		LeaseID:          s.d.LeaseID,
		MachineID:        s.d.MachineID,
		BaseWorldID:      b.world,
		BaseGeneration:   b.gen,
		MinecraftVersion: s.d.MinecraftVersion,
		Files:            files,
		Players:          players,
	}
	gen, sent, err := worldsync.UploadAndCommitCounted(ctx, s.d.Client, s.staging, req)
	if len(req.Players) > 0 && playersRefused(err) {
		log.Printf("host: Koordinator kennt die Spielerliste im Stand nicht (%v) – ohne sie neu eingereicht, in dieser Sitzung nicht mehr gesendet", err)
		r.refused = true
		req.Players = nil
		var more int64
		gen, more, err = worldsync.UploadAndCommitCounted(ctx, s.d.Client, s.staging, req)
		sent += more
	}
	r.stats.upload, r.stats.sent = time.Since(t), sent
	if err != nil {
		r.err = err
		// Ohne Antwort des Koordinators (Zeitüberschreitung, Verbindung weg)
		// ist offen, ob der Commit angekommen ist.
		_, isStatus := errors.AsType[*client.StatusError](err)
		r.uncertain = !isStatus && !errors.Is(err, worldsync.ErrMissingBlobs)
		return r
	}
	r.gen = gen
	return r
}

// playersRefused: Ein älterer Koordinator lehnt das Feld "players" im
// Commit als unbekannt ab (400, die Meldung nennt es).
func playersRefused(err error) bool {
	se, ok := errors.AsType[*client.StatusError](err)
	return ok && se.StatusCode == http.StatusBadRequest && strings.Contains(se.Message, "players")
}

// resolveBase klärt nach einem unklaren Commit die Basis: Ist der Head ein
// Stand dieses Zuschlags direkt auf b, ist der Commit angekommen.
func (s *Session) resolveBase(ctx context.Context, b baseRef) (baseRef, error) {
	head, err := s.d.Client.Head(ctx)
	if err != nil {
		return b, err
	}
	nb := baseRef{world: b.world, gen: b.gen}
	if head != nil && head.WorldID == b.world && head.Number == b.gen+1 && head.Parent == b.gen &&
		head.LeaseID == s.d.LeaseID && head.MachineID == s.d.MachineID {
		log.Printf("host: voriger Commit ist doch angekommen (%s/%d)", head.WorldID, head.Number)
		nb.gen = head.Number
	}
	return nb, nil
}

func (s *Session) onUploaded(r uploadResult, final bool) {
	if !final {
		s.snapBusy = false
	}
	if r.refused {
		s.playersRefused = true
	}
	if r.adopted {
		// Auch wenn danach etwas scheitert: Die Basis ist jetzt geklärt.
		s.setBase(r.base.world, r.base.gen, false)
	}
	switch {
	case r.err == nil:
		// Erst nach dem Schluss-Stand (oder beim Weiterspielen) ist alles
		// Lokale eingereicht; nach dem Stop ist der Stand neuer als jeder
		// Zwischenstand.
		saveErr := s.setBase(r.gen.WorldID, r.gen.Number, final || s.phase == phHosting)
		s.noteCommitted(r.gen, false)
		log.Printf("host: Stand %s/%d eingereicht", r.gen.WorldID, r.gen.Number)
		logStats(r.gen.Number, final, r.stats)
		if final {
			s.release = true
			if s.markerKept && saveErr == nil {
				if err := removeMarker(s.marker); err != nil {
					log.Printf("host: Markierungsdatei nicht gelöscht: %v", err)
				}
			}
		}
	case s.fenced != nil:
		// Abgebrochen, nichts mehr hochladen.
	case final && !s.mainPort && errors.Is(r.err, client.ErrLeaseLost):
		// Wartender Zuschlag, nie befördert: Es wurde nichts gespielt, das
		// verloren ginge (der Stand des alten Hosts ist der aktuelle).
		// Kein Fencing, nichts nachzureichen.
		log.Printf("host: Schluss-Stand nicht angenommen (Zuschlag wurde nie befördert): %v", r.err)
		s.st.PendingUpload = false
		s.saveState()
		s.release = true
	case !final && !s.mainPort && errors.Is(r.err, client.ErrLeaseLost):
		// Wartender Zuschlag einer Übergabe, noch nicht befördert: Der
		// Koordinator nimmt noch keinen Stand an. Still auslassen, der
		// nächste Takt versucht es wieder (hält der Zuschlag nicht mehr,
		// sagt es das Lebenszeichen).
		log.Printf("host: Zwischenstand noch nicht angenommen (Zuschlag wartet auf die Beförderung): %v", r.err)
		s.played = true
		for _, n := range r.players {
			s.seen[n] = true
		}
	case errors.Is(r.err, client.ErrLeaseLost):
		s.fence(fmt.Errorf("Koordinator lehnt den Commit ab: %w", r.err))
	default:
		if r.uncertain {
			s.baseUncertain = true
		}
		log.Printf("host: Stand nicht eingereicht: %v", r.err)
		if !final {
			// Was seit dem vorigen Stand gespielt wurde, ist noch nicht
			// eingereicht: Der nächste Zwischenstand kommt auf jeden Fall,
			// und seine Spielerliste nennt auch die Spieler dieses Stands.
			s.played = true
			for _, n := range r.players {
				s.seen[n] = true
			}
		}
		if final {
			// Erst wiederholen, solange der Zuschlag hält: Nachreichen geht
			// nur, wenn bis dahin niemand anderes hostet.
			now := s.clk.Now()
			if next := now.Add(finalRetryPause); !next.After(s.finalFirst.Add(finalRetryFor)) {
				log.Printf("host: Schluss-Stand: neuer Versuch in %v", finalRetryPause)
				s.finalStarted = false
				s.finalRetryAt = next
				return
			}
			s.result = fmt.Errorf("Schluss-Stand nicht hochgeladen, er wird nachgereicht: %w", r.err)
			s.release = true
		}
	}
	if final {
		s.phase = phDone
	}
	s.maybeStartFinal()
}

// setBase setzt die Basis und schreibt sie in den Zustand; clearPending:
// alles Lokale ist eingereicht.
func (s *Session) setBase(world string, gen int64, clearPending bool) error {
	s.baseWorld, s.baseGen, s.baseUncertain = world, gen, false
	s.st.LastUploadedWorldID, s.st.LastUploadedGeneration = world, gen
	if clearPending {
		s.st.PendingUpload = false
	}
	return s.saveState()
}

func (s *Session) saveState() error {
	err := s.st.Save()
	if err != nil {
		log.Printf("host: Zustand nicht gespeichert: %v", err)
	}
	return err
}

// ---- Ende ----

// requestEnd beginnt das saubere Ende.
func (s *Session) requestEnd(why string) {
	if s.ending || s.fenced != nil || s.phase == phDone {
		return
	}
	s.hoPending = false
	if s.hoHold(why) {
		return
	}
	s.ending = true
	log.Printf("host: Ende: %s", why)
	s.stopTunnel()
	switch s.phase {
	case phStarting:
		s.startCancel()
	case phHosting:
		s.beginStop()
	}
}

// fence stoppt sofort: Arbeit abbrechen, tunnel beenden, Server stoppen.
// Der Server bekommt fenceStopGrace zum sauberen Stoppen (auch ein schon
// laufender oder erst nach dem Start folgender Stop), dann wird er hart
// beendet.
func (s *Session) fence(cause error) {
	if s.fenced != nil || s.phase == phDone {
		return
	}
	s.fenced = cause
	log.Printf("host: FENCING: %v – tunnel und Server werden sofort beendet, nichts wird mehr hochgeladen", cause)
	s.stopWork(cause)
	s.stopTunnel()
	if s.phase != phFinal {
		s.stopBy = s.clk.Now().Add(fenceStopGrace)
	}
	switch s.phase {
	case phHosting:
		s.beginStop()
	case phFinal:
		// Läuft der Schluss-Upload, bricht er jetzt ab; sonst gibt es nichts
		// mehr zu tun.
		if !s.finalStarted {
			s.phase = phDone
		}
	}
	s.release = true
}

// beginStop stoppt den Server – aber nie, solange ein Zwischenstand
// zwischen SaveBarrier und release steht: Minecraft schaltet beim Stoppen
// das Speichern wieder ein und schreibt die Welt, während Stage kopiert
// (gemischter Stand), und unter Windows scheitert das Ersetzen einer Datei,
// die Stage gerade offen hat. Der Zwischenstand wird abgebrochen und
// verworfen; Stop kommt, sobald release durch ist (tunnel ist schon
// beendet, die Wartezeit also begrenzt).
func (s *Session) beginStop() {
	s.phase = phStopping
	s.exited = nil
	s.players, s.emptySince = []string{}, time.Time{}
	if s.snapHolding {
		s.snapCancel(errors.New("Server wird gestoppt"))
		s.stopWaiting = true
		log.Printf("host: Stop wartet, bis der Zwischenstand abgebrochen ist")
		return
	}
	s.launchStop()
}

func (s *Session) launchStop() {
	began := time.Now()
	s.stopBegan = began // Messwert des Schluss-Stands
	// Nie mit einem schon beendeten Kontext: der würde Java sofort hart
	// beenden – außer die Frist nach dem Fencing ist schon um, dann soll er
	// genau das.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), stopTimeout)
	if s.stopLate {
		cancel()
	}
	s.stopCancel = cancel
	s.launch(func() func() {
		err := s.d.Server.Stop(ctx)
		// Nur Stop selbst: Der Schluss-Stand kann danach noch auf einen
		// laufenden Zwischenstand warten.
		took := time.Since(began)
		cancel()
		return func() {
			s.finalStop = took
			s.onStopped(err)
		}
	})
}

// expireStop: Die Frist für den sauberen Stop nach dem Fencing ist um. Ein
// laufender Stop beendet den Server jetzt hart, ein noch ausstehender
// (Zwischenstand gibt erst frei, Start läuft noch) gleich zu Beginn.
func (s *Session) expireStop() {
	if s.phase != phStarting && s.phase != phStopping {
		return // Server schon gestoppt oder nie gestartet
	}
	s.stopLate = true
	log.Printf("host: Server %v nach dem Fencing nicht sauber gestoppt – er wird hart beendet, der Stand wird nicht vorgemerkt", fenceStopGrace)
	if s.stopCancel != nil {
		s.stopCancel()
	}
}

func (s *Session) onStopped(err error) {
	s.stopCancel, s.stopBy = nil, time.Time{}
	if err != nil {
		// Nicht sauber (hart beendet): Markierung bleibt, nichts hochladen.
		log.Printf("host: Server nicht sauber gestoppt, der Stand wird nicht hochgeladen: %v", err)
		if s.fenced == nil {
			s.result = fmt.Errorf("Server nicht sauber gestoppt, der Stand wird nicht hochgeladen: %w", err)
		}
		s.release = true
		s.phase = phDone
		return
	}
	if s.handedOver && s.fenced == nil {
		// Übergeben: Der Übergabe-Stand ist eingereicht (und die Basis); was
		// der Server beim Stoppen noch geschrieben hat (Spectator-Modi), ist
		// kein Stand. Nichts vormerken, nichts hochladen, Zuschlag zurück.
		s.st.PendingUpload = false
		s.st.LastUploadedWorldID, s.st.LastUploadedGeneration = s.baseWorld, s.baseGen
		if err := s.saveState(); err != nil {
			s.markerKept = true
		} else if err := removeMarker(s.marker); err != nil {
			log.Printf("host: Markierungsdatei nicht gelöscht: %v", err)
		}
		log.Printf("host: Welt übergeben, Server gestoppt – kein Schluss-Stand")
		s.release = true
		s.phase = phDone
		return
	}
	// Sauber gestoppt: Die Welt ist vollständig. Erst vormerken, dann die
	// Markierung löschen – bei einem Absturz dazwischen wird verworfen,
	// nie Halbes hochgeladen.
	s.st.PendingUpload = true
	s.st.LastUploadedWorldID, s.st.LastUploadedGeneration = s.baseWorld, s.baseGen
	if err := s.saveState(); err != nil {
		// Ohne gespeicherte Vormerkung wüsste ein späterer Start nicht, dass
		// der Stand fehlt: Markierung behalten (wird dann verworfen, nie
		// halb nachgereicht). Nach einem erfolgreichen Schluss-Stand wird
		// sie doch noch gelöscht.
		s.markerKept = true
	} else if err := removeMarker(s.marker); err != nil {
		log.Printf("host: Markierungsdatei nicht gelöscht: %v", err)
	}
	if s.fenced != nil {
		log.Printf("host: Server sauber gestoppt; der Stand wird nachgereicht, wenn seitdem niemand gehostet hat")
		s.phase = phDone
		return
	}
	s.phase = phFinal
	s.maybeStartFinal()
}

func (s *Session) onServerExit(err error) {
	if s.phase != phHosting {
		return
	}
	if err == nil {
		err = errors.New("ohne Stop beendet")
	}
	log.Printf("host: Minecraft-Server unerwartet beendet: %v", err)
	s.result = fmt.Errorf("Minecraft-Server unerwartet beendet: %w", err)
	// Die Welt kann halb geschrieben sein: Markierung bleibt, nichts mehr
	// hochladen.
	s.stopWork(errors.New("Server beendet"))
	s.stopTunnel()
	s.release = true
	s.phase = phDone
}

// releaseLease gibt den Zuschlag zurück (auch wenn ctx schon beendet ist).
func releaseLease(ctx context.Context, c *client.Client, leaseID int64, machineID string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := c.Release(rctx, api.ReleaseRequest{LeaseID: leaseID, MachineID: machineID}); err != nil {
		log.Printf("host: Zuschlag nicht zurückgegeben: %v", err)
		return
	}
	log.Printf("host: Zuschlag zurückgegeben")
}

// ---- tunnel ----

// startTunnel startet tunnel – nur solange der Zuschlag gilt und der Server
// laufen soll.
func (s *Session) startTunnel(now time.Time) {
	for _, t := range s.tunnels {
		s.startSlot(t, now)
	}
}

// startSlot startet den tunnel-Prozess t, falls er noch nicht läuft.
func (s *Session) startSlot(slot *tunnelSlot, now time.Time) {
	if !s.tunnelOn || slot.proc != nil || !s.active() {
		return
	}
	ctx, cancel := context.WithCancel(s.work)
	cmd := s.tunnelCmd(ctx, slot.port)
	if cmd == nil {
		cancel()
		log.Printf("host: kein tunnel-Befehl")
		s.scheduleTunnel(slot, now, 0)
		return
	}
	var logf *os.File
	if cmd.Stdout == nil && cmd.Stderr == nil {
		logf = openTunnelLog()
		if logf != nil {
			cmd.Stdout, cmd.Stderr = logf, logf
		}
	}
	err := cmd.Start()
	if logf != nil {
		logf.Close() // der Prozess hat seine eigene Kopie
	}
	if err != nil {
		cancel()
		log.Printf("host: tunnel starten: %v", err)
		s.scheduleTunnel(slot, now, 0)
		return
	}
	log.Printf("host: tunnel gestartet (PID %d, Port %d)", cmd.Process.Pid, slot.port)
	if s.job != nil {
		if err := s.job.assign(cmd.Process.Pid); err != nil {
			log.Printf("host: tunnel nicht im Job-Objekt, er überlebt einen Absturz des Starters: %v", err)
		}
	}
	// Für Recover: Stirbt der Starter, ohne dass Windows tunnel mitnimmt,
	// beendet der nächste Start ihn anhand dieser Angaben.
	ref := identify(cmd.Process.Pid)
	if slot.index == 0 {
		s.mark.Tunnel = &ref
	} else {
		s.mark.Tunnel2 = &ref
	}
	s.mark.Written = time.Now().UTC()
	if err := writeMarker(s.marker, s.mark); err != nil {
		log.Printf("host: tunnel nicht in die Markierung geschrieben: %v", err)
	}
	t := &tunnelProc{cmd: cmd, cancel: cancel, done: make(chan struct{}), started: now}
	slot.proc = t
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t.err = cmd.Wait()
		close(t.done)
		s.post(func() { s.onTunnelExit(slot, t) })
	}()
}

func (s *Session) tunnelCmd(ctx context.Context, port int) *exec.Cmd {
	if s.d.TunnelCmd != nil {
		return s.d.TunnelCmd(ctx)
	}
	cmd := exec.CommandContext(ctx, s.d.Paths.Tunnel,
		"tcp", strconv.Itoa(MinecraftPort), "--port", strconv.Itoa(port), "--config", tunnelConfigPath())
	cmd.SysProcAttr = hiddenProcAttr()
	return cmd
}

func (s *Session) onTunnelExit(slot *tunnelSlot, t *tunnelProc) {
	if t != slot.proc {
		return // von uns beendet
	}
	slot.proc = nil
	t.cancel()
	if !s.active() {
		return
	}
	log.Printf("host: tunnel (Port %d) beendet: %v", slot.port, t.err)
	now := s.clk.Now()
	s.scheduleTunnel(slot, now, now.Sub(t.started))
}

// scheduleTunnel plant den Neustart mit Pause 2 s, 5 s, 10 s, dann 30 s;
// lief tunnel zuletzt länger als tunnelStable, wieder ab 2 s.
func (s *Session) scheduleTunnel(slot *tunnelSlot, now time.Time, ranFor time.Duration) {
	if ranFor >= tunnelStable {
		slot.fails = 0
	}
	pause := tunnelBackoff[min(slot.fails, len(tunnelBackoff)-1)]
	slot.fails++
	slot.at = now.Add(pause)
	log.Printf("host: tunnel (Port %d) startet in %v neu", slot.port, pause)
}

// stopTunnel beendet alle tunnel-Prozesse und wartet kurz auf ihr Ende;
// kein Neustart.
func (s *Session) stopTunnel() {
	for _, slot := range s.tunnels {
		slot.at = time.Time{}
		t := slot.proc
		if t == nil {
			continue
		}
		slot.proc = nil
		t.cancel()
		if err := t.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			log.Printf("host: tunnel beenden: %v", err)
		}
		select {
		case <-t.done:
			log.Printf("host: tunnel beendet")
		case <-time.After(tunnelKillWait):
			log.Printf("host: tunnel reagiert nicht auf das Beenden")
		}
	}
}

// openMainPort: Der Koordinator hat den Zuschlag befördert (MainPort im
// Lebenszeichen): Der Server wird zusätzlich auf dem Haupt-Port
// freigegeben, mit derselben Überwachung wie der erste tunnel. Der erste
// (Ersatz-Port) bleibt, die übergebenen Spieler hängen an ihm.
func (s *Session) openMainPort() {
	if s.mainPort {
		return
	}
	s.mainPort = true
	slot := &tunnelSlot{port: PublicPort, index: 1}
	s.tunnels = append(s.tunnels, slot)
	log.Printf("host: Zuschlag befördert – Haupt-Port %d wird zusätzlich geöffnet", PublicPort)
	s.startSlot(slot, s.clk.Now())
}

func tunnelConfigPath() string { return filepath.Join(group.Dir(), "tunnel.yaml") }

// openTunnelLog öffnet Home()/logs/tunnel.log zum Anhängen (nil bei Fehler).
func openTunnelLog() *os.File {
	dir := filepath.Join(group.Home(), "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "tunnel.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return f
}

type tunnelConfig struct {
	Server             string `yaml:"server"`
	Token              string `yaml:"token"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
}

// writeTunnelConfig schreibt Dir()/tunnel.yaml für den tunnel-Client. Die
// Datei enthält den persönlichen Token; nur der eigene Benutzer (und
// SYSTEM) darf sie lesen.
func writeTunnelConfig(g *group.Group) error {
	data, err := yaml.Marshal(tunnelConfig{
		Server:             g.TunnelServer,
		Token:              g.Token,
		InsecureSkipVerify: g.TunnelInsecureSkipVerify,
	})
	if err != nil {
		return err
	}
	data = append([]byte("# Von wanderwelt geschrieben. Enthält den persönlichen Token.\n"), data...)
	path := tunnelConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Erst die leere Temp-Datei abschließen, dann den Token hineinschreiben.
	f, err := os.CreateTemp(filepath.Dir(path), ".tunnel.yaml.*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	err = restrictFile(tmp)
	if err == nil {
		err = os.WriteFile(tmp, data, 0o600)
	}
	if err == nil {
		err = renameRetry(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
