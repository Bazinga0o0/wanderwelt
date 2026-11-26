// Package wartung hält den Wartungszustand des Koordinators: „wartung
// start“ auf dem Server schreibt ihn nach data_dir/wartung.json, der
// laufende Koordinator liest ihn von dort (jeder Takt und SIGHUP), und ein
// Neustart findet ihn wieder. Der Bericht des Koordinators (wartung-
// bericht.json) sagt dem Befehl, ob noch jemand hängt.
//
// Zustandsmaschine (Phase): Aus → Ansage (Frist bis StopAt läuft; der Host
// sagt an, Spielen ist schon abgelehnt) → Gestoppt (der Host muss gestoppt
// sein; wer noch einen Zuschlag hält, wird nach einer Nachfrist entzogen)
// → Aus („wartung ende“).
package wartung

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DateiName ist der Zustand in data_dir; BerichtName der Bericht des
	// Koordinators daneben.
	DateiName   = "wartung.json"
	BerichtName = "wartung-bericht.json"
	// MaxGrund begrenzt den Grund (Chat-Ansage, Anzeige).
	MaxGrund = 200
	// StandardGrund gilt ohne --grund.
	StandardGrund = "Update des Koordinators"
)

// Phase ist die Stufe der Wartung zu einer Zeit.
type Phase int

const (
	Aus      Phase = iota // keine Wartung
	Ansage                // angekündigt, StopAt noch nicht erreicht
	Gestoppt              // Frist abgelaufen
)

func (p Phase) String() string {
	switch p {
	case Ansage:
		return "Ansage"
	case Gestoppt:
		return "Gestoppt"
	}
	return "Aus"
}

// Zustand ist der gespeicherte Wartungszustand. Der Nullwert heißt: keine
// Wartung.
type Zustand struct {
	Aktiv  bool      `json:"aktiv"`
	Grund  string    `json:"grund,omitempty"`
	Since  time.Time `json:"since"`
	StopAt time.Time `json:"stop_at"`
}

// Starten beginnt eine Wartung zur Zeit now mit der Frist in. Eine schon
// laufende Wartung wird mit der neuen Frist und dem neuen Grund ersetzt
// (Since bleibt). Eine negative Frist oder ein zu langer Grund ist ein
// Fehler; in 0 heißt: sofort.
func Starten(alt Zustand, now time.Time, in time.Duration, grund string) (Zustand, error) {
	if in < 0 {
		return Zustand{}, errors.New("die Frist darf nicht negativ sein")
	}
	grund = strings.TrimSpace(grund)
	if grund == "" {
		grund = StandardGrund
	}
	if n := len([]rune(grund)); n > MaxGrund {
		return Zustand{}, fmt.Errorf("der Grund ist zu lang (%d von höchstens %d Zeichen)", n, MaxGrund)
	}
	z := Zustand{Aktiv: true, Grund: grund, Since: now.UTC(), StopAt: now.Add(in).UTC()}
	if alt.Aktiv && !alt.Since.IsZero() {
		z.Since = alt.Since
	}
	return z, nil
}

// Phase liefert die Stufe zur Zeit now.
func (z Zustand) Phase(now time.Time) Phase {
	switch {
	case !z.Aktiv:
		return Aus
	case now.Before(z.StopAt):
		return Ansage
	}
	return Gestoppt
}

// Rest ist die Zeit bis StopAt (nie negativ), auf ganze Sekunden aufgerundet.
func (z Zustand) Rest(now time.Time) time.Duration {
	d := z.StopAt.Sub(now)
	if d <= 0 {
		return 0
	}
	return (d + time.Second - 1) / time.Second * time.Second
}

// Pfad liefert den Pfad des Zustands in dataDir.
func Pfad(dataDir string) string { return filepath.Join(dataDir, DateiName) }

// BerichtPfad liefert den Pfad des Berichts in dataDir.
func BerichtPfad(dataDir string) string { return filepath.Join(dataDir, BerichtName) }

// Laden liest den Zustand; ohne Datei ist er „aus“. Eine kaputte Datei ist
// ein Fehler (der Koordinator behält dann den bisherigen Zustand).
func Laden(dataDir string) (Zustand, error) {
	var z Zustand
	err := lesen(Pfad(dataDir), &z)
	if errors.Is(err, fs.ErrNotExist) {
		return Zustand{}, nil
	}
	if err != nil {
		return Zustand{}, fmt.Errorf("%s: %w", DateiName, err)
	}
	return z, nil
}

// Speichern schreibt den Zustand atomar; ein Zustand „aus“ löscht die Datei.
func Speichern(dataDir string, z Zustand) error {
	if !z.Aktiv {
		err := os.Remove(Pfad(dataDir))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return schreiben(Pfad(dataDir), z)
}

// Bericht ist, was der Koordinator im Takt über den Stand der Wartung
// schreibt: wer noch einen Zuschlag hält, wie viele Uploads offen sind.
type Bericht struct {
	Time time.Time `json:"time"`
	// Since: die Wartung, auf die er sich bezieht (Zustand.Since).
	Since time.Time `json:"since"`
	// Bereit: nach StopAt, kein Zuschlag, keine offenen Uploads.
	Bereit bool `json:"bereit"`
	// Zuschlag: Name des Hosts (Minecraft-Name, sonst „ein PC“); leer ohne.
	Zuschlag string `json:"zuschlag,omitempty"`
	// NurUpload: der Zuschlag dient nur dem Hochladen.
	NurUpload bool `json:"nur_upload,omitempty"`
	// Uploads: offene Blob-Uploads.
	Uploads int `json:"uploads,omitempty"`
	// Uebergabe: eine Host-Übergabe läuft noch.
	Uebergabe bool `json:"uebergabe,omitempty"`
}

// Haengt beschreibt auf Deutsch, wer die Bereitschaft noch hält.
func (b Bericht) Haengt() string {
	var t []string
	if b.Zuschlag != "" {
		if b.NurUpload {
			t = append(t, b.Zuschlag+" lädt noch einen Stand hoch")
		} else {
			t = append(t, b.Zuschlag+" hostet noch")
		}
	}
	if b.Uebergabe {
		t = append(t, "eine Host-Übergabe läuft noch")
	}
	if b.Uploads > 0 {
		t = append(t, fmt.Sprintf("%d Upload(s) offen", b.Uploads))
	}
	return strings.Join(t, ", ")
}

// BerichtSchreiben schreibt den Bericht atomar.
func BerichtSchreiben(dataDir string, b Bericht) error { return schreiben(BerichtPfad(dataDir), b) }

// BerichtLesen liest den Bericht; ohne Datei ist ok false.
func BerichtLesen(dataDir string) (b Bericht, ok bool, err error) {
	err = lesen(BerichtPfad(dataDir), &b)
	if errors.Is(err, fs.ErrNotExist) {
		return Bericht{}, false, nil
	}
	if err != nil {
		return Bericht{}, false, fmt.Errorf("%s: %w", BerichtName, err)
	}
	return b, true, nil
}

// BerichtLoeschen entfernt den Bericht (Wartung vorbei).
func BerichtLoeschen(dataDir string) {
	_ = os.Remove(BerichtPfad(dataDir))
}

func lesen(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// schreiben: Temp-Datei im selben Ordner, dann umbenennen.
func schreiben(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-wartung-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
