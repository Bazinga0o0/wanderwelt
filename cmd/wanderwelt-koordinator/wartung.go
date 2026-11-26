package main

// „wartung start|ende|status“: Hosten für ein Update serverseitig beenden.
//
// Der Befehl schreibt den Zustand nach data_dir/wartung.json (wartung.json
// im Paket wartung); der laufende Koordinator liest ihn im nächsten Takt
// (höchstens 5 s) oder bei SIGHUP, schickt ihn an die Starter und schreibt
// im Takt den Bericht, auf den „start“ wartet. Ohne laufenden Koordinator
// gilt die Wartung ab dessen nächstem Start.

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

const wartungUsage = `  wanderwelt-koordinator wartung start [--in 60s] [--grund TEXT] [--frist 5m] [--nicht-warten] [--config PATH]
      kündigt die Wartung an: Spielen wird abgelehnt, der Host sagt im Chat an und stoppt nach --in
      sauber (0 = sofort); danach wartet der Befehl bis zu --frist, bis kein Zuschlag und kein Upload
      mehr besteht, und meldet „bereit für das Update“ (Ausgang 3, wenn jemand hängt)
  wanderwelt-koordinator wartung ende [--config PATH]
      beendet die Wartung: Spielen ist wieder möglich, das Aufräumen löscht wieder
  wanderwelt-koordinator wartung status [--config PATH]
      zeigt Zustand und Bericht der Wartung
`

const (
	// berichtPoll: So oft liest „wartung start“ den Bericht.
	berichtPoll = 2 * time.Second
	// berichtStale: So lange darf der Bericht ausbleiben, bevor „start“
	// meldet, dass der Koordinator ihn nicht schreibt.
	berichtStale = 20 * time.Second
	// standardFrist: So lange wartet „start“ nach Ablauf der Ansage.
	standardFrist = 5 * time.Minute
	// standardIn: Ansage-Frist ohne --in.
	standardIn = 60 * time.Second
)

// wartungEnv: Uhr und Pause (in Tests von Hand gestellt).
type wartungEnv struct {
	now   func() time.Time
	sleep func(time.Duration)
}

func realWartungEnv() wartungEnv { return wartungEnv{now: time.Now, sleep: time.Sleep} }

func wartungMain(args []string, env wartungEnv, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "Aufruf:\n"+wartungUsage)
		return 2
	}
	switch args[0] {
	case "start":
		return wartungStart(args[1:], env, stdout, stderr)
	case "ende":
		return wartungEnde(args[1:], env, stdout, stderr)
	case "status":
		return wartungStatus(args[1:], env, stdout, stderr)
	}
	fmt.Fprintf(stderr, "wartung: unbekannter Unterbefehl %q\nAufruf:\n%s", args[0], wartungUsage)
	return 2
}

// dataDirOf liest data_dir aus der Konfiguration.
func dataDirOf(cfgPath string) (string, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return "", err
	}
	if cfg.DataDir == "" {
		return "", fmt.Errorf("%s: data_dir fehlt", cfgPath)
	}
	return cfg.DataDir, nil
}

func uhr(t time.Time) string { return t.Local().Format("15:04:05") }

func wartungStart(args []string, env wartungEnv, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wartung start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	in := fs.Duration("in", standardIn, "Frist bis zum Stopp des Hosts (0 = sofort)")
	grund := fs.String("grund", "", "Grund für Chat-Ansage und Hinweis (Standard: "+wartung.StandardGrund+")")
	frist := fs.Duration("frist", standardFrist, "so lange nach Ablauf der Ansage auf „bereit“ warten")
	nicht := fs.Bool("nicht-warten", false, "nur ankündigen, nicht auf „bereit“ warten")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := dataDirOf(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "wartung start:", err)
		return 1
	}
	old, err := wartung.Laden(dir)
	if err != nil {
		fmt.Fprintln(stderr, "wartung start:", err)
		return 1
	}
	saved := env.now()
	z, err := wartung.Starten(old, saved, *in, *grund)
	if err != nil {
		fmt.Fprintln(stderr, "wartung start:", err)
		return 2
	}
	if err := wartung.Speichern(dir, z); err != nil {
		fmt.Fprintln(stderr, "wartung start:", err)
		return 1
	}
	if old.Aktiv {
		fmt.Fprintln(stdout, "Hinweis: Es lief schon eine Wartung; Frist und Grund sind ersetzt.")
	}
	fmt.Fprintf(stdout, "Wartung angekündigt (%s): Spielen wird abgelehnt, der Host stoppt um %s.\n", z.Grund, uhr(z.StopAt))
	if *nicht {
		fmt.Fprintln(stdout, "Mit „wartung status“ nachsehen, mit „wartung ende“ beenden.")
		return 0
	}
	return wartungWarten(dir, z, saved, *frist, env, stdout, stderr)
}

// wartungWarten wartet, bis der Bericht „bereit“ meldet (0), die Frist nach
// z.StopAt um ist (3) oder – ohne Bericht – nichts weiterhilft.
func wartungWarten(dir string, z wartung.Zustand, saved time.Time, frist time.Duration, env wartungEnv, stdout, stderr io.Writer) int {
	until := z.StopAt.Add(frist)
	var last string
	warned := false
	for {
		now := env.now()
		b, ok, err := wartung.BerichtLesen(dir)
		fresh := err == nil && ok && b.Time.After(saved)
		switch {
		case fresh && b.Bereit:
			fmt.Fprintf(stdout, "Bereit für das Update (%s). Danach „wartung ende“ nicht vergessen.\n", uhr(now))
			return 0
		case fresh:
			warned = false
			if msg := b.Haengt(); msg != last {
				last = msg
				if msg == "" {
					msg = "die Frist läuft noch"
				}
				fmt.Fprintf(stdout, "%s – wartet: %s\n", uhr(now), msg)
			}
		case now.Sub(saved) > berichtStale && !warned:
			warned = true
			fmt.Fprintln(stderr, "Hinweis: Der Koordinator schreibt keinen Bericht – läuft er? "+
				"Ohne ihn gilt die Wartung erst ab dem nächsten Start (journalctl -u wanderwelt-koordinator).")
		}
		if !now.Before(until) {
			break
		}
		env.sleep(min(berichtPoll, until.Sub(now)))
	}
	if b, ok, _ := wartung.BerichtLesen(dir); ok && b.Time.After(saved) {
		fmt.Fprintf(stderr, "Nicht bereit nach %s Frist: %s.\n", frist, cmpHaengt(b))
	} else {
		fmt.Fprintf(stderr, "Nicht bereit nach %s Frist: kein aktueller Bericht des Koordinators.\n", frist)
	}
	fmt.Fprintln(stderr, "Später mit „wartung status“ erneut prüfen; der Host entzieht sich nach der Nachfrist von selbst.")
	return 3
}

func cmpHaengt(b wartung.Bericht) string {
	if h := b.Haengt(); h != "" {
		return h
	}
	return "die Ansage-Frist läuft noch"
}

func wartungEnde(args []string, env wartungEnv, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wartung ende", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := dataDirOf(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "wartung ende:", err)
		return 1
	}
	z, err := wartung.Laden(dir)
	if err != nil {
		fmt.Fprintln(stderr, "wartung ende:", err)
		return 1
	}
	if !z.Aktiv {
		fmt.Fprintln(stdout, "Es läuft keine Wartung.")
		return 0
	}
	if err := wartung.Speichern(dir, wartung.Zustand{}); err != nil {
		fmt.Fprintln(stderr, "wartung ende:", err)
		return 1
	}
	wartung.BerichtLoeschen(dir)
	fmt.Fprintf(stdout, "Wartung beendet (dauerte seit %s). Der Koordinator übernimmt das im nächsten Takt: "+
		"Spielen ist wieder möglich, das Aufräumen löscht wieder.\n", uhr(z.Since))
	return 0
}

func wartungStatus(args []string, env wartungEnv, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("wartung status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir, err := dataDirOf(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "wartung status:", err)
		return 1
	}
	z, err := wartung.Laden(dir)
	if err != nil {
		fmt.Fprintln(stderr, "wartung status:", err)
		return 1
	}
	now := env.now()
	switch z.Phase(now) {
	case wartung.Aus:
		fmt.Fprintln(stdout, "Keine Wartung.")
		return 0
	case wartung.Ansage:
		fmt.Fprintf(stdout, "Wartung angekündigt seit %s (%s): der Host stoppt um %s (in %s).\n",
			uhr(z.Since), z.Grund, uhr(z.StopAt), z.Rest(now))
	default:
		fmt.Fprintf(stdout, "Wartung seit %s (%s): der Host sollte seit %s gestoppt sein.\n", uhr(z.Since), z.Grund, uhr(z.StopAt))
	}
	b, ok, err := wartung.BerichtLesen(dir)
	switch {
	case err != nil:
		fmt.Fprintln(stderr, "Bericht:", err)
	case !ok || now.Sub(b.Time) > berichtStale:
		fmt.Fprintln(stdout, "Kein aktueller Bericht des Koordinators – läuft er?")
	case b.Bereit:
		fmt.Fprintf(stdout, "Bereit für das Update (Bericht von %s).\n", uhr(b.Time))
	default:
		fmt.Fprintf(stdout, "Noch nicht bereit: %s.\n", cmpHaengt(b))
	}
	return 0
}
