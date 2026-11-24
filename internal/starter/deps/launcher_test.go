package deps

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// In der Testumgebung (WANDERWELT_MINECRAFT_DIR zeigt nicht auf den echten
// .minecraft) öffnet OpenLauncher den echten Launcher nicht und meldet
// ErrTestEnvironment. Zeigt die Variable auf den echten Ordner, gilt
// sie nicht als Testumgebung.
func TestOpenLauncherRespectsTestEnvironment(t *testing.T) {
	var started []string
	old := startDetached
	startDetached = func(name string, args ...string) error {
		started = append(started, name)
		return nil
	}
	t.Cleanup(func() { startDetached = old })

	// Ein klassischer Launcher, den OpenLauncher fände.
	appData, pf := t.TempDir(), t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("ProgramFiles(x86)", pf)
	t.Setenv("ProgramFiles", pf)
	exe := filepath.Join(pf, "Minecraft Launcher", "MinecraftLauncher.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WANDERWELT_MINECRAFT_DIR", t.TempDir())
	if err := OpenLauncher(); !errors.Is(err, ErrTestEnvironment) {
		t.Fatalf("Testumgebung: %v, erwartet ErrTestEnvironment", err)
	}
	if len(started) != 0 {
		t.Fatalf("Testumgebung: %q gestartet", started)
	}

	if runtime.GOOS != "windows" {
		t.Skip("der echte Launcher nur unter Windows")
	}
	for _, dir := range []string{"", filepath.Join(appData, ".minecraft")} {
		started = nil
		t.Setenv("WANDERWELT_MINECRAFT_DIR", dir)
		if err := OpenLauncher(); err != nil || len(started) != 1 || !strings.EqualFold(started[0], exe) {
			t.Errorf("WANDERWELT_MINECRAFT_DIR=%q: gestartet %q, Fehler %v; erwartet %s", dir, started, err, exe)
		}
	}
}
