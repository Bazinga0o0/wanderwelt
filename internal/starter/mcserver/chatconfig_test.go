package mcserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

func chatCfg(t *testing.T, dir string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "config", "wanderwelt-chatverlauf.json"))), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func schreibeChatCfg(t *testing.T, dir, inhalt string) {
	t.Helper()
	p := filepath.Join(dir, "config", "wanderwelt-chatverlauf.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(inhalt), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Ohne Angabe (älterer Koordinator) bleibt die Datei, wie sie ist (oder fehlt).
func TestChatConfigOhneAngabeLaesstDateiStehen(t *testing.T) {
	dir := t.TempDir()
	if err := applyChatConfig(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "wanderwelt-chatverlauf.json")); !os.IsNotExist(err) {
		t.Fatalf("Datei ohne Angabe angelegt: %v", err)
	}
	schreibeChatCfg(t, dir, `{"max_bytes": 1}`)
	if err := applyChatConfig(dir, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "config", "wanderwelt-chatverlauf.json")); got != `{"max_bytes": 1}` {
		t.Fatalf("Datei verändert: %q", got)
	}
}

// Fehlt die Datei, entstehen genau die zwei Schlüssel; die Größe wird in Bytes
// geschrieben (MiB * 1048576).
func TestChatConfigNeueDatei(t *testing.T) {
	dir := t.TempDir()
	if err := applyChatConfig(dir, &api.ChatSettings{MiB: 12}); err != nil {
		t.Fatal(err)
	}
	m := chatCfg(t, dir)
	if len(m) != 2 || m["aktiv"] != true || m["max_bytes"] != float64(12*1048576) {
		t.Fatalf("Konfig: %v", m)
	}
	// Aus: aktiv=false; ungültige Größe (0, 99) fällt auf den Standard.
	for _, mib := range []int{0, 99, -3} {
		if err := applyChatConfig(dir, &api.ChatSettings{Off: true, MiB: mib}); err != nil {
			t.Fatal(err)
		}
		m = chatCfg(t, dir)
		if m["aktiv"] != false || m["max_bytes"] != float64(5*1048576) {
			t.Fatalf("MiB %d: Konfig %v", mib, m)
		}
	}
}

// Was ein Admin sonst in die Datei geschrieben hat, bleibt; "aus" löscht keinen
// gespeicherten Chat.
func TestChatConfigBehaeltFremdeSchluesselUndLoeschtNichts(t *testing.T) {
	dir := t.TempDir()
	schreibeChatCfg(t, dir, `{"aktiv": true, "max_bytes": 1, "wiedergabe_zeilen": 20, "zeit": "grau", "segment_bytes": 8192}`)
	verlauf := filepath.Join(dir, "world", "wanderwelt-chatverlauf", "chat-000001.jsonl")
	if err := os.MkdirAll(filepath.Dir(verlauf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verlauf, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyChatConfig(dir, &api.ChatSettings{Off: true, MiB: 7}); err != nil {
		t.Fatal(err)
	}
	m := chatCfg(t, dir)
	if m["aktiv"] != false || m["max_bytes"] != float64(7*1048576) ||
		m["wiedergabe_zeilen"] != float64(20) || m["zeit"] != "grau" || m["segment_bytes"] != float64(8192) {
		t.Fatalf("Konfig: %v", m)
	}
	if got := readFile(t, verlauf); got != "{}\n" {
		t.Fatalf("gespeicherter Chat verändert: %q", got)
	}
	// Wieder an: aktiv=true, Rest bleibt; ein zweiter Lauf mit gleichen Werten schreibt nichts neu.
	if err := applyChatConfig(dir, &api.ChatSettings{MiB: 7}); err != nil {
		t.Fatal(err)
	}
	if m := chatCfg(t, dir); m["aktiv"] != true || m["zeit"] != "grau" {
		t.Fatalf("Konfig: %v", m)
	}
	p := filepath.Join(dir, "config", "wanderwelt-chatverlauf.json")
	vorher, _ := os.Stat(p)
	if err := applyChatConfig(dir, &api.ChatSettings{MiB: 7}); err != nil {
		t.Fatal(err)
	}
	nachher, _ := os.Stat(p)
	if !vorher.ModTime().Equal(nachher.ModTime()) {
		t.Fatal("unveränderte Konfig wurde neu geschrieben")
	}
}

// Kaputte Datei (kein JSON-Objekt): wird mit den zwei Schlüsseln ersetzt.
func TestChatConfigKaputteDatei(t *testing.T) {
	for _, inhalt := range []string{"kein json", "[1,2]", "null", ""} {
		dir := t.TempDir()
		schreibeChatCfg(t, dir, inhalt)
		if err := applyChatConfig(dir, &api.ChatSettings{Off: true, MiB: 3}); err != nil {
			t.Fatalf("%q: %v", inhalt, err)
		}
		m := chatCfg(t, dir)
		if len(m) != 2 || m["aktiv"] != false || m["max_bytes"] != float64(3*1048576) {
			t.Fatalf("%q: Konfig %v", inhalt, m)
		}
	}
}

// Ist das Ziel keine normale Datei, wird nichts geschrieben (Fehler).
func TestChatConfigZielIstOrdner(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config", "wanderwelt-chatverlauf.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyChatConfig(dir, &api.ChatSettings{Off: true, MiB: 3}); err == nil {
		t.Fatal("kein Fehler für Ordner als Ziel")
	}
}

func prepareDateien(t *testing.T) (root, java, jar string) {
	t.Helper()
	root = t.TempDir()
	java = filepath.Join(root, "java.exe")
	jar = filepath.Join(root, "fabric.jar")
	for _, p := range []string{java, jar} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, java, jar
}

// Prepare trägt die Einstellung ein, und zwar nach den Konfigdateien aus dem
// Manifest (eine enforce-Datei am selben Pfad verliert gegen die Gruppeneinstellung).
func TestPrepareSchreibtChatEinstellung(t *testing.T) {
	root, java, jar := prepareDateien(t)
	manifest := cfgFor(t, "config/wanderwelt-chatverlauf.json", `{"aktiv":true,"max_bytes":999999,"zeit":"aus"}`, "enforce")
	srv := filepath.Join(root, "srv")
	err := (&fabric{}).Prepare(t.Context(), PrepareInput{
		Dir:      srv,
		Paths:    &deps.Paths{Java: java, FabricServerJar: jar, Configs: []deps.ConfigPath{manifest}},
		Profile:  api.Profile{XmxMB: minXmxMB},
		Settings: api.HostSettings{Chat: &api.ChatSettings{Off: true, MiB: 9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := chatCfg(t, srv)
	if m["aktiv"] != false || m["max_bytes"] != float64(9*1048576) || m["zeit"] != "aus" {
		t.Fatalf("Konfig: %v", m)
	}
}

// Scheitert das Eintragen bei "aus", startet der Server nicht; bei "an" schon.
func TestPrepareChatSchreibfehler(t *testing.T) {
	root, java, jar := prepareDateien(t)
	srv := filepath.Join(root, "srv")
	if err := os.MkdirAll(filepath.Join(srv, "config", "wanderwelt-chatverlauf.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	prep := func(off bool) error {
		return (&fabric{}).Prepare(t.Context(), PrepareInput{
			Dir:      srv,
			Paths:    &deps.Paths{Java: java, FabricServerJar: jar},
			Profile:  api.Profile{XmxMB: minXmxMB},
			Settings: api.HostSettings{Chat: &api.ChatSettings{Off: off, MiB: 5}},
		})
	}
	if err := prep(true); err == nil {
		t.Fatal("Prepare bei \"aus\" und Schreibfehler ohne Fehler")
	}
	if err := prep(false); err != nil {
		t.Fatalf("Prepare bei \"an\" und Schreibfehler: %v", err)
	}
}
