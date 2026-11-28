package server

import (
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/benachrichtigung"
)

// SetUUIDTimeout setzt die Frist für das Auflösen der Minecraft-Namen
// (nur Tests).
func (s *Server) SetUUIDTimeout(d time.Duration) { s.uuidTimeout = d }

// NewMojangResolverAt ist NewMojangResolver mit eigener API-Adresse und Uhr
// (nur Tests).
func NewMojangResolverAt(cachePath, base string, now func() time.Time) *MojangResolver {
	m := NewMojangResolver(cachePath)
	m.base, m.now = base, now
	return m
}

// SetNow setzt die Uhr des Servers (nur Tests; vor dem ersten Aufruf).
func (s *Server) SetNow(now func() time.Time) {
	s.now = now
	s.health.started = now() // die Laufzeit zählt ab der gestellten Uhr
}

// SetBlobStall setzt die Frist, nach der ein Blob-Teil ohne neue Daten
// abbricht, und liefert die Rücknahme (nur Tests).
func SetBlobStall(d time.Duration) (restore func()) {
	old := blobStall
	blobStall = d
	return func() { blobStall = old }
}

// SetDiskFree setzt, wie viele Bytes auf dem Dateisystem von data_dir frei
// sind (nur Tests; vor dem ersten Aufruf).
func (s *Server) SetDiskFree(free func(dir string) int64) {
	s.diskFree = func(dir string) (int64, bool) { return free(dir), true }
}

// SetManifestCheckHook setzt eine Funktion, die zu Beginn jeder Prüfung
// eines neuen Manifests läuft (nur Tests; vor dem ersten Aufruf).
func (s *Server) SetManifestCheckHook(f func()) { s.manifestCheckHook = f }

// SetDiskTotal setzt die Gesamtgröße des Dateisystems von data_dir (nur
// Tests; vor dem ersten Aufruf).
func (s *Server) SetDiskTotal(total func(dir string) int64) {
	s.health.diskTotal = func(dir string) (int64, bool) { return total(dir), true }
}

// SetBenachrichtigung setzt den Discord-Dienst (nur Tests; vor dem ersten
// Aufruf).
func (s *Server) SetBenachrichtigung(d *benachrichtigung.Dienst) { s.benach = d }

// SetJobPoll setzt, wie lange ein Abruf des Hosts ohne Aufträge offen bleibt
// (nur Tests).
func (s *Server) SetJobPoll(d time.Duration) { s.jobPoll = d }

// SetJobWait setzt, wie lange ein Admin auf das Ergebnis eines Auftrags
// wartet, und liefert die Rücknahme (nur Tests).
func SetJobWait(d time.Duration) (restore func()) {
	old := jobWait
	jobWait = d
	return func() { jobWait = old }
}

// IDOf liefert die Mitglieds-Kennung der Adresse (nur Tests).
func (s *Server) IDOf(email string) string { return s.id(email) }
