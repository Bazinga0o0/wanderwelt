package app

// Lokale Steuerung für das Fenster („wanderwelt.exe fenster“, eigener
// Prozess; Typen und Client in internal/starter/fensterapi): Ansichten
// (Zustand, Stände, Speicherstände, Log, Mitglieder) und Aktionen. Die
// Aktionen laufen über dieselben Methoden wie die Einträge im Tray-Menü;
// Daten des Koordinators werden durchgereicht. Token und Host-Kopf prüft die
// Steuerung (control.go) für jeden Endpunkt.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shirou/gopsutil/v4/mem"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/bench"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

const (
	// logZeilen: so viele Zeilen liefert /v1/log ohne Angabe; höchstens
	// maxLogZeilen.
	logZeilen    = 300
	maxLogZeilen = 2000
	// maxLogTail: So viel vom Ende des Logs wird höchstens gelesen.
	maxLogTail = 1 << 20
	// koordWait begrenzt einen durchgereichten Aufruf beim Koordinator.
	koordWait = 25 * time.Second

	// textZuAlt: Der Koordinator kennt den Endpunkt noch nicht.
	textZuAlt = "Der Koordinator ist zu alt für diese Ansicht"
	// textStartetNoch: Gruppendatei und Zustand sind noch nicht geladen.
	textStartetNoch = "wanderwelt startet noch – bitte gleich noch einmal."
)

// Aktionen des Fensters (POST /v1/<name>); „play“ nimmt die Steuerung wie
// von der Kommandozeile entgegen (onControl → Tray-Schleife → Play).
const (
	aktionBeenden       = "beenden"
	aktionBeendenOhne   = "beendenohne" // Hosten beenden, ohne die Welt weiterzugeben
	aktionImport        = "import"
	aktionProfil        = "profil"
	aktionAutostart     = "autostart"
	aktionZurueckholen  = "zurueckholen"
	aktionSpeicher      = "speicher"
	aktionRolle         = "rolle"
	aktionLogordner     = "logordner"
	aktionNotfall       = "notfallkopie"        // Haken „Notfallkopie behalten“
	aktionNotfallOrdner = "notfallordner"       // Ordner der Notfallkopie öffnen
	aktionMcName        = "mcname"              // Admin: Minecraft-Namen eines Mitglieds ändern
	aktionBeitritt      = "beitritt"            // Admin: Beitrittsanfrage annehmen oder ablehnen
	aktionGruppe        = "gruppe"              // aktive Gruppe wechseln
	aktionWelt          = "welt"                // Welt für „Spielen“ wählen
	aktionWeltNeu       = "weltneu"             // Admin: Welt anlegen
	aktionWeltAendern   = "weltaendern"         // Admin: Welt umbenennen / Eigenschaften ändern
	aktionWeltLoeschen  = "weltloeschen"        // Admin: Welt löschen (mit Bestätigung)
	aktionFortsetzen    = "fortsetzenabbrechen" // Hosten nach der Wartung nicht fortsetzen
)

// setAutostart trägt die installierte Kopie in den Autostart ein bzw.
// entfernt sie; autostartOn meldet, ob sie eingetragen ist; openFolder
// öffnet einen Ordner im Explorer. Im Test austauschbar.
var (
	setAutostart = func(on bool) error {
		if on {
			return winsys.SetAutostart(installedExe())
		}
		return winsys.RemoveAutostart()
	}
	autostartOn = func() bool {
		v, err := winsys.Autostart()
		return err == nil && v != ""
	}
	openFolder = func(dir string) error {
		cmd := exec.Command("explorer.exe", dir)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("Explorer: %w", err)
		}
		return cmd.Process.Release()
	}
)

// ramTotalMB ist der gesamte Arbeitsspeicher in MiB (0: unbekannt).
var ramTotalMB = sync.OnceValue(func() int {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return 0
	}
	return int(vm.Total >> 20)
})

// ansicht beantwortet GET /v1/<name> (fensterSteuerung).
func (a *App) ansicht(w http.ResponseWriter, r *http.Request, name string) {
	switch name {
	case "zustand":
		antwortJSON(w, http.StatusOK, a.zustand())
	case "staende":
		koordAufruf(a, w, r, "Stände", func(ctx context.Context, c *client.Client) (*api.GenerationsResponse, error) {
			return c.Generations(ctx)
		})
	case "mitglieder":
		koordAufruf(a, w, r, "Mitglieder", func(ctx context.Context, c *client.Client) ([]api.MemberInfo, error) {
			return c.Members(ctx)
		})
	case "statistik":
		koordAufruf(a, w, r, "Statistik", func(ctx context.Context, c *client.Client) (*api.Stats, error) {
			return c.Stats(ctx)
		})
	case "beitritte":
		koordAufruf(a, w, r, "Beitrittsanfragen", func(ctx context.Context, c *client.Client) ([]api.JoinRequestInfo, error) {
			return c.ListJoinRequests(ctx)
		})
	case "gesundheit":
		koordAufruf(a, w, r, "Gesundheit", func(ctx context.Context, c *client.Client) (*api.Health, error) {
			return c.Health(ctx)
		})
	case "welten":
		koordAufruf(a, w, r, "Welten", func(ctx context.Context, c *client.Client) (*api.WorldList, error) {
			return c.Worlds(ctx)
		})
	case "entdecken":
		a.entdeckenAnsicht(w, r)
	case "speicherstaende":
		a.speicherstaende(w, r)
	case "log":
		logAnsicht(w, r)
	default:
		http.Error(w, "unbekannte Ansicht", http.StatusNotFound)
	}
}

// aktion führt POST /v1/<name> aus (fensterSteuerung). play, beenden und
// import antworten 202 und laufen im Hintergrund weiter, die übrigen 200
// mit dem Ergebnis; Fehler als 4xx bzw. 5xx mit {"fehler": …}.
func (a *App) aktion(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	switch name {
	case aktionBeenden, aktionBeendenOhne, aktionImport, aktionProfil, aktionAutostart, aktionZurueckholen,
		aktionSpeicher, aktionRolle, aktionLogordner, aktionMcName, aktionBeitritt, aktionNotfall, aktionNotfallOrdner, aktionGruppe, aktionEntdeckenBeitritt,
		aktionWelt, aktionWeltNeu, aktionWeltAendern, aktionWeltLoeschen, aktionFortsetzen:
		log.Printf("Fenster: %s", name)
	default:
		http.Error(w, "unbekannte Aktion", http.StatusNotFound)
		return
	}
	switch name {
	case aktionBeenden:
		a.mu.Lock()
		playing := a.hostCancel != nil || a.phase == phJoined
		a.mu.Unlock()
		if !playing {
			antwortFehler(w, http.StatusConflict, "Du hostest gerade nicht und spielst auch nicht mit.")
			return
		}
		go a.endPlaying(false) // gefragt hat das Fenster
		w.WriteHeader(http.StatusAccepted)
	case aktionFortsetzen:
		a.fortAbbrechen()
		w.WriteHeader(http.StatusOK)
	case aktionBeendenOhne:
		if !a.isHosting() {
			antwortFehler(w, http.StatusConflict, "Du hostest gerade nicht.")
			return
		}
		go a.stopHostingPlain(false) // gefragt hat das Fenster
		w.WriteHeader(http.StatusAccepted)
	case aktionImport:
		a.fensterImport(w, body)
	case aktionProfil:
		var p api.Profile
		if err := json.Unmarshal(body, &p); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Leistungsprofil nicht lesbar: "+err.Error())
			return
		}
		if err := a.saveProfile(p); err != nil {
			antwortFehler(w, http.StatusBadRequest, err.Error())
			return
		}
		antwortJSON(w, http.StatusOK, p)
	case aktionAutostart:
		var req struct {
			An bool `json:"an"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Autostart: Körper nicht lesbar: "+err.Error())
			return
		}
		if err := setAutostart(req.An); err != nil {
			log.Printf("Fenster: Autostart %s: %v", onOff(req.An), err)
			antwortFehler(w, http.StatusInternalServerError, "Autostart lässt sich nicht umstellen: "+err.Error())
			return
		}
		log.Printf("Autostart %s (Fenster)", onOff(req.An))
		antwortJSON(w, http.StatusOK, map[string]bool{"an": autostartOn()})
	case aktionZurueckholen:
		var req api.RestoreRequest
		if err := json.Unmarshal(body, &req); err != nil || req.WorldID == "" || req.Number <= 0 {
			antwortFehler(w, http.StatusBadRequest, "Welcher Stand? Erwartet {\"world_id\": …, \"number\": …}.")
			return
		}
		res, ok := koordAufruf(a, w, r, "Zurückholen", func(ctx context.Context, c *client.Client) (*api.RestoreResponse, error) {
			return c.Restore(ctx, req)
		})
		if ok {
			log.Printf("Fenster: Stand %s/%d zurückholen – angewandt %v, ausstehend %v", req.WorldID, req.Number, res.Applied, res.Pending)
			go a.pollStatus(false)
		}
	case aktionSpeicher:
		var req api.BudgetRequest
		if err := json.Unmarshal(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Speicherbudget nicht lesbar: "+err.Error())
			return
		}
		koordAufruf(a, w, r, "Speicherbudget", func(ctx context.Context, c *client.Client) (*api.StorageInfo, error) {
			return c.SetStorageBudget(ctx, req)
		})
	case aktionRolle:
		var req struct {
			Email string `json:"email"`
			Admin bool   `json:"admin"`
			Op    bool   `json:"op"`
		}
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Email) == "" {
			antwortFehler(w, http.StatusBadRequest, "Welches Mitglied? Erwartet {\"email\": …, \"admin\": …, \"op\": …}.")
			return
		}
		_, ok := koordAufruf(a, w, r, "Rolle", func(ctx context.Context, c *client.Client) (*api.MemberInfo, error) {
			return c.SetRole(ctx, req.Email, api.RoleRequest{Admin: req.Admin, Op: req.Op})
		})
		a.mu.Lock()
		self := a.me != nil && strings.EqualFold(a.me.Email, req.Email)
		a.mu.Unlock()
		if ok && self {
			go a.pollStatus(true) // das eigene Admin-Recht hat sich vielleicht geändert
		}
	case aktionMcName:
		var req struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Email) == "" {
			antwortFehler(w, http.StatusBadRequest, "Welches Mitglied? Erwartet {\"email\": …, \"name\": …}.")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if !mcNameRE.MatchString(req.Name) {
			antwortFehler(w, http.StatusBadRequest, "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.")
			return
		}
		a.mu.Lock()
		admin := a.me != nil && a.me.Admin
		a.mu.Unlock()
		if !admin {
			antwortFehler(w, http.StatusForbidden, "Minecraft-Namen anderer ändern dürfen nur Admins.")
			return
		}
		_, ok := koordAufruf(a, w, r, "Minecraft-Name", func(ctx context.Context, c *client.Client) (struct{}, error) {
			return struct{}{}, c.SetMemberMinecraft(ctx, req.Email, req.Name)
		})
		a.mu.Lock()
		self := a.me != nil && strings.EqualFold(a.me.Email, req.Email)
		a.mu.Unlock()
		if ok && self {
			go a.pollStatus(true)
		}
	case aktionBeitritt:
		var req struct {
			Email    string `json:"email"`
			Annehmen bool   `json:"annehmen"`
		}
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Email) == "" {
			antwortFehler(w, http.StatusBadRequest, "Welche Anfrage? Erwartet {\"email\": …, \"annehmen\": …}.")
			return
		}
		a.mu.Lock()
		admin := a.me != nil && a.me.Admin
		a.mu.Unlock()
		if !admin {
			antwortFehler(w, http.StatusForbidden, "Beitrittsanfragen bearbeiten dürfen nur Admins.")
			return
		}
		_, ok := koordAufruf(a, w, r, "Beitrittsanfrage", func(ctx context.Context, c *client.Client) (struct{}, error) {
			if req.Annehmen {
				return struct{}{}, c.AcceptJoin(ctx, req.Email)
			}
			return struct{}{}, c.DeclineJoin(ctx, req.Email)
		})
		if ok {
			go a.pollStatus(true) // Zähler und Tray-Hinweis neu
		}
	case aktionNotfall:
		var req struct {
			An bool `json:"an"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Notfallkopie: Körper nicht lesbar: "+err.Error())
			return
		}
		if err := a.setNotfall(req.An); err != nil {
			antwortFehler(w, http.StatusInternalServerError, "Notfallkopie lässt sich nicht umstellen: "+err.Error())
			return
		}
		log.Printf("Notfallkopie %s (Fenster)", onOff(req.An))
		antwortJSON(w, http.StatusOK, map[string]bool{"an": a.notfallAn()})
	case aktionNotfallOrdner:
		n := a.notfallZustand()
		if !n.Vorhanden {
			antwortFehler(w, http.StatusConflict, "Es gibt noch keine Notfallkopie.")
			return
		}
		if err := openFolder(n.Ordner); err != nil {
			antwortFehler(w, http.StatusInternalServerError, err.Error())
			return
		}
		antwortJSON(w, http.StatusOK, struct{}{})
	case aktionEntdeckenBeitritt:
		a.entdeckenBeitritt(w, r, body)
	case aktionGruppe:
		var req struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.ID == "" {
			antwortFehler(w, http.StatusBadRequest, "Welche Gruppe? Erwartet {\"id\": …}.")
			return
		}
		if err := a.switchGroup(req.ID); err != nil {
			antwortFehler(w, http.StatusConflict, err.Error())
			return
		}
		antwortJSON(w, http.StatusOK, struct{}{})
	case aktionWelt, aktionWeltNeu, aktionWeltAendern, aktionWeltLoeschen:
		a.weltenAktion(w, r, name, body)
	case aktionLogordner:
		if err := openFolder(filepath.Dir(LogPath())); err != nil {
			antwortFehler(w, http.StatusInternalServerError, err.Error())
			return
		}
		antwortJSON(w, http.StatusOK, struct{}{})
	}
}

// zustand ist der Schnappschuss für das Fenster. Die Abfrage zählt: Das
// Fenster gilt danach fensterFrist lang als offen (Meldungen nur dort).
func (a *App) zustand() fensterapi.Zustand {
	a.mu.Lock()
	z := fensterapi.Zustand{
		Version: a.version,
		Phase:   phaseText(a.phase),
		Detail:  a.detail,
		Busy:    a.busy,
		LastErr: a.lastErr,
		Hosting: a.hostCancel != nil,
		Status:  a.status,
		Me:      a.me,
	}
	if a.statusErr != nil {
		z.StatusErr = explain(a.statusErr)
	}
	// Die Adresse zum Verbinden: als Host die der Sitzung, sonst die der
	// Gruppe.
	switch {
	case z.Hosting:
		z.HostAddr = a.hostAddr
	case a.status != nil && a.status.MinecraftAddress != "":
		z.HostAddr = a.status.MinecraftAddress
	case a.me != nil:
		z.HostAddr = a.me.MinecraftAddress
	}
	a.mu.Unlock()
	z.Gruppencode = a.groupCode()
	z.Gruppen, _ = a.gruppenListe()
	z.WechselSperre = a.wechselSperre()
	z.Welt = a.weltZustand()
	a.mu.Lock()
	z.ZeigeEntdecken = time.Now().Before(a.zeigeEntdeckenBis)
	a.mu.Unlock()
	if p, err := bench.LoadProfile(); err == nil {
		z.Profil = p
	} else {
		// Unlesbar (von Hand verdorben): Startwerte; Speichern aus dem
		// Fenster ersetzt die Datei.
		z.Profil = bench.DefaultProfile()
		z.Profil.Dauerbetrieb = a.dauer.on.Load()
	}
	z.RAMTotalMB = ramTotalMB()
	z.Autostart = autostartOn()
	z.Notfallkopie = a.notfallZustand()
	z.Hinweise = a.hinweise.abfrage(fensterNow())
	z.Fortsetzen = a.fortZustand()
	return z
}

// phaseText ist der Zustand wie im Tooltip, groß geschrieben („Hostet“).
func phaseText(p phase) string {
	s := p.String()
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// fensterImport: „Welt übernehmen“ aus dem Fenster. Nur ein Ordner direkt
// im saves-Ordner von Minecraft; danach wie die Übergabe von der
// Kommandozeile (dieselben Prüfungen, Ergebnis als Meldung).
func (a *App) fensterImport(w http.ResponseWriter, body []byte) {
	var req importRequest
	if err := json.Unmarshal(body, &req); err != nil {
		antwortFehler(w, http.StatusBadRequest, "Welt übernehmen: Körper nicht lesbar: "+err.Error())
		return
	}
	saves := savesDir()
	dir, ok := saveDirIn(saves, req.Dir)
	if !ok {
		antwortFehler(w, http.StatusBadRequest, "Übernehmen lässt sich nur eine Welt aus "+saves+".")
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		antwortFehler(w, http.StatusBadRequest, "Die Welt „"+filepath.Base(dir)+"“ gibt es nicht (mehr).")
		return
	}
	a.mu.Lock()
	ready := a.st != nil && a.hc != nil
	a.mu.Unlock()
	if !ready {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return
	}
	if ok, running := a.begin("Welt übernehmen"); !ok {
		antwortFehler(w, http.StatusConflict, "Erst wenn „"+running+"“ fertig ist.")
		return
	}
	req.Dir = dir
	req.FromWindow = true
	go func() { a.endWith(a.importRequested(req)) }()
	w.WriteHeader(http.StatusAccepted)
}

// saveDirIn prüft, ob dir (absolut) nach dem Bereinigen ein Ordner direkt in
// saves ist – unter Windows ohne Rücksicht auf Groß- und Kleinschreibung.
// Ergebnis ist der bereinigte Pfad.
func saveDirIn(saves, dir string) (string, bool) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", false
	}
	d := filepath.Clean(dir)
	parent, want := filepath.Dir(d), filepath.Clean(saves)
	same := parent == want
	if runtime.GOOS == "windows" {
		same = strings.EqualFold(parent, want)
	}
	if !same {
		return "", false
	}
	return d, true
}

// speicherstaende: die lokalen Einzelspieler-Welten mit dem Grund, warum
// sich eine nicht übernehmen lässt (Prüfung gegen die Manifest-Version).
func (a *App) speicherstaende(w http.ResponseWriter, r *http.Request) {
	c := a.client()
	if c == nil {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	m, err := retry401(ctx, func() (*api.Manifest, error) { return c.Manifest(ctx) })
	if err != nil {
		log.Printf("Fenster: Speicherstände: Versions-Manifest: %v", err)
		code, text := koordFehler(err)
		antwortFehler(w, code, "Versions-Manifest: "+text)
		return
	}
	saves, err := ListSaves(savesDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		antwortFehler(w, http.StatusInternalServerError, "Speicherstände lesen: "+err.Error())
		return
	}
	out := make([]fensterapi.Speicherstand, 0, len(saves))
	for _, s := range saves {
		sp := fensterapi.Speicherstand{Folder: s.Folder, LevelName: s.LevelName, Version: s.Version,
			LastPlayed: s.LastPlayed, Size: s.Size, PlayerFile: s.PlayerFile}
		if refuse, _ := CheckSave(s, m.MinecraftVersion); refuse != nil {
			sp.Problem = refuse.Error()
		} else if worldOpen(s.Dir) {
			sp.Problem = "„" + s.Folder + "“ ist gerade in Minecraft geöffnet. Bitte die Welt erst verlassen."
		}
		out = append(out, sp)
	}
	antwortJSON(w, http.StatusOK, out)
}

// logAnsicht: die letzten Zeilen von wanderwelt.log als Text (?zeilen=N,
// Standard logZeilen, höchstens maxLogZeilen).
func logAnsicht(w http.ResponseWriter, r *http.Request) {
	n := logZeilen
	if q := r.URL.Query().Get("zeilen"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 1 {
			antwortFehler(w, http.StatusBadRequest, "zeilen: erwartet eine ganze Zahl ab 1")
			return
		}
		n = min(v, maxLogZeilen)
	}
	text, err := logTail(LogPath(), n)
	if err != nil {
		antwortFehler(w, http.StatusInternalServerError, "Log lesen: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, text)
}

// logTail liefert die letzten n Zeilen der Datei path (aus höchstens
// maxLogTail Bytes vom Ende; eine angeschnittene erste Zeile fällt weg).
// Fehlt die Datei, ist das Ergebnis leer.
func logTail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := max(0, fi.Size()-maxLogTail)
	buf := make([]byte, fi.Size()-start)
	k, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:k]
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil // nicht eine ganze Zeile
		}
	}
	// Von hinten n Zeilenenden zählen (ein letztes Zeilenende gehört zur
	// letzten Zeile).
	end := len(buf)
	if end > 0 && buf[end-1] == '\n' {
		end--
	}
	cut := 0
	for i := end - 1; i >= 0; i-- {
		if buf[i] == '\n' {
			if n--; n == 0 {
				cut = i + 1
				break
			}
		}
	}
	return string(buf[cut:]), nil
}

// koordAufruf führt einen Aufruf beim Koordinator für das Fenster aus und
// schreibt das Ergebnis (200, JSON) oder den Fehler. ok: Ergebnis
// geschrieben.
func koordAufruf[T any](a *App, w http.ResponseWriter, r *http.Request, was string,
	f func(context.Context, *client.Client) (T, error)) (v T, ok bool) {
	c := a.client()
	if c == nil {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return v, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	v, err := retry401(ctx, func() (T, error) { return f(ctx, c) })
	if err != nil {
		log.Printf("Fenster: %s: %s", was, logFehler(err))
		code, text := koordFehler(err)
		antwortFehler(w, code, text)
		return v, false
	}
	antwortJSON(w, http.StatusOK, v)
	return v, true
}

// logFehler ist err fürs Log, ohne den Pfad des Koordinator-Aufrufs: Der
// enthält die E-Mail-Adresse anderer Mitglieder (/v1/members/<email>), und
// das Log steht im Log-Tab des Fensters.
func logFehler(err error) string {
	if se, ok := errors.AsType[*client.StatusError](err); ok {
		s := fmt.Sprintf("Koordinator: %s %d", se.Method, se.StatusCode)
		if se.Message != "" {
			s += ": " + se.Message
		}
		return s
	}
	// Transportfehler tragen die ganze URL samt E-Mail im Pfad: nur Op und Ursache.
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return "Koordinator: " + ue.Op + ": " + ue.Err.Error()
	}
	return err.Error()
}

// koordFehler macht aus einem Fehler des Koordinators Status und Text für
// das Fenster: Ein älterer Koordinator ist „zu alt“; 4xx behalten Code und
// Meldung des Koordinators (401/403 erklärt); sonst 502.
func koordFehler(err error) (int, string) {
	if errors.Is(err, client.ErrNotSupported) {
		return http.StatusNotFound, textZuAlt
	}
	se, ok := errors.AsType[*client.StatusError](err)
	switch {
	case !ok || se.StatusCode < 400 || se.StatusCode > 499:
		return http.StatusBadGateway, explain(err)
	case se.StatusCode == http.StatusUnauthorized || se.StatusCode == http.StatusForbidden:
		return se.StatusCode, explain(err)
	}
	return se.StatusCode, cmp.Or(se.Message, explain(err))
}

func antwortJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// antwortFehler schreibt {"fehler": text}; das Fenster zeigt text.
func antwortFehler(w http.ResponseWriter, code int, text string) {
	antwortJSON(w, code, struct {
		Fehler string `json:"fehler"`
	}{text})
}
