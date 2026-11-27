// Package bench erhebt die Messwerte, die ein Starter für die Wahl des Hosts
// meldet (Leistung eines CPU-Kerns, Upload zum Koordinator, RAM), und
// verwaltet das Leistungsprofil des PCs (Dir()/profil.yaml), mit dem er den
// Minecraft-Server startet, wenn er hostet.
package bench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"gopkg.in/yaml.v3"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// CPU-Messung: Der Wert ist der Durchsatz von SHA-256 auf einem Kern in MB/s
// (10^6 Byte pro Sekunde). Gerechnet wird über mindestens cpuMinBytes und
// mindestens cpuMinTime. Ein aktueller Kern mit SHA-Befehlen schafft 256 MiB
// in gut 0,1 s; das ist zu kurz für eine verlässliche Zeitmessung (Taktanstieg,
// grobe Uhr unter Windows). So dauert die Messung auf jedem PC ab 256 MB/s
// etwa 1 s, nur langsamere brauchen für die 256 MiB länger.
const (
	cpuMinBytes = 256 << 20
	cpuMinTime  = time.Second
	// cpuWarmup wird vorher ungezählt gerechnet, damit der Kern hochtaktet.
	cpuWarmup = 100 * time.Millisecond
	cpuChunk  = 1 << 20
)

const (
	profileFileName = "profil.yaml"
	profileHeader   = "# Leistungsprofil dieses PCs; gilt, wenn er hostet.\n" +
		"# Fehlende Schlüssel nehmen die Startwerte aus der Hardware.\n" +
		"# dauerbetrieb: true – dieser PC hostet von selbst, sobald niemand hostet,\n" +
		"# auch ohne Spieler (nur am Netz; im Tray umschaltbar, Standard aus).\n"

	// minXmxMB: Der Server startet mit -Xms1024M; ein kleineres Xmx lehnt
	// Java ab.
	minXmxMB = 1024
	// Erlaubter Bereich für view-distance und simulation-distance in
	// server.properties.
	minDistance = 3
	maxDistance = 32
)

// Measure erhebt die Messwerte für die Wahl des Hosts:
//   - CPUScore: SHA-256-Durchsatz eines Kerns in MB/s (siehe cpuScore),
//   - UploadMbps: Upload zum Koordinator in Mbit/s (client.SpeedTest),
//   - RAMFreeMB, RAMTotalMB: verfügbarer und gesamter RAM in MiB (wie -Xmx),
//   - PingMs: Median von 7 Round-Trips zum Koordinator (client.Ping); nur
//     gesetzt, wenn der Koordinator ping_ms kennt (GET /v1/ping antwortet) und
//     die Messung gelang, sonst 0 (fehlt; in der Host-Wahl neutral),
//   - XmxMB: der übergebene Wert, also das Xmx des Profils, mit dem dieser
//     PC hosten würde.
//
// Die Messungen laufen nacheinander, damit sich CPU-Messung und Upload nicht
// gegenseitig stören (der Ping kommt zuerst, solange die Leitung und der Kern
// ruhig sind); zusammen dauern sie etwa 1 s plus den Upload von 8 MiB.
// Scheitert die Ping-Messung, fehlt PingMs (0) – Spielen geht trotzdem weiter.
// Scheitert die Upload-Messung, zählt der letzte gemessene Wert dieses
// Prozesses (sonst 0) – Spielen geht trotzdem weiter. Scheitert eine andere
// Messung oder endet ctx, ist das Ergebnis leer und der Fehler nennt die
// Messung.
func Measure(ctx context.Context, c *client.Client, xmxMB int) (api.Score, error) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return api.Score{}, fmt.Errorf("RAM-Messung: %w", err)
	}
	pingMs := measurePing(ctx, c)
	cpu, err := measureCPU(ctx)
	if err != nil {
		return api.Score{}, fmt.Errorf("CPU-Messung: %w", err)
	}
	up, err := c.SpeedTest(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return api.Score{}, fmt.Errorf("Upload-Messung: %w", err)
		}
		up = lastUpload.get()
		log.Printf("bench: Upload-Messung gescheitert, nehme %.1f Mbit/s (letzter Wert): %v", up, err)
	} else {
		lastUpload.set(up)
	}
	return api.Score{
		UploadMbps: up,
		CPUScore:   cpu,
		RAMFreeMB:  int(vm.Available >> 20),
		RAMTotalMB: int(vm.Total >> 20),
		XmxMB:      xmxMB,
		PingMs:     pingMs,
	}, nil
}

// measurePing liefert den Ping zum Koordinator in ms für Score.PingMs, 0 wenn
// er fehlen soll: Messung gescheitert oder der Koordinator ist älter (404 auf
// GET /v1/ping; er lehnt das Feld ping_ms ab, der geschätzte Wert kommt nur
// ins Log).
func measurePing(ctx context.Context, c *client.Client) float64 {
	res, err := c.Ping(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("bench: Ping-Messung gescheitert, kein Ping-Wert: %v", err)
		}
		return 0
	}
	ms := pingToMs(res.Median)
	if res.Estimated {
		log.Printf("bench: Koordinator ohne /v1/ping (älter): Ping geschätzt %.1f ms, wird nicht gesendet", ms)
		return 0
	}
	log.Printf("bench: Ping zum Server: %.1f ms", ms)
	return ms
}

// pingToMs rechnet d in Millisekunden um, mindestens 0,1 (0 hieße „fehlt“).
func pingToMs(d time.Duration) float64 {
	return max(0.1, float64(d)/float64(time.Millisecond))
}

// lastUpload ist die zuletzt gemessene Upload-Rate dieses Prozesses.
var lastUpload uploadMemo

type uploadMemo struct {
	mu   sync.Mutex
	mbps float64
}

func (u *uploadMemo) get() float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.mbps
}

func (u *uploadMemo) set(v float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.mbps = v
}

// measureCPU ist die CPU-Messung; im Test austauschbar.
var measureCPU = cpuScore

// cpuScore misst den SHA-256-Durchsatz in MB/s auf der aufrufenden Goroutine,
// also auf einem Kern: ein Hash über einen Datenstrom aus einem 1-MiB-Puffer,
// bis mindestens cpuMinBytes gerechnet und cpuMinTime vergangen sind.
func cpuScore(ctx context.Context) (float64, error) {
	buf := make([]byte, cpuChunk)
	for i := range buf {
		buf[i] = byte(i * 131)
	}
	h := sha256.New()

	for warm := time.Now(); time.Since(warm) < cpuWarmup; {
		h.Write(buf)
	}
	h.Reset()

	var n int64
	start := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		h.Write(buf)
		n += cpuChunk
		if elapsed := time.Since(start); n >= cpuMinBytes && elapsed >= cpuMinTime {
			h.Sum(nil)
			return float64(n) / elapsed.Seconds() / 1e6, nil
		}
	}
}

// DefaultProfile ist das Startprofil aus der Hardware dieses PCs:
//   - ab 32 GB RAM: Xmx 6144 MB, Sichtweite 12, Simulationsweite 10,
//   - ab 16 GB: 4096 MB, 10, 8,
//   - darunter: 3072 MB, 8, 6,
//   - C2ME an ab 8 logischen Kernen, Threads = Kerne/2 (mindestens 1).
//
// Windows meldet etwas weniger RAM, als verbaut ist (Firmware, Grafik); ein
// PC mit 32 GB zeigt etwa 31,8 GiB. Eine Stufe gilt deshalb schon ab 90 % ihrer
// Nenngröße. Ist der RAM nicht lesbar, gilt die unterste Stufe.
//
// Die Thread-Zahl ist auch bei ausgeschaltetem C2ME belegt, damit ein
// Einschalten von Hand in profil.yaml einen sinnvollen Wert vorfindet.
func DefaultProfile() api.Profile {
	totalMB := 0
	if vm, err := mem.VirtualMemory(); err == nil {
		totalMB = int(vm.Total >> 20)
	}
	return defaultProfile(totalMB, runtime.NumCPU())
}

func defaultProfile(totalMB, cores int) api.Profile {
	var p api.Profile
	switch {
	case atLeastGB(totalMB, 32):
		p = api.Profile{XmxMB: 6144, ViewDistance: 12, SimulationDistance: 10}
	case atLeastGB(totalMB, 16):
		p = api.Profile{XmxMB: 4096, ViewDistance: 10, SimulationDistance: 8}
	default:
		p = api.Profile{XmxMB: 3072, ViewDistance: 8, SimulationDistance: 6}
	}
	p.C2ME = cores >= 8
	p.C2METhreads = max(1, cores/2)
	return p
}

// atLeastGB: totalMB (MiB) erreicht mindestens 90 % von gb GiB.
func atLeastGB(totalMB, gb int) bool { return totalMB*10 >= gb*1024*9 }

// ProfilePath ist der Ort des Leistungsprofils: Dir()/profil.yaml.
func ProfilePath() string { return filepath.Join(group.Dir(), profileFileName) }

// LoadProfile liest Dir()/profil.yaml.
//
// Fehlt die Datei, ist das Ergebnis DefaultProfile() ohne Fehler (wie bei
// group.LoadState); wer wissen will, ob sie existiert, prüft ProfilePath()
// selbst. Fehlende Schlüssel behalten die Werte aus DefaultProfile().
// Unbekannte Schlüssel (Tippfehler beim Bearbeiten von Hand) und Werte, mit
// denen der Server nicht startet, sind Fehler.
func LoadProfile() (api.Profile, error) {
	path := ProfilePath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultProfile(), nil
	}
	if err != nil {
		return api.Profile{}, err
	}
	p := DefaultProfile()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil && !errors.Is(err, io.EOF) {
		return api.Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := checkProfile(p); err != nil {
		return api.Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// SaveProfile prüft p und schreibt es atomar (Temp-Datei, dann Umbenennen)
// nach Dir()/profil.yaml.
func SaveProfile(p api.Profile) error {
	if err := checkProfile(p); err != nil {
		return err
	}
	data, err := yaml.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(group.Dir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(ProfilePath(), append([]byte(profileHeader), data...))
}

// checkProfile lehnt Werte ab, mit denen der Minecraft-Server nicht startet.
func checkProfile(p api.Profile) error {
	if p.XmxMB < minXmxMB {
		return fmt.Errorf("Leistungsprofil: xmx_mb %d ist zu klein (mindestens %d)", p.XmxMB, minXmxMB)
	}
	if p.ViewDistance < minDistance || p.ViewDistance > maxDistance {
		return fmt.Errorf("Leistungsprofil: view_distance %d liegt nicht in %d–%d", p.ViewDistance, minDistance, maxDistance)
	}
	if p.SimulationDistance < minDistance || p.SimulationDistance > maxDistance {
		return fmt.Errorf("Leistungsprofil: simulation_distance %d liegt nicht in %d–%d", p.SimulationDistance, minDistance, maxDistance)
	}
	if p.C2ME && p.C2METhreads < 1 {
		return fmt.Errorf("Leistungsprofil: c2me_threads %d, bei eingeschaltetem C2ME mindestens 1", p.C2METhreads)
	}
	return nil
}

// writeFileAtomic schreibt data in eine Temp-Datei neben path und benennt sie
// dann um; Leser sehen also entweder den alten oder den neuen Inhalt. (Wie in
// group, dort nicht exportiert.)
func writeFileAtomic(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Unter Windows halten Virenscanner oder Indexdienst die Zieldatei
	// manchmal einen Moment offen; deshalb kurz wiederholen.
	for i := range 10 {
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}
