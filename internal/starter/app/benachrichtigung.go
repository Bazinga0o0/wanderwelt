package app

// Windows-Hinweise zu dem, was in der Gruppe passiert (Benachrichtigungen):
// Jeder Statusabruf (alle 30 s) wird mit dem vorigen verglichen
// (internal/ereignis), die Ereignisse laufen durch die Schalter dieses PCs
// und erscheinen als Hinweis (notify: Toast, oder im Fenster, wenn es offen
// ist). Ereignisse, die man selbst ausgelöst hat, zeigt der Starter schon
// anders und nicht doppelt. Die Schalter liegen je Gruppe in
// Dir()/benachrichtigungen.json; die Datei enthält nur, was vom Standard
// abweicht.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const benachDatei = "benachrichtigungen.json"

// maxHinweisText begrenzt fremden Text (Namen, Wartungsgrund) in Hinweisen.
const maxHinweisText = 100

// benachState gehört zu App.benach.
type benachState struct {
	mu sync.Mutex // schützt benachrichtigungen.json
}

// benachEinstellung ist der Inhalt von Dir()/benachrichtigungen.json: die
// Schalter, die vom Standard abweichen (Schlüssel aus ereignis.Katalog).
type benachEinstellung struct {
	Arten map[string]bool `json:"arten,omitempty"`
}

func benachPfad() string { return filepath.Join(group.Dir(), benachDatei) }

func ladeBenach() (benachEinstellung, error) {
	var e benachEinstellung
	data, err := os.ReadFile(benachPfad())
	if errors.Is(err, fs.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(data, &e); err != nil {
		return benachEinstellung{}, fmt.Errorf("%s: %w", benachPfad(), err)
	}
	return e, nil
}

func speichereBenach(e benachEinstellung) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	dir := group.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, benachDatei+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if err := os.Rename(name, benachPfad()); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// benachAn: Ist der Windows-Hinweis art eingeschaltet? Ohne (lesbare) Datei
// gilt der Standard.
func (a *App) benachAn(art ereignis.Art) bool {
	a.benach.mu.Lock()
	defer a.benach.mu.Unlock()
	e, _ := ladeBenach() // unlesbar: Standard
	if v, ok := e.Arten[string(art)]; ok {
		return v
	}
	return ereignis.Standard(ereignis.Windows, art)
}

// setBenach schaltet den Hinweis art. Ein Wert wie der Standard wird nicht
// gespeichert, damit ein späterer anderer Standard greift.
func (a *App) setBenach(art ereignis.Art, an bool) error {
	if !ereignis.Bekannt(art) {
		return fmt.Errorf("Unbekannter Hinweis %q.", art)
	}
	a.benach.mu.Lock()
	defer a.benach.mu.Unlock()
	e, err := ladeBenach()
	if err != nil {
		e = benachEinstellung{} // verdorben: neu anfangen
	}
	// Nur Schlüssel des Katalogs bleiben (Reste alter Versionen fallen weg).
	arten := map[string]bool{}
	for k, v := range e.Arten {
		if ereignis.Bekannt(ereignis.Art(k)) && v != ereignis.Standard(ereignis.Windows, ereignis.Art(k)) {
			arten[k] = v
		}
	}
	if an == ereignis.Standard(ereignis.Windows, art) {
		delete(arten, string(art))
	} else {
		arten[string(art)] = an
	}
	e.Arten = arten
	if len(arten) == 0 {
		e.Arten = nil
	}
	return speichereBenach(e)
}

// benachZustand füllt den Teil des Fenster-Zustands: je Schalter Titel,
// Hilfe und Wert (Admin-Schalter nur für Admins).
func (a *App) benachZustand(me *api.Me) []fensterapi.BenachSchalter {
	a.benach.mu.Lock()
	e, _ := ladeBenach()
	a.benach.mu.Unlock()
	admin := me != nil && me.Admin
	var out []fensterapi.BenachSchalter
	for _, s := range ereignis.Katalog {
		if s.NurAdmin && !admin {
			continue
		}
		an, ok := e.Arten[string(s.Art)]
		if !ok {
			an = s.Windows
		}
		out = append(out, fensterapi.BenachSchalter{Art: string(s.Art), Titel: s.Titel, Hilfe: s.Hilfe, An: an})
	}
	return out
}

// eigenesFiltern entfernt aus e, was me selbst ausgelöst hat (eigenes
// Hosten, eigener Beitritt, eigene Übergabe): Dazu zeigt der Starter schon
// eigene Hinweise. skip: Es bleibt nichts zu melden.
func eigenesFiltern(e ereignis.Ereignis, me *api.Me) (ereignis.Ereignis, bool) {
	if me == nil {
		return e, false
	}
	// Der Schlüssel eines Hosts ist seine Kennung (älterer Koordinator: seine
	// Adresse, klein); beides gehört dem Mitglied selbst.
	mail := strings.ToLower(strings.TrimSpace(me.Email))
	ich := func(key string) bool { return key != "" && (key == mail || key == me.ID) }
	switch e.Art {
	case ereignis.HostStartet, ereignis.WeltOnline, ereignis.NiemandHostet:
		return e, ich(e.WerKey)
	case ereignis.Uebergabe:
		return e, ich(e.WerKey) || ich(e.NachKey)
	case ereignis.Spieler:
		e.Neu = fensterapi.AndereSpieler(e.Neu, me.MinecraftName)
		e.Weg = fensterapi.AndereSpieler(e.Weg, me.MinecraftName)
		return e, len(e.Neu) == 0 && len(e.Weg) == 0
	}
	return e, false
}

// hinweiseAus zeigt die Windows-Hinweise zu den Veränderungen von alt zu neu.
// Das Ende einer Wartung meldet pollStatus selbst (mit dem Text zum
// Fortsetzen).
func (a *App) hinweiseAus(alt, neu *api.Status, me *api.Me) {
	var texte []string
	for _, e := range ereignis.Aus(alt, neu) {
		if e.Art == ereignis.Wartung && e.Ende {
			continue
		}
		e, skip := eigenesFiltern(e, me)
		if skip || !a.benachAn(e.Art) {
			continue
		}
		texte = append(texte, ereignis.Text(e, func(s string) string { return ereignis.Kurz(s, maxHinweisText) }))
	}
	if len(texte) == 0 {
		return
	}
	// In einer Goroutine nacheinander: Die Reihenfolge („hostet jetzt“, dann
	// „online“) bleibt, und der Statusabruf wartet nie auf die Anzeige.
	go func() {
		for _, t := range texte {
			notify(t)
		}
	}()
}
