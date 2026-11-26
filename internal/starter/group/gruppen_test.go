package group

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("WANDERWELT_HOME", h)
	return h
}

func groupYAML(coord string) string {
	return "coordinator: " + coord + "\ntunnel_server: t.example.org:7443\ntoken: geheimes-token-123\n"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIDFromCoordinator(t *testing.T) {
	ok := map[string]string{
		"https://wanderwelt.example.org":  "wanderwelt.example.org",
		"https://Wanderwelt.Example.ORG/": "wanderwelt.example.org",
		"http://localhost:8470":           "localhost-8470",
		"http://127.0.0.1:9":              "127.0.0.1-9",
		"https://a-b.c.example.org:443":   "a-b.c.example.org-443",
	}
	for in, want := range ok {
		got, err := IDFromCoordinator(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "nicht-url", "https://", "https://[::1]:8470", "https://..", "https://.", "https://-x.example.org",
		"https://ü.example.org", "https://a..b", "https://" + strings.Repeat("a", 120) + ".org",
		"https://exa mple.org",
	}
	for _, in := range bad {
		if id, err := IDFromCoordinator(in); err == nil {
			t.Errorf("%q: erwartet Fehler, bekam %q", in, id)
		}
	}
}

func TestValidIDKeinePfadtricks(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "A", "a b", ".hidden", "-x", "a..b", "a.", "con:", "a\x00b", "../x"} {
		if ValidID(id) {
			t.Errorf("%q darf keine gültige ID sein", id)
		}
	}
	if IDDir("../x") != "" {
		t.Error("IDDir muss ungültige IDs verweigern")
	}
	for _, id := range []string{"a", "wanderwelt.example.org", "localhost-8470", "1"} {
		if !ValidID(id) {
			t.Errorf("%q sollte gültig sein", id)
		}
	}
}

func TestOhneGruppeDirIstHome(t *testing.T) {
	h := testHome(t)
	if Dir() != h || ActiveID() != "" || len(List()) != 0 || Legacy() {
		t.Fatalf("Dir %q aktiv %q", Dir(), ActiveID())
	}
}

func TestAddGroupUndWahl(t *testing.T) {
	h := testHome(t)
	a := &Group{Coordinator: "https://a.example.org", TunnelServer: "t:1", Token: "tok-a"}
	b := &Group{Coordinator: "https://b.example.org", TunnelServer: "t:1", Token: "tok-b"}
	if err := AddGroup(b, true); err != nil {
		t.Fatal(err)
	}
	if err := AddGroup(a, false); err != nil {
		t.Fatal(err)
	}
	if ActiveID() != "b.example.org" {
		t.Fatalf("aktiv %q", ActiveID())
	}
	if Dir() != filepath.Join(h, "gruppen", "b.example.org") {
		t.Fatal(Dir())
	}
	if l := List(); len(l) != 2 || l[0].ID != "a.example.org" || l[1].ID != "b.example.org" {
		t.Fatalf("Liste %+v", l)
	}
	if err := SetActive("a.example.org"); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadGroup(GroupPath()); err != nil || got.Token != "tok-a" {
		t.Fatalf("GroupPath %v %v", got, err)
	}
	if err := SetActive("../x"); err == nil {
		t.Error("ungültige Wahl")
	}
	if err := SetActive("c.example.org"); err == nil {
		t.Error("unbekannte Wahl")
	}
	// Gelöschte aktive Gruppe: erste.
	os.RemoveAll(IDDir("a.example.org"))
	if ActiveID() != "b.example.org" {
		t.Errorf("Rückfall: %q", ActiveID())
	}
	// Kaputte aktiv.json: erste.
	writeFile(t, filepath.Join(h, "aktiv.json"), "müll")
	if ActiveID() != "b.example.org" {
		t.Errorf("kaputte aktiv.json: %q", ActiveID())
	}
}

func TestStateGehoertZurGruppe(t *testing.T) {
	testHome(t)
	for _, c := range []string{"https://a.example.org", "https://b.example.org"} {
		if err := AddGroup(&Group{Coordinator: c, TunnelServer: "t:1", Token: "x"}, false); err != nil {
			t.Fatal(err)
		}
	}
	SetActive("a.example.org")
	sa, err := LoadState()
	if err != nil {
		t.Fatal(err)
	}
	sa.MachineID = "pc-a"
	if err := sa.Save(); err != nil {
		t.Fatal(err)
	}
	// Wechsel: sa schreibt weiter in Gruppe a, nie in b.
	SetActive("b.example.org")
	sa.LastUploadedGeneration = 7
	if err := sa.Save(); err != nil {
		t.Fatal(err)
	}
	sb, _ := LoadState()
	if sb.MachineID != "" || sb.LastUploadedGeneration != 0 {
		t.Fatalf("Zustand von b verfälscht: %+v", sb)
	}
	if !strings.Contains(readFile(t, filepath.Join(IDDir("a.example.org"), "state.json")), `"last_uploaded_generation": 7`) {
		t.Error("Zustand von a nicht gespeichert")
	}
	if _, err := os.Stat(filepath.Join(IDDir("b.example.org"), "state.json")); err == nil {
		t.Error("b hat einen Zustand bekommen")
	}
}

func TestInstallGroupSchreibtInGruppenordner(t *testing.T) {
	h := testHome(t)
	src := filepath.Join(t.TempDir(), "g.yaml")
	writeFile(t, src, "# Kommentar\n"+groupYAML("https://x.example.org"))
	if _, err := InstallGroup(src); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, filepath.Join(h, "gruppen", "x.example.org", "gruppe.yaml")), "# Kommentar") {
		t.Error("Kommentar verloren")
	}
	if ActiveID() != "x.example.org" {
		t.Error(ActiveID())
	}
	if _, err := os.Stat(filepath.Join(h, "gruppe.yaml")); err == nil {
		t.Error("Gruppendatei in Home()")
	}
}

func TestAddGroupVerweigertLink(t *testing.T) {
	h := testHome(t)
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, "gruppen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(h, "gruppen", "x.example.org")); err != nil {
		t.Skip("keine Links möglich:", err)
	}
	if err := AddGroup(&Group{Coordinator: "https://x.example.org", TunnelServer: "t:1", Token: "x"}, true); err == nil {
		t.Fatal("Link muss verweigert werden")
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Error("außerhalb geschrieben")
	}
}

func legacyLayout(t *testing.T, h string) {
	t.Helper()
	writeFile(t, filepath.Join(h, "gruppe.yaml"), groupYAML("https://Alt.Example.org"))
	writeFile(t, filepath.Join(h, "state.json"), `{"machine_id":"pc1","installed_version":"1"}`)
	writeFile(t, filepath.Join(h, "profil.yaml"), "x: 1\n")
	writeFile(t, filepath.Join(h, "tunnel.yaml"), "token: t\n")
	writeFile(t, filepath.Join(h, "notfallkopie.json"), "{}")
	writeFile(t, filepath.Join(h, "server", "world", "level.dat"), "WELT")
	writeFile(t, filepath.Join(h, "verworfen-20260101-000000", "a.txt"), "alt")
	writeFile(t, filepath.Join(h, "hashcache.json"), "{}")
	writeFile(t, filepath.Join(h, "deps", "x.jar"), "jar")
	writeFile(t, filepath.Join(h, "logs", "wanderwelt.log"), "log")
	writeFile(t, filepath.Join(h, "wanderwelt.exe"), "exe")
}

func TestLegacyLayoutBleibtBenutzbar(t *testing.T) {
	h := testHome(t)
	legacyLayout(t, h)
	if !Legacy() || Dir() != h || ActiveID() != "alt.example.org" {
		t.Fatalf("legacy %v dir %q id %q", Legacy(), Dir(), ActiveID())
	}
	if _, err := LoadGroup(GroupPath()); err != nil {
		t.Fatal(err)
	}
	if err := SetActive("alt.example.org"); err == nil {
		t.Error("Wechsel im alten Layout")
	}
}

func TestMigrateLegacy(t *testing.T) {
	h := testHome(t)
	legacyLayout(t, h)
	id, err := MigrateLegacy()
	if err != nil || id != "alt.example.org" {
		t.Fatalf("%q %v", id, err)
	}
	dir := filepath.Join(h, "gruppen", id)
	for _, p := range []string{"gruppe.yaml", "state.json", "profil.yaml", "tunnel.yaml", "notfallkopie.json",
		filepath.Join("server", "world", "level.dat"), filepath.Join("verworfen-20260101-000000", "a.txt")} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s fehlt im Gruppenordner: %v", p, err)
		}
	}
	if readFile(t, filepath.Join(dir, "server", "world", "level.dat")) != "WELT" {
		t.Error("Weltdatei verändert")
	}
	for _, p := range []string{"gruppe.yaml", "state.json", "profil.yaml", "tunnel.yaml", "notfallkopie.json", "server", "verworfen-20260101-000000"} {
		if _, err := os.Lstat(filepath.Join(h, p)); err == nil {
			t.Errorf("%s liegt noch in Home()", p)
		}
	}
	for _, p := range []string{"hashcache.json", "wanderwelt.exe", filepath.Join("deps", "x.jar"), filepath.Join("logs", "wanderwelt.log")} {
		if _, err := os.Stat(filepath.Join(h, p)); err != nil {
			t.Errorf("gemeinsame Datei %s weg: %v", p, err)
		}
	}
	if Legacy() || ActiveID() != id || Dir() != dir {
		t.Errorf("danach: legacy %v aktiv %q dir %q", Legacy(), ActiveID(), Dir())
	}
	if st, err := LoadState(); err != nil || st.MachineID != "pc1" {
		t.Errorf("Zustand %+v %v", st, err)
	}
	// Zweiter Lauf: nichts zu tun.
	if id2, err := MigrateLegacy(); id2 != "" || err != nil {
		t.Errorf("zweiter Lauf: %q %v", id2, err)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			m[rel] = "<dir>"
		} else {
			b, _ := os.ReadFile(p)
			m[rel] = string(b)
		}
		return nil
	})
	return m
}

func TestMigrateFehlerLegtAllesZurueck(t *testing.T) {
	// Der Fehler tritt bei jedem Schritt einmal auf; danach ist alles wie vorher.
	for fail := 1; fail <= 7; fail++ {
		h := testHome(t)
		legacyLayout(t, h)
		before := snapshot(t, h)
		n := 0
		orig := rename
		rename = func(from, to string) error {
			// Nur Vorwärtsschritte (nach gruppen\) zählen.
			if strings.Contains(to, "gruppen") {
				n++
				if n == fail {
					return errors.New("Zugriff verweigert (Test)")
				}
			}
			return os.Rename(from, to)
		}
		id, err := MigrateLegacy()
		rename = orig
		if err == nil {
			t.Fatalf("fail=%d: kein Fehler, id %q", fail, id)
		}
		if id != "" {
			t.Errorf("fail=%d: id %q trotz Fehler", fail, id)
		}
		after := snapshot(t, h)
		delete(after, "gruppen") // ein leerer Ordner gruppen\<id> darf bleiben
		delete(after, filepath.Join("gruppen", "alt.example.org"))
		for k, v := range before {
			if after[k] != v {
				t.Errorf("fail=%d: %s verändert (%q -> %q)", fail, k, v, after[k])
			}
		}
		for k := range after {
			if _, ok := before[k]; !ok {
				t.Errorf("fail=%d: neu aufgetaucht: %s", fail, k)
			}
		}
		if !Legacy() {
			t.Errorf("fail=%d: Layout nicht mehr alt", fail)
		}
	}
}

func TestMigrateAbbruchMitteDrinWirdNachgeholt(t *testing.T) {
	h := testHome(t)
	legacyLayout(t, h)
	// Absturz-Simulation: ein Teil schon im Zielordner.
	dir := filepath.Join(h, "gruppen", "alt.example.org")
	os.MkdirAll(dir, 0o755)
	if err := os.Rename(filepath.Join(h, "server"), filepath.Join(dir, "server")); err != nil {
		t.Fatal(err)
	}
	if id, err := MigrateLegacy(); err != nil || id != "alt.example.org" {
		t.Fatalf("%q %v", id, err)
	}
	if readFile(t, filepath.Join(dir, "server", "world", "level.dat")) != "WELT" {
		t.Error("Welt fehlt")
	}
}

func TestMigrateKonfliktWirdVerweigert(t *testing.T) {
	h := testHome(t)
	legacyLayout(t, h)
	// Zielordner hat schon einen fremden Inhalt.
	writeFile(t, filepath.Join(h, "gruppen", "alt.example.org", "fremd.txt"), "x")
	before := snapshot(t, h)
	if _, err := MigrateLegacy(); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if got := snapshot(t, h); len(got) != len(before) {
		t.Errorf("verändert: %v", got)
	}
	// Ziel schon eingerichtet.
	h2 := testHome(t)
	legacyLayout(t, h2)
	writeFile(t, filepath.Join(h2, "gruppen", "alt.example.org", "gruppe.yaml"), groupYAML("https://alt.example.org"))
	if _, err := MigrateLegacy(); err == nil {
		t.Fatal("Fehler erwartet (Ziel eingerichtet)")
	}
	if _, err := os.Stat(filepath.Join(h2, "server", "world", "level.dat")); err != nil {
		t.Error("Welt angefasst")
	}
	// Eintrag in beiden: nie überschreiben.
	h3 := testHome(t)
	legacyLayout(t, h3)
	writeFile(t, filepath.Join(h3, "gruppen", "alt.example.org", "state.json"), "ZIEL")
	if _, err := MigrateLegacy(); err == nil {
		t.Fatal("Fehler erwartet (Doppel)")
	}
	if readFile(t, filepath.Join(h3, "gruppen", "alt.example.org", "state.json")) != "ZIEL" ||
		!strings.Contains(readFile(t, filepath.Join(h3, "state.json")), "pc1") {
		t.Error("überschrieben")
	}
}

func TestMigrateKaputteGruppendatei(t *testing.T) {
	h := testHome(t)
	writeFile(t, filepath.Join(h, "gruppe.yaml"), "müll: [")
	writeFile(t, filepath.Join(h, "state.json"), "{}")
	if _, err := MigrateLegacy(); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if _, err := os.Stat(filepath.Join(h, "state.json")); err != nil {
		t.Error("state.json bewegt")
	}
}

func TestMigrateOhneAltesLayout(t *testing.T) {
	testHome(t)
	if id, err := MigrateLegacy(); id != "" || err != nil {
		t.Fatal(id, err)
	}
}
