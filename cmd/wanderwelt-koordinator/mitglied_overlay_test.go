package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// setupWithData legt die Konfiguration mit einem eigenen data_dir an und
// schreibt dort die Überlagerungen (Dateiname → Inhalt).
func setupWithData(t *testing.T, overlays map[string]string) (cfgPath, aus, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	content := strings.Replace(testConfig, "data_dir: /var/lib/wanderwelt", "data_dir: "+filepath.ToSlash(dataDir), 1)
	if content == testConfig {
		t.Fatal("data_dir steht nicht in testConfig")
	}
	cfgPath, aus = setup(t, content)
	for name, data := range overlays {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath, aus, dataDir
}

// „mitglied liste“ zeigt die wirksamen Namen und Rollen, wie der
// Koordinator sie sieht, und markiert, was von der Konfiguration abweicht.
func TestMitgliedListeZeigtWirksameRollen(t *testing.T) {
	cfgPath, _, _ := setupWithData(t, map[string]string{
		config.RolesFile: `{"bob@wanderwelt.invalid": {"admin": true, "op": true}, "weg@example.org": {"admin": true}}`,
		config.NamesFile: `{"freund@example.org": {"minecraft": "Kumpel", "by_admin": true}}`,
	})
	code, stdout, stderr := runMitglied(t, &fakeRunner{}, "liste", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	want := [][]string{
		{"E-MAIL", "MINECRAFT", "ROLLEN"},
		{"admin@example.org", "AdminName", "admin,", "op"},
		{"bob@wanderwelt.invalid", "Bob_42", "admin,", "op", "*"},
		{"freund@example.org", "Kumpel", "*", "-"},
	}
	if len(lines) != len(want)+1 {
		t.Fatalf("Ausgabe:\n%s", stdout)
	}
	for i, w := range want {
		if got := strings.Fields(lines[i]); strings.Join(got, " ") != strings.Join(w, " ") {
			t.Errorf("Zeile %d: %q, want %q", i, lines[i], strings.Join(w, " "))
		}
	}
	if !strings.Contains(lines[len(lines)-1], "weicht von der Konfiguration ab") {
		t.Errorf("Fußnote fehlt:\n%s", stdout)
	}
	if strings.Contains(stdout, "weg@example.org") {
		t.Errorf("Nicht-Mitglied aus roles.json in der Liste:\n%s", stdout)
	}

	// Kaputte roles.json: Warnung, die Rollen aus der Konfiguration gelten.
	cfgPath, _, _ = setupWithData(t, map[string]string{config.RolesFile: `{kaputt`})
	code, stdout, stderr = runMitglied(t, &fakeRunner{}, "liste", "--config", cfgPath)
	if code != 0 || !strings.Contains(stderr, "Warnung") || !strings.Contains(stderr, "roles.json") {
		t.Fatalf("Exit %d, stderr: %s", code, stderr)
	}
	if strings.Contains(stdout, "*") || !strings.Contains(stdout, "bob@wanderwelt.invalid  Bob_42") {
		t.Errorf("Ausgabe mit kaputter roles.json:\n%s", stdout)
	}
}

// „mitglied entfernen“ zählt die wirksamen Admins (auch die im Fenster
// gesetzten) und findet Mitglieder auch über einen Namen aus names.json.
func TestMitgliedEntfernenWirksameRollen(t *testing.T) {
	const users = `[{"id": 1, "email": "admin@example.org", "created_at": "", "disabled_at": ""},
  {"id": 3, "email": "freund@example.org", "created_at": "", "disabled_at": ""}]`

	// Der Admin aus der Konfiguration ist im Fenster keiner mehr, Bob ist
	// jetzt der einzige: Bob bleibt, der frühere Admin darf gehen.
	cfgPath, _, _ := setupWithData(t, map[string]string{
		config.RolesFile: `{"admin@example.org": {"admin": false, "op": true}, "bob@wanderwelt.invalid": {"admin": true}}`,
	})
	code, _, stderr := runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, "Bob_42")
	if code != 1 || !strings.Contains(stderr, "letzte Admin-Mitglied") {
		t.Fatalf("einziger wirksamer Admin: Exit %d, stderr: %s", code, stderr)
	}
	if code, _, stderr := runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, "admin@example.org"); code != 0 {
		t.Fatalf("Admin nur laut Konfiguration: Exit %d, stderr: %s", code, stderr)
	}
	if ms := members(t, cfgPath); len(ms) != 2 {
		t.Errorf("Mitglieder: %+v", ms)
	}

	// Über den im Fenster gesetzten Namen.
	cfgPath, _, _ = setupWithData(t, map[string]string{
		config.NamesFile: `{"freund@example.org": {"minecraft": "Kumpel", "by_admin": true}}`,
	})
	code, stdout, stderr := runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, "kumpel")
	if code != 0 {
		t.Fatalf("über Namen aus names.json: Exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Mitglied entfernt: Kumpel (freund@example.org)") {
		t.Errorf("stdout: %s", stdout)
	}
	code, _, stderr = runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, " ")
	if code != 1 || !strings.Contains(stderr, "leer") {
		t.Errorf("leerer Name: Exit %d, stderr: %s", code, stderr)
	}
}

// „mitglied neu“ geht ohne --minecraft: kein Schlüssel minecraft in der
// Konfiguration, Hinweis auf das Melden beim Einrichten. Ein Name, den ein
// Mitglied schon über names.json hat, ist vergeben.
func TestMitgliedNeuOhneMinecraft(t *testing.T) {
	cfgPath, aus, _ := setupWithData(t, map[string]string{
		config.NamesFile: `{"freund@example.org": {"minecraft": "Kumpel", "by_admin": true}}`,
	})
	f := &fakeRunner{users: `[]`}
	code, stdout, stderr := runMitglied(t, f, "neu", "--name", "dora", "--aus", aus, "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d, stderr: %s", code, stderr)
	}
	ms := members(t, cfgPath)
	if last := ms[len(ms)-1]; last != (config.Member{Email: "dora@wanderwelt.invalid"}) {
		t.Errorf("neues Mitglied: %+v", last)
	}
	if !strings.Contains(stdout, "noch ohne Minecraft-Namen") || strings.Contains(stdout, "UUID nicht auflösbar") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if _, err := group.LoadGroup(filepath.Join(aus, "gruppe-dora.yaml")); err != nil {
		t.Errorf("Gruppendatei: %v", err)
	}

	f = &fakeRunner{users: `[]`}
	code, _, stderr = runMitglied(t, f, "neu", "--name", "erik", "--minecraft", "kumpel", "--aus", aus, "--config", cfgPath)
	if code != 1 || !strings.Contains(stderr, "gehört schon zum Mitglied freund@example.org") {
		t.Errorf("vergebener Name aus names.json: Exit %d, stderr: %s", code, stderr)
	}
	if len(f.calls) != 0 {
		t.Errorf("tunneld trotz Ablehnung aufgerufen: %v", f.calls)
	}
}

// Angenommene Mitglieder aus members.json: „mitglied liste“ zeigt sie,
// „mitglied entfernen“ nimmt sie aus members.json (nicht aus der
// Konfiguration), und ein Name aus members.json ist für „neu“ vergeben.
func TestMitgliedBeigetreten(t *testing.T) {
	const users = `[{"id": 4, "email": "dora@example.org", "created_at": "", "disabled_at": ""}]`
	cfgPath, _, dataDir := setupWithData(t, map[string]string{
		config.MembersFile: `{"dora@example.org": {"email": "Dora@Example.org", "minecraft": "Dora", "accepted_by": "admin@example.org", "at": "2026-11-24T10:00:00Z"},
  "freund@example.org": {"email": "freund@example.org", "minecraft": "Doppelt", "accepted_by": "x", "at": "2026-11-24T10:00:00Z"}}`,
		config.NamesFile: `{"dora@example.org": {"minecraft": "DoraNeu", "by_admin": true}}`,
	})
	code, stdout, stderr := runMitglied(t, &fakeRunner{}, "liste", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	// Dora einmal (angenommen, mit Admin-Namen), Freund nur aus der Konfiguration.
	if strings.Count(stdout, "Freund") != 1 || strings.Contains(stdout, "Doppelt") ||
		!strings.Contains(stdout, "Dora@Example.org (beigetreten)") || !strings.Contains(stdout, "DoraNeu *") {
		t.Errorf("Liste:\n%s", stdout)
	}

	code, _, stderr = runMitglied(t, &fakeRunner{}, "neu", "--name", "x", "--minecraft", "DoraNeu", "--config", cfgPath)
	if code != 1 || !strings.Contains(stderr, "gehört schon zum Mitglied Dora@Example.org") {
		t.Errorf("vergebener Name: Exit %d, stderr: %s", code, stderr)
	}

	before := len(members(t, cfgPath))
	code, stdout, stderr = runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, "doranEU")
	if code != 0 {
		t.Fatalf("entfernen: Exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Mitglied entfernt: DoraNeu (Dora@Example.org)") {
		t.Errorf("stdout: %s", stdout)
	}
	if got := len(members(t, cfgPath)); got != before {
		t.Errorf("Konfiguration geändert: %d Mitglieder, vorher %d", got, before)
	}
	j, err := config.LoadJoined(filepath.Join(dataDir, config.MembersFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := j["dora@example.org"]; ok || len(j) != 1 {
		t.Errorf("members.json nach dem Entfernen: %+v", j)
	}
}

// Steht ein Mitglied in der Konfiguration UND in members.json, geht es aus
// beiden (und seine offene Anfrage aus joins.json); die Meldung sagt, woher.
func TestMitgliedEntfernenAusKonfigurationUndMembersJson(t *testing.T) {
	const users = `[{"id": 3, "email": "freund@example.org", "created_at": "", "disabled_at": ""}]`
	cfgPath, _, dataDir := setupWithData(t, map[string]string{
		config.MembersFile: `{"freund@example.org": {"email": "freund@example.org", "minecraft": "Freund", "accepted_by": "x", "at": "2026-11-24T10:00:00Z"},
  "dora@example.org": {"email": "dora@example.org", "minecraft": "Dora", "accepted_by": "x", "at": "2026-11-24T10:00:00Z"}}`,
		config.JoinsFile: `{"freund@example.org": {"email": "freund@example.org", "minecraft": "Freund", "requested": "2026-11-24T09:00:00Z", "state": "offen"}}`,
	})
	code, stdout, stderr := runMitglied(t, &fakeRunner{users: users}, "entfernen", "--config", cfgPath, "freund@example.org")
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	for _, w := range []string{"aus der Konfiguration", "aus " + config.MembersFile, "24 Stunden gesperrt", "tunnel-Konto freund@example.org bleibt bestehen", "tunneld-Admin-Panel"} {
		if !strings.Contains(stdout, w) {
			t.Errorf("Meldung ohne %q: %s", w, stdout)
		}
	}
	if ms := members(t, cfgPath); len(ms) != 2 {
		t.Errorf("Konfiguration: %+v", ms)
	}
	j, err := config.LoadJoined(filepath.Join(dataDir, config.MembersFile))
	if err != nil || len(j) != 1 || j["dora@example.org"].Minecraft != "Dora" {
		t.Errorf("members.json: %+v, %v", j, err)
	}
	js, err := config.LoadJoins(filepath.Join(dataDir, config.JoinsFile))
	if e, ok := js["freund@example.org"]; err != nil || len(js) != 1 || !ok || e.State != config.JoinStateDeclined ||
		e.Minecraft != "Freund" || time.Since(e.Decided) > time.Minute {
		t.Errorf("joins.json: Sperre fehlt: %+v, %v", js, err)
	}
	eff, _ := config.EffectiveMembers(members(t, cfgPath), dataDir)
	for _, m := range eff {
		if m.Email == "freund@example.org" {
			t.Error("Freund gilt weiter als Mitglied")
		}
	}
}

// Der Betreiber behält die Sicht: „mitglied liste“ zeigt neben der E-Mail die
// Kennung, die Mitglieder und Admins im Fenster sehen, sobald der Koordinator
// den Schlüssel angelegt hat; „mitglied entfernen“ findet ein Mitglied auch
// über die Kennung.
func TestMitgliedListeZeigtKennung(t *testing.T) {
	cfgPath, _, dataDir := setupWithData(t, nil)
	k, _, err := kennung.Laden(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runMitglied(t, &fakeRunner{}, "liste", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d: %s", code, stderr)
	}
	for _, email := range []string{"admin@example.org", "bob@wanderwelt.invalid"} {
		if !strings.Contains(stdout, email+" ") || !strings.Contains(stdout, k.ID(email)) {
			t.Errorf("Zeile zu %s ohne Kennung %s:\n%s", email, k.ID(email), stdout)
		}
	}
	if !strings.Contains(stdout, "KENNUNG") {
		t.Errorf("Spaltenkopf KENNUNG fehlt:\n%s", stdout)
	}
	// Ein Mitglied lässt sich über die Kennung entfernen (hier scheitert es
	// erst am letzten Admin: Es wurde also gefunden).
	code, _, stderr = runMitglied(t, &fakeRunner{}, "entfernen", "--config", cfgPath, k.ID("admin@example.org"))
	if code != 1 || !strings.Contains(stderr, "letzte Admin-Mitglied") {
		t.Errorf("entfernen über Kennung: Exit %d, stderr: %s", code, stderr)
	}
}
