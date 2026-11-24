package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// commentedConfig ist eine Konfiguration, wie ein Admin sie pflegt:
// Kommentare, Leerzeilen, Zeilenkommentare, ein Abschnitt provisioning.
const commentedConfig = `# Kopfkommentar der Datei.
listen: 127.0.0.1:8470   # Zeilenkommentar, ausgerichtet
data_dir: /var/lib/wanderwelt

auth:
  # tunneld oder dev
  mode: tunneld

# Mitglieder: tunnel-Konto und Minecraft-Name.
members:
  - email: admin@example.org   # der Admin
    minecraft: AdminName
    admin: true
    op: true

  - email: freund@example.org
    minecraft: FreundName

# Welt-Einstellungen.
world_settings:
  difficulty: normal
  motd: "wanderwelt – unsere Welt"

provisioning:
  tunneld_config: /etc/tunneld/config.yaml   # tunneld
storage_budget: 15GiB
`

func writeConfig(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "koordinator.yaml")
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sameOutsideMembers prüft, dass sich außer members nichts geändert hat.
func sameOutsideMembers(t *testing.T, before, after *Config) {
	t.Helper()
	a, b := *before, *after
	a.Members, b.Members = nil, nil
	if !reflect.DeepEqual(a, b) {
		t.Errorf("Konfiguration außerhalb von members geändert:\nvorher  %+v\nnachher %+v", a, b)
	}
}

// isSubsequence: alle Zeilen von want stehen in got, in derselben Reihenfolge.
func isSubsequence(want, got []string) bool {
	i := 0
	for _, l := range got {
		if i < len(want) && l == want[i] {
			i++
		}
	}
	return i == len(want)
}

func TestAddMemberKeepsCommentsAndKeys(t *testing.T) {
	path := writeConfig(t, commentedConfig, 0o640)
	before, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	bob := Member{Email: "bob@wanderwelt.invalid", Minecraft: "Bob", Op: true}
	// Ein Minecraft-Name, den YAML sonst als null läse, muss in
	// Anführungszeichen landen.
	nul := Member{Email: "n@wanderwelt.invalid", Minecraft: "null"}
	for _, m := range []Member{bob, nul} {
		if err := AddMember(path, m); err != nil {
			t.Fatalf("AddMember(%s): %v", m.Email, err)
		}
	}

	after, err := Load(path)
	if err != nil {
		t.Fatalf("Load nach AddMember: %v", err)
	}
	want := append(slices.Clone(before.Members), bob, nul)
	if !slices.Equal(after.Members, want) {
		t.Errorf("members = %+v\nwant %+v", after.Members, want)
	}
	sameOutsideMembers(t, before, after)

	// Jede Zeile der alten Datei – Kommentare, Leerzeilen, Ausrichtung –
	// steht unverändert und in derselben Reihenfolge noch da.
	got := readFile(t, path)
	if !isSubsequence(strings.Split(commentedConfig, "\n"), strings.Split(got, "\n")) {
		t.Errorf("alte Zeilen nicht erhalten:\n%s", got)
	}
	if !strings.Contains(got, "  - email: bob@wanderwelt.invalid\n    minecraft: Bob\n    op: true\n") {
		t.Errorf("neuer Eintrag nicht wie die übrigen eingerückt:\n%s", got)
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o640 {
			t.Errorf("Rechte = %04o, want 0640", fi.Mode().Perm())
		}
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*.tmp")); len(m) != 0 {
		t.Errorf("Temp-Dateien liegen geblieben: %v", m)
	}
}

func TestRemoveMemberKeepsCommentsAndKeys(t *testing.T) {
	path := writeConfig(t, commentedConfig, 0o600)
	before, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveMember(path, "FREUND@example.org"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	after, err := Load(path)
	if err != nil {
		t.Fatalf("Load nach RemoveMember: %v", err)
	}
	if !slices.Equal(after.Members, before.Members[:1]) {
		t.Errorf("members = %+v, want nur den Admin", after.Members)
	}
	sameOutsideMembers(t, before, after)

	got := readFile(t, path)
	wantText := strings.Replace(commentedConfig, "  - email: freund@example.org\n    minecraft: FreundName\n", "", 1)
	if got != wantText {
		t.Errorf("Datei nach RemoveMember:\n%s\nwant:\n%s", got, wantText)
	}

	// Unbekanntes Mitglied: Fehler, Datei unverändert.
	if err := RemoveMember(path, "fremd@example.org"); err == nil {
		t.Error("RemoveMember(fremd): kein Fehler")
	}
	if readFile(t, path) != wantText {
		t.Error("Datei nach fehlgeschlagenem RemoveMember verändert")
	}
}

// Ohne Blockliste schreibt AddMember über den yaml.v3-Knoten neu: Kommentare
// und übrige Schlüssel bleiben auch dann.
func TestAddMemberWithoutBlockList(t *testing.T) {
	tests := map[string]string{
		"Flow-Liste": `# Kopf
auth: {mode: dev}   # Modus
# Mitglieder
members: [{email: a@x.de, minecraft: Anna, admin: true}]
# Ende
world_settings: {pvp: "true"}
`,
		"members fehlt": `# Kopf
auth:
  mode: dev
# Ende
world_settings:
  pvp: "true"
`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, content, 0o600)
			before, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			bob := Member{Email: "bob@wanderwelt.invalid", Minecraft: "Bob", Admin: true}
			if err := AddMember(path, bob); err != nil {
				t.Fatalf("AddMember: %v", err)
			}
			after, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if want := append(slices.Clone(before.Members), bob); !slices.Equal(after.Members, want) {
				t.Errorf("members = %+v, want %+v", after.Members, want)
			}
			sameOutsideMembers(t, before, after)
			got := readFile(t, path)
			for _, l := range strings.Split(content, "\n") {
				if c, ok := strings.CutPrefix(strings.TrimSpace(l), "#"); ok && !strings.Contains(got, c) {
					t.Errorf("Kommentar %q verloren:\n%s", l, got)
				}
			}
		})
	}
}

// Die Beispiel-Konfiguration lädt (samt provisioning) und lässt sich
// zeilengenau ändern: Hinzufügen und wieder Entfernen ergibt dieselbe Datei.
func TestEditExampleConfig(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "koordinator.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, string(example), 0o600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Beispiel-Konfiguration: %v", err)
	}
	if p := cfg.Provisioning.WithDefaults(); p.Check(true) != nil || p.WebURL != "https://tunnel.example.org" {
		t.Errorf("provisioning des Beispiels: %+v, Check: %v", p, p.Check(true))
	}
	if err := AddMember(path, Member{Email: "neu@wanderwelt.invalid", Minecraft: "Neu"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if !isSubsequence(strings.Split(string(example), "\n"), strings.Split(readFile(t, path), "\n")) {
		t.Errorf("Beispiel nach AddMember nicht zeilengenau erhalten:\n%s", readFile(t, path))
	}
	if err := RemoveMember(path, "neu@wanderwelt.invalid"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if got := readFile(t, path); got != string(example) {
		t.Errorf("nach Hinzufügen und Entfernen:\n%s\nwant:\n%s", got, example)
	}
}

func TestAddMemberRejectsDuplicateEmail(t *testing.T) {
	path := writeConfig(t, commentedConfig, 0o600)
	if err := AddMember(path, Member{Email: "Freund@Example.org", Minecraft: "Neu"}); err == nil {
		t.Fatal("AddMember mit vorhandener E-Mail: kein Fehler")
	}
	if readFile(t, path) != commentedConfig {
		t.Error("Datei nach abgelehntem AddMember verändert")
	}
}
