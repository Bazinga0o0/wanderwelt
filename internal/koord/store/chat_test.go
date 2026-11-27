package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Die Chat-Einstellung einer Welt: Standard (an, 0 = 5 MiB), ändern, prüfen,
// bleibt nach dem Neuöffnen erhalten; ein Wert außerhalb der Grenzen ändert
// nichts.
func TestChatEinstellungSpeichern(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir, 1<<30)
	w, err := s.AddGroupWorldChat("Survival", "", "", true, 7, t0)
	if err != nil || !w.ChatOff || w.ChatMiB != 7 {
		t.Fatalf("Anlegen: %+v %v", w, err)
	}
	if e, err := s.AddGroupWorld("Neu", "", "", t0); err != nil || e.ChatOff || e.ChatMiB != 0 {
		t.Fatalf("Standard: %+v %v", e, err)
	}
	for _, bad := range []int{-1, 51} {
		if _, err := s.AddGroupWorldChat("Zu", "", "", false, bad, t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("Anlegen mit %d MiB: %v", bad, err)
		}
		if _, err := s.UpdateGroupWorld(w.ID, WorldPatch{ChatMiB: &bad}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Ändern auf %d MiB: %v", bad, err)
		}
	}
	on, mib := false, 20
	if e, err := s.UpdateGroupWorld(w.ID, WorldPatch{ChatOff: &on, ChatMiB: &mib}); err != nil || e.ChatOff || e.ChatMiB != 20 {
		t.Fatalf("Ändern: %+v %v", e, err)
	}
	off := true
	if _, err := s.UpdateGroupWorld(w.ID, WorldPatch{ChatOff: &off}); err != nil {
		t.Fatal(err)
	}

	s2 := openStore(t, dir, 1<<30)
	e, ok := s2.GroupWorld(w.ID)
	if !ok || !e.ChatOff || e.ChatMiB != 20 {
		t.Fatalf("nach dem Neuöffnen: %+v", e)
	}
}

// Eine current.json aus der Zeit vor der Chat-Einstellung (ohne die Felder) lässt
// sich lesen: an, Standardgröße. Eine Größe außerhalb der Grenzen in der Datei
// verweigert das Öffnen.
func TestChatEinstellungAeltereCurrent(t *testing.T) {
	dir := t.TempDir()
	openStore(t, dir, 1<<30)
	cur := `{"next":2,"worlds":[{"id":"w1","name":"Welt","gamemode":"","difficulty":"","created":"2026-11-20T10:00:00Z","store_world":"","head":0}]}`
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(cur), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := s.GroupWorld("w1"); !ok || e.ChatOff || e.ChatMiB != 0 {
		t.Fatalf("ältere current.json: %+v", e)
	}
	bad := `{"next":2,"worlds":[{"id":"w1","name":"Welt","chat_mib":99,"created":"2026-11-20T10:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(dir, "current.json"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, 1<<30); err == nil {
		t.Fatal("Open mit 99 MiB in der Weltliste ohne Fehler")
	}
}
