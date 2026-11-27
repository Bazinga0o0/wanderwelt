package fenster

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Attrappe der lokalen Steuerung (die echte Server-Seite baut Task 5) und
// Beispieldaten für Rauchtest, Bildschirmfotos und Handprobe.

const attrappenToken = "attrappe-0123456789abcdef"

// attrappe ist eine Fake-Steuerung mit control.json in home.
type attrappe struct {
	srv  *httptest.Server
	home string

	mu        sync.Mutex
	zaehler   map[string]int // GET-Pfad → Anzahl
	aktionen  []string       // "name körper"
	z         fensterapi.Zustand
	staende   api.GenerationsResponse
	saves     []fensterapi.Speicherstand
	log       string
	members   []api.MemberInfo
	statistik api.Stats
	fehler    map[string]string // Aktion → {"fehler": …} mit 400
	beiAktion func(a *attrappe, name string, body []byte)
	// protokoll (darf nil sein) bekommt jede Anfrage außer /v1/zustand.
	protokoll func(format string, args ...any)
}

func starteAttrappe(t testing.TB, home string) *attrappe {
	t.Helper()
	a := &attrappe{home: home, zaehler: map[string]int{}, fehler: map[string]string{}}
	a.setzeBeispiel(time.Now(), true)
	a.srv = httptest.NewServer(http.HandlerFunc(a.bedienen))
	t.Cleanup(a.srv.Close)
	a.schreibeControl(t)
	return a
}

func (a *attrappe) schreibeControl(t testing.TB) {
	t.Helper()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(a.srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	data, _ := json.Marshal(map[string]any{"port": p, "token": attrappenToken, "pid": os.Getpid()})
	if err := os.WriteFile(filepath.Join(a.home, "control.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (a *attrappe) anzahl(pfad string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.zaehler[pfad]
}

func (a *attrappe) bedienen(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+attrappenToken || r.Host != strings.TrimPrefix(a.srv.URL, "http://") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	antwort := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	if a.protokoll != nil && r.URL.Path != "/v1/zustand" && r.Method == http.MethodGet {
		a.protokoll("%s %s", r.Method, r.URL.RequestURI())
	}
	if r.Method == http.MethodGet {
		a.zaehler[r.URL.Path]++
		switch r.URL.Path {
		case "/v1/zustand":
			antwort(a.z)
		case "/v1/staende":
			antwort(a.staende)
		case "/v1/speicherstaende":
			antwort(a.saves)
		case "/v1/mitglieder":
			antwort(a.members)
		case "/v1/statistik":
			antwort(a.statistik)
		case "/v1/log":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, a.log)
		default:
			http.NotFound(w, r)
		}
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/v1/")
	body, _ := io.ReadAll(r.Body)
	if a.protokoll != nil {
		a.protokoll("POST %s %s", r.URL.Path, body)
	}
	a.aktionen = append(a.aktionen, strings.TrimSpace(name+" "+string(body)))
	if f := a.fehler[name]; f != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"fehler": f})
		return
	}
	if a.beiAktion != nil {
		a.beiAktion(a, name, body)
	}
	switch name {
	case "play", "beenden", "import":
		w.WriteHeader(http.StatusAccepted)
	default:
		antwort(map[string]bool{"ok": true})
	}
}

// setzeBeispiel füllt die Attrappe mit Beispieldaten (Zeit jetzt).
func (a *attrappe) setzeBeispiel(jetzt time.Time, admin bool) {
	a.z = *beispielZustand(jetzt, admin, true)
	a.staende = *beispielStaende(jetzt)
	a.saves = beispielSaves(jetzt)
	a.log = beispielLog(jetzt)
	a.members = beispielMitglieder()
	a.statistik = *beispielStatistik(jetzt)
}

// ---- Beispieldaten ----

func beispielZustand(jetzt time.Time, admin, host bool) *fensterapi.Zustand {
	z := &fensterapi.Zustand{
		Version:    "0.9.3",
		Phase:      "Bereit",
		RAMTotalMB: 32 * 1024 * 95 / 100,
		Autostart:  true,
		Profil:     api.Profile{XmxMB: 6144, ViewDistance: 12, SimulationDistance: 10, C2ME: true, C2METhreads: 4},
		Me:         &api.Me{Email: "lukas@example.de", MinecraftName: "Lukas", Admin: admin, MinecraftAddress: "wanderwelt.example.de"},
		Status: &api.Status{
			HeadGeneration: 412, WorldID: "w-2026-11-03", MinecraftAddress: "wanderwelt.example.de",
			HeadCreated: jetzt.Add(-3*time.Minute - 20*time.Second), SnapshotIntervalS: 300,
		},
		Hinweise: []fensterapi.Hinweis{{Seq: 1, Zeit: jetzt.Add(-time.Hour), Art: "info", Text: "wanderwelt ist bereit."}},
		Benachrichtigungen: []fensterapi.BenachSchalter{
			{Art: "host_startet", Titel: "Jemand hostet", Hilfe: "Ein Mitglied startet den Server – die Welt ist gleich bereit.", An: true},
			{Art: "welt_online", Titel: "Welt ist online", Hilfe: "Der Server läuft, ihr könnt euch verbinden.", An: true},
			{Art: "spieler", Titel: "Spieler kommen und gehen", Hilfe: "Wer der Welt beitritt oder sie verlässt (mehrere zusammen in einer Meldung)."},
		},
	}
	z.Status.DiscordChannel = admin
	if host {
		z.Status.Host = &api.HostInfo{Email: "mara@example.de", MinecraftName: "Mara", MachineID: "pc-mara"}
		z.Status.HostReady = true
		z.Status.Players = []string{"Mara", "Jonas", "Lukas"}
		z.Phase = "Spielt mit"
	}
	return z
}

func beispielStaende(jetzt time.Time) *api.GenerationsResponse {
	gen := func(welt string, n int64, vor time.Duration, host string, spieler []string, size int64) api.GenerationInfo {
		return api.GenerationInfo{WorldID: welt, Number: n, Created: jetzt.Add(-vor), HostName: host, Players: spieler, Size: size, MinecraftVersion: "26.2"}
	}
	aktiv := api.WorldInfo{WorldID: "w-2026-11-03", Active: true}
	aktiv.Generations = []api.GenerationInfo{
		gen("w-2026-11-03", 412, 3*time.Minute, "Mara", []string{"Mara", "Jonas", "Lukas"}, 1288490188),
		gen("w-2026-11-03", 411, 8*time.Minute, "Mara", []string{"Mara", "Jonas"}, 1285000000),
		gen("w-2026-11-03", 410, 13*time.Minute, "Mara", []string{"Mara"}, 1284000000),
		gen("w-2026-11-03", 405, 2*time.Hour, "Jonas", []string{"Jonas", "Lukas"}, 1270000000),
		gen("w-2026-11-03", 398, 26*time.Hour, "Lukas", nil, 1250000000),
		gen("w-2026-11-03", 371, 5*24*time.Hour, "Lukas", []string{"Lukas"}, 1100000000),
	}
	aktiv.Generations[3].RestoredFrom = &api.GenRef{WorldID: "w-2026-11-03", Number: 398}
	archiv := api.WorldInfo{WorldID: "w-2026-10-12", Generations: []api.GenerationInfo{
		gen("w-2026-10-12", 88, 21*24*time.Hour, "Jonas", []string{"Jonas"}, 734003200),
		gen("w-2026-10-12", 87, 21*24*time.Hour+time.Hour, "Jonas", nil, 730000000),
	}}
	return &api.GenerationsResponse{
		Worlds:  []api.WorldInfo{aktiv, archiv},
		Storage: &api.StorageInfo{UsedBytes: 7 << 30, BudgetBytes: 20 << 30, DiskFreeBytes: 41 << 30},
	}
}

func beispielSaves(jetzt time.Time) []fensterapi.Speicherstand {
	return []fensterapi.Speicherstand{
		{Folder: "Neue Welt", LevelName: "Neue Welt", Version: "26.2", LastPlayed: jetzt.Add(-50 * time.Hour), Size: 312 << 20, PlayerFile: true},
		{Folder: "Inselbau", LevelName: "Inselbau mit Jonas", Version: "26.2", LastPlayed: jetzt.Add(-9 * 24 * time.Hour), Size: 1 << 30, PlayerFile: false},
		{Folder: "Alt", LevelName: "Alt", Version: "1.21.4", LastPlayed: jetzt.Add(-400 * 24 * time.Hour), Size: 95 << 20, PlayerFile: true,
			Problem: "„Alt“ ist mit Minecraft 1.21.4 gespeichert. Welten vor 26.1 werden nicht übernommen."},
	}
}

// beispielStatistik: vier Mitglieder, eines ohne Hostzeit, eines mit sehr
// kleinem Anteil; im 30-Tage-Zeitraum trägt ein anderes am meisten.
func beispielStatistik(jetzt time.Time) *api.Stats {
	periode := func(ms ...api.MemberStats) api.StatsPeriod {
		var gesamt int64
		for _, m := range ms {
			gesamt += m.HostS
		}
		for i := range ms {
			if gesamt > 0 {
				ms[i].HostShare = float64(ms[i].HostS) / float64(gesamt)
			}
		}
		return api.StatsPeriod{HostTotalS: gesamt, Members: ms}
	}
	h := int64(3600)
	return &api.Stats{
		Since: jetzt.Add(-90 * 24 * time.Hour),
		Total: periode(
			api.MemberStats{MinecraftName: "Lukas", PlayS: 212*h + 1500, HostS: 140 * h, HostSessions: 61},
			api.MemberStats{MinecraftName: "Mara", PlayS: 180 * h, HostS: 75*h + 600, HostSessions: 33},
			api.MemberStats{MinecraftName: "Jonas", PlayS: 95 * h, HostS: 20 * h, HostSessions: 9},
			api.MemberStats{MinecraftName: "KimTheBuilder", PlayS: 40*h + 30, HostS: 40, HostSessions: 1},
			api.MemberStats{MinecraftName: "Neuling", PlayS: 0, HostS: 0, HostSessions: 0},
		),
		Last30: periode(
			api.MemberStats{MinecraftName: "Lukas", PlayS: 30 * h, HostS: 6 * h, HostSessions: 5},
			api.MemberStats{MinecraftName: "Mara", PlayS: 41 * h, HostS: 22 * h, HostSessions: 14},
			api.MemberStats{MinecraftName: "Jonas", PlayS: 12 * h, HostS: 0, HostSessions: 0},
			api.MemberStats{MinecraftName: "KimTheBuilder", PlayS: 50, HostS: 0, HostSessions: 0},
			api.MemberStats{MinecraftName: "Neuling", PlayS: 0},
		),
	}
}

func beispielMitglieder() []api.MemberInfo {
	return []api.MemberInfo{
		{Email: "lukas@example.de", MinecraftName: "Lukas", Admin: true, Op: true, Online: true},
		{Email: "mara@example.de", MinecraftName: "Mara", Op: true, Online: true},
		{Email: "jonas.berger@example.de", MinecraftName: "Jonas", Online: true},
		{Email: "kim@example.de", MinecraftName: "KimTheBuilder"},
	}
}

func beispielLog(jetzt time.Time) string {
	var b strings.Builder
	zeilen := []string{
		"wanderwelt 0.9.3 startet (Home C:\\Users\\lukas\\AppData\\Local\\wanderwelt, PID 4120)",
		"Tray bereit",
		"Status: Host Mara, Spieler [Mara Jonas]",
		"Steuerung: play",
		"Spielen: Mara hostet – verbinde als Mitspieler",
		"Launcher geöffnet (Profil wanderwelt)",
		"Status: Fehler beim Abruf: Get \"https://wanderwelt.example.de/v1/status\": context deadline exceeded",
		"Status: Host Mara, Spieler [Mara Jonas Lukas]",
	}
	for i := 0; i < 40; i++ {
		t := jetzt.Add(time.Duration(i-40) * 47 * time.Second)
		fmt.Fprintf(&b, "%s %s\n", t.Format("2006/01/02 15:04:05"), zeilen[i%len(zeilen)])
	}
	return b.String()
}

func beispielAnfragen(jetzt time.Time) []api.JoinRequestInfo {
	return []api.JoinRequestInfo{
		{Email: "tim@example.de", MinecraftName: "Tim_Redstone", Requested: jetzt.Add(-3 * time.Hour)},
		{Email: "a-sehr-lange-adresse.mit.vielen.teilen@subdomain.beispiel-universitaet.example.de", MinecraftName: "Zwoelf_Zeichen", Requested: jetzt.Add(-30 * time.Second)},
	}
}
