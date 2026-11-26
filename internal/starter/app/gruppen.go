package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// migrateLayout verschiebt beim Start das alte Einzel-Layout nach
// gruppen\<id> (group.MigrateLegacy; nichts wird gelöscht, bei einem Fehler
// bleibt alles beim Alten). Läuft nach der Sperre der einzigen Instanz und
// vor dem Laden von Gruppe und Zustand.
func migrateLayout() {
	id, err := group.MigrateLegacy()
	if err != nil {
		log.Printf("Gruppen-Layout: %v (das bisherige Layout bleibt, nur eine Gruppe möglich)", err)
		return
	}
	if id == "" {
		return
	}
	log.Printf("Gruppen-Layout: bisherige Daten nach gruppen/%s verschoben", id)
	// Die bisherige Notfallkopie gehört jetzt dieser Gruppe: ihren Ordner
	// behalten, statt ihn zu verwaisen.
	if root, err := notfallRoot(); err == nil {
		if fi, err := os.Stat(filepath.Join(root, notfallName)); err == nil && fi.IsDir() {
			e, err := ladeNotfall()
			if err != nil {
				e = notfallEinstellung{}
			}
			e.Ordner = notfallName
			if err := speichereNotfall(e); err != nil {
				log.Printf("Gruppen-Layout: Notfallkopie-Ordner nicht vermerkt: %v", err)
			}
		}
	}
}

// notfallOrdnerName ist der Ordnername der Notfallkopie der aktiven Gruppe:
// je Gruppe ein eigener Ordner, damit nie die Kopie einer anderen Gruppe
// überschrieben wird.
func notfallOrdnerName() string {
	if e, err := ladeNotfall(); err == nil && e.Ordner != "" {
		if e.Ordner == notfallName ||
			(strings.HasPrefix(e.Ordner, notfallName+"-") && group.ValidID(strings.TrimPrefix(e.Ordner, notfallName+"-"))) {
			return e.Ordner
		}
	}
	id := group.ActiveID()
	if group.Legacy() || id == "" {
		return notfallName
	}
	return notfallName + "-" + id
}

// ---- Auswahl und Wechsel ----

const (
	textWechselSpielt = "Du hostest oder spielst gerade. Beende erst dein Spiel („Spiel für mich beenden“), dann kannst du die Gruppe wechseln."
	textWechselBusy   = "Gerade läuft noch ein Vorgang (%s). Warte, bis er fertig ist, und wechsle dann die Gruppe."
	textWechselWelt   = "Die Welt dieser Gruppe wird gerade geladen oder hochgeladen. Warte, bis das fertig ist, und wechsle dann die Gruppe."
)

// wechselSperreLocked: Grund (deutscher Text), warum gerade nicht gewechselt
// werden darf; "" = erlaubt. Aufruf mit gehaltenem a.mu.
func (a *App) wechselSperreLocked() string {
	switch {
	case a.hostCancel != nil, a.phase == phHosting, a.phase == phJoined, a.phase == phStarting, a.joinCancel != nil:
		return textWechselSpielt
	case a.phase == phUploading, a.recoverLoop, a.prefetchCancel != nil, a.notfallLaeuftLocked():
		return textWechselWelt
	}
	return ""
}

// wechselSperre ist wechselSperreLocked mit eigenem Lock; zusätzlich gilt
// jede laufende Aktion (busy) als Sperre.
func (a *App) wechselSperre() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r := a.wechselSperreLocked(); r != "" {
		return r
	}
	if a.busy != "" {
		return fmt.Sprintf(textWechselBusy, a.busy)
	}
	if len(a.world) > 0 { // Vorladen, Nachreichen, Host-Sitzung …
		return textWechselWelt
	}
	return ""
}

func (a *App) notfallLaeuftLocked() bool {
	a.notfall.mu.Lock()
	defer a.notfall.mu.Unlock()
	return len(a.notfall.cancels) > 0
}

// switchGroup macht die Gruppe id zur aktiven. Es gilt nie während Hosten,
// Spielen oder einem Vorgang an der Welt (Fehlertext für den Nutzer); die
// Daten der bisherigen Gruppe werden nicht angefasst, Gruppendatei, Zustand,
// Server-Ordner und Profil kommen danach aus dem Ordner der neuen.
func (a *App) switchGroup(id string) error {
	if !group.ValidID(id) {
		return errors.New("Diese Gruppe gibt es nicht.")
	}
	if group.Legacy() {
		return errors.New("Das bisherige Datenlayout ließ sich nicht auf mehrere Gruppen umstellen (Details im Log); es geht nur eine Gruppe.")
	}
	if id == group.ActiveID() {
		return nil
	}
	if ok, running := a.begin("Gruppe wechseln"); !ok {
		return fmt.Errorf(textWechselBusy, running)
	}
	defer a.end()
	// Die Welt belegen: Während des Wechsels startet nichts, was den
	// Server-Ordner braucht.
	if !a.tryWorld("wechsel") {
		return errors.New(textWechselWelt)
	}
	released := false
	release := func() {
		if !released {
			released = true
			a.releaseWorld()
		}
	}
	defer release()
	a.mu.Lock()
	reason := a.wechselSperreLocked()
	a.mu.Unlock()
	if reason != "" {
		return errors.New(reason)
	}
	prev := group.ActiveID()
	oldProf, oldProfErr := bench.LoadProfile()
	fresh, err := a.gruppeVorbereiten(id)
	if err != nil {
		return fmt.Errorf("Die Gruppe lässt sich nicht einrichten: %w", err)
	}
	if err := group.SetActive(id); err != nil {
		return err
	}
	if fresh && oldProfErr == nil {
		// Neue Gruppe: das Leistungsprofil dieses PCs übernehmen (Dauerbetrieb
		// bleibt aus; es gehört in keine andere Gruppe).
		if _, err := os.Stat(bench.ProfilePath()); err != nil {
			oldProf.Dauerbetrieb = false
			if err := bench.SaveProfile(oldProf); err != nil {
				log.Printf("Gruppe %s: Profil nicht übernommen: %v", id, err)
			}
		}
	}
	if err := a.load(); err != nil {
		// Zurück zur bisherigen Gruppe; die neue ist unbrauchbar.
		if rerr := group.SetActive(prev); rerr == nil {
			_ = a.load()
		}
		return fmt.Errorf("Die Gruppe lässt sich nicht laden: %w", err)
	}
	a.mu.Lock()
	a.me, a.status, a.statusErr = nil, nil, nil
	a.joinGemeldet = nil
	a.detail, a.lastErr = "", ""
	a.phase = phReady
	a.mu.Unlock()
	log.Printf("Gruppe gewechselt: %s", id)
	a.loadDauerbetrieb()
	if fresh {
		if p, err := bench.LoadProfile(); err == nil {
			go a.reportProfile(p) // der Koordinator dieser Gruppe kennt dieses Profil noch nicht
		}
	}
	release()
	a.recoverAtStart() // Reste (Markierung, vorgemerkter Stand) dieser Gruppe
	go a.pollStatus(true)
	a.refresh()
	return nil
}

// gruppenListe füllt Auswahl und Titel für das Fenster.
func (a *App) gruppenListe() ([]fensterapi.GruppeEintrag, string) {
	aktiv := group.ActiveID()
	var out []fensterapi.GruppeEintrag
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range group.List() {
		out = append(out, fensterapi.GruppeEintrag{ID: e.ID, Titel: a.titel[e.ID], Aktiv: e.ID == aktiv})
	}
	return out, aktiv
}

// setTitel merkt den Titel einer Gruppe (aus GET /v1/info; nur lokal
// angezeigt, nie in Discord).
func (a *App) setTitel(id, titel string) {
	if id == "" || titel == "" {
		return
	}
	a.mu.Lock()
	if a.titel == nil {
		a.titel = map[string]string{}
	}
	a.titel[id] = titel
	a.mu.Unlock()
}

// trayGruppe: Zusatz für den Tooltip, wenn es mehrere Gruppen gibt
// („ [Titel]“), sonst "".
func (a *App) trayGruppe() string {
	list := group.List()
	if len(list) < 2 {
		return ""
	}
	id := group.ActiveID()
	a.mu.Lock()
	t := a.titel[id]
	a.mu.Unlock()
	if t == "" {
		t = id
	}
	return " [" + t + "]"
}

// gruppeVorbereiten richtet eine Gruppe, die es noch nie gab (aus „Entdecken“
// angelegt), beim ersten Wechsel ein: eigene PC-Kennung und Zustand in ihrem
// Ordner. Abhängigkeiten und Launcher-Profil holt „Spielen“ wie immer.
func (a *App) gruppeVorbereiten(id string) (fresh bool, err error) {
	st, err := group.LoadStateOf(id)
	if err != nil {
		return false, err
	}
	if st.MachineID != "" {
		return false, nil
	}
	st.MachineID = newMachineID()
	st.InstalledVersion = a.version
	if st.InstalledVersion == "" {
		st.InstalledVersion = "dev"
	}
	if err := st.Save(); err != nil {
		return false, err
	}
	log.Printf("Gruppe %s: neue PC-Kennung %s", id, st.MachineID)
	return true, nil
}
