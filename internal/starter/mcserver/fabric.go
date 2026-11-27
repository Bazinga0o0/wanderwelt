package mcserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/gorcon/rcon"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const (
	serverIP     = "127.0.0.1"
	serverPort   = 25565
	rconPort     = 25575
	defaultWorld = "world"
	// minXmxMB: der Server startet mit -Xms1024M, Xmx darf nicht kleiner sein.
	minXmxMB = 1024
	// opLevel ist die Berechtigungsstufe der Ops (wie /op auf dem Server).
	opLevel = 4

	readyTimeout = 5 * time.Minute  // erster Start lädt den Vanilla-Server
	stopTimeout  = 2 * time.Minute  // Stop (auch nach gescheitertem Start): so lange auf das Ende warten
	stopRetry    = 5 * time.Second  // "stop" wiederholen, bis RCON es annimmt
	pollInterval = time.Second      // Bereitschaft prüfen
	probeTimeout = 10 * time.Second // ein Versuch beim Warten auf Bereitschaft
	cmdTimeout   = 30 * time.Second // einfache RCON-Befehle
	saveTimeout  = 5 * time.Minute  // save-all flush auf großer Welt
	waitDelay    = 10 * time.Second // Ausgabe-Pipes nach Prozessende

	// Antworten des Servers (Sprache en_us, Stand 26.2).
	respSaveOff      = "Automatic saving is now disabled"
	respSaveOffAgain = "Saving is already turned off"
	respSaved        = "Saved the game"
	respSaveOn       = "Automatic saving is now enabled"
	respSaveOnAgain  = "Saving is already turned on"
	respListPrefix   = "There are "
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewFabric liefert den Fabric-Server-Baustein: Fabric-Server-Launcher mit
// den Mods aus dem Manifest, gesteuert über RCON auf 127.0.0.1:25575.
func NewFabric() Server { return &fabric{} }

type fabric struct {
	mu   sync.Mutex
	cfg  *config   // nach erfolgreichem Prepare
	proc *process  // zuletzt gestarteter Prozess
	rc   *rconConn // RCON zu proc
}

// config ist das geprüfte Ergebnis von Prepare.
type config struct {
	dir   string
	level string
	java  string
	jar   string
	xmx   int
	props map[string]string // Welt-Einstellungen + Profil, ohne feste Werte
}

// ---- Prepare ----

// Prepare schreibt eula.txt mit eula=true: Der Aufrufer muss vorher die
// Zustimmung zur Minecraft-EULA eingeholt haben (Installation).
func (f *fabric) Prepare(ctx context.Context, in PrepareInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.proc.running() {
		return errors.New("mcserver: Prepare, während der Server läuft")
	}
	f.cfg = nil

	cfg, err := checkInput(in)
	if err != nil {
		return fmt.Errorf("mcserver: %w", err)
	}
	if err := os.MkdirAll(cfg.dir, 0o755); err != nil {
		return fmt.Errorf("mcserver: %w", err)
	}

	wl, err := whitelistJSON(in.Settings.Whitelist)
	if err != nil {
		return fmt.Errorf("mcserver: Whitelist: %w", err)
	}
	ops, err := opsJSON(in.Settings.Ops)
	if err != nil {
		return fmt.Errorf("mcserver: Ops: %w", err)
	}
	files := []struct {
		name string
		data []byte
	}{
		{"whitelist.json", wl},
		{"ops.json", ops},
		{"eula.txt", []byte("# Zustimmung zur Minecraft-EULA (https://aka.ms/MinecraftEULA) im wanderwelt-Starter\neula=true\n")},
	}
	for _, fl := range files {
		if err := writeFileAtomic(filepath.Join(cfg.dir, fl.name), fl.data, 0o644); err != nil {
			return fmt.Errorf("mcserver: %s: %w", fl.name, err)
		}
	}

	if err := syncMods(ctx, filepath.Join(cfg.dir, "mods"), in.Paths.Mods); err != nil {
		return fmt.Errorf("mcserver: Mods: %w", err)
	}

	// Konfigdateien: Fehler hier halten den Serverstart nicht auf.
	applyConfigs(cfg.dir, in.Paths.Configs)

	// Chat-Einstellung der Welt in die Konfig-Datei der Chatverlauf-Mod; nach
	// den Konfigdateien aus dem Manifest, damit die Gruppeneinstellung gilt.
	// Scheitert das Schreiben bei „aus“, startet der Server nicht (sonst
	// würde der Chat entgegen der Einstellung gespeichert).
	if err := applyChatConfig(cfg.dir, in.Settings.Chat); err != nil {
		if in.Settings.Chat.Off {
			return fmt.Errorf("mcserver: Chat-Einstellung: %w", err)
		}
		log.Printf("mcserver: Chat-Einstellung nicht eingetragen, der Server startet trotzdem: %v", err)
	}

	// server.properties schon jetzt, damit der Ordner vollständig ist; Start
	// schreibt sie mit neuem RCON-Passwort noch einmal.
	pw, err := randomPassword()
	if err != nil {
		return fmt.Errorf("mcserver: %w", err)
	}
	if err := writeProperties(cfg, pw); err != nil {
		return fmt.Errorf("mcserver: server.properties: %w", err)
	}
	f.cfg = cfg
	return nil
}

func checkInput(in PrepareInput) (*config, error) {
	if strings.TrimSpace(in.Dir) == "" {
		return nil, errors.New("kein Server-Ordner")
	}
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	level := in.WorldDirName
	if level == "" {
		level = defaultWorld
	}
	if !simpleName(level) {
		return nil, fmt.Errorf("ungültiger Weltordner %q", level)
	}
	if in.Paths == nil {
		return nil, errors.New("keine Pfade (deps.Ensure)")
	}
	for _, p := range []string{in.Paths.Java, in.Paths.FabricServerJar} {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s ist keine Datei", p)
		}
	}
	if in.Profile.XmxMB < minXmxMB {
		return nil, fmt.Errorf("Xmx %d MB ist zu klein (mindestens %d MB)", in.Profile.XmxMB, minXmxMB)
	}

	props := make(map[string]string, len(in.Settings.Properties)+2)
	for k, v := range in.Settings.Properties {
		if k = strings.TrimSpace(k); k != "" {
			props[k] = v
		}
	}
	// Das Profil überschreibt nur seine Leistungsschlüssel.
	if in.Profile.ViewDistance > 0 {
		props["view-distance"] = strconv.Itoa(in.Profile.ViewDistance)
	}
	if in.Profile.SimulationDistance > 0 {
		props["simulation-distance"] = strconv.Itoa(in.Profile.SimulationDistance)
	}
	return &config{
		dir:   dir,
		level: level,
		java:  in.Paths.Java,
		jar:   in.Paths.FabricServerJar,
		xmx:   in.Profile.XmxMB,
		props: props,
	}, nil
}

// simpleName lässt nur einen einfachen Ordnernamen zu.
func simpleName(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

// fixedProperties sind die Werte aus den Global Constraints; sie gehen allen
// Welt-Einstellungen vor.
func fixedProperties(level, password string) map[string]string {
	return map[string]string{
		"server-ip":         serverIP,
		"server-port":       strconv.Itoa(serverPort),
		"online-mode":       "true",
		"white-list":        "true",
		"enforce-whitelist": "true",
		"enable-rcon":       "true",
		// Für die Übergabe des Hostings: /transfer schickt Spieler an einen
		// anderen Server und nimmt Spieler von einem anderen an.
		"accepts-transfers": "true",
		"rcon.port":         strconv.Itoa(rconPort),
		"rcon.password":     password,
		// Sonst sähen Ops alle 15 s die Antwort auf "list" im Chat.
		"broadcast-rcon-to-ops": "false",
		"level-name":            level,
	}
}

// writeProperties schreibt server.properties: Welt-Einstellungen und Profil,
// darüber die festen Werte.
func writeProperties(cfg *config, password string) error {
	all := make(map[string]string, len(cfg.props)+10)
	for k, v := range cfg.props {
		all[k] = v
	}
	for k, v := range fixedProperties(cfg.level, password) {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# Von wanderwelt bei jedem Start neu geschrieben.\n")
	for _, k := range keys {
		b.WriteString(escapeProp(k, true))
		b.WriteByte('=')
		b.WriteString(escapeProp(all[k], false))
		b.WriteByte('\n')
	}
	return writeFileAtomic(filepath.Join(cfg.dir, "server.properties"), []byte(b.String()), 0o600)
}

// escapeProp maskiert nach dem Format von java.util.Properties; alles
// außerhalb von ASCII wird zu \uXXXX.
func escapeProp(s string, key bool) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '=' || r == ':' || r == '#' || r == '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == ' ' && (key || i == 0):
			b.WriteString(`\ `)
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&b, `\u%04X`, u)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type listEntry struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type opEntry struct {
	UUID                string `json:"uuid"`
	Name                string `json:"name"`
	Level               int    `json:"level"`
	BypassesPlayerLimit bool   `json:"bypassesPlayerLimit"`
}

// checkPlayers prüft UUIDs und Namen und entfernt doppelte UUIDs.
func checkPlayers(in []api.Player) ([]api.Player, error) {
	out := make([]api.Player, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, p := range in {
		id := strings.ToLower(strings.TrimSpace(p.UUID))
		name := strings.TrimSpace(p.Name)
		if !uuidRe.MatchString(id) {
			return nil, fmt.Errorf("Spieler %q: ungültige UUID %q", p.Name, p.UUID)
		}
		if name == "" || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 }) {
			return nil, fmt.Errorf("Spieler %s: ungültiger Name %q", id, p.Name)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, api.Player{UUID: id, Name: name})
	}
	return out, nil
}

func whitelistJSON(players []api.Player) ([]byte, error) {
	ps, err := checkPlayers(players)
	if err != nil {
		return nil, err
	}
	list := make([]listEntry, 0, len(ps))
	for _, p := range ps {
		list = append(list, listEntry{UUID: p.UUID, Name: p.Name})
	}
	return marshalList(list)
}

func opsJSON(players []api.Player) ([]byte, error) {
	ps, err := checkPlayers(players)
	if err != nil {
		return nil, err
	}
	list := make([]opEntry, 0, len(ps))
	for _, p := range ps {
		list = append(list, opEntry{UUID: p.UUID, Name: p.Name, Level: opLevel})
	}
	return marshalList(list)
}

func marshalList(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// syncMods gleicht den Mods-Ordner exakt auf mods ab: Fehlendes oder
// Abweichendes wird kopiert, alles andere entfernt.
func syncMods(ctx context.Context, dir string, mods []string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	want := make(map[string]string, len(mods)) // Name klein → Quelle
	for _, src := range mods {
		name := filepath.Base(src)
		if !simpleName(name) {
			return fmt.Errorf("ungültiger Mod-Dateiname %q", src)
		}
		key := strings.ToLower(name)
		if _, dup := want[key]; dup {
			return fmt.Errorf("Mod %s doppelt", name)
		}
		want[key] = src
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		src, ok := want[strings.ToLower(e.Name())]
		if ok && e.Type().IsRegular() && e.Name() == filepath.Base(src) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
		log.Printf("mcserver: mods/%s entfernt", e.Name())
	}

	for _, src := range mods {
		if err := ctx.Err(); err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.Base(src))
		same, err := sameContent(src, dst)
		if err != nil {
			return err
		}
		if same {
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
		log.Printf("mcserver: mods/%s kopiert", filepath.Base(src))
	}
	return nil
}

// sameContent meldet, ob dst existiert und denselben Inhalt wie src hat.
func sameContent(src, dst string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	di, err := os.Stat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if si.Size() != di.Size() {
		return false, nil
	}
	a, err := fileHash(src)
	if err != nil {
		return false, err
	}
	b, err := fileHash(dst)
	if err != nil {
		return false, nil // unlesbar → neu kopieren
	}
	return a == b, nil
}

func fileHash(p string) (string, error) {
	fh, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".wanderwelt-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameRetry(name, dst)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// writeFileAtomic schreibt über eine Temp-Datei im selben Ordner.
func writeFileAtomic(p string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && runtime.GOOS != "windows" {
		err = os.Chmod(name, perm)
	}
	if err == nil {
		err = renameRetry(name, p)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// renameRetry wiederholt das Umbenennen kurz (Virenscanner unter Windows).
func renameRetry(from, to string) error {
	var err error
	for i := range 10 {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}

func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("RCON-Passwort: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ---- Prozess ----

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	log    *logFile
	done   chan struct{} // geschlossen, wenn der Prozess beendet ist
	err    error         // Ergebnis; gültig nach done
	exited chan error    // ein Wert für Exited, dann geschlossen

	stopping    atomic.Bool // stop wurde von uns verlangt
	killed      atomic.Bool // hart beendet
	consoleBusy atomic.Bool // ein Schreiben auf stdin ist unterwegs
	stopMu      sync.Mutex  // ein Stop zur Zeit
}

func (p *process) running() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// wait wartet auf das Prozessende und meldet es.
func (p *process) wait() {
	err := p.cmd.Wait()
	switch {
	case err == nil && p.stopping.Load():
		p.err = nil
	case err == nil:
		p.err = errors.New("Minecraft-Server hat sich ohne Stop beendet (Exit-Code 0)")
	case p.killed.Load():
		p.err = fmt.Errorf("Minecraft-Server wurde hart beendet: %w", err)
	default:
		p.err = fmt.Errorf("Minecraft-Server unerwartet beendet: %w%s", err, p.log.lastLines(5))
	}
	if p.err == nil {
		p.log.mark("Server sauber beendet")
	} else {
		p.log.mark(p.err.Error())
	}
	p.log.Close()
	close(p.done)
	p.exited <- p.err
	close(p.exited)
}

// console schickt eine Zeile an die Server-Konsole (stdin), ohne zu
// blockieren: Liest der Server nicht und ist die Pipe voll, hinge sonst
// shutdown fest. Höchstens ein Schreiben ist unterwegs; nach dem
// Prozessende schließt Wait die Pipe und beendet es.
func (p *process) console(line string) {
	if !p.consoleBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.consoleBusy.Store(false)
		if _, err := io.WriteString(p.stdin, line+"\n"); err != nil && p.running() {
			log.Printf("mcserver: Konsole: %v", err)
		}
	}()
}

// shutdown bittet den Server, sich zu beenden, und wartet höchstens grace
// bzw. bis ctx endet auf das Prozessende; danach wird der Prozess hart
// beendet. Meldet, ob hart beendet werden musste.
//
// "stop" geht über RCON, solange das nicht klappt über die Konsole, und wird
// alle stopRetry wiederholt, bis RCON es angenommen hat: Eine Konsolenzeile,
// die ankommt, während der Server noch lädt, geht verloren.
func (p *process) shutdown(ctx context.Context, rc *rconConn, grace time.Duration) (killed bool) {
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	if !p.running() {
		return false
	}
	p.stopping.Store(true)
	if rc != nil {
		defer rc.close()
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	retry := time.NewTicker(stopRetry)
	defer retry.Stop()

	accepted := false
wait:
	for attempt := 0; ; attempt++ {
		if !accepted {
			accepted = p.sendStop(ctx, rc, attempt == 0)
		}
		select {
		case <-p.done:
			return false
		case <-retry.C:
		case <-timer.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}
	p.killed.Store(true)
	if err := p.cmd.Process.Kill(); err != nil {
		log.Printf("mcserver: Prozess beenden: %v", err)
	}
	<-p.done
	return true
}

// sendStop schickt "stop" über RCON oder, wenn das nicht geht, über die
// Konsole. Meldet, ob RCON den Befehl angenommen hat.
func (p *process) sendStop(ctx context.Context, rc *rconConn, first bool) bool {
	if rc != nil {
		cctx, cancel := context.WithTimeout(ctx, probeTimeout)
		resp, err := rc.exec(cctx, "stop")
		cancel()
		if err == nil {
			log.Printf("mcserver: stop: %s", strings.TrimSpace(resp))
			// Selbst zuerst schließen, bevor der Server die Verbindung beim
			// Beenden schließt.
			rc.close()
			return true
		}
		if first {
			log.Printf("mcserver: stop über RCON: %v – über die Konsole", err)
		}
	}
	p.console("stop")
	return false
}

// ---- Start, Stop ----

func (f *fabric) Start(ctx context.Context) error {
	p, rc, err := f.launch()
	if err != nil {
		return fmt.Errorf("mcserver: Start: %w", err)
	}
	if err := waitReady(ctx, p, rc); err != nil {
		if !p.running() {
			rc.close()
			return fmt.Errorf("mcserver: Start: %w", err)
		}
		// Nicht bereit: stoppen und wie bei Stop warten, erst dann hart
		// beenden – die Welt kann gerade geladen oder erzeugt werden. Der
		// ursprüngliche ctx kann schon beendet sein.
		killed := p.shutdown(context.WithoutCancel(ctx), rc, stopTimeout)
		rc.close()
		if killed {
			return fmt.Errorf("mcserver: Start: %w (Server danach hart beendet)", err)
		}
		return fmt.Errorf("mcserver: Start: %w (Server wieder gestoppt)", err)
	}
	log.Printf("mcserver: Server bereit (%s:%d)", serverIP, serverPort)
	return nil
}

// launch startet den Java-Prozess.
func (f *fabric) launch() (*process, *rconConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfg == nil {
		return nil, nil, errors.New("Prepare fehlt oder ist gescheitert")
	}
	if f.proc.running() {
		return nil, nil, errors.New("Server läuft schon")
	}
	cfg := f.cfg

	pw, err := randomPassword()
	if err != nil {
		return nil, nil, err
	}
	if err := writeProperties(cfg, pw); err != nil {
		return nil, nil, fmt.Errorf("server.properties: %w", err)
	}
	if err := checkPorts(); err != nil {
		return nil, nil, err
	}
	lf, err := openLog(filepath.Join(cfg.dir, "logs"))
	if err != nil {
		return nil, nil, fmt.Errorf("Server-Log: %w", err)
	}

	cmd := serverCmd(cfg)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = waitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		lf.Close()
		return nil, nil, err
	}

	lf.mark("Start: " + strings.Join(cmd.Args, " "))
	if err := cmd.Start(); err != nil {
		lf.mark(fmt.Sprintf("Start gescheitert: %v", err))
		lf.Close()
		return nil, nil, fmt.Errorf("Java starten: %w", err)
	}
	log.Printf("mcserver: Server gestartet (PID %d) in %s", cmd.Process.Pid, cfg.dir)

	p := &process{
		cmd:    cmd,
		stdin:  stdin,
		log:    lf,
		done:   make(chan struct{}),
		exited: make(chan error, 1),
	}
	go p.wait()
	rc := newRCON(net.JoinHostPort(serverIP, strconv.Itoa(rconPort)), pw)
	f.proc, f.rc = p, rc
	return p, rc, nil
}

// serverCmd baut den Java-Aufruf des Servers (ohne ihn zu starten).
func serverCmd(cfg *config) *exec.Cmd {
	cmd := exec.Command(cfg.java,
		"-Xms1024M", fmt.Sprintf("-Xmx%dM", cfg.xmx),
		"-jar", jarArg(cfg.dir, cfg.jar), "nogui")
	cmd.Dir = cfg.dir
	return cmd
}

// jarArg ist der Pfad des Server-Jars für -jar, relativ zum Arbeitsordner
// dir des Servers: Java liest seine Kommandozeile unter Windows in der
// ANSI-Codepage, Zeichen außerhalb davon im Benutzerpfad (etwa
// C:\Users\Łukasz) kämen verstümmelt an. Der relative Pfad enthält nur die
// Ordner unterhalb von Home(). Geht das nicht (anderes Laufwerk), bleibt er
// absolut.
func jarArg(dir, jar string) string {
	abs, err := filepath.Abs(jar)
	if err != nil {
		return jar
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil || filepath.IsAbs(rel) {
		return abs
	}
	return rel
}

// checkPorts prüft, dass Spiel- und RCON-Port frei sind (sonst läuft wohl
// noch ein alter Server, und RCON spräche mit ihm).
func checkPorts() error {
	for _, port := range []int{serverPort, rconPort} {
		l, err := net.Listen("tcp", net.JoinHostPort(serverIP, strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("Port %d auf %s ist belegt (läuft noch ein Minecraft-Server?): %w", port, serverIP, err)
		}
		l.Close()
	}
	return nil
}

var errNotReady = errors.New("Server nach 5 Minuten nicht bereit")

// waitReady wartet, bis RCON verbindet und "list" beantwortet.
func waitReady(ctx context.Context, p *process, rc *rconConn) error {
	ctx, cancel := context.WithTimeoutCause(ctx, readyTimeout, errNotReady)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var last error
	for {
		select {
		case <-p.done:
			return fmt.Errorf("Server hat sich beim Start beendet: %w", p.err)
		case <-ctx.Done():
			if c := context.Cause(ctx); c == errNotReady && last != nil {
				return fmt.Errorf("%w (zuletzt: %v)%s", c, last, p.log.lastLines(5))
			}
			return context.Cause(ctx)
		case <-tick.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, probeTimeout)
		_, err := rc.exec(pctx, "list")
		pcancel()
		if err == nil {
			return nil
		}
		last = err
	}
}

func (f *fabric) Stop(ctx context.Context) error {
	f.mu.Lock()
	p, rc := f.proc, f.rc
	f.mu.Unlock()
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		if p.err != nil {
			return fmt.Errorf("mcserver: Stop: %w", p.err)
		}
		return nil
	default:
	}
	if p.shutdown(ctx, rc, stopTimeout) {
		why := fmt.Sprintf("nach %v", stopTimeout)
		if ctx.Err() != nil {
			why = "nach Abbruch"
		}
		return fmt.Errorf("mcserver: Server hat auf stop nicht reagiert, %s hart beendet (Welt evtl. nicht vollständig gespeichert)", why)
	}
	if p.err != nil {
		return fmt.Errorf("mcserver: Stop: %w", p.err)
	}
	log.Printf("mcserver: Server sauber gestoppt")
	return nil
}

func (f *fabric) Exited() <-chan error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.proc == nil {
		return nil
	}
	return f.proc.exited
}

// PID liefert die Prozess-ID des Java-Servers, solange er läuft (schon
// während Start), sonst 0. Die Host-Sitzung schreibt sie in ihre
// Markierungsdatei, damit ein verwaister Server nach einem Absturz des
// Starters gefunden und beendet werden kann.
func (f *fabric) PID() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.proc.running() {
		return 0
	}
	return f.proc.cmd.Process.Pid
}

// ---- Befehle ----

// client liefert die RCON-Verbindung des laufenden Servers.
func (f *fabric) client() (*rconConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.proc.running() {
		return nil, errors.New("mcserver: Server läuft nicht")
	}
	return f.rc, nil
}

func (f *fabric) Players(ctx context.Context) ([]string, error) {
	rc, err := f.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	resp, err := rc.exec(ctx, "list")
	if err != nil {
		return nil, fmt.Errorf("mcserver: Spielerliste: %w", err)
	}
	names, err := parseList(resp)
	if err != nil {
		return nil, fmt.Errorf("mcserver: Spielerliste: %w", err)
	}
	return names, nil
}

func (f *fabric) Command(ctx context.Context, cmd string) (string, error) {
	rc, err := f.client()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	resp, err := rc.exec(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("mcserver: %w", err)
	}
	return resp, nil
}

// Say schickt "say <text>"; Steuerzeichen (etwa Zeilenumbrüche) werden zu
// Leerzeichen, damit der Text ein Befehl bleibt.
func (f *fabric) Say(ctx context.Context, text string) error {
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
	_, err := f.Command(ctx, "say "+text)
	return err
}

// parseList liest die Antwort auf "list":
// "There are 2 of a max of 20 players online: Alex, Steve".
func parseList(resp string) ([]string, error) {
	resp = strings.TrimSpace(resp)
	_, rest, ok := strings.Cut(resp, ":")
	if !ok || !strings.HasPrefix(resp, respListPrefix) {
		return nil, fmt.Errorf("unerwartete Antwort %q", resp)
	}
	names := []string{}
	for _, n := range strings.Split(rest, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

func (f *fabric) SaveBarrier(ctx context.Context) (func(context.Context) error, error) {
	rc, err := f.client()
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	resp, err := rc.exec(cctx, "save-off")
	cancel()
	if err == nil && !strings.Contains(resp, respSaveOff) && !strings.Contains(resp, respSaveOffAgain) {
		err = fmt.Errorf("unerwartete Antwort %q", strings.TrimSpace(resp))
	}
	if err != nil {
		// save-off kann trotzdem angekommen sein.
		saveOnAfterError(ctx, rc)
		return nil, fmt.Errorf("mcserver: save-off: %w", err)
	}

	cctx, cancel = context.WithTimeout(ctx, saveTimeout)
	resp, err = rc.exec(cctx, "save-all flush")
	cancel()
	if err == nil && !strings.Contains(resp, respSaved) {
		err = fmt.Errorf("Speichern nicht bestätigt: %q", strings.TrimSpace(resp))
	}
	if err != nil {
		saveOnAfterError(ctx, rc)
		return nil, fmt.Errorf("mcserver: save-all flush: %w", err)
	}

	release := func(ctx context.Context) error {
		if err := saveOn(ctx, rc); err != nil {
			return fmt.Errorf("mcserver: save-on: %w", err)
		}
		return nil
	}
	return release, nil
}

func saveOn(ctx context.Context, rc *rconConn) error {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	resp, err := rc.exec(ctx, "save-on")
	if err != nil {
		return err
	}
	if !strings.Contains(resp, respSaveOn) && !strings.Contains(resp, respSaveOnAgain) {
		return fmt.Errorf("unerwartete Antwort %q", strings.TrimSpace(resp))
	}
	return nil
}

// saveOnAfterError schaltet das Speichern nach einer gescheiterten Barriere
// wieder ein, auch wenn ctx schon beendet ist.
func saveOnAfterError(ctx context.Context, rc *rconConn) {
	if err := saveOn(context.WithoutCancel(ctx), rc); err != nil {
		log.Printf("mcserver: save-on nach Fehler: %v", err)
	}
}

// ---- RCON ----

// rconConn ist eine RCON-Verbindung, die bei Bedarf (neu) aufgebaut wird.
// Befehle laufen nacheinander; ctx bricht Verbindungsaufbau und Befehl ab.
type rconConn struct {
	addr, password string
	sem            chan struct{} // ein Befehl zur Zeit

	mu     sync.Mutex
	conn   *rcon.Conn
	closed bool
}

func newRCON(addr, password string) *rconConn {
	return &rconConn{addr: addr, password: password, sem: make(chan struct{}, 1)}
}

func (r *rconConn) exec(ctx context.Context, command string) (string, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("RCON %q: %w", command, context.Cause(ctx))
	}
	defer func() { <-r.sem }()

	conn, err := r.get(ctx)
	if err != nil {
		return "", fmt.Errorf("RCON %q: %w", command, err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	resp, err := conn.Execute(command)
	if !stop() {
		r.drop(conn)
		return "", fmt.Errorf("RCON %q: %w", command, context.Cause(ctx))
	}
	if err != nil {
		r.drop(conn)
		return "", fmt.Errorf("RCON %q: %w", command, err)
	}
	return resp, nil
}

// get liefert die bestehende Verbindung oder baut eine neue auf.
func (r *rconConn) get(ctx context.Context) (*rcon.Conn, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("Verbindung geschlossen")
	}
	if c := r.conn; c != nil {
		r.mu.Unlock()
		return c, nil
	}
	r.mu.Unlock()

	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	// Keine festen Fristen in rcon: Abbruch nur über ctx.
	c, err := rcon.Open(nc, r.password, rcon.SetDeadline(0))
	if !stop() {
		nc.Close()
		return nil, context.Cause(ctx)
	}
	if err != nil {
		nc.Close()
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		c.Close()
		return nil, errors.New("Verbindung geschlossen")
	}
	r.conn = c
	return c, nil
}

// drop schließt eine gestörte Verbindung; der nächste Befehl baut neu auf.
func (r *rconConn) drop(c *rcon.Conn) {
	c.Close()
	r.mu.Lock()
	if r.conn == c {
		r.conn = nil
	}
	r.mu.Unlock()
}

// close schließt die Verbindung endgültig.
func (r *rconConn) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
}
