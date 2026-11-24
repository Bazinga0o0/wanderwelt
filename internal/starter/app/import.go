package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/host"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// „Welt übernehmen“ (Spec 6.2.4): Ein Admin macht eine Einzelspieler-Welt
// zur Gruppenwelt. Der Speicherstand selbst wird nie verändert: Er wird in
// einen eigenen Bereitstellungsordner kopiert und von dort hochgeladen.

// importStagingName: Bereitstellungsordner in Home(), wird danach gelöscht.
const importStagingName = "import-staging"

// errBusy: Der Koordinator gibt keinen Zuschlag zum Übernehmen (jemand
// hostet, eine Wahl läuft oder der Stand hat sich geändert).
var errBusy = errors.New("gerade hostet jemand")

// worldOpenAfterCopy prüft nach dem Kopieren noch einmal, ob Minecraft die
// Welt geöffnet hat; im Test austauschbar.
var worldOpenAfterCopy = worldOpen

// openImportProgress öffnet das Fortschrittsfenster von „Welt übernehmen“;
// im Test austauschbar.
var openImportProgress = func() (zenity.ProgressDialog, error) {
	return zenity.Progress(zenity.Title("wanderwelt – Welt übernehmen"), windowIcon(), zenity.Pulsate(), zenity.CancelLabel("Abbrechen"))
}

// importWorld lädt den Speicherstand save als neue Gruppenwelt hoch:
// Head lesen → Zuschlag nur zum Hochladen auf dieser Basis → kopieren →
// Manifest → Upload und Commit mit NewWorld → Zuschlag zurück.
func importWorld(ctx context.Context, c *client.Client, machineID string, save SaveInfo, version string,
	hc *worldsync.HashCache, progress func(string)) (*api.Generation, error) {
	head, err := retry401(ctx, func() (*api.Generation, error) { return c.Head(ctx) })
	if err != nil {
		return nil, fmt.Errorf("Stand der Gruppenwelt abfragen: %w", err)
	}
	var baseWorld string
	var baseGen int64
	if head != nil {
		baseWorld, baseGen = head.WorldID, head.Number
	}
	resp, err := retry401(ctx, func() (*api.PlayResponse, error) {
		return c.Play(ctx, api.PlayRequest{
			MachineID:      machineID,
			UploadOnly:     true,
			BaseWorldID:    baseWorld,
			BaseGeneration: baseGen,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("Zuschlag zum Übernehmen: %w", err)
	}
	if resp.Role != api.RoleHost {
		return nil, fmt.Errorf("%w (%s) – die Welt lässt sich nur übernehmen, wenn niemand spielt", errBusy, resp.Reason)
	}
	lease := resp.LeaseID
	log.Printf("Welt übernehmen: Zuschlag %d auf Basis %q/%d", lease, baseWorld, baseGen)
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := c.Release(rctx, api.ReleaseRequest{LeaseID: lease, MachineID: machineID}); err != nil {
			log.Printf("Welt übernehmen: Zuschlag nicht zurückgegeben: %v", err)
		} else {
			log.Printf("Welt übernehmen: Zuschlag zurückgegeben")
		}
	}()

	lctx, lost := context.WithCancelCause(ctx)
	defer lost(nil)
	stop := keepLease(lctx, c, lease, machineID, lost)
	defer stop()

	// Bereitstellen, Manifest und Upload teilen Rezeptablage und Hash-Cache
	// mit Download und Ständen der Host-Sitzung: nie gleichzeitig.
	unlock, err := host.HoldSync(lctx)
	if err != nil {
		return nil, leaseCause(lctx, err)
	}
	defer unlock()

	staging := filepath.Join(group.Home(), importStagingName)
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			log.Printf("Welt übernehmen: %s nicht gelöscht: %v", staging, err)
		}
	}()
	progress(fmt.Sprintf("kopiert „%s“ (%s) …", save.Folder, humanSize(save.Size)))
	if err := worldsync.StageContext(lctx, save.Dir, staging); err != nil {
		return nil, leaseCause(lctx, fmt.Errorf("Welt kopieren: %w", err))
	}
	// Wurde die Welt während des Kopierens in Minecraft geöffnet, kann die
	// Kopie gemischt sein: nicht einreichen.
	if worldOpenAfterCopy(save.Dir) {
		return nil, fmt.Errorf("„%s“ wurde während des Kopierens in Minecraft geöffnet – abgebrochen, die Gruppenwelt ist unverändert. Bitte die Welt erst verlassen und dann noch einmal", save.Folder)
	}
	progress("berechnet Prüfsummen …")
	files, err := worldsync.BuildManifest(staging, hc)
	if err != nil {
		return nil, leaseCause(lctx, err)
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	progress(fmt.Sprintf("lädt %d Dateien (%s) hoch …", len(files), humanSize(total)))
	gen, err := worldsync.UploadAndCommit(lctx, c, staging, api.CommitRequest{
		LeaseID:        lease,
		MachineID:      machineID,
		BaseWorldID:    baseWorld,
		BaseGeneration: baseGen,
		NewWorld:       true,
		// Spec 6.2.4: Der Import läuft in der Manifest-Version; eine ältere
		// Welt hebt der Server beim ersten Start an.
		MinecraftVersion: version,
		Files:            files,
	})
	if err != nil {
		return nil, leaseCause(lctx, fmt.Errorf("hochladen: %w", err))
	}
	log.Printf("Welt übernehmen: „%s“ ist jetzt %s/%d (%d Dateien, %s)", save.Folder, gen.WorldID, gen.Number, len(files), humanSize(total))
	return gen, nil
}

// leaseCause bevorzugt den Grund, aus dem lctx beendet wurde (Zuschlag
// verloren).
func leaseCause(lctx context.Context, err error) error {
	if lctx.Err() != nil {
		if cause := context.Cause(lctx); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
	}
	return err
}

// keepLease schickt alle 10 s ein Lebenszeichen, bis stop aufgerufen wird.
// Lehnt der Koordinator ab, beendet lost die Arbeit.
func keepLease(ctx context.Context, c *client.Client, lease int64, machine string, lost context.CancelCauseFunc) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(host.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			hctx, cancel := context.WithTimeout(ctx, host.HeartbeatInterval)
			_, err := c.Heartbeat(hctx, api.HeartbeatRequest{LeaseID: lease, MachineID: machine, Players: []string{}})
			cancel()
			if errors.Is(err, client.ErrLeaseLost) {
				lost(fmt.Errorf("Zuschlag zum Übernehmen verloren: %w", err))
				return
			}
			if err != nil {
				log.Printf("Welt übernehmen: Lebenszeichen: %v", err)
			}
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

func savesDir() string { return filepath.Join(deps.MinecraftDir(), "saves") }

// ---- Tray: „Welt übernehmen …“ ----

// importRequest: Ein „wanderwelt.exe import -ja …“ übergibt die Welt an die
// laufende Instanz (Steuerung, Aktion "import"), damit Welt- und
// Cache-Arbeit nie aus zwei Prozessen kommt.
type importRequest struct {
	Dir                string `json:"dir"`
	AllowMissingPlayer bool   `json:"ohne_spielerdatei"`

	// FromWindow: das Fenster hat gefragt und zeigt den Fortschritt selbst
	// (Zustand.Detail); kein zenity-Fortschrittsfenster daneben. Nie aus
	// dem Körper, nur von fensterImport gesetzt.
	FromWindow bool `json:"-"`
}

// importContext ist, was beide Wege (Dialog und Übergabe) vor der Auswahl
// brauchen.
type importContext struct {
	c         *client.Client
	machineID string
	m         *api.Manifest
}

// importPrepare: nicht beim Hosten, Welt-Ordner belegen (keine
// Host-Sitzung, kein Vorladen, kein Nachreichen daneben), Admin prüfen,
// Manifest holen. refused nicht nil: geht nicht, refused ist der Grund zum
// Anzeigen. Sonst gibt release den Welt-Ordner frei.
func (a *App) importPrepare() (ic importContext, release func(), refused *resultDialog) {
	if a.isHosting() {
		return ic, nil, infoDialog("Während du hostest, lässt sich keine Welt übernehmen.")
	}
	if err := a.acquireWorld(a.ctx, "import"); err != nil {
		return ic, nil, infoDialog("Welt übernehmen geht gerade nicht: " + err.Error())
	}
	a.mu.Lock()
	ic.c, ic.machineID = a.c, a.machineID
	a.mu.Unlock()
	ctx := a.ctx
	me, err := retry401(ctx, func() (*api.Me, error) { return ic.c.Me(ctx) })
	if err != nil {
		a.releaseWorld()
		return ic, nil, errorDialog("Koordinator: " + explain(err))
	}
	a.mu.Lock()
	a.me = me
	a.mu.Unlock()
	a.refresh()
	if !me.Admin {
		a.releaseWorld()
		return ic, nil, errorDialog("Welt übernehmen dürfen nur Admins.")
	}
	ic.m, err = retry401(ctx, func() (*api.Manifest, error) { return ic.c.Manifest(ctx) })
	if err != nil {
		a.releaseWorld()
		return ic, nil, errorDialog("Versions-Manifest: " + explain(err))
	}
	return ic, a.releaseWorld, nil
}

// importDialog: Tray-Menü „Welt übernehmen …“. Das Ergebnis erscheint erst
// nach dem Ende der Aktion (endWith).
func (a *App) importDialog() {
	if ok, running := a.begin("Welt übernehmen"); !ok {
		go notify("Moment – „" + running + "“ läuft noch.")
		return
	}
	a.endWith(a.chooseAndImport())
}

// chooseAndImport lässt die Welt auswählen und übernimmt sie; das Ergebnis
// (nil: nichts anzuzeigen) zeigt importDialog. Der Welt-Ordner ist danach
// wieder frei.
func (a *App) chooseAndImport() *resultDialog {
	ic, release, refused := a.importPrepare()
	if refused != nil {
		return refused
	}
	defer release()

	dir := savesDir()
	saves, err := ListSaves(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(saves) == 0) {
		return infoDialog("In " + dir + " gibt es keine Minecraft-Welten.")
	}
	if err != nil {
		return errorDialog("Speicherstände lesen: " + err.Error())
	}
	labels := make([]string, len(saves))
	for i, s := range saves {
		labels[i] = s.Label()
	}
	choice, err := zenity.List(
		"Welche Welt soll die Gruppenwelt werden?\nDie bisherige Gruppenwelt bleibt als Archiv auf dem Koordinator.",
		labels,
		zenity.Title("wanderwelt – Welt übernehmen"), windowIcon(),
		zenity.Width(720), zenity.Height(420),
		zenity.OKLabel("Weiter"), zenity.CancelLabel("Abbrechen"),
		zenity.DisallowEmpty())
	if err != nil {
		return nil
	}
	idx := slices.Index(labels, choice)
	if idx < 0 {
		return nil
	}
	save := saves[idx]

	if ok, refused := checkSaveUI(save, ic.m.MinecraftVersion, false); !ok {
		return refused
	}
	if !confirm(fmt.Sprintf("„%s“ (Minecraft %s, %s) wird zur Gruppenwelt und ab jetzt in Minecraft %s gespielt.\n\n"+
		"Die bisherige Gruppenwelt bleibt als Archiv auf dem Koordinator. Dein Speicherstand selbst bleibt unverändert.\n\nJetzt übernehmen?",
		save.Folder, save.Version, humanSize(save.Size), ic.m.MinecraftVersion), "Übernehmen", "Abbrechen") {
		return nil
	}
	return a.importWithProgress(ic, save, true)
}

// importFromControl: „wanderwelt.exe import -ja <ordner>“ hat die Welt an
// diese Instanz übergeben. Die Prüfungen hat die Kommandozeile schon
// gezeigt; hier laufen sie noch einmal, die Bestätigung gab -ja. Das
// Ergebnis erscheint erst nach dem Ende der Aktion (endWith).
func (a *App) importFromControl(req importRequest) {
	if ok, running := a.begin("Welt übernehmen"); !ok {
		go notify("Welt übernehmen von der Kommandozeile: erst wenn „" + running + "“ fertig ist.")
		return
	}
	a.endWith(a.importRequested(req))
}

// importRequested übernimmt die Welt aus req; das Ergebnis zeigt
// importFromControl. Der Welt-Ordner ist danach wieder frei.
func (a *App) importRequested(req importRequest) *resultDialog {
	log.Printf("Welt übernehmen von der Kommandozeile: %s", req.Dir)
	ic, release, refused := a.importPrepare()
	if refused != nil {
		return refused
	}
	defer release()
	save := ReadSave(req.Dir)
	if ok, refused := checkSaveUI(save, ic.m.MinecraftVersion, req.AllowMissingPlayer); !ok {
		return refused
	}
	return a.importWithProgress(ic, save, !req.FromWindow)
}

// checkSaveUI prüft save (Spec 6.2.4) und fragt bei einer Warnung nach.
// ok false: nicht übernehmen; refused ist dann die Ablehnung zum Anzeigen
// (nil, wenn bei der Nachfrage abgebrochen wurde).
func checkSaveUI(save SaveInfo, manifestVersion string, allowMissingPlayer bool) (ok bool, refused *resultDialog) {
	refuse, warn := CheckSave(save, manifestVersion)
	if refuse != nil {
		return false, errorDialog(refuse.Error())
	}
	if worldOpen(save.Dir) {
		return false, errorDialog("„" + save.Folder + "“ ist gerade in Minecraft geöffnet. Bitte die Welt erst verlassen.")
	}
	if warn != "" && !allowMissingPlayer && !confirm(warn+"\n\nTrotzdem übernehmen?", "Trotzdem übernehmen", "Abbrechen") {
		return false, nil
	}
	return true, nil
}

// importWithProgress lädt save mit Fortschrittsfenster hoch und liefert
// das Ergebnisfenster (der Aufrufer zeigt es nach dem Ende der Aktion).
// Benutzt den Hash-Cache der Instanz.
func (a *App) importWithProgress(ic importContext, save SaveInfo, mitDialog bool) *resultDialog {
	ctx := a.ctx
	ictx, cancel := context.WithCancel(ctx)
	defer cancel()
	var dlg zenity.ProgressDialog
	if mitDialog {
		var err error
		if dlg, err = openImportProgress(); err != nil {
			dlg = nil
		}
	}
	if dlg != nil {
		go func() {
			select {
			case <-dlg.Done():
				cancel()
			case <-ictx.Done():
			}
		}()
	}
	var dmu sync.Mutex
	progress := func(text string) {
		a.setPhase(phUploading, "Welt übernehmen: "+text)
		dmu.Lock()
		defer dmu.Unlock()
		if dlg != nil {
			dlg.Text("Welt übernehmen: " + text)
		}
	}
	gen, err := importWorld(ictx, ic.c, ic.machineID, save, ic.m.MinecraftVersion, a.hc, progress)
	// Vor dem Schließen: Close beendet auch dlg.Done() und damit ictx.
	userCanceled := ictx.Err() != nil && ctx.Err() == nil
	dmu.Lock()
	if dlg != nil {
		dlg.Close()
		dlg = nil
	}
	dmu.Unlock()
	if err != nil {
		if userCanceled {
			a.setPhase(phReady, "")
			return infoDialog("Welt übernehmen abgebrochen. Die Gruppenwelt ist unverändert.")
		}
		a.showFailed("Welt übernehmen: "+explain(err), err)
		return errorDialog("Welt übernehmen gescheitert:\n\n" + explain(err))
	}
	a.setPhase(phReady, "")
	go a.pollStatus(false)
	return infoDialog(fmt.Sprintf("„%s“ ist jetzt die Gruppenwelt (Stand %s/%d).", save.Folder, gen.WorldID, gen.Number))
}

// ---- Kommandozeile: wanderwelt import ----

// ImportCLI ist „wanderwelt.exe import [-ja] [-ohne-spielerdatei] <ordner>“:
// dasselbe wie „Welt übernehmen …“, ohne Dialoge. Ergebnis ist der
// Exit-Code.
func ImportCLI(args []string) int {
	fset := flag.NewFlagSet("import", flag.ContinueOnError)
	yes := fset.Bool("ja", false, "die Welt wirklich übernehmen (ohne: nur prüfen)")
	noPlayer := fset.Bool("ohne-spielerdatei", false, "auch übernehmen, wenn die Spielerdatei des Einzelspielers fehlt")
	fset.Usage = func() {
		fmt.Fprintln(fset.Output(), "Aufruf: wanderwelt import [-ja] [-ohne-spielerdatei] <weltordner>")
		fmt.Fprintln(fset.Output(), "Macht eine Einzelspieler-Welt zur Gruppenwelt (nur Admin, nur wenn niemand hostet).")
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if fset.NArg() != 1 {
		fset.Usage()
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "import: "+format+"\n", a...)
		return 1
	}

	// Läuft der Tray, übernimmt er die Welt (Übergabe über die Steuerung):
	// Welt- und Cache-Arbeit kommt nie aus zwei Prozessen. Sonst hält dieser
	// Prozess die Sperre, bis er fertig ist.
	release, owner, err := lockInstance(false)
	if err != nil {
		return fail("%v", err)
	}
	if owner {
		defer release()
		if logc, err := setupLog(true); err == nil {
			defer logc.Close()
		}
	} else {
		log.SetOutput(os.Stderr) // das Log gehört der laufenden Instanz
	}
	if !installed() {
		return fail("wanderwelt ist auf diesem PC noch nicht eingerichtet (erst wanderwelt.exe ohne Argumente starten)")
	}
	g, err := group.LoadGroup(group.GroupPath())
	if err != nil {
		return fail("%v", err)
	}
	st, err := group.LoadState()
	if err != nil {
		return fail("%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	c := client.New(g)
	me, err := retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) })
	if err != nil {
		return fail("%s", explain(err))
	}
	if !me.Admin {
		return fail("Welt übernehmen dürfen nur Admins (%s ist keiner)", me.Email)
	}
	m, err := retry401(ctx, func() (*api.Manifest, error) { return c.Manifest(ctx) })
	if err != nil {
		return fail("Versions-Manifest: %s", explain(err))
	}

	dir := fset.Arg(0)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fail("%s ist kein Ordner", dir)
	}
	save := ReadSave(dir)
	fmt.Printf("Welt:     %s\n", save.Label())
	fmt.Printf("Ordner:   %s\n", save.Dir)
	if save.PlayerUUID != "" {
		fmt.Printf("Spieler:  %s (Datei vorhanden: %v)\n", save.PlayerUUID, save.PlayerFile)
	}
	refuse, warn := CheckSave(save, m.MinecraftVersion)
	if refuse != nil {
		return fail("%v", refuse)
	}
	if worldOpen(save.Dir) {
		return fail("„%s“ ist gerade in Minecraft geöffnet – erst die Welt verlassen", save.Folder)
	}
	if warn != "" {
		if !*noPlayer {
			return fail("%s\nMit -ohne-spielerdatei trotzdem übernehmen.", warn)
		}
		fmt.Println("Warnung: " + warn)
	}
	if !*yes {
		fmt.Printf("Prüfung bestanden. „%s“ würde zur Gruppenwelt (gespielt in %s); die bisherige bleibt als Archiv.\nZum Übernehmen mit -ja aufrufen.\n", save.Folder, m.MinecraftVersion)
		return 3
	}
	if !owner {
		body, _ := json.Marshal(importRequest{Dir: save.Dir, AllowMissingPlayer: *noPlayer})
		err := sendToRunningBody(actionImportCLI, body)
		if errors.Is(err, errUnknownAction) {
			// Eine ältere laufende Instanz kennt die Übergabe unter dem alten Namen.
			err = sendToRunningBody(actionImportOld, body)
		}
		if err != nil {
			return fail("%v", err)
		}
		fmt.Println("Übergeben: Die laufende wanderwelt-Instanz (Tray) übernimmt die Welt jetzt und zeigt Fortschritt und Ergebnis an.")
		return 0
	}
	gen, err := importWorld(ctx, c, st.MachineID, save, m.MinecraftVersion, worldsync.LoadHashCache(), func(s string) {
		fmt.Println("… " + s)
	})
	if err != nil {
		return fail("%s", explain(err))
	}
	fmt.Printf("Fertig: „%s“ ist jetzt die Gruppenwelt (Stand %s/%d).\n", save.Folder, gen.WorldID, gen.Number)
	return 0
}
