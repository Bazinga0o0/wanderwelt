package server

import "time"

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
func (s *Server) SetNow(now func() time.Time) { s.now = now }

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
