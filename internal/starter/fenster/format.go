package fenster

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Texte für Zeiten und Größen, wie sie das Fenster zeigt.

var wochentage = [...]string{"So.", "Mo.", "Di.", "Mi.", "Do.", "Fr.", "Sa."}

// seit: „gerade eben“, „vor 2 Min.“, „vor 3 Std.“, „gestern“, „vor 4
// Tagen“, danach das Datum. Zeiten in der Zukunft (Uhren gehen nie ganz
// gleich) gelten als „gerade eben“.
func seit(t, jetzt time.Time) string {
	d := jetzt.Sub(t)
	switch {
	case d < time.Minute:
		return "gerade eben"
	case d < time.Hour:
		return fmt.Sprintf("vor %d Min.", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("vor %d Std.", int(d/time.Hour))
	}
	tage := kalenderTage(t, jetzt)
	switch {
	case tage <= 1:
		return "gestern"
	case tage < 30:
		return fmt.Sprintf("vor %d Tagen", tage)
	}
	return t.Local().Format("02.01.2006")
}

// bis: „gleich“, „in 3 Min.“, „in 2 Std.“ – für den nächsten Stand.
func bis(t, jetzt time.Time) string {
	d := t.Sub(jetzt)
	switch {
	case d < time.Minute:
		return "gleich"
	case d < time.Hour:
		// Aufrunden: Bei 2:30 Min. Rest ist „in 3 Min.“ ehrlicher als „in 2“.
		return fmt.Sprintf("in %d Min.", int((d+time.Minute-1)/time.Minute))
	}
	return fmt.Sprintf("in %d Std.", int(d/time.Hour))
}

// dauer: „5 Min.“, „2 Std.“, „30 s“.
func dauer(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d Min.", int(d/time.Minute))
	}
	return fmt.Sprintf("%d Std.", int(d/time.Hour))
}

// zeitpunkt: „heute 14:05“, „gestern 21:30“, „Mo., 18.11. 21:30“, aus einem
// anderen Jahr „18.11.2025 21:30“ (Ortszeit).
func zeitpunkt(t, jetzt time.Time) string {
	t, jetzt = t.Local(), jetzt.Local()
	uhr := t.Format("15:04")
	switch tage := kalenderTage(t, jetzt); {
	case tage == 0:
		return "heute " + uhr
	case tage == 1:
		return "gestern " + uhr
	case t.Year() == jetzt.Year():
		return wochentage[t.Weekday()] + ", " + t.Format("02.01.") + " " + uhr
	}
	return t.Format("02.01.2006") + " " + uhr
}

// kalenderTage zählt die Tageswechsel (Ortszeit) von t bis jetzt.
func kalenderTage(t, jetzt time.Time) int {
	t, jetzt = t.Local(), jetzt.Local()
	a := time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, time.Local)
	b := time.Date(jetzt.Year(), jetzt.Month(), jetzt.Day(), 12, 0, 0, 0, time.Local)
	return int((b.Sub(a) + 12*time.Hour) / (24 * time.Hour))
}

// groesse: „1,2 GB“, „350 MB“, „12 KB“ (Zweierpotenzen, wie der Explorer).
func groesse(n int64) string {
	switch {
	case n < 0:
		return "?"
	case n >= 1<<40:
		return komma(float64(n)/(1<<40)) + " TB"
	case n >= 1<<30:
		return komma(float64(n)/(1<<30)) + " GB"
	case n >= 1<<20:
		return strconv.FormatInt((n+1<<19)>>20, 10) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt((n+1<<9)>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

// komma: eine Nachkommastelle mit Komma; „,0“ fällt weg ab 10.
func komma(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if f >= 10 {
		s = strings.TrimSuffix(s, ".0")
	}
	return strings.Replace(s, ".", ",", 1)
}

// gb: MiB als „6 GB“ bzw. „5,5 GB“ (Arbeitsspeicher).
func gb(mb int) string {
	s := strconv.FormatFloat(float64(mb)/1024, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return strings.Replace(s, ".", ",", 1) + " GB"
}

// prozent liest einen Fortschritt wie „lädt die Welt: 45 %“ aus detail.
func prozent(detail string) (float32, bool) {
	i := strings.LastIndex(detail, "%")
	if i < 0 {
		return 0, false
	}
	s := strings.TrimRight(detail[:i], "   ")
	j := len(s)
	for j > 0 && s[j-1] >= '0' && s[j-1] <= '9' {
		j--
	}
	if j == len(s) {
		return 0, false
	}
	p, err := strconv.Atoi(s[j:])
	if err != nil || p > 100 {
		return 0, false
	}
	return float32(p) / 100, true
}

// gross: erster Buchstabe groß („bereit“ → „Bereit“).
func gross(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || unicode.IsUpper(r) {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// liste: „A, B und C“.
func liste(namen []string) string {
	switch len(namen) {
	case 0:
		return ""
	case 1:
		return namen[0]
	}
	return strings.Join(namen[:len(namen)-1], ", ") + " und " + namen[len(namen)-1]
}

// kurzID kürzt eine Welt-Kennung für die Anzeige.
func kurzID(id string) string {
	if utf8.RuneCountInString(id) <= 12 {
		return id
	}
	r := []rune(id)
	return string(r[:8]) + "…"
}

// gbEingabe liest eine Eingabe wie „20“, „12,5“ oder „12.5“ als Bytes
// (GiB).
func gbEingabe(s string) (int64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "GB"))
	s = strings.Replace(s, ",", ".", 1)
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 || f > 1<<20 {
		return 0, fmt.Errorf("bitte eine Zahl in GB eingeben, etwa 20")
	}
	return int64(f * (1 << 30)), nil
}

// merkerFrist: So lange hält ein merker einen gespeicherten Wert höchstens
// fest.
const merkerFrist = 10 * time.Second

// merker hält einen gerade gespeicherten Wert fest, bis der Schnappschuss
// ihn zeigt (die Abfrage kommt erst nach der Aktion), eine neue Instanz
// verbunden ist oder merkerFrist um ist – damit Formular und Schalter nach
// dem Speichern nicht kurz auf den alten Stand zurückspringen.
type merker[T comparable] struct {
	wert  T
	aktiv bool
	bis   time.Time
	runde uint64
}

func (m *merker[T]) setze(v T, jetzt time.Time, runde uint64) {
	*m = merker[T]{wert: v, aktiv: true, bis: jetzt.Add(merkerFrist), runde: runde}
}

// gilt liefert den gemerkten Wert, solange ist ihn noch nicht zeigt; sonst
// ist (und der merker ist erledigt).
func (m *merker[T]) gilt(ist T, jetzt time.Time, runde uint64) (T, bool) {
	if !m.aktiv {
		return ist, false
	}
	if ist == m.wert || runde != m.runde || !jetzt.Before(m.bis) {
		m.aktiv = false
		return ist, false
	}
	return m.wert, true
}
