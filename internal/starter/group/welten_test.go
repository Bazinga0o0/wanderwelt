package group

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// gruppeEinrichten legt eine Gruppe im Temp-Ordner an und liefert ihren Ordner.
func gruppeEinrichten(t *testing.T) string {
	t.Helper()
	h := testHome(t)
	writeFile(t, filepath.Join(h, "gruppen", "wanderwelt.example.org", "gruppe.yaml"), groupYAML("https://wanderwelt.example.org"))
	if err := os.MkdirAll(filepath.Join(h, "gruppen", "wanderwelt.example.org"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Dir(); got != filepath.Join(h, "gruppen", "wanderwelt.example.org") {
		t.Fatalf("Dir() = %s", got)
	}
	return Dir()
}

func TestValidWorldID(t *testing.T) {
	for _, id := range []string{"w1", "w2", "w10", "w123456789"} {
		if !ValidWorldID(id) {
			t.Errorf("%q sollte gültig sein", id)
		}
	}
	for _, id := range []string{"", "w", "w0", "w01", "W1", "x1", "w1/..", "..", "w1\\x", "w-1", "w1234567890", "w1 ", "welt"} {
		if ValidWorldID(id) {
			t.Errorf("%q sollte ungültig sein", id)
		}
	}
}

// Die erste Welt liegt im Gruppenordner wie schon immer; jede weitere in
// ihrem eigenen Ordner unter welten\.
func TestWeltOrdner(t *testing.T) {
	dir := gruppeEinrichten(t)
	for _, id := range []string{"", "w1"} {
		if h, err := WorldHome(id); err != nil || h != dir {
			t.Errorf("WorldHome(%q) = %q, %v; want %q", id, h, err, dir)
		}
		if s, err := ServerDir(id); err != nil || s != filepath.Join(dir, "server") {
			t.Errorf("ServerDir(%q) = %q, %v", id, s, err)
		}
	}
	s2, err := ServerDir("w2")
	if err != nil || s2 != filepath.Join(dir, "welten", "w2", "server") {
		t.Fatalf("ServerDir(w2) = %q, %v", s2, err)
	}
	s3, _ := ServerDir("w3")
	if s2 == s3 || filepath.Dir(s2) == filepath.Dir(s3) {
		t.Fatal("zwei Welten teilen sich einen Ordner")
	}
	// Nie ein Pfad aus einer Kennung.
	for _, bad := range []string{"..", "../x", "w1/../../x", "/etc", `C:\x`, "w0"} {
		if _, err := ServerDir(bad); err == nil {
			t.Errorf("ServerDir(%q) ohne Fehler", bad)
		}
	}
	// Beiseitegelegte Stände liegen bei der jeweiligen Welt.
	if h := WorldHomeOfServerDir(s2); h != filepath.Dir(s2) {
		t.Errorf("WorldHomeOfServerDir(w2) = %q", h)
	}
	first, _ := ServerDir("w1")
	if h := WorldHomeOfServerDir(first); h != dir {
		t.Errorf("WorldHomeOfServerDir(w1) = %q, want %q", h, dir)
	}
	if h := WorldHomeOfServerDir(filepath.Join(t.TempDir(), "irgendwo")); h != dir {
		t.Errorf("fremder Ordner: %q", h)
	}
	if h := WorldHomeOfServerDir(filepath.Join(dir, "welten")); h != dir {
		t.Errorf("welten selbst: %q", h)
	}
}

// Der Zustand jeder Welt ist ihr eigener; PC-Kennung und Version kommen aus
// der Gruppe. Die erste Welt ist der Zustand der Gruppe selbst.
func TestWeltZustandGetrennt(t *testing.T) {
	dir := gruppeEinrichten(t)
	base := &State{MachineID: "pc-1", InstalledVersion: "0.1.2", LastUploadedWorldID: "WELT-A", LastUploadedGeneration: 7}
	if err := base.Save(); err != nil {
		t.Fatal(err)
	}
	if ws, err := LoadWorldState("w1", base); err != nil || ws != base {
		t.Fatalf("erste Welt: %p %v (Gruppe %p)", ws, err, base)
	}
	if ws, _ := LoadWorldState("", base); ws != base {
		t.Fatal("ohne Angabe nicht der Gruppenzustand")
	}

	w2, err := LoadWorldState("w2", base)
	if err != nil {
		t.Fatal(err)
	}
	if w2.MachineID != "pc-1" || w2.InstalledVersion != "0.1.2" || w2.LastUploadedWorldID != "" || w2.PendingUpload {
		t.Fatalf("frischer Zustand der zweiten Welt: %+v", w2)
	}
	w2.LastUploadedWorldID, w2.LastUploadedGeneration, w2.PendingUpload = "WELT-B", 3, true
	if err := w2.Save(); err != nil {
		t.Fatal(err)
	}
	// Die Datei liegt im Ordner der zweiten Welt, die der Gruppe blieb unberührt.
	if _, err := os.Stat(filepath.Join(dir, "welten", "w2", "state.json")); err != nil {
		t.Fatalf("state.json der zweiten Welt: %v", err)
	}
	gs, err := LoadState()
	if err != nil || gs.LastUploadedWorldID != "WELT-A" || gs.LastUploadedGeneration != 7 || gs.PendingUpload {
		t.Fatalf("Zustand der Gruppe hat sich verändert: %+v %v", gs, err)
	}
	// Neu laden: der Stand der zweiten Welt, mit der (aktuellen) PC-Kennung der Gruppe.
	base.MachineID = "pc-neu"
	again, err := LoadWorldState("w2", base)
	if err != nil || again.LastUploadedWorldID != "WELT-B" || !again.PendingUpload || again.MachineID != "pc-neu" {
		t.Fatalf("zweite Welt nach Neuladen: %+v %v", again, err)
	}
	// Eine dritte Welt weiß nichts von der zweiten.
	w3, _ := LoadWorldState("w3", base)
	if w3.LastUploadedWorldID != "" || w3.PendingUpload {
		t.Fatalf("dritte Welt: %+v", w3)
	}
	if _, err := LoadWorldState("../x", base); err == nil {
		t.Fatal("ungültige Kennung geladen")
	}
}

func TestLocalWorldsUndAuswahl(t *testing.T) {
	dir := gruppeEinrichten(t)
	if got := LocalWorlds(); !slices.Equal(got, []string{"w1"}) {
		t.Fatalf("ohne weitere Welten: %v", got)
	}
	for _, d := range []string{"w10", "w2", "w3", "unsinn", "w1"} {
		if err := os.MkdirAll(filepath.Join(dir, "welten", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(dir, "welten", "w5"), "kein Ordner")
	if got := LocalWorlds(); !slices.Equal(got, []string{"w1", "w2", "w3", "w10"}) {
		t.Fatalf("LocalWorlds: %v", got)
	}

	if SelectedWorld() != "" {
		t.Fatal("Auswahl ohne Datei")
	}
	if err := SetSelectedWorld("w2"); err != nil {
		t.Fatal(err)
	}
	if got := SelectedWorld(); got != "w2" {
		t.Fatalf("Auswahl: %q", got)
	}
	if err := SetSelectedWorld("../w2"); err == nil {
		t.Fatal("ungültige Auswahl gespeichert")
	}
	writeFile(t, filepath.Join(dir, "welt.json"), `{"gewaehlt":"../../x"}`)
	if got := SelectedWorld(); got != "" {
		t.Fatalf("manipulierte Auswahl: %q", got)
	}
	writeFile(t, filepath.Join(dir, "welt.json"), `kaputt`)
	if got := SelectedWorld(); got != "" {
		t.Fatalf("kaputte Auswahl: %q", got)
	}
	// Die Auswahl gehört zur Gruppe: eine andere Gruppe sieht sie nicht.
	h := Home()
	writeFile(t, filepath.Join(h, "gruppen", "z.example.org", "gruppe.yaml"), groupYAML("https://z.example.org"))
	if err := SetSelectedWorld("w3"); err != nil {
		t.Fatal(err)
	}
	if err := SetActive("z.example.org"); err != nil {
		t.Fatal(err)
	}
	if got := SelectedWorld(); got != "" {
		t.Fatalf("Auswahl der anderen Gruppe: %q", got)
	}
}
