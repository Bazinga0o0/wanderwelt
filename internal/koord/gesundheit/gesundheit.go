// Package gesundheit rechnet die Warnungen und die Ampel aus den Werten, die
// GET /v1/health sammelt. Die Berechnung ist rein (keine Uhr, kein
// Dateisystem): Der Server gibt alle Werte hinein, Tests geben Fake-Werte.
package gesundheit

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Schwellen der Warnungen.
const (
	// FreiAnteilWarnung: Unter diesem Anteil freien Platzes am Dateisystem
	// (also strikt kleiner) gibt es eine Warnung.
	FreiAnteilWarnung = 0.15
	// FreiBytesFehler: Unter dieser Zahl freier Bytes (strikt kleiner) gibt
	// es einen Fehler (5 GB; GB = 2^30 Bytes wie in der Anzeige des Fensters).
	FreiBytesFehler int64 = 5 << 30
	// SicherungMaxAlter: Eine ältere letzte Sicherung (strikt älter) warnt.
	SicherungMaxAlter = 7 * 24 * time.Hour
	// HostBeatWarnung: Ein Host, der seit länger (strikt) kein Lebenszeichen
	// schickte, obwohl der Zuschlag gilt, ist eine Warnung (der Server meldet
	// ihn dann auch nicht mehr als bereit; ein Zuschlag verfällt nach
	// lease.LeaseTTL = 90 s ohne Lebenszeichen).
	HostBeatWarnung = 25 * time.Second
	// HostBeatFehler: … und länger als dies ein Fehler (der Zuschlag hätte
	// längst verfallen müssen: der Takt des Koordinators hängt oder die Uhr
	// ging).
	HostBeatFehler = 2 * time.Minute
)

// Starter ist ein zuletzt gesehener PC mit seiner Starter-Version.
type Starter struct {
	Name    string // Minecraft-Name, "" unbekannt
	Version string
}

// Host ist der aktuelle Zuschlag.
type Host struct {
	Name     string
	LastBeat time.Time
}

// Eingabe sind die Werte, aus denen die Warnungen entstehen.
type Eingabe struct {
	Now time.Time

	// Speicherplatz: FreiBekannt false, wenn nicht ermittelbar; Gesamt 0:
	// unbekannt (dann gilt nur die Byte-Schwelle).
	FreiBekannt  bool
	Frei, Gesamt int64

	// Sicherung: Zeit der letzten (SicherungBekannt false: unbekannt).
	SicherungBekannt bool
	Sicherung        time.Time

	// ManifestStarter: Starter-Version im Manifest ("" ohne); Starters: die
	// zuletzt gesehenen PCs.
	ManifestStarter string
	Starters        []Starter

	OffeneBeitritte int

	// Host: der aktuelle Zuschlag, nil ohne.
	Host *Host

	// AufraeumenFehler: der letzte Aufräumlauf scheiterte (Text, "" sonst);
	// UeberBudget: der Speicher liegt trotz Aufräumen über dem Budget.
	AufraeumenFehler string
	UeberBudget      bool

	// KarteFehler: der letzte Lauf der Kartenerzeugung scheiterte (Text, ""
	// sonst).
	KarteFehler string
}

// Warnungen berechnet die Warnungen, nach Stufe sortiert (Fehler zuerst),
// innerhalb einer Stufe in der Reihenfolge der Prüfungen.
func Warnungen(in Eingabe) []api.HealthWarning {
	w := []api.HealthWarning{}
	add := func(level, format string, args ...any) {
		w = append(w, api.HealthWarning{Level: level, Text: fmt.Sprintf(format, args...)})
	}

	// Speicherplatz.
	if in.FreiBekannt {
		switch {
		case in.Frei < FreiBytesFehler:
			add(api.WarnFehler, "Nur noch %s Speicherplatz frei – der Server braucht mehr Platz, sonst scheitern Uploads.", Bytes(in.Frei))
		case in.Gesamt > 0 && float64(in.Frei) < FreiAnteilWarnung*float64(in.Gesamt):
			add(api.WarnWarnung, "Nur noch %.0f %% des Speicherplatzes frei (%s von %s).",
				100*float64(in.Frei)/float64(in.Gesamt), Bytes(in.Frei), Bytes(in.Gesamt))
		}
	}
	if in.UeberBudget {
		add(api.WarnWarnung, "Der Speicher der Stände liegt trotz Aufräumen über dem Budget.")
	}
	if in.AufraeumenFehler != "" {
		add(api.WarnFehler, "Das letzte Aufräumen des Speichers ist fehlgeschlagen: %s", in.AufraeumenFehler)
	}

	if in.KarteFehler != "" {
		add(api.WarnWarnung, "Die Karte konnte zuletzt nicht erzeugt werden (die bisherige wird weiter ausgeliefert): %s", in.KarteFehler)
	}

	// Sicherung.
	switch {
	case !in.SicherungBekannt:
		add(api.WarnWarnung, "Letzte Sicherung unbekannt – „wanderwelt-koordinator sicherung“ hat noch keine Marke geschrieben.")
	case in.Now.Sub(in.Sicherung) > SicherungMaxAlter:
		add(api.WarnWarnung, "Die letzte Sicherung ist %d Tage alt (älter als 7 Tage).", int(in.Now.Sub(in.Sicherung)/(24*time.Hour)))
	}

	// Host.
	if h := in.Host; h != nil {
		wer := cmp.Or(h.Name, "ein PC")
		switch ohne := in.Now.Sub(h.LastBeat); {
		case ohne > HostBeatFehler:
			add(api.WarnFehler, "%s hält den Zuschlag, hat aber seit %s kein Lebenszeichen gesendet.", wer, Dauer(ohne))
		case ohne > HostBeatWarnung:
			add(api.WarnWarnung, "%s (Host) hat seit %s kein Lebenszeichen gesendet – der Zuschlag verfällt gleich.", wer, Dauer(ohne))
		}
	}

	// Starter-Versionen.
	if mv, ok := api.ReleaseVersion(in.ManifestStarter); ok {
		var alt []string
		for _, s := range in.Starters {
			if v, ok := api.ReleaseVersion(s.Version); ok && slices.Compare(v[:], mv[:]) < 0 {
				alt = append(alt, fmt.Sprintf("%s (%s)", cmp.Or(s.Name, "ein PC"), strings.TrimPrefix(s.Version, "v")))
			}
		}
		if len(alt) > 0 {
			add(api.WarnInfo, "Starter älter als die Version im Manifest (%s): %s.", strings.TrimPrefix(in.ManifestStarter, "v"), strings.Join(alt, ", "))
		}
	}

	// Beitrittsanfragen.
	switch {
	case in.OffeneBeitritte == 1:
		add(api.WarnInfo, "Eine offene Beitrittsanfrage wartet auf eine Entscheidung.")
	case in.OffeneBeitritte > 1:
		add(api.WarnInfo, "%d offene Beitrittsanfragen warten auf eine Entscheidung.", in.OffeneBeitritte)
	}

	rank := map[string]int{api.WarnFehler: 0, api.WarnWarnung: 1, api.WarnInfo: 2}
	slices.SortStableFunc(w, func(a, b api.HealthWarning) int { return rank[a.Level] - rank[b.Level] })
	return w
}

// Ampel liefert die Gesamtstufe: Fehler, sonst Warnung, sonst OK (info zählt
// als OK).
func Ampel(w []api.HealthWarning) string {
	status := api.HealthOK
	for _, x := range w {
		switch x.Level {
		case api.WarnFehler:
			return api.HealthFehler
		case api.WarnWarnung:
			status = api.HealthWarnung
		}
	}
	return status
}

// Bytes schreibt eine Größe lesbar (Zweierpotenzen, „GB“ wie im Explorer und im
// Fenster).
func Bytes(n int64) string {
	switch {
	case n >= 1<<40:
		return komma(float64(n)/(1<<40)) + " TB"
	case n >= 1<<30:
		return komma(float64(n)/(1<<30)) + " GB"
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", max(n, 0)>>10)
}

// Dauer schreibt eine Zeitspanne knapp auf Deutsch („45 s“, „3 Min.“).
func Dauer(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	case d < 90*time.Minute:
		return fmt.Sprintf("%d Min.", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("%d Std.", int(d.Round(time.Hour)/time.Hour))
}

// komma schreibt f mit einer Nachkommastelle und Komma („12,3“).
func komma(f float64) string { return strings.Replace(fmt.Sprintf("%.1f", f), ".", ",", 1) }
