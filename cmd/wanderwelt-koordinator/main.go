// Command wanderwelt-koordinator ist der Koordinator auf dem Mini-Server:
// Zuschlag, Wahl, Speicher für Stände und Manifest hinter tunneld.
//
//	wanderwelt-koordinator serve --config PATH
//	wanderwelt-koordinator world activate --config PATH --world ID
//	wanderwelt-koordinator mitglied neu|entfernen|liste … (mitglied.go)
//	wanderwelt-koordinator wartung start|ende|status … (wartung.go)
//	wanderwelt-koordinator sicherung <ziel> (sicherung.go)
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

const (
	defaultConfig = "/etc/wanderwelt/koordinator.yaml"
	defaultListen = "127.0.0.1:8470"

	expireEvery = 5 * time.Second
	pruneEvery  = time.Hour
)

// version wird beim Bauen gesetzt (-ldflags "-X main.version=…"); GET /v1/health
// meldet sie.
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})))
	args := os.Args[1:]
	switch {
	case len(args) >= 1 && args[0] == "serve":
		os.Exit(serve(args[1:]))
	case len(args) >= 2 && args[0] == "world" && args[1] == "activate":
		os.Exit(worldActivate(args[2:]))
	case len(args) >= 1 && args[0] == "mitglied":
		os.Exit(mitglied(args[1:], execRunner{}, os.Stdout, os.Stderr))
	case len(args) >= 1 && args[0] == "wartung":
		os.Exit(wartungMain(args[1:], realWartungEnv(), os.Stdout, os.Stderr))
	case len(args) >= 1 && args[0] == "sicherung":
		os.Exit(sicherungMain(args[1:], realWartungEnv(), os.Stdout, os.Stderr))
	case len(args) >= 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help"):
		usage()
		os.Exit(0)
	default:
		usage()
		os.Exit(2)
	}
}

// logLevel ist Info; WANDERWELT_DEBUG=1 schaltet Debug ein (dort stehen etwa
// Fehler beim Senden an Discord, nie die Webhook-Adresse).
func logLevel() slog.Level {
	if os.Getenv("WANDERWELT_DEBUG") == "1" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func usage() {
	fmt.Fprint(os.Stderr, `Aufruf:
  wanderwelt-koordinator serve --config PATH
      startet den Koordinator
  wanderwelt-koordinator world activate --config PATH --world ID
      macht eine archivierte Welt wieder zur Gruppenwelt (Koordinator vorher stoppen)
`+mitgliedUsage+wartungUsage+sicherungUsage)
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := run(*cfgPath); err != nil {
		slog.Error("Koordinator beendet", "err", err)
		return 1
	}
	return 0
}

func run(cfgPath string) error {
	// SIGHUP (systemctl reload, ausgelöst von „mitglied neu|entfernen“)
	// liest die Mitgliederliste und das Versions-Manifest neu. Abgefangen
	// wird es gleich zu Beginn: Ohne Handler beendet SIGHUP den Prozess, und
	// systemd wertet ein Ende durch SIGHUP als sauber (Restart=on-failure
	// startet dann nicht neu). Ein Neuladen während des Starts bleibt im
	// Puffer (eins genügt, es liest ohnehin die aktuellen Dateien) und
	// kommt, sobald der Server steht.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.DataDir == "" {
		return fmt.Errorf("%s: data_dir fehlt", cfgPath)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	// Artefakte, die der Koordinator selbst ausliefert (wanderwelt:<name>),
	// legt der Admin hier ab.
	if err := os.MkdirAll(server.ArtifactDir(cfg.DataDir), 0o750); err != nil {
		return err
	}
	// Das Manifest einmal lesen; ein kaputtes verhindert den Start. Geprüft
	// (Artefakte, starter) wird es, sobald der Server steht (UseManifest).
	var manifest *api.Manifest
	if cfg.ManifestFile == "" {
		slog.Warn("manifest_file fehlt: /v1/manifest antwortet 503")
	} else if manifest, err = config.LoadManifest(cfg.ManifestFile); err != nil {
		return err
	}

	lm, err := lease.New(filepath.Join(cfg.DataDir, "lease.json"), server.SystemClock{})
	if err != nil {
		return err
	}
	st, err := store.Open(storeDir(cfg), cfg.StorageBudget)
	if err != nil {
		return err
	}
	ph, err := portHolder(cfg)
	if err != nil {
		return err
	}
	uuids := server.NewMojangResolver(filepath.Join(cfg.DataDir, "uuids.json"))
	srv := server.New(cfg, lm, st, ph, uuids)
	srv.SetVersion(version)
	if manifest != nil {
		srv.UseManifest(manifest)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				srv.ReloadMembers(ctx, cfgPath)
				srv.ReloadManifest()
				srv.ReloadWartung()
			}
		}
	}()

	listen := cmp.Or(cfg.Listen, defaultListen)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Keine Gesamtzeit für Lesen/Schreiben: Blob-Teile und Downloads
		// dürfen auf langsamen Leitungen lange laufen.
	}

	// Ablauf: sofort einmal (ein Zuschlag kann während der Pause abgelaufen
	// sein), dann alle 5 s. Aufräumen nie beim Start, sondern stündlich:
	// Welche Blobs ein laufender Upload schon geschickt hat, weiß der
	// Speicher nur im Speicher.
	go srv.RunBenachrichtigung(ctx)
	go every(ctx, expireEvery, true, srv.ExpireTick)
	go every(ctx, pruneEvery, false, srv.PruneTick)
	go srv.WarmUUIDs(ctx)
	// Belegung des Speichers für die Stände-Liste gleich ausrechnen, ohne
	// aufzuräumen (das bleibt dem stündlichen Prune).
	go func() {
		if err := st.RefreshUsage(time.Now()); err != nil {
			slog.Error("Belegung des Speichers nicht ermittelt", "err", err)
		}
	}()

	slog.Info("Koordinator läuft", "version", version, "listen", ln.Addr().String(), "auth", cfg.Auth.Mode, "data_dir", cfg.DataDir)
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("Koordinator wird beendet")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Ein Aufräumen nach einem neuen Speicherbudget läuft zu Ende.
	srv.Wait()
	srv.FlushStats()
	return nil
}

// every ruft f im Takt d auf, bis ctx endet; mit now auch sofort einmal.
func every(ctx context.Context, d time.Duration, now bool, f func(context.Context)) {
	if now {
		f(ctx)
	}
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f(ctx)
		}
	}
}

// portHolder baut den Client der tunneld-Dienst-Schnittstelle; ohne
// port_holder.api gibt es keinen (nur für den lokalen Betrieb).
func portHolder(cfg *config.Config) (portholder.Client, error) {
	if cfg.PortHolder.API == "" {
		slog.Warn("port_holder.api fehlt: kein Port-Halter, die harte Sperre gegen zwei Hosts fehlt")
		return portholder.Noop(), nil
	}
	if cfg.PortHolder.Port <= 0 || cfg.PortHolder.Port > 65535 {
		return nil, fmt.Errorf("port_holder.port %d ist kein gültiger Port", cfg.PortHolder.Port)
	}
	if cfg.PortHolder.SecretFile == "" {
		return nil, errors.New("port_holder.secret_file fehlt")
	}
	data, err := os.ReadFile(cfg.PortHolder.SecretFile)
	if err != nil {
		return nil, fmt.Errorf("port_holder.secret_file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return nil, fmt.Errorf("port_holder.secret_file %s ist leer", cfg.PortHolder.SecretFile)
	}
	return portholder.New(cfg.PortHolder.API, secret), nil
}

func storeDir(cfg *config.Config) string { return filepath.Join(cfg.DataDir, "store") }

func worldActivate(args []string) int {
	fs := flag.NewFlagSet("world activate", flag.ContinueOnError)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	world := fs.String("world", "", "Weltkennung (Archiv), die für ihre Welt wieder gelten soll")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *world == "" {
		fmt.Fprintln(os.Stderr, "world activate: --world fehlt")
		return 2
	}
	if err := activate(*cfgPath, *world); err != nil {
		fmt.Fprintln(os.Stderr, "world activate:", err)
		return 1
	}
	fmt.Printf("Die Weltkennung %s gilt jetzt wieder für ihre Welt.\n", *world)
	return 0
}

func activate(cfgPath, world string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if cfg.DataDir == "" {
		return fmt.Errorf("%s: data_dir fehlt", cfgPath)
	}
	// Der Speicher darf nur von einem Prozess benutzt werden. Ist die
	// Adresse des Koordinators belegt, läuft er vermutlich noch.
	listen := cmp.Or(cfg.Listen, defaultListen)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("%s ist belegt – läuft der Koordinator noch? Erst stoppen: %w", listen, err)
	}
	ln.Close()
	st, err := store.Open(storeDir(cfg), cfg.StorageBudget)
	if err != nil {
		return err
	}
	return st.Activate(world)
}
