package app

// Fehlerbericht und wandernde Logs (Paket fehlerbericht): „Fehlerbericht
// erstellen“ im Fenster (Aktion fehlerbericht) und im Tray-Menü packt ein Zip
// nach Downloads (lokal, nie ein Upload); die Host-Sitzung legt vor jedem
// Stand die wanderwelt-Logzeilen in <Welt>/wanderwelt-logs/.

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fehlerbericht"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/winsys"
)

// berichtManifestWait: So lange darf der Bericht auf das Manifest des
// Koordinators warten (ohne es entsteht er trotzdem).
const berichtManifestWait = 8 * time.Second

// Im Test austauschbar.
var (
	// downloadsOrdner liefert den Ordner für den Bericht: der Known Folder
	// „Downloads“, sonst %USERPROFILE%\Downloads.
	downloadsOrdner = func() (string, error) {
		if d := os.Getenv("WANDERWELT_DOWNLOADS"); d != "" {
			return d, nil
		}
		d, err := winsys.DownloadsDir()
		if err == nil && d != "" {
			return d, nil
		}
		h, herr := os.UserHomeDir()
		if herr != nil {
			return "", herr
		}
		return filepath.Join(h, "Downloads"), nil
	}
	berichtJetzt = time.Now
)

// berichtLaeuft: Ein zweiter Bericht während der Erstellung wird abgelehnt.
var berichtLaeuft atomic.Bool

// weltLogsHaken ist Deps.WeltLogs der Host-Sitzung dieses PCs: Die
// wanderwelt-Logzeilen des Tages kommen in <Welt>/wanderwelt-logs/. Fehler
// stehen im Log, nie im Weg (host fängt auch einen Absturz ab).
func weltLogsHaken(maschinenID string) func(worldDir string) {
	return func(worldDir string) {
		s := saeuberer()
		if err := fehlerbericht.SchreibeWeltLogs(worldDir, LogPath(), maschinenID, time.Now(), s); err != nil {
			log.Printf("host: wanderwelt-logs: %v", err)
		}
	}
}

// saeuberer: der Säuberer dieses PCs samt der bekannten Geheimnisse (Token
// der Gruppendatei, RCON-Passwörter der Server-Ordner).
func saeuberer() *fehlerbericht.Saeuberer {
	return fehlerbericht.Standard(bekannteGeheimnisse()...)
}

// bekannteGeheimnisse liest die Werte, die in keiner Form im Bericht oder in
// den Logs stehen dürfen: das Token der aktiven Gruppe und das RCON-Passwort
// jedes lokalen Servers (server.properties).
func bekannteGeheimnisse() []string {
	var out []string
	if g, err := group.LoadGroup(group.GroupPath()); err == nil && g != nil {
		out = append(out, g.Token)
	}
	for _, id := range group.LocalWorlds() {
		dir, err := group.ServerDir(id)
		if err != nil {
			continue
		}
		out = append(out, rconPasswort(filepath.Join(dir, "server.properties")))
	}
	return out
}

// rconPasswort liest rcon.password aus einer server.properties ("" ohne).
func rconPasswort(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "rcon.password="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// fehlerberichtErstellen packt den Bericht und liefert den Pfad.
func (a *App) fehlerberichtErstellen(ctx context.Context) (string, error) {
	if !berichtLaeuft.CompareAndSwap(false, true) {
		return "", errBerichtLaeuft
	}
	defer berichtLaeuft.Store(false)

	ziel, err := downloadsOrdner()
	if err != nil {
		return "", fmt.Errorf("Downloads-Ordner nicht gefunden: %w", err)
	}
	in := fehlerbericht.Eingabe{
		Version:    a.version,
		System:     systemText(),
		StarterLog: LogPath(),
		Jetzt:      berichtJetzt(),
		San:        saeuberer(),
	}
	for _, id := range group.LocalWorlds() {
		if dir, err := group.ServerDir(id); err == nil {
			in.Welten = append(in.Welten, fehlerbericht.Welt{ID: id, ServerDir: dir})
		}
	}
	if z, err := json.MarshalIndent(a.berichtZustand(), "", "  "); err == nil {
		in.Zustand = z
	} else {
		in.Hinweise = append(in.Hinweise, "Zustand nicht lesbar: "+err.Error())
	}
	in.Manifest = a.berichtManifest(ctx)
	pfad, err := fehlerbericht.Erstellen(in, ziel)
	if err != nil {
		return "", err
	}
	log.Printf("Fehlerbericht: %s", filepath.Base(pfad))
	return pfad, nil
}

var errBerichtLaeuft = fmt.Errorf("Der Fehlerbericht wird gerade erstellt.")

// berichtZustand ist der Zustand der Instanz ohne E-Mail, Spielernamen und
// Gruppencode (alles andere hilft bei der Fehlersuche).
func (a *App) berichtZustand() fensterZustandBericht {
	z := a.zustand()
	out := fensterZustandBericht{
		Version: z.Version, Phase: z.Phase, Detail: z.Detail, Busy: z.Busy, LastErr: z.LastErr,
		Hosting: z.Hosting, HostAddr: z.HostAddr, StatusErr: z.StatusErr, Welt: z.Welt,
		Autostart: z.Autostart, RAMTotalMB: z.RAMTotalMB, Profil: z.Profil,
		Fortsetzen: z.Fortsetzen != nil, WechselSperre: z.WechselSperre, Gruppen: len(z.Gruppen),
	}
	out.Notfallkopie.Behalten, out.Notfallkopie.Vorhanden = z.Notfallkopie.Behalten, z.Notfallkopie.Vorhanden
	out.Notfallkopie.NichtHochgeladen = z.Notfallkopie.NichtHochgeladen
	out.Notfallkopie.Zeit, out.Notfallkopie.Groesse = z.Notfallkopie.Zeit, z.Notfallkopie.Groesse
	if s := z.Status; s != nil {
		out.Status = &berichtStatus{
			HostReady: s.HostReady, HostUploading: s.HostUploading, Spieler: len(s.Players),
			HeadGeneration: s.HeadGeneration, WorldID: s.WorldID, SnapshotIntervalS: s.SnapshotIntervalS,
			Maintenance: s.Maintenance != nil, Handover: s.Handover != nil, RestorePending: s.RestorePending != nil,
			Members: s.Members, Worlds: len(s.Worlds), RunningWorld: s.RunningWorld,
		}
		if s.Host != nil {
			out.Status.HostSeit = fmt.Sprint(s.Host)
		}
	}
	if z.Me != nil {
		out.Rolle = &berichtRolle{Admin: z.Me.Admin, Op: z.Me.Op}
	}
	for i := len(z.Hinweise); i > 0 && len(out.Hinweise) < 10; i-- {
		out.Hinweise = append(out.Hinweise, z.Hinweise[i-1].Text)
	}
	return out
}

// fensterZustandBericht und Verwandte: der Zustand fürs Zip (zustand.json).
type fensterZustandBericht struct {
	Version       string
	Phase         string
	Detail        string
	Busy          string
	LastErr       string
	Hosting       bool
	HostAddr      string
	StatusErr     string
	Autostart     bool
	RAMTotalMB    int
	Profil        api.Profile
	Welt          fensterapi.WeltZustand
	Fortsetzen    bool
	WechselSperre string
	Gruppen       int
	Notfallkopie  struct {
		Behalten, Vorhanden, NichtHochgeladen bool
		Zeit                                  time.Time
		Groesse                               int64
	}
	Status   *berichtStatus
	Rolle    *berichtRolle
	Hinweise []string
}

type berichtStatus struct {
	HostReady, HostUploading bool
	Spieler                  int
	HeadGeneration           int64
	WorldID                  string
	SnapshotIntervalS        int
	Maintenance, Handover    bool
	RestorePending           bool
	Members                  int
	Worlds                   int
	RunningWorld             string
	HostSeit                 string `json:",omitempty"`
}

type berichtRolle struct{ Admin, Op bool }

// berichtManifest holt das Versions-Manifest als Text (Version, Mods mit
// Hash); leer, wenn der Koordinator nicht antwortet.
func (a *App) berichtManifest(ctx context.Context) string {
	c := a.client()
	if c == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, berichtManifestWait)
	defer cancel()
	m, err := c.Manifest(ctx)
	if err != nil || m == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Minecraft: %s\n", m.MinecraftVersion)
	if m.Starter != nil {
		fmt.Fprintf(&b, "Starter im Manifest: %s\n", m.Starter.Version)
	}
	fmt.Fprintf(&b, "Fabric-Server: %s (%s)\n", m.FabricServer.Name, m.FabricServer.Hash)
	fmt.Fprintf(&b, "Java: %s (%s)\n", m.Java.Name, m.Java.Hash)
	fmt.Fprintf(&b, "tunnel: %s (%s)\n", m.Tunnel.Name, m.Tunnel.Hash)
	b.WriteString("Mods:\n")
	for _, mod := range m.Mods {
		fmt.Fprintf(&b, "  - %s (%s)\n", mod.Name, mod.Hash)
	}
	return b.String()
}

// systemText: Windows, Prozessor, Arbeitsspeicher und freier Platz am
// Datenordner.
func systemText() string {
	s := winsys.SystemInfo()
	if mb := ramTotalMB(); mb > 0 {
		s += fmt.Sprintf("Arbeitsspeicher: %d MiB\n", mb)
	}
	if free, err := winsys.FreeSpace(group.Home()); err == nil {
		s += fmt.Sprintf("Freier Platz am Datenordner: %s\n", humanSize(free))
	}
	return s
}

// berichtAktion: „Fehlerbericht erstellen“ aus Fenster und Tray: Zip packen,
// den Ordner im Explorer öffnen und dem Nutzer sagen, wo es liegt. Liefert
// den Pfad.
func (a *App) berichtAktion(ctx context.Context) (string, error) {
	pfad, err := a.fehlerberichtErstellen(ctx)
	if err != nil {
		return "", err
	}
	if err := openFolder(filepath.Dir(pfad)); err != nil {
		log.Printf("Fehlerbericht: Ordner nicht geöffnet: %v", err)
	}
	melden(hinweisInfo, berichtHinweis(pfad), toast)
	return pfad, nil
}

// berichtHinweis: der Text im Fenster nach dem Erstellen.
func berichtHinweis(pfad string) string {
	return "Fehlerbericht gespeichert: " + pfad + " – er liegt nur auf diesem PC (kein Upload). " +
		"Geheimnisse sind entfernt; die Logs können Spielernamen und Chat enthalten, bitte vor dem Weitergeben ansehen."
}

// fensterBericht: POST /v1/fehlerbericht.
func (a *App) fensterBericht(w http.ResponseWriter, r *http.Request) {
	if berichtLaeuft.Load() {
		antwortFehler(w, http.StatusConflict, errBerichtLaeuft.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	pfad, err := a.berichtAktion(ctx)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errBerichtLaeuft) {
			code = http.StatusConflict
		}
		log.Printf("Fehlerbericht: %v", err)
		antwortFehler(w, code, cmp.Or(err.Error(), "Fehlerbericht nicht erstellt"))
		return
	}
	antwortJSON(w, http.StatusOK, map[string]string{"pfad": pfad})
}

// berichtTray: der Tray-Eintrag; Fehler als Dialog (das Fenster ist nicht
// unbedingt offen).
func (a *App) berichtTray() {
	if _, err := a.berichtAktion(context.Background()); err != nil {
		showError("Der Fehlerbericht konnte nicht erstellt werden: " + err.Error())
	}
}
