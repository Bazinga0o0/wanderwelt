package app

// Selbst-Update über den Koordinator: Das Versions-Manifest nennt unter
// starter die aktuelle Release-Version des Starters (Version, URL
// wanderwelt:<datei>, SHA-256, Größe). Beim Start und dann alle 6 Stunden
// vergleicht der Starter sie mit seiner eigenen. Ist sie größer, lädt er
// die exe über den angemeldeten Koordinator-Client nach
// Home()/update/wanderwelt-<version>.exe, prüft Hash und Größe, wartet, bis
// dieser PC nicht hostet und keine Aktion läuft, prüft den Hash noch einmal
// und startet die neue exe. Die nimmt den vorhandenen Update-Weg (Run: sie
// findet die Sperre belegt, schickt „update“, diese Instanz endet, die neue
// kopiert sich nach Home() und startet dort mit --aktualisiert).
//
// Nur Release-Versionen nehmen teil: „dev“, Commit-Kurzformen, Snapshots
// und Vorabversionen aktualisieren sich nie von selbst (eine Log-Zeile).
// Gegen einen Kreislauf (etwa eine exe, die eine andere Version meldet als
// das Manifest) merkt sich Home()/update/zustand.json den letzten Versuch
// (Übergabe angestoßen, Datei fehlt beim Koordinator, Hash oder Größe
// falsch): Denselben Stand (SHA-256) versucht der Starter höchstens einmal
// in selfUpdateRetry. Fehler: weiterlaufen mit der alten Version, Log, höchstens
// ein Hinweis pro Tag; der Hinweis „aktualisiert sich“ kommt einmal pro
// Version.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	// selfUpdateEvery: So oft fragt der Starter nach einer neuen Version.
	selfUpdateEvery = 6 * time.Hour
	// selfUpdateRetry: Denselben Stand höchstens einmal in dieser Zeit
	// versuchen; so oft höchstens auch ein Fehler-Hinweis.
	selfUpdateRetry = 24 * time.Hour
	// selfUpdateMax begrenzt den Download, wenn das Manifest keine Größe
	// nennt.
	selfUpdateMax = 256 << 20
	// selfUpdateLabel ist die laufende Aktion (App.busy) während der
	// Übergabe an die neue Version.
	selfUpdateLabel = "Update"
	// updateDirName ist der Ordner in Home() für die geladene exe.
	updateDirName = "update"
	// updateStateFile in updateDirName: letzter Versuch und Hinweise.
	updateStateFile = "zustand.json"
)

// Im Test austauschbar.
var (
	// selfUpdatePoll: So oft prüft ein wartendes Update, ob der PC frei ist.
	selfUpdatePoll = time.Minute
	// selfUpdateHandover: So lange wartet diese Instanz nach dem Start der
	// neuen exe darauf, dass die sie zum Enden auffordert.
	selfUpdateHandover = 2 * time.Minute
	selfUpdateNow      = time.Now
	suLogf             = log.Printf
	// startNewVersion startet die geladene exe; sie übernimmt über den
	// Update-Weg (Run → updateRunning → handOffToInstalled).
	startNewVersion = func(exe string) error {
		cmd := exec.Command(exe)
		cmd.Dir = group.Home()
		if err := cmd.Start(); err != nil {
			return err
		}
		suLogf("Selbst-Update: %s gestartet (PID %d)", exe, cmd.Process.Pid)
		return cmd.Process.Release()
	}
)

// selfUpdateState ist der Zustand des Selbst-Updates im Prozess (App.su).
type selfUpdateState struct {
	mu     sync.Mutex
	logged map[string]string // Schlüssel → zuletzt geloggte Meldung (logOnce)
}

// updateState ist Home()/update/zustand.json.
type updateState struct {
	// Versuch: zuletzt versuchter Stand (Übergabe angestoßen, Datei fehlt
	// beim Koordinator, Hash oder Größe falsch, exe wie die installierte).
	TriedVersion string    `json:"versuch_version,omitempty"`
	TriedSHA256  string    `json:"versuch_sha256,omitempty"`
	TriedAt      time.Time `json:"versuch_zeit,omitzero"`
	// NoticeVersion: Für diese Version kam der Hinweis „aktualisiert sich“.
	NoticeVersion string `json:"hinweis_version,omitempty"`
	// ErrorNoticeAt: letzter Fehler-Hinweis (höchstens einer pro Tag).
	ErrorNoticeAt time.Time `json:"hinweis_fehler_zeit,omitzero"`
}

func updateDir() string { return filepath.Join(group.Home(), updateDirName) }

func loadUpdateState() updateState {
	var st updateState
	b, err := os.ReadFile(filepath.Join(updateDir(), updateStateFile))
	if err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			suLogf("Selbst-Update: %s unlesbar, fange leer an: %v", updateStateFile, err)
			st = updateState{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		suLogf("Selbst-Update: %s: %v", updateStateFile, err)
	}
	return st
}

func saveUpdateState(st updateState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(updateDir(), updateStateFile), b, 0o644)
}

// compareSemVer vergleicht zwei Release-Versionen "MAJOR.MINOR.PATCH" (mit
// oder ohne "v" davor): -1, 0 oder 1 für a <, =, > b. ok false, wenn eine
// keine Release-Version ist (api.ReleaseVersion: "dev", Commit-Kurzform,
// Snapshot, Vorabversion).
func compareSemVer(a, b string) (int, bool) {
	va, ok := api.ReleaseVersion(a)
	if !ok {
		return 0, false
	}
	vb, ok := api.ReleaseVersion(b)
	if !ok {
		return 0, false
	}
	for i := range va {
		switch {
		case va[i] < vb[i]:
			return -1, true
		case va[i] > vb[i]:
			return 1, true
		}
	}
	return 0, true
}

// selfUpdateLoop prüft gleich nach dem Start und dann alle selfUpdateEvery,
// bis das Programm endet.
func (a *App) selfUpdateLoop() {
	t := time.NewTicker(selfUpdateEvery)
	defer t.Stop()
	for {
		a.selfUpdate(a.ctx)
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// suLogOnce loggt die Meldung unter key nur, wenn sie sich seit dem
// letzten Mal geändert hat.
func (a *App) suLogOnce(key, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	a.su.mu.Lock()
	if a.su.logged == nil {
		a.su.logged = map[string]string{}
	}
	same := a.su.logged[key] == msg
	a.su.logged[key] = msg
	a.su.mu.Unlock()
	if !same {
		suLogf("Selbst-Update: %s", msg)
	}
}

// selfUpdate ist eine Prüfung: neue Version laden und, sobald der PC frei
// ist, an sie übergeben. Kehrt zurück, wenn nichts zu tun ist, ein Fehler
// auftrat (Log, höchstens ein Hinweis pro Tag) oder die Übergabe läuft.
func (a *App) selfUpdate(ctx context.Context) {
	if _, ok := api.ReleaseVersion(a.version); !ok {
		a.suLogOnce("version", "aus – %q ist keine Release-Version (Entwicklungsstand)", a.version)
		return
	}
	c := a.client()
	if c == nil {
		return
	}
	m, err := retry401(ctx, func() (*api.Manifest, error) { return c.Manifest(ctx) })
	if err != nil {
		if ctx.Err() == nil {
			suLogf("Selbst-Update: Versions-Manifest: %v", err)
		}
		return
	}
	rel := m.Starter
	if rel == nil {
		cleanUpdateDir("")
		a.suLogOnce("stand", "der Koordinator nennt keine Starter-Version")
		return
	}
	newer, ok := compareSemVer(rel.Version, a.version)
	if !ok {
		cleanUpdateDir("")
		a.suLogOnce("stand", "die Starter-Version %q im Manifest ist keine Release-Version", rel.Version)
		return
	}
	if newer <= 0 {
		cleanUpdateDir("")
		a.suLogOnce("stand", "aktuell (diese Version %s, Koordinator %s)", a.version, rel.Version)
		return
	}
	v, _ := api.ReleaseVersion(rel.Version)
	version := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
	fileName := "wanderwelt-" + version + ".exe"
	cleanUpdateDir(fileName)

	scheme, _, _ := strings.Cut(rel.URL, ":")
	if !strings.EqualFold(scheme, "wanderwelt") {
		a.suFail(version, fmt.Errorf("url %q im Manifest ist keine Adresse beim Koordinator (wanderwelt:<datei>)", rel.URL))
		return
	}
	sum, ok := rel.StarterSHA256()
	if !ok {
		a.suFail(version, fmt.Errorf("sha256 %q im Manifest ist kein SHA-256", rel.SHA256))
		return
	}
	if st := loadUpdateState(); st.TriedSHA256 == sum {
		if since := selfUpdateNow().Sub(st.TriedAt); since >= 0 && since < selfUpdateRetry {
			a.suLogOnce("stand", "das Update auf %s wurde schon am %s versucht (diese Instanz: %s) – nächster Versuch frühestens %s",
				version, st.TriedAt.Format("02.01. 15:04"), a.version, st.TriedAt.Add(selfUpdateRetry).Format("02.01. 15:04"))
			return
		}
	}

	a.suLogOnce("stand", "neue Version %s (diese %s) – lade %s", version, a.version, rel.URL)
	exe, err := deps.FetchFile(ctx, api.Artifact{Name: "wanderwelt", URL: rel.URL, Hash: "sha256:" + sum},
		c, updateDir(), fileName, rel.Size, selfUpdateMax)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// Antwortet der Koordinator mit 4xx (Datei fehlt) oder passen Hash
		// oder Größe nicht, hilft ein neuer Download desselben Stands nicht:
		// merken, erst nach selfUpdateRetry wieder. Netzfehler: nächste Prüfung.
		if se, ok := errors.AsType[*client.StatusError](err); (ok && se.StatusCode >= 400 && se.StatusCode < 500) || errors.Is(err, deps.ErrMismatch) {
			a.rememberTry(version, sum)
		}
		a.suFail(version, err)
		return
	}

	// Dieselben Bytes wie die installierte Kopie: Die exe meldet eine andere
	// Version als das Manifest; eine Übergabe änderte nichts.
	if same, err := sameContent(exe, installedExe()); err == nil && same {
		a.rememberTry(version, sum)
		a.suFail(version, fmt.Errorf("%s ist dieselbe Datei wie die installierte %s – passt die Version im Manifest zur exe?", rel.URL, installedExe()))
		return
	}

	if !a.waitForUpdateSlot(ctx) {
		return
	}
	// Ab hier ist die Aktion belegt (App.busy): kein Spielen, kein
	// Dauerbetrieb, bis die Übergabe läuft oder scheitert.
	if err := checkUpdateFile(exe, sum, rel.Size); err != nil {
		os.Remove(exe)
		a.end()
		a.suFail(version, err)
		return
	}
	st := loadUpdateState()
	notice := st.NoticeVersion != version
	st.TriedVersion, st.TriedSHA256, st.TriedAt = version, sum, selfUpdateNow()
	st.NoticeVersion = version
	if err := saveUpdateState(st); err != nil {
		// Ohne Merker droht ein Kreislauf: lieber nicht übergeben.
		a.end()
		a.suFail(version, fmt.Errorf("%s nicht geschrieben: %w", updateStateFile, err))
		return
	}
	suLogf("Selbst-Update: übergebe von %s an %s (%s)", a.version, version, exe)
	if notice {
		go notify("wanderwelt aktualisiert sich auf Version " + version + " …")
	}
	if err := startNewVersion(exe); err != nil {
		a.end()
		a.suFail(version, fmt.Errorf("neue Version nicht gestartet: %w", err))
		return
	}
	// Die neue exe schickt „update“ (quitForUpdate): Dann endet ctx.
	select {
	case <-ctx.Done():
	case <-time.After(selfUpdateHandover):
		a.end()
		a.suFail(version, fmt.Errorf("die neue Version hat nach %v nicht übernommen", selfUpdateHandover))
	}
}

// rememberTry merkt sich den Stand sum als versucht (jetzt): Denselben
// versucht der Starter erst nach selfUpdateRetry wieder.
func (a *App) rememberTry(version, sum string) {
	st := loadUpdateState()
	st.TriedVersion, st.TriedSHA256, st.TriedAt = version, sum, selfUpdateNow()
	if err := saveUpdateState(st); err != nil {
		suLogf("Selbst-Update: %s nicht geschrieben: %v", updateStateFile, err)
	}
}

// waitForUpdateSlot wartet, bis dieser PC weder hostet noch mitspielt,
// keine Aktion läuft, niemand den Welt-Ordner benutzt und weder das
// Programm noch die Windows-Sitzung endet, und belegt dann die Aktion
// (selfUpdateLabel). false: ctx endete vorher.
func (a *App) waitForUpdateSlot(ctx context.Context) bool {
	logged := false
	for {
		a.mu.Lock()
		free := !a.quitting && !a.ending && a.busy == "" && a.hostCancel == nil &&
			a.joinCancel == nil && a.worldHolder == ""
		what := a.busy
		switch {
		case a.hostCancel != nil:
			what = "hostet"
		case what == "" && a.joinCancel != nil:
			what = "spielt mit"
		case what == "" && a.worldHolder != "":
			what = "Welt-Ordner: " + a.worldHolder
		}
		if free {
			a.busy = selfUpdateLabel
		}
		a.mu.Unlock()
		if free {
			a.refresh()
			return true
		}
		if !logged {
			suLogf("Selbst-Update: wartet, bis dieser PC frei ist (%s)", what)
			logged = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(selfUpdatePoll):
		}
	}
}

// checkUpdateFile prüft die geladene exe unmittelbar vor dem Start noch
// einmal (sie lag womöglich Stunden).
func checkUpdateFile(exe, sum string, size int64) error {
	fi, err := os.Stat(exe)
	if err != nil {
		return err
	}
	if size > 0 && fi.Size() != size {
		return fmt.Errorf("%s hat %d Bytes, erwartet %d", exe, fi.Size(), size)
	}
	got, err := fileSHA256(exe)
	if err != nil {
		return err
	}
	if got != sum {
		return fmt.Errorf("%s hat sha256 %s, erwartet %s", exe, got, sum)
	}
	return nil
}

// suFail: Das Update auf version ging nicht. Log; ein Hinweis höchstens
// einmal in selfUpdateRetry (über Neustarts hinweg).
func (a *App) suFail(version string, err error) {
	suLogf("Selbst-Update auf %s gescheitert, wanderwelt läuft mit %s weiter: %v", version, a.version, err)
	st := loadUpdateState()
	now := selfUpdateNow()
	if since := now.Sub(st.ErrorNoticeAt); !st.ErrorNoticeAt.IsZero() && since >= 0 && since < selfUpdateRetry {
		return
	}
	st.ErrorNoticeAt = now
	if err := saveUpdateState(st); err != nil {
		suLogf("Selbst-Update: %s nicht geschrieben: %v", updateStateFile, err)
	}
	go notify(fmt.Sprintf("wanderwelt konnte sich nicht auf Version %s aktualisieren und läuft mit %s weiter. Einzelheiten stehen im Log.", version, a.version))
}

// cleanUpdateDir löscht in Home()/update alles außer zustand.json und keep
// (ältere geladene Versionen, abgebrochene Downloads, die exe nach einem
// gelungenen Update). Läuft eine davon noch (die neue exe endet gerade),
// bleibt sie bis zur nächsten Prüfung liegen.
func cleanUpdateDir(keep string) {
	entries, err := os.ReadDir(updateDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); n != updateStateFile && n != keep {
			if err := os.RemoveAll(filepath.Join(updateDir(), n)); err == nil {
				suLogf("Selbst-Update: %s gelöscht", n)
			}
		}
	}
}
