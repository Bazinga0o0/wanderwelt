package fehlerbericht

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Der Fehlerbericht ist ein Zip, das nur auf diesem PC entsteht (kein Upload):
// Der Nutzer gibt es weiter, wem er will. Jeder Text darin ist gesäubert.

const (
	// MaxLogStueck: So viel vom Ende einer Logdatei kommt in den Bericht.
	MaxLogStueck = 2 << 20
	// BerichtPrefix und BerichtEndung bilden den Dateinamen.
	BerichtPrefix = "wanderwelt-fehlerbericht-"
	BerichtEndung = ".zip"
)

// Welt ist eine lokale Welt der Gruppe: Kennung ("" = erste Welt) und
// Server-Ordner (darin logs/, mods/ und world/).
type Welt struct {
	ID        string
	ServerDir string
}

// Eingabe sind die Quellen des Berichts; was fehlt, steht als Hinweis in
// info.txt.
type Eingabe struct {
	Version    string
	System     string // Text zu Windows und Hardware (mehrzeilig)
	StarterLog string // Pfad von wanderwelt.log (.1 wird mitgenommen)
	Welten     []Welt
	Zustand    []byte   // Zustand der Instanz als JSON
	Manifest   string   // Text zum Versions-Manifest ("" = nicht erreichbar)
	Hinweise   []string // was der Aufrufer nicht beschaffen konnte
	Jetzt      time.Time
	San        *Saeuberer
}

var weltIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func weltOrdnerName(id string) string {
	if id == "" {
		return "host-haupt"
	}
	if !weltIDRe.MatchString(id) {
		return "host-welt"
	}
	return "host-" + id
}

// Erstellen packt den Bericht nach zielDir/wanderwelt-fehlerbericht-<Datum-Zeit>.zip
// und liefert den Pfad. Das Zip entsteht als Temp-Datei und wird erst fertig
// umbenannt. Fehlende oder unlesbare Quellen sind kein Fehler (sie stehen als
// Hinweis in info.txt); nur ein nicht schreibbares Ziel ist einer.
func Erstellen(in Eingabe, zielDir string) (string, error) {
	if in.San == nil {
		in.San = Standard()
	}
	if in.Jetzt.IsZero() {
		in.Jetzt = time.Now()
	}
	jetzt := in.Jetzt.Local()
	if err := os.MkdirAll(zielDir, 0o755); err != nil {
		return "", fmt.Errorf("Ordner für den Fehlerbericht: %w", err)
	}
	tmp, err := os.CreateTemp(zielDir, ".wanderwelt-fehlerbericht-*.tmp")
	if err != nil {
		return "", fmt.Errorf("Fehlerbericht anlegen: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			os.Remove(tmpName)
		}
	}()

	b := &bau{zw: zip.NewWriter(tmp), san: in.San, zeit: jetzt, hinweise: slices.Clone(in.Hinweise)}

	// Zuerst die Logs, damit info.txt alle Hinweise kennt.
	b.starterLogs(in.StarterLog, jetzt)
	for _, w := range in.Welten {
		b.welt(w)
	}
	if len(in.Zustand) > 0 {
		b.text("zustand.json", string(in.Zustand))
	} else {
		b.hinweis("Zustand der Instanz nicht verfügbar")
	}
	b.text("manifest.txt", b.manifestText(in))
	b.text("info.txt", b.infoText(in, jetzt))

	if err := b.zw.Close(); err != nil {
		return "", fmt.Errorf("Fehlerbericht schreiben: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("Fehlerbericht schreiben: %w", err)
	}
	base := BerichtPrefix + jetzt.Format("20060102-150405")
	for i := 1; ; i++ {
		name := base + BerichtEndung
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", base, i, BerichtEndung)
		}
		final := filepath.Join(zielDir, name)
		// Nie eine vorhandene Datei überschreiben (zwei Berichte in einer
		// Sekunde).
		if _, err := os.Lstat(final); err == nil {
			if i > 50 {
				return "", errors.New("zu viele Fehlerberichte mit demselben Namen")
			}
			continue
		}
		if err := os.Rename(tmpName, final); err != nil {
			return "", fmt.Errorf("Fehlerbericht fertigstellen: %w", err)
		}
		ok = true
		return final, nil
	}
}

type bau struct {
	zw       *zip.Writer
	san      *Saeuberer
	zeit     time.Time
	hinweise []string
	dateien  []string
}

func (b *bau) hinweis(f string, a ...any) {
	b.hinweise = append(b.hinweise, fmt.Sprintf(f, a...))
}

// text legt eine gesäuberte Textdatei ins Zip.
func (b *bau) text(name, inhalt string) {
	w, err := b.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: b.zeit})
	if err != nil {
		b.hinweis("%s nicht ins Zip geschrieben: %v", name, err)
		return
	}
	if _, err := w.Write([]byte(b.san.Text(inhalt))); err != nil {
		b.hinweis("%s nicht ins Zip geschrieben: %v", name, err)
		return
	}
	b.dateien = append(b.dateien, name)
}

// logEnde legt das Ende einer Logdatei (höchstens MaxLogStueck) ins Zip;
// fehlt die Datei, ist das ein Hinweis nur, wenn wichtig.
func (b *bau) logEnde(name, pfad string, wichtig bool) {
	data, err := leseEnde(pfad, MaxLogStueck)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if wichtig {
			b.hinweis("%s fehlt (noch nicht entstanden)", name)
		}
	case err != nil:
		b.hinweis("%s nicht lesbar: %v", name, err)
	default:
		b.text(name, string(data))
	}
}

// starterLogs: wanderwelt.log(.1) nach Tagen: heute und gestern.
func (b *bau) starterLogs(pfad string, jetzt time.Time) {
	if pfad == "" {
		b.hinweis("Starter-Log: kein Pfad")
		return
	}
	heute := jetzt.Format("2006/01/02") + " "
	gestern := jetzt.AddDate(0, 0, -1).Format("2006/01/02") + " "
	var hz, gz []string
	gelesen := false
	for _, p := range []string{pfad + ".1", pfad} {
		data, err := leseEnde(p, 4*MaxLogStueck)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				b.hinweis("%s nicht lesbar: %v", filepath.Base(p), err)
			}
			continue
		}
		gelesen = true
		tag := 0 // Zeilen ohne Datum gehören zum Tag davor
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			switch {
			case strings.HasPrefix(l, heute):
				tag = 1
			case strings.HasPrefix(l, gestern):
				tag = 2
			case len(l) > 11 && l[4] == '/' && l[7] == '/' && l[10] == ' ':
				tag = 0
			}
			switch tag {
			case 1:
				hz = append(hz, l)
			case 2:
				gz = append(gz, l)
			}
		}
	}
	if !gelesen {
		b.hinweis("Starter-Log nicht gefunden")
	}
	b.text("starter-heute.log", endeBytes(hz, MaxLogStueck))
	b.text("starter-gestern.log", endeBytes(gz, MaxLogStueck))
}

// endeBytes verbindet Zeilen und behält höchstens die letzten max Bytes.
func endeBytes(z []string, max int) string {
	if len(z) == 0 {
		return ""
	}
	return strings.Join(kuerzenAufBytes(z, max), "\n") + "\n"
}

// welt nimmt die Host-Logs, die gewanderten Logs und die Mods einer Welt.
func (b *bau) welt(w Welt) {
	dir := weltOrdnerName(w.ID)
	if w.ServerDir == "" {
		return
	}
	if _, err := os.Stat(w.ServerDir); err != nil {
		return // diese Welt hat auf diesem PC keinen Server-Ordner
	}
	b.logEnde(dir+"/wanderwelt-server.log", filepath.Join(w.ServerDir, "logs", "wanderwelt-server.log"), false)
	b.logEnde(dir+"/latest.log", filepath.Join(w.ServerDir, "logs", "latest.log"), false)
	logs := filepath.Join(w.ServerDir, "world", WeltLogOrdner)
	entries, err := os.ReadDir(logs)
	if err != nil {
		return
	}
	var gesamt int64
	for _, e := range entries {
		if e.IsDir() || !weltLogName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || gesamt+info.Size() > 2*MaxWeltLogGesamt {
			continue
		}
		gesamt += info.Size()
		b.logEnde(dir+"/"+WeltLogOrdner+"/"+e.Name(), filepath.Join(logs, e.Name()), false)
	}
}

// manifestText: Manifest-Version und Mods des Koordinators sowie, was im
// Server-Ordner liegt.
func (b *bau) manifestText(in Eingabe) string {
	var s strings.Builder
	if in.Manifest != "" {
		s.WriteString("Versions-Manifest (vom Koordinator):\n")
		s.WriteString(in.Manifest)
		if !strings.HasSuffix(in.Manifest, "\n") {
			s.WriteString("\n")
		}
	} else {
		s.WriteString("Versions-Manifest: nicht verfügbar (Koordinator nicht erreichbar oder nicht angemeldet)\n")
	}
	for _, w := range in.Welten {
		if w.ServerDir == "" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(w.ServerDir, "mods"))
		if err != nil {
			continue
		}
		fmt.Fprintf(&s, "\nMods im Server-Ordner (%s):\n", weltOrdnerName(w.ID))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if info, err := e.Info(); err == nil {
				fmt.Fprintf(&s, "  %s (%d Bytes)\n", e.Name(), info.Size())
			}
		}
	}
	return s.String()
}

func (b *bau) infoText(in Eingabe, jetzt time.Time) string {
	var s strings.Builder
	s.WriteString("wanderwelt Fehlerbericht\n")
	fmt.Fprintf(&s, "Erstellt: %s\n", jetzt.Format("2006-01-02 15:04:05 -0700"))
	fmt.Fprintf(&s, "Version: %s\n", in.Version)
	if in.System != "" {
		s.WriteString(strings.TrimRight(in.System, "\n") + "\n")
	}
	s.WriteString("\nDer Bericht entstand nur auf diesem PC und wurde nirgends hochgeladen. Geheimnisse\n")
	s.WriteString("(Token, Passwörter, Webhooks, Gruppencodes) und der Windows-Benutzername sind entfernt.\n")
	s.WriteString("Die Logs können trotzdem Spielernamen und Chat des Servers enthalten: bitte vor dem\n")
	s.WriteString("Weitergeben ansehen.\n")
	if len(b.hinweise) > 0 {
		s.WriteString("\nHinweise:\n")
		for _, h := range b.hinweise {
			fmt.Fprintf(&s, "  - %s\n", h)
		}
	}
	s.WriteString("\nInhalt:\n")
	names := slices.Clone(b.dateien)
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(&s, "  %s\n", n)
	}
	s.WriteString("  info.txt\n")
	return s.String()
}

// ZipNamen liefert die Namen der Einträge eines Berichts (für Tests und
// Prüfungen).
func ZipNamen(pfad string) ([]string, error) {
	zr, err := zip.OpenReader(pfad)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out, nil
}

// ZipLesen liest einen Eintrag eines Berichts.
func ZipLesen(pfad, name string) ([]byte, error) {
	zr, err := zip.OpenReader(pfad)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return nil, fs.ErrNotExist
}
