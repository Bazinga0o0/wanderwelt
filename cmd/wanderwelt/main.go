// Command wanderwelt ist der Starter für Windows.
//
//	wanderwelt.exe            nicht eingerichtet: Installation; sonst Tray
//	                          (eine neuere Version ersetzt die installierte Kopie)
//	wanderwelt.exe play       „Spielen“ in der laufenden Instanz (startet sie, wenn nötig)
//	wanderwelt.exe fenster    das wanderwelt-Fenster (startet die Instanz, wenn nötig)
//	wanderwelt.exe import [-ja] [-ohne-spielerdatei] <weltordner>
//	                          Einzelspieler-Welt zur Gruppenwelt machen (wie „Welt übernehmen …“)
//	wanderwelt.exe discord-<ID>://…   wie ohne Argument (Discord startet so für „Beitreten“)
//	wanderwelt.exe version
//
// Versteckte Prüfhilfen: deps-check (Abhängigkeiten laden), winsys-check
// (Herunterfahren-Abfangen mit synthetischen Nachrichten).
//
// Gebaut wird mit -ldflags -H=windowsgui (kein Konsolenfenster für den
// Tray); die Unterbefehle hängen sich an die Konsole des Aufrufers.
package main

// Symbol-Ressource (rsrc_windows_amd64.syso) und wanderwelt.ico neu erzeugen.
//go:generate go run ../../tools/makeicon

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/app"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fenster"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// version wird beim Bauen gesetzt (-ldflags "-X main.version=…").
var version = "dev"

// gruppeEingebettet sind die Gruppen-Adressen eines Gruppen-Builds: Base64
// eines YAML mit coordinator, tunnel_server und web_url (optional
// token_url), per -ldflags "-X main.gruppeEingebettet=..." gesetzt. Leer im
// Repo: Die Einrichtung fragt dann nach einer Gruppendatei.
var gruppeEingebettet string

func main() {
	args := os.Args[1:]
	handoff, updated := false, false
flags:
	for len(args) > 0 {
		switch args[0] {
		case app.HandoffFlag:
			handoff = true
		case app.UpdatedFlag:
			updated = true
		default:
			break flags
		}
		args = args[1:]
	}
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
		if strings.HasPrefix(cmd, "discord-") { // Discord startet uns über discord-<ID>://… („Beitreten“)
			cmd = ""
		}
	}
	switch cmd {
	case "", "run":
		os.Exit(app.Run(app.Options{Version: version, Handoff: handoff, Updated: updated, Invite: gruppeEingebettet}))
	case "play":
		os.Exit(app.Run(app.Options{Version: version, Play: true, Handoff: handoff, Updated: updated, Invite: gruppeEingebettet}))
	case "fenster":
		os.Exit(fensterCmd())
	}

	winsys.AttachParentConsole()
	// Gios init (über das Fenster mitgelinkt) leitet das Standard-Log eines
	// GUI-Programms ohne stderr nach OutputDebugString um und nimmt die
	// Zeitstempel weg; die Unterbefehle schreiben wieder auf die Konsole.
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)
	switch cmd {
	case "import":
		os.Exit(app.ImportCLI(args[1:]))
	case "version", "-version", "--version":
		fmt.Println("wanderwelt", version)
	case "deps-check":
		os.Exit(depsCheck(args[1:]))
	case "winsys-check":
		os.Exit(winsysCheck())
	case "help", "-h", "--help", "/?":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Aufruf:
  wanderwelt                 einrichten bzw. im Infobereich (Tray) starten
  wanderwelt play            „Spielen“ (in der laufenden Instanz)
  wanderwelt fenster         das wanderwelt-Fenster öffnen
  wanderwelt import [-ja] [-ohne-spielerdatei] <weltordner>
                             Einzelspieler-Welt zur Gruppenwelt machen (nur Admin);
                             ohne -ja wird nur geprüft
  wanderwelt version
`)
}

// fensterCmd öffnet das wanderwelt-Fenster (eigener Prozess, vom Tray
// gestartet); läuft keine Instanz, startet das Fenster sie.
func fensterCmd() int {
	exe, err := os.Executable()
	if err == nil {
		err = fenster.Run(context.Background(), exe, version)
	}
	if err != nil {
		zenity.Error("Das wanderwelt-Fenster lässt sich nicht öffnen:\n\n"+err.Error(),
			zenity.Title("wanderwelt"), zenity.ErrorIcon)
		return 1
	}
	return 0
}

// winsysCheck prüft das Abfangen des Herunterfahrens mit synthetischen
// Nachrichten an das eigene versteckte Fenster (Prüfhilfe, nicht in der
// Hilfe aufgeführt). Echtes Herunterfahren lässt sich so nicht ersetzen.
func winsysCheck() int {
	var hosting atomic.Bool
	hosting.Store(true)
	finished := make(chan struct{})
	ended := make(chan bool, 4)
	err := winsys.OnShutdown(winsys.ShutdownHooks{
		Hosting: hosting.Load,
		Finish: func() {
			fmt.Println("  Finish: Hosten wird beendet (Prüfung: 3 s) …")
			time.Sleep(3 * time.Second)
			fmt.Println("  Finish: fertig")
			close(finished)
		},
		Ended: func(ending bool) { ended <- ending },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "winsys-check:", err)
		return 1
	}
	ok := true
	check := func(cond bool, format string, a ...any) {
		mark := "ok  "
		if !cond {
			mark, ok = "FEHL", false
		}
		fmt.Println(mark, fmt.Sprintf(format, a...))
	}

	fmt.Println("1) WM_QUERYENDSESSION, während gehostet wird")
	allow, err := winsys.SimulateQueryEndSession()
	check(err == nil && !allow, "Antwort FALSE (Herunterfahren aufgehalten): allow=%v err=%v", allow, err)
	reason, has := winsys.ActiveBlockReason()
	check(has && reason == winsys.BlockReason, "ShutdownBlockReasonQuery: %q (gesetzt: %v)", reason, has)
	allow, _ = winsys.SimulateQueryEndSession()
	check(!allow, "zweites WM_QUERYENDSESSION während des Aufhaltens: FALSE")
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		check(false, "Finish nicht aufgerufen")
	}
	time.Sleep(500 * time.Millisecond)
	_, has = winsys.ActiveBlockReason()
	check(!has, "nach Finish: Grund entfernt (ShutdownBlockReasonDestroy)")

	fmt.Println("2) WM_QUERYENDSESSION ohne Hosten")
	hosting.Store(false)
	allow, err = winsys.SimulateQueryEndSession()
	check(err == nil && allow, "Antwort TRUE: allow=%v err=%v", allow, err)
	_, has = winsys.ActiveBlockReason()
	check(!has, "kein Grund gesetzt")

	fmt.Println("3) WM_ENDSESSION")
	winsys.SimulateEndSession(false)
	winsys.SimulateEndSession(true)
	e1, e2 := <-ended, <-ended
	check(!e1 && e2, "Ended(false), Ended(true) gemeldet: %v, %v", e1, e2)

	if !ok {
		return 1
	}
	fmt.Println("alles ok")
	return 0
}

// depsCheck lädt alles aus einem Manifest nach <Home>/deps und meldet die
// Pfade (Prüfhilfe, nicht in der Hilfe aufgeführt). Einträge wanderwelt:<name>
// kommen vom Koordinator der eingerichteten Gruppendatei.
func depsCheck(args []string) int {
	fset := flag.NewFlagSet("deps-check", flag.ContinueOnError)
	c2me := fset.Bool("c2me", true, "C2ME mitladen (wie Profil c2me: true)")
	fset.Usage = func() {
		fmt.Fprintln(fset.Output(), "Aufruf: wanderwelt deps-check [-c2me=false] <manifest.yaml>")
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if fset.NArg() != 1 {
		fset.Usage()
		return 2
	}
	m, err := config.LoadManifest(fset.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "deps-check:", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	last := map[string]int64{} // je Artefakt zuletzt gemeldete Bytes
	step := map[string]int64{} // je Artefakt nächste Ausgabe ab
	var loaded int64
	progress := func(name string, done, total int64) {
		loaded += done - last[name]
		last[name] = done
		switch {
		case done == 0:
			fmt.Printf("  lade %s (%s)\n", name, size(total))
			step[name] = 25
		case total > 0 && done*100/total >= step[name]:
			fmt.Printf("  %s: %d %%\n", name, done*100/total)
			step[name] += 25
		}
	}

	var src deps.ArtifactSource
	if g, err := group.LoadGroup(group.GroupPath()); err == nil {
		src = client.New(g)
	} else {
		fmt.Fprintf(os.Stderr, "deps-check: keine Gruppendatei (%v) – Einträge wanderwelt:<name> lassen sich nicht laden\n", err)
	}
	start := time.Now()
	p, err := deps.Ensure(ctx, m, api.Profile{C2ME: *c2me}, src, progress)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deps-check:", err)
		return 1
	}
	names := make([]string, 0, len(last))
	for n := range last {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("Java:          %s\n", p.Java)
	fmt.Printf("Fabric-Server: %s\n", p.FabricServerJar)
	fmt.Printf("tunnel:        %s\n", p.Tunnel)
	for _, mod := range p.Mods {
		fmt.Printf("Mod:           %s\n", mod)
	}
	fmt.Printf("heruntergeladen: %d Artefakte %v, %s, in %s\n",
		len(names), names, size(loaded), time.Since(start).Round(time.Millisecond))
	return 0
}

func size(n int64) string {
	if n < 0 {
		return "Größe unbekannt"
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
