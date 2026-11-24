package deps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// profileKey ist Schlüssel und Anzeigename des Launcher-Profils.
	profileKey = "wanderwelt"
	// backupSuffix: Kopie der Profildatei vor der ersten Änderung.
	backupSuffix = ".wanderwelt-bak"

	storePackage = "Microsoft.4297127D64EC6_8wekyb3d8bbwe"
	storeAppID   = `shell:AppsFolder\` + storePackage + `!Minecraft`
)

// profileFiles sind die Profildateien des Launchers im .minecraft-Ordner;
// die zweite gibt es nur bei manchen Store-Installationen.
var profileFiles = []string{"launcher_profiles.json", "launcher_profiles_microsoft_store.json"}

// MinecraftDir liefert den .minecraft-Ordner: WANDERWELT_MINECRAFT_DIR, falls
// gesetzt (Prüfungen gegen eine Kopie), sonst %APPDATA%\.minecraft.
func MinecraftDir() string { return minecraftDir() }

// minecraftDir liefert den .minecraft-Ordner; für Prüfungen gegen eine Kopie
// austauschbar.
var minecraftDir = func() string {
	if d := os.Getenv("WANDERWELT_MINECRAFT_DIR"); d != "" {
		return d
	}
	return defaultMinecraftDir()
}

// defaultMinecraftDir ist der echte .minecraft-Ordner des Benutzers.
func defaultMinecraftDir() string {
	if a := os.Getenv("APPDATA"); a != "" {
		return filepath.Join(a, ".minecraft")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".minecraft")
}

// EnsureLauncherProfile legt in launcher_profiles.json (und, falls vorhanden,
// launcher_profiles_microsoft_store.json) das Profil "wanderwelt" an, fest
// auf version, bzw. aktualisiert es. Alles andere in der Datei bleibt
// erhalten. lastUsed wird auf jetzt gesetzt, damit der Launcher das Profil
// vorwählt. Vor der ersten Änderung entsteht eine Sicherung
// <datei>.wanderwelt-bak. Gibt es keine der Dateien (Launcher nie
// gestartet), ist das ein Fehler, der fs.ErrNotExist enthält.
func EnsureLauncherProfile(version string) error {
	if version == "" {
		return errors.New("Launcher-Profil: keine Version")
	}
	dir := minecraftDir()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	found := 0
	for _, name := range profileFiles {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := updateProfileFile(p, version, now); err != nil {
			return fmt.Errorf("Launcher-Profil: %w", err)
		}
		found++
	}
	if found == 0 {
		return fmt.Errorf("Launcher-Profil: %s fehlt (Minecraft-Launcher einmal starten): %w",
			filepath.Join(dir, profileFiles[0]), fs.ErrNotExist)
	}
	return nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// updateProfileFile setzt das Profil in einer Profildatei.
func updateProfileFile(p, version, now string) error {
	orig, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(orig, utf8BOM)))
	dec.UseNumber() // Zahlen unverändert zurückschreiben
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s: unerwartete Daten nach dem JSON-Objekt", p)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	var profiles map[string]any
	switch v := doc["profiles"].(type) {
	case nil:
		profiles = map[string]any{}
		doc["profiles"] = profiles
	case map[string]any:
		profiles = v
	default:
		return fmt.Errorf("%s: \"profiles\" ist kein Objekt", p)
	}

	prof, _ := profiles[profileKey].(map[string]any)
	if prof == nil {
		prof = map[string]any{"created": now, "icon": "Grass"}
	}
	prof["type"] = "custom"
	prof["lastVersionId"] = version
	prof["name"] = profileKey
	prof["lastUsed"] = now
	profiles[profileKey] = prof

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}

	bak := p + backupSuffix
	if _, err := os.Stat(bak); errors.Is(err, fs.ErrNotExist) {
		if err := writeFileAtomic(bak, orig, fi.Mode().Perm()); err != nil {
			return fmt.Errorf("Sicherung %s: %w", bak, err)
		}
	}
	return writeFileAtomic(p, buf.Bytes(), fi.Mode().Perm())
}

// writeFileAtomic schreibt über eine temporäre Datei im selben Ordner und
// benennt sie dann um.
func writeFileAtomic(p string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), filepath.Base(p)+".tmp-*")
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
		return err
	}
	return nil
}

// renameRetry wiederholt das Umbenennen kurz, falls der Launcher oder ein
// Virenscanner die Zieldatei gerade offen hält (nur Windows).
func renameRetry(from, to string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(from, to); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// ErrTestEnvironment: OpenLauncher öffnet in der Testumgebung nichts.
var ErrTestEnvironment = errors.New("Testumgebung – der echte Minecraft-Launcher wird nicht geöffnet")

// OpenLauncher startet den offiziellen Minecraft-Launcher: zuerst den
// klassischen unter Program Files (x86), sonst die Store-App. Wird keiner
// gefunden, ist das ein Fehler; der Aufrufer zeigt dann nur den Hinweis.
//
// Zeigt WANDERWELT_MINECRAFT_DIR auf einen anderen Ordner als den echten
// .minecraft (Testumgebung), öffnet OpenLauncher nichts und liefert
// ErrTestEnvironment: Der Launcher des Benutzers sähe die Testumgebung
// ohnehin nicht.
func OpenLauncher() error {
	if d := os.Getenv("WANDERWELT_MINECRAFT_DIR"); d != "" && !strings.EqualFold(filepath.Clean(d), filepath.Clean(defaultMinecraftDir())) {
		return fmt.Errorf("%w (WANDERWELT_MINECRAFT_DIR=%s)", ErrTestEnvironment, d)
	}
	if runtime.GOOS != "windows" {
		return errors.New("Minecraft-Launcher: nur unter Windows")
	}
	for _, exe := range classicLauncherPaths() {
		if fi, err := os.Stat(exe); err == nil && fi.Mode().IsRegular() {
			return startDetached(exe)
		}
	}
	if storeLauncherInstalled() {
		explorer := "explorer.exe"
		if root := os.Getenv("SystemRoot"); root != "" {
			explorer = filepath.Join(root, "explorer.exe")
		}
		return startDetached(explorer, storeAppID)
	}
	return errors.New("Minecraft-Launcher nicht gefunden")
}

func classicLauncherPaths() []string {
	const rel = `Minecraft Launcher\MinecraftLauncher.exe`
	var out []string
	pf86 := os.Getenv("ProgramFiles(x86)")
	if pf86 == "" {
		pf86 = `C:\Program Files (x86)`
	}
	out = append(out, filepath.Join(pf86, rel))
	if pf := os.Getenv("ProgramFiles"); pf != "" && !samePath(pf, pf86) {
		out = append(out, filepath.Join(pf, rel))
	}
	return out
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// storeLauncherInstalled erkennt die Store-App am Paketordner des Benutzers.
func storeLauncherInstalled() bool {
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(la, "Packages", storePackage))
	return err == nil && fi.IsDir()
}

// startDetached startet ein Programm und wartet nicht darauf; im Test
// austauschbar.
var startDetached = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = filepath.Dir(name)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Minecraft-Launcher starten: %w", err)
	}
	return cmd.Process.Release()
}
