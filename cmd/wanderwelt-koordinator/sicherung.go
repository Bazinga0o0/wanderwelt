package main

// „sicherung <ziel>“: Sicherung vor einem Update, Blobs per Hardlink (siehe
// Paket sicherung). Läuft nur bei gestopptem Dienst oder in der Wartung
// (bereit für das Update), damit Metadaten und Blobs zusammenpassen.

import (
	"cmp"
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/sicherung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

const sicherungUsage = `  wanderwelt-koordinator sicherung <ziel> [--config PATH]
      sichert data_dir nach <ziel> (muss auf demselben Dateisystem liegen): Blobs als Hardlink (kein
      zusätzlicher Platz), Metadaten und Konfiguration als Kopie; nur bei gestopptem Dienst oder in der
      Wartung, wenn „wartung start“ „bereit“ gemeldet hat. Danach steht
      Zeit und Ziel in data_dir/sicherung.json (die Gesundheit der Admins warnt bei einer Sicherung
      über 7 Tage). Rückweg: Dienst stoppen, altes Programm
      zurück, data_dir durch die Sicherung ersetzen
`

// dienstLaeuft meldet, ob unter listen schon ein Koordinator lauscht (in
// Tests ersetzbar).
var dienstLaeuft = func(listen string) bool {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

func sicherungMain(args []string, env wartungEnv, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sicherung", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	// Das Ziel darf vor oder nach den Schaltern stehen.
	var ziel string
	rest := args
	if len(rest) > 0 && len(rest[0]) > 0 && rest[0][0] != '-' {
		ziel, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if ziel == "" && fs.NArg() == 1 {
		ziel = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprint(stderr, "sicherung: genau ein Ziel angeben\nAufruf:\n"+sicherungUsage)
		return 2
	}
	if ziel == "" {
		fmt.Fprint(stderr, "sicherung: Ziel fehlt\nAufruf:\n"+sicherungUsage)
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "sicherung:", err)
		return 1
	}
	if cfg.DataDir == "" {
		fmt.Fprintf(stderr, "sicherung: %s: data_dir fehlt\n", *cfgPath)
		return 1
	}
	if dienstLaeuft(cmp.Or(cfg.Listen, defaultListen)) {
		if msg := nichtBereit(cfg.DataDir, env.now()); msg != "" {
			fmt.Fprintln(stderr, "sicherung:", msg)
			return 1
		}
	}
	res, err := sicherung.Anlegen(cfg.DataDir, ziel)
	if err != nil {
		fmt.Fprintln(stderr, "sicherung:", err)
		return 1
	}
	// Konfiguration und Manifest liegen meist außerhalb von data_dir.
	for _, f := range []struct{ src, name string }{{*cfgPath, "koordinator.yaml"}, {cfg.ManifestFile, "manifest.yaml"}} {
		if f.src == "" {
			continue
		}
		if err := sicherung.Datei(f.src, filepath.Join(ziel, "konfiguration", f.name)); err != nil {
			fmt.Fprintf(stderr, "sicherung: %s nicht gesichert: %v\n", f.src, err)
			return 1
		}
	}
	// Marke für die Gesundheit (GET /v1/health): Zeit und Ziel der letzten Sicherung.
	// Scheitert sie, ist die Sicherung trotzdem gelungen.
	if err := sicherung.MarkeSchreiben(cfg.DataDir, sicherung.Marke{Zeit: env.now().UTC(), Ziel: absOr(ziel)}); err != nil {
		fmt.Fprintf(stderr, "sicherung: %s nicht geschrieben (die Gesundheit zeigt die Sicherung als unbekannt): %v\n", sicherung.MarkeName, err)
	}
	fmt.Fprintf(stdout, "Gesichert nach %s: %d Blobs als Hardlink (kein zusätzlicher Platz), %d Dateien kopiert (%d Bytes); "+
		"Konfiguration in %s.\n", ziel, res.Verlinkt, res.Kopiert, res.KopiertByte, filepath.Join(ziel, "konfiguration"))
	return 0
}

// nichtBereit erklärt, warum bei laufendem Dienst nicht gesichert werden
// darf ("" = darf: Wartung läuft und der Bericht meldet „bereit“).
func nichtBereit(dataDir string, now time.Time) string {
	z, err := wartung.Laden(dataDir)
	if err != nil {
		return err.Error()
	}
	if !z.Aktiv {
		return "Der Koordinator läuft. Erst „wartung start“ (und auf „bereit“ warten) oder den Dienst stoppen."
	}
	b, ok, err := wartung.BerichtLesen(dataDir)
	switch {
	case err != nil:
		return err.Error()
	case !ok || now.Sub(b.Time) > berichtStale:
		return "Kein aktueller Bericht des Koordinators – „wartung status“ prüfen."
	case !b.Bereit:
		return "Die Wartung ist noch nicht bereit: " + cmpHaengt(b) + "."
	}
	return ""
}

// absOr liefert den absoluten Pfad von p, bei einem Fehler p selbst.
func absOr(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
