package config

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Die Überlagerung setzt Admin und Op der genannten Mitglieder (E-Mail
// Groß/Klein egal); Einträge für Unbekannte zählen nicht, die Eingabe
// bleibt unverändert.
func TestRolesOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), RolesFile)
	writeTestFile(t, path, `{
  "Bob@Example.org": {"admin": true, "op": false},
  "alice@example.org": {"admin": false, "op": false},
  "fremd@example.org": {"admin": true, "op": true}
}`)
	r, err := LoadRoles(path)
	if err != nil {
		t.Fatal(err)
	}
	members := []Member{
		{Email: "alice@example.org", Minecraft: "Alice", Admin: true, Op: true},
		{Email: "BOB@example.org", Minecraft: "Bob"},
		{Email: "carol@example.org", Minecraft: "Carol", Op: true},
	}
	orig := slices.Clone(members)
	got := ApplyRoles(members, r)
	want := []Member{
		{Email: "alice@example.org", Minecraft: "Alice"},
		{Email: "BOB@example.org", Minecraft: "Bob", Admin: true},
		{Email: "carol@example.org", Minecraft: "Carol", Op: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("ApplyRoles:\n%+v\nwant\n%+v", got, want)
	}
	if !slices.Equal(members, orig) {
		t.Errorf("Eingabe verändert: %+v", members)
	}
}

// SaveRoles schreibt atomar mit 0600; LoadRoles liest es zurück. Eine
// fehlende Datei ist leer.
func TestSaveLoadOverlays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, RolesFile)
	if r, err := LoadRoles(path); err != nil || len(r) != 0 {
		t.Fatalf("fehlende Datei: %v, %v", r, err)
	}
	want := Roles{"bob@example.org": {Admin: true, Op: true}}
	if err := SaveRoles(path, want); err != nil {
		t.Fatal(err)
	}
	if err := SaveRoles(path, want); err != nil { // ersetzt die vorhandene
		t.Fatal(err)
	}
	got, err := LoadRoles(path)
	if err != nil || len(got) != 1 || got["bob@example.org"] != (api.RoleRequest{Admin: true, Op: true}) {
		t.Fatalf("zurückgelesen: %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("Rechte %v, want 0600", fi.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("Temp-Dateien liegen geblieben: %v", entries)
	}

	names := Names{"carol@example.org": {Minecraft: "Carol", ByAdmin: true}}
	npath := filepath.Join(dir, NamesFile)
	if err := SaveNames(npath, names); err != nil {
		t.Fatal(err)
	}
	if n, err := LoadNames(npath); err != nil || n["carol@example.org"] != names["carol@example.org"] {
		t.Errorf("names.json: %+v, %v", n, err)
	}
	spath := filepath.Join(dir, SettingsFile)
	if s, err := LoadSettings(spath); err != nil || s.StorageBudgetBytes != 0 {
		t.Errorf("fehlendes settings.json: %+v, %v", s, err)
	}
	if err := SaveSettings(spath, Settings{StorageBudgetBytes: 20 << 30}); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadSettings(spath); err != nil || s.StorageBudgetBytes != 20<<30 {
		t.Errorf("settings.json: %+v, %v", s, err)
	}
}

// Kaputte Überlagerungen sind Fehler (der Koordinator lässt sie dann nicht
// gelten).
func TestOverlaysCorrupt(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name, file, content string
	}{
		{"leer", RolesFile, ""},
		{"kein JSON", RolesFile, `{"a@x.de": {"admin": tr`},
		{"unbekanntes Feld", RolesFile, `{"a@x.de": {"admin": true, "farbe": 1}}`},
		{"doppelte E-Mail", RolesFile, `{"a@x.de": {"admin": true}, "A@x.de": {"op": true}}`},
		{"leere E-Mail", RolesFile, `{"": {"admin": true}}`},
		{"Daten danach", RolesFile, `{"a@x.de": {"admin": true}} {}`},
		{"Liste statt Objekt", RolesFile, `[{"admin": true}]`},
		{"ungültiger Name", NamesFile, `{"a@x.de": {"minecraft": "Anna Lena"}}`},
		{"leerer Name", NamesFile, `{"a@x.de": {"minecraft": ""}}`},
		{"negatives Budget", SettingsFile, `{"storage_budget_bytes": -5}`},
		{"Budget als Text", SettingsFile, `{"storage_budget_bytes": "viel"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.file)
			writeTestFile(t, path, c.content)
			var err error
			switch c.file {
			case RolesFile:
				_, err = LoadRoles(path)
			case NamesFile:
				_, err = LoadNames(path)
			default:
				_, err = LoadSettings(path)
			}
			if err == nil {
				t.Fatal("kein Fehler")
			}
			if !strings.Contains(err.Error(), c.file) {
				t.Errorf("Fehler ohne Dateinamen: %v", err)
			}
		})
	}
}

// Ein selbst gemeldeter Name gilt nur ohne Namen in der Konfiguration, einer
// vom Admin immer; EffectiveMembers wendet Namen und Rollen an und lässt
// eine kaputte Überlagerung weg.
func TestEffectiveMembers(t *testing.T) {
	members := []Member{
		{Email: "a@x.de", Minecraft: "Anna", Admin: true},
		{Email: "b@x.de"},
		{Email: "c@x.de", Minecraft: "Carl"},
		{Email: "d@x.de", Minecraft: "Dora"},
	}
	names := Names{
		"b@x.de": {Minecraft: "Berta"},
		"c@x.de": {Minecraft: "Carla"},
		"d@x.de": {Minecraft: "Doro", ByAdmin: true},
	}
	got := ApplyNames(members, names)
	var mc []string
	for _, m := range got {
		mc = append(mc, m.Minecraft)
	}
	if want := []string{"Anna", "Berta", "Carl", "Doro"}; !slices.Equal(mc, want) {
		t.Errorf("ApplyNames: %v, want %v", mc, want)
	}

	dir := t.TempDir()
	if err := SaveNames(filepath.Join(dir, NamesFile), names); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, RolesFile), "{kaputt")
	eff, err := EffectiveMembers(members, dir)
	if err == nil || !strings.Contains(err.Error(), RolesFile) {
		t.Errorf("kaputte roles.json ohne Fehler: %v", err)
	}
	if eff[1].Minecraft != "Berta" || !eff[0].Admin {
		t.Errorf("EffectiveMembers: %+v", eff)
	}
	if err := SaveRoles(filepath.Join(dir, RolesFile), Roles{"a@x.de": {}, "b@x.de": {Admin: true}}); err != nil {
		t.Fatal(err)
	}
	eff, err = EffectiveMembers(members, dir)
	if err != nil || eff[0].Admin || !eff[1].Admin || eff[1].Minecraft != "Berta" {
		t.Errorf("EffectiveMembers: %+v, %v", eff, err)
	}
	if eff, err := EffectiveMembers(members, ""); err != nil || !slices.Equal(eff, members) {
		t.Errorf("ohne data_dir: %+v, %v", eff, err)
	}
}

// Mitglieder dürfen ohne Minecraft-Namen in der Konfiguration stehen;
// AddMember schreibt dann keinen Schlüssel minecraft.
func TestMemberWithoutMinecraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "koordinator.yaml")
	writeTestFile(t, path, "auth: {mode: tunneld}\nmembers:\n  - email: a@x.de\n    minecraft: Anna\n    admin: true\n  - email: b@x.de\n")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Members) != 2 || c.Members[1].Minecraft != "" {
		t.Fatalf("Mitglieder: %+v", c.Members)
	}
	if err := AddMember(path, Member{Email: "c@x.de", Op: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "  - email: c@x.de\n    op: true\n") {
		t.Errorf("Eintrag ohne minecraft:\n%s", data)
	}
	writeTestFile(t, path, "auth: {mode: tunneld}\nmembers:\n  - minecraft: Anna\n")
	if _, err := Load(path); err == nil {
		t.Error("Mitglied ohne E-Mail geladen")
	}
}

// members.json und joins.json: gültige Dateien laden, die Konfiguration
// gewinnt bei gleicher E-Mail, kaputte Einträge sind Fehler, RemoveJoined
// streicht genau einen Eintrag.
func TestJoinedAndJoins(t *testing.T) {
	dir := t.TempDir()
	mp := filepath.Join(dir, MembersFile)
	j := JoinedMembers{
		"dora@example.org":  {Email: "Dora@example.org", Minecraft: "Dora", AcceptedBy: "a@example.org", At: time.Unix(200, 0).UTC()},
		"emil@example.org":  {Email: "emil@example.org", Minecraft: "Emil", AcceptedBy: "a@example.org", At: time.Unix(100, 0).UTC()},
		"alice@example.org": {Email: "alice@example.org", Minecraft: "Zweitname", At: time.Unix(50, 0).UTC()},
	}
	if err := SaveJoined(mp, j); err != nil {
		t.Fatal(err)
	}
	got, err := LoadJoined(mp)
	if err != nil || len(got) != 3 || got["dora@example.org"].Email != "Dora@example.org" {
		t.Fatalf("LoadJoined: %+v, %v", got, err)
	}
	cfg := []Member{{Email: "ALICE@example.org", Minecraft: "Alice", Admin: true}}
	merged := MergeJoined(cfg, got)
	want := []Member{cfg[0], {Email: "emil@example.org", Minecraft: "Emil"}, {Email: "Dora@example.org", Minecraft: "Dora"}}
	if !slices.Equal(merged, want) {
		t.Errorf("MergeJoined: %+v", merged)
	}
	if eff, err := EffectiveMembers(cfg, dir); err != nil || !slices.Equal(eff, want) {
		t.Errorf("EffectiveMembers: %+v, %v", eff, err)
	}
	if err := RemoveJoined(dir, "DORA@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveJoined(dir, "dora@example.org"); err == nil {
		t.Error("zweites Entfernen ohne Fehler")
	}
	if got, _ := LoadJoined(mp); len(got) != 2 {
		t.Errorf("nach dem Entfernen: %+v", got)
	}

	for name, body := range map[string]string{
		"E-Mail passt nicht": `{"a@example.org": {"email": "b@example.org", "minecraft": "Abc"}}`,
		"Name ungültig":      `{"a@example.org": {"email": "a@example.org", "minecraft": "A b"}}`,
		"unbekanntes Feld":   `{"a@example.org": {"email": "a@example.org", "minecraft": "Abc", "x": 1}}`,
	} {
		p := filepath.Join(dir, "kaputt.json")
		os.WriteFile(p, []byte(body), 0o600)
		if _, err := LoadJoined(p); err == nil {
			t.Errorf("members.json %q: kein Fehler", name)
		}
	}

	jp := filepath.Join(dir, JoinsFile)
	e := JoinEntries{"a@example.org": {Email: "a@example.org", Minecraft: "Abc", Requested: time.Unix(1, 0).UTC(), State: JoinStateOpen}}
	if err := SaveJoins(jp, e); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadJoins(jp); err != nil || got["a@example.org"].State != JoinStateOpen {
		t.Errorf("LoadJoins: %+v, %v", got, err)
	}
	os.WriteFile(jp, []byte(`{"a@example.org": {"email": "a@example.org", "minecraft": "Abc", "state": "angenommen"}}`), 0o600)
	if _, err := LoadJoins(jp); err == nil {
		t.Error("joins.json mit Zustand angenommen: kein Fehler")
	}
}
