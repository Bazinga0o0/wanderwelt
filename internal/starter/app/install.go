package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

const (
	eulaURL = "https://aka.ms/MinecraftEULA"
	// ShortcutName ist die Desktop-Verknüpfung, die „Spielen“ auslöst.
	ShortcutName = "wanderwelt – Spielen.lnk"
)

// install richtet wanderwelt ein (Spec 6.3.1). Die Kopie nach Home() ist
// schon geschehen (Run). Reihenfolge: Gruppendatei → Mitglied? → EULA →
// Autostart → PC-Kennung → Abhängigkeiten → Launcher-Profil →
// Leistungsprofil → Desktop-Verknüpfung. Das Vorladen der Welt startet Run
// danach im Hintergrund. Jeder Schritt verträgt eine Wiederholung (eine
// abgebrochene Installation läuft beim nächsten Start einfach noch einmal).
func (a *App) install(ctx context.Context) error {
	log.Printf("Installation beginnt")
	var notes []string

	// Gruppendatei und Mitgliedschaft.
	var (
		g   *group.Group
		me  *api.Me
		err error
	)
	if a.needsGroupCodeOffer() {
		// Ohne Gruppe: Gruppencode anbieten (Adressen daraus, erst nach
		// Bestätigung); sonst Gruppendatei wie gewohnt.
		inv, err := a.groupCodeOffer()
		if err != nil {
			return err
		}
		if inv != nil {
			a.invite = inv
		}
	}
	if a.invite != nil {
		// Gruppen-Build: Token einfügen, ggf. Beitritt anfragen.
		g, me, err = a.chooseGroupInvite(ctx)
	} else {
		g, me, err = chooseGroup(ctx, "Willkommen bei wanderwelt!\n\nWähle die Gruppendatei, die du vom Admin bekommen hast.")
	}
	if err != nil {
		return err
	}
	log.Printf("Installation: Mitglied %s (Minecraft-Name %s, Admin %v)", me.Email, me.MinecraftName, me.Admin)

	// Minecraft-Name: gleich hier fragen (nicht erst beim Tray-Start, das gäbe
	// zwei Fenster übereinander mit dem Abschlussdialog). Nach einem Update
	// läuft alles still; dann fragt der Tray.
	if !a.updated && me.MinecraftName == "" {
		a.nameAsked.Store(true)
		if nm := a.askNameFor(ctx, client.New(g)); nm != nil {
			me = nm
		}
	}

	// Minecraft-EULA.
	if err := askEULA(); err != nil {
		return err
	}

	// Autostart.
	exe := installedExe()
	if err := winsys.SetAutostart(exe); err != nil {
		log.Printf("Installation: %v", err)
		notes = append(notes, "Der Autostart ließ sich nicht eintragen ("+err.Error()+"). Starte wanderwelt dann selbst.")
	}

	// PC-Kennung.
	st, err := group.LoadState()
	if err != nil {
		return err
	}
	if st.MachineID == "" {
		st.MachineID = newMachineID()
		if err := st.Save(); err != nil {
			return err
		}
		log.Printf("Installation: PC-Kennung %s", st.MachineID)
	}

	// Abhängigkeiten.
	c := client.New(g)
	m, err := retry401(ctx, func() (*api.Manifest, error) { return c.Manifest(ctx) })
	if err != nil {
		return fmt.Errorf("Versions-Manifest holen: %w", err)
	}
	prof, err := bench.LoadProfile()
	if err != nil {
		log.Printf("Installation: Leistungsprofil unlesbar, nehme das Standardprofil: %v", err)
		prof = bench.DefaultProfile()
	}
	if err := ensureDepsDialog(ctx, c, m, prof); err != nil {
		return err
	}

	// Launcher-Profil.
	if err := deps.EnsureLauncherProfile(m.MinecraftVersion); err != nil {
		log.Printf("Installation: %v", err)
		if errors.Is(err, fs.ErrNotExist) {
			notes = append(notes, "Der Minecraft-Launcher wurde noch nie gestartet. Das Profil „wanderwelt“ legt wanderwelt beim ersten „Spielen“ an.")
		} else {
			notes = append(notes, "Das Launcher-Profil „wanderwelt“ ließ sich nicht anlegen: "+err.Error())
		}
	}

	// Leistungsprofil.
	if _, err := os.Stat(bench.ProfilePath()); errors.Is(err, fs.ErrNotExist) {
		if err := bench.SaveProfile(prof); err != nil {
			return fmt.Errorf("Leistungsprofil: %w", err)
		}
	}
	if _, err := retry401(ctx, func() (struct{}, error) { return struct{}{}, c.PutProfile(ctx, st.MachineID, prof) }); err != nil {
		log.Printf("Installation: Profil nicht gemeldet: %v", err)
	}

	// Desktop-Verknüpfung.
	if err := createShortcut(exe); err != nil {
		log.Printf("Installation: %v", err)
		notes = append(notes, "Die Desktop-Verknüpfung ließ sich nicht anlegen ("+err.Error()+").")
	}

	st.InstalledVersion = a.version
	if st.InstalledVersion == "" {
		st.InstalledVersion = "dev"
	}
	if err := st.Save(); err != nil {
		return err
	}
	log.Printf("Installation fertig")

	abschluss(a.updated, me.MinecraftName, notes)
	return nil
}

// mitKomma hängt einen Namen als Anrede an („, Mara“); ohne Namen nichts.
func mitKomma(name string) string {
	if name == "" {
		return ""
	}
	return ", " + name
}

// abschluss meldet das Ende der Einrichtung. Nach einem Update (updated)
// läuft sie still nach: Es gibt keine Begrüßung, nur den Hinweis
// „wanderwelt wurde aktualisiert“ (Run); die Hinweise gehen ins Log.
func abschluss(updated bool, name string, notes []string) {
	if updated {
		for _, n := range notes {
			log.Printf("Installation: Hinweis: %s", n)
		}
		return
	}
	text := fmt.Sprintf("Fertig%s! wanderwelt läuft jetzt im Infobereich der Taskleiste und startet mit Windows.\n\n"+
		"Zum Spielen: Desktop-Verknüpfung „wanderwelt – Spielen“ oder Rechtsklick aufs Symbol → Spielen.\n\n"+
		"Die Welt wird jetzt im Hintergrund vorgeladen.", mitKomma(name))
	if len(notes) > 0 {
		text += "\n\nHinweise:\n• " + strings.Join(notes, "\n• ")
	}
	showInfo(text)
}

// chooseGroup lässt eine Gruppendatei wählen, prüft beim Koordinator die
// Mitgliedschaft und installiert sie erst dann. Eine schon installierte,
// gültige Gruppendatei wird ohne Dialog weiterverwendet (abgebrochene
// Installation).
func chooseGroup(ctx context.Context, intro string) (*group.Group, *api.Me, error) {
	if g, err := group.LoadGroup(group.GroupPath()); err == nil {
		me, err := memberCheck(ctx, g)
		if err == nil {
			return g, me, nil
		}
		log.Printf("installierte Gruppendatei: %v", err)
		showError("Mit der eingerichteten Gruppendatei geht es nicht weiter:\n\n" + explain(err) + "\n\nBitte wähle eine (neue) Gruppendatei.")
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("installierte Gruppendatei: %v", err)
	}
	return chooseGroupFile(ctx, intro)
}

// selectGroupFile fragt den Pfad der Gruppendatei; im Test austauschbar.
var selectGroupFile = func() (string, error) {
	return zenity.SelectFile(
		zenity.Title("Gruppendatei wählen"), windowIcon(),
		zenity.FileFilters{{Name: "Gruppendatei (*.yaml)", Patterns: []string{"*.yaml", "*.yml"}, CaseFold: true}},
	)
}

// chooseGroupFile lässt eine Gruppendatei wählen, prüft sie und installiert
// sie (ohne vorher die schon installierte zu prüfen).
func chooseGroupFile(ctx context.Context, intro string) (*group.Group, *api.Me, error) {
	first := true
	for {
		if first && intro != "" {
			showInfo(intro)
		}
		first = false
		path, err := selectGroupFile()
		if errors.Is(err, zenity.ErrCanceled) {
			return nil, nil, errCanceled
		}
		if err != nil {
			return nil, nil, err
		}
		g, err := group.LoadGroup(path)
		if err != nil {
			showError("Diese Gruppendatei ist nicht lesbar:\n\n" + err.Error())
			continue
		}
		me, err := memberCheck(ctx, g)
		if err != nil {
			showError("Mit dieser Gruppendatei geht es nicht:\n\n" + explain(err))
			continue
		}
		if _, err := group.InstallGroup(path); err != nil {
			return nil, nil, fmt.Errorf("Gruppendatei übernehmen: %w", err)
		}
		log.Printf("Gruppendatei %s übernommen", path)
		return g, me, nil
	}
}

// memberCheck fragt den Koordinator, wer man ist (GET /v1/me). 403 heißt
// „nicht Mitglied“ (siehe explain).
func memberCheck(ctx context.Context, g *group.Group) (*api.Me, error) {
	c := client.New(g)
	return retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) })
}

// askEULA zeigt die Minecraft-EULA zur Zustimmung.
func askEULA() error {
	text := "Als Host startet wanderwelt auf deinem PC einen Minecraft-Server. " +
		"Dafür musst du der Minecraft-EULA (Endbenutzer-Lizenzvertrag) von Mojang zustimmen:\n\n" +
		eulaURL + "\n\nStimmst du der Minecraft-EULA zu?"
	for {
		err := zenity.Question(text,
			zenity.Title("wanderwelt – Minecraft-EULA"), windowIcon(),
			zenity.OKLabel("Zustimmen"),
			zenity.CancelLabel("Ablehnen"),
			zenity.ExtraButton("EULA lesen"),
			zenity.NoWrap())
		switch {
		case err == nil:
			log.Printf("Installation: Minecraft-EULA zugestimmt")
			return nil
		case errors.Is(err, zenity.ErrExtraButton):
			if err := winsys.OpenURL(eulaURL); err != nil {
				log.Printf("EULA öffnen: %v", err)
			}
		case errors.Is(err, zenity.ErrCanceled):
			showInfo("Ohne Zustimmung zur Minecraft-EULA kann wanderwelt nicht eingerichtet werden.")
			return errCanceled
		default:
			return err
		}
	}
}

// depsProgressMax ist der Höchstwert des Balkens beim Einrichten.
const depsProgressMax = 1000

// openDepsProgress öffnet das Fortschrittsfenster der Einrichtung; im Test
// austauschbar.
var openDepsProgress = func() (zenity.ProgressDialog, error) {
	return oeffneFortschritt(
		zenity.Title("wanderwelt einrichten"), windowIcon(),
		zenity.MaxValue(depsProgressMax),
		zenity.CancelLabel("Abbrechen"))
}

// ensureDeps lädt die Abhängigkeiten; im Test austauschbar.
var ensureDeps = deps.Ensure

// ensureDepsDialog lädt die Abhängigkeiten mit Fortschrittsfenster. Der
// Balken zeigt den Gesamtfortschritt über alle Abhängigkeiten und erreicht
// das Ende erst mit Complete: zenity macht „OK“ klickbar, sobald der Wert
// den Höchstwert erreicht, und OK schließt das Fenster – das gälte als
// Abbruch.
func ensureDepsDialog(ctx context.Context, c *client.Client, m *api.Manifest, prof api.Profile) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dlg, err := openDepsProgress()
	if err != nil {
		log.Printf("Fortschrittsfenster: %v", err)
		dlg = nil
	}
	if dlg != nil {
		setzeFortschrittText(dlg, textPruefeDeps)
		go func() {
			select {
			case <-dlg.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	pr := newDepsProgress(dlg, deps.Names(m, prof))
	_, err = ensureDeps(ctx, m, prof, c, pr.report)
	// Vor dem Schließen: Close beendet auch dlg.Done() und damit ctx.
	canceled := ctx.Err() != nil
	if dlg != nil {
		pr.mu.Lock()
		if err == nil {
			dlg.Complete()
		}
		dlg.Close()
		pr.mu.Unlock()
	}
	if err != nil {
		if canceled {
			return errCanceled
		}
		return fmt.Errorf("Abhängigkeiten laden: %w", err)
	}
	return nil
}

// depsProgress rechnet die Fortschritte der einzelnen Downloads von
// deps.Ensure in den Gesamtfortschritt um. Die Größen stehen nicht im
// Manifest; deshalb hat jede Abhängigkeit (in der Reihenfolge von
// deps.Ensure) einen gleich großen Abschnitt des Balkens, und vorhandene
// Dateien (ohne Download) überspringt der Balken. Er läuft nie zurück und
// bleibt unter dem Höchstwert, bis ensureDepsDialog Complete ruft.
type depsProgress struct {
	mu    sync.Mutex
	dlg   zenity.ProgressDialog // nil: kein Fenster
	max   int
	index map[string]int // Name → Platz in der Reihenfolge von deps.Ensure
	n     int
	last  int
}

func newDepsProgress(dlg zenity.ProgressDialog, names []string) *depsProgress {
	p := &depsProgress{dlg: dlg, index: make(map[string]int, len(names)), n: max(len(names), 1)}
	for i, name := range names {
		p.index[name] = i
	}
	if dlg != nil {
		p.max = dlg.MaxValue()
	}
	return p
}

// report ist der Fortschritts-Rückruf für deps.Ensure (total -1: Größe
// unbekannt).
func (p *depsProgress) report(name string, done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dlg == nil {
		return
	}
	// Der Name wird gekürzt, nicht die Größe dahinter (eine Zeile, dialoge.go).
	size := done
	if total > 0 {
		size = total
	}
	setzeFortschrittText(p.dlg, zeileMitName("Lade ", name, fmt.Sprintf(" (%s) …", humanSize(size)), maxZeile))
	i, ok := p.index[name]
	if !ok || p.max <= 1 {
		return
	}
	// Abschnitt i, darin der Anteil dieser Datei (unbekannte Größe: der
	// Anfang des Abschnitts).
	v := int64(i) * int64(p.max)
	if total > 0 {
		v += min(done, total) * int64(p.max) / total
	}
	v /= int64(p.n)
	if v >= int64(p.max) {
		v = int64(p.max) - 1
	}
	if int(v) <= p.last {
		return
	}
	p.last = int(v)
	p.dlg.Value(p.last)
}

// createShortcut legt auf dem Desktop „wanderwelt – Spielen“ an
// (wanderwelt.exe play).
func createShortcut(exe string) error {
	desk, err := winsys.DesktopDir()
	if err != nil {
		return fmt.Errorf("Desktop-Ordner: %w", err)
	}
	return winsys.CreateShortcut(filepath.Join(desk, ShortcutName), exe, "play", filepath.Dir(exe),
		"wanderwelt: hosten oder mitspielen")
}
