package mcserver

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// -jar geht relativ zum Server-Ordner an Java: Zeichen außerhalb der
// ANSI-Codepage im Benutzerpfad stehen dann nicht auf der Kommandozeile.
func TestJarArgRelativeToServerDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Łukasz", "wanderwelt")
	dir := filepath.Join(home, "server")
	jar := filepath.Join(home, "deps", "fabric-server-launch", "fabric-server-launch.jar")
	got := jarArg(dir, jar)
	if want := filepath.Join("..", "deps", "fabric-server-launch", "fabric-server-launch.jar"); got != want {
		t.Fatalf("jarArg = %q, erwartet %q", got, want)
	}
	if strings.Contains(got, "Łukasz") {
		t.Fatalf("jarArg enthält den Benutzerpfad: %q", got)
	}
	// So steht es im Aufruf von Java, mit dem Server-Ordner als Arbeitsordner.
	cmd := serverCmd(&config{dir: dir, java: filepath.Join(home, "deps", "java", "bin", "java.exe"), jar: jar, xmx: 2048})
	if cmd.Dir != dir || !slices.Equal(cmd.Args[1:], []string{"-Xms1024M", "-Xmx2048M", "-jar", got, "nogui"}) {
		t.Fatalf("Aufruf %q in %q", cmd.Args, cmd.Dir)
	}
	if runtime.GOOS == "windows" {
		other := `Z:\anderes\server.jar`
		if got := jarArg(`C:\wanderwelt\server`, other); got != other {
			t.Fatalf("anderes Laufwerk: %q, erwartet %q", got, other)
		}
	}
}
