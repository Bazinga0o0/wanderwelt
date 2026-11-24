package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
)

// Versions-Manifest: Der Koordinator liest manifest_file beim Start und bei
// jedem Neuladen (SIGHUP, systemctl reload) und liefert dazwischen immer
// denselben, geprüften Stand aus. Eine kaputte Datei beim Neuladen ändert
// nichts (der bisherige Stand gilt weiter, Fehler im Log). Den Eintrag
// starter liefert er nur aus, wenn die Datei im Artefakt-Ordner liegt und
// Hash (und Größe) passen – sonst Fehler im Log und das Manifest ohne
// starter, damit kein Starter ein Update versucht, das nicht gehen kann.

// UseManifest übernimmt m (gerade gelesen) als ausgeliefertes Manifest:
// prüft die wanderwelt:-Artefakte und den Eintrag starter, meldet Befunde
// im Log und entfernt einen nicht auslieferbaren starter. Geprüft (gehasht)
// wird ohne manifestMu: /v1/manifest liefert solange den bisherigen Stand.
func (s *Server) UseManifest(m *api.Manifest) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.setManifest(s.checkManifest(m))
}

// ReloadManifest liest manifest_file neu (SIGHUP). Ist die Datei nicht
// lesbar oder ungültig, bleibt das bisherige Manifest stehen.
func (s *Server) ReloadManifest() error {
	if s.cfg.ManifestFile == "" {
		return nil
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	m, err := config.LoadManifest(s.cfg.ManifestFile)
	if err != nil {
		s.log.Error("VERSIONS-MANIFEST NICHT NEU GELADEN – das bisherige gilt weiter", "datei", s.cfg.ManifestFile, "err", err)
		return err
	}
	s.setManifest(s.checkManifest(m))
	return nil
}

// checkManifest prüft m, meldet Befunde im Log, entfernt einen nicht
// auslieferbaren starter und liefert m. Läuft unter reloadMu, aber ohne
// manifestMu: Das Hashen der Artefakte kann dauern.
func (s *Server) checkManifest(m *api.Manifest) *api.Manifest {
	if s.manifestCheckHook != nil {
		s.manifestCheckHook()
	}
	for _, p := range CheckArtifacts(s.cfg.DataDir, m) {
		s.log.Error("ARTEFAKT AUS DEM MANIFEST NICHT AUSLIEFERBAR – so kann niemand hosten", "befund", p)
	}
	starter := "-"
	if m.Starter != nil {
		if p := CheckStarter(s.cfg.DataDir, m.Starter); p != "" {
			s.log.Error("STARTER-UPDATE NICHT AUSLIEFERBAR – das Manifest geht ohne starter hinaus", "befund", p)
			m.Starter = nil
		} else {
			starter = m.Starter.Version
		}
	}
	s.log.Info("Versions-Manifest geladen", "datei", s.cfg.ManifestFile, "minecraft", m.MinecraftVersion, "starter", starter)
	return m
}

// setManifest setzt das ausgelieferte Manifest (kurz unter manifestMu).
func (s *Server) setManifest(m *api.Manifest) {
	s.manifestMu.Lock()
	s.manifest = m
	s.manifestMu.Unlock()
}

// currentManifest liefert das ausgelieferte Manifest. Solange noch keins
// gelesen ist (etwa ein Server ohne UseManifest), liest es manifest_file;
// das gelingt einmal, danach gilt es bis zum nächsten Neuladen.
func (s *Server) currentManifest() (*api.Manifest, *httpError) {
	if s.cfg.ManifestFile == "" {
		return nil, &httpError{http.StatusServiceUnavailable, "kein Versions-Manifest konfiguriert (manifest_file)"}
	}
	if m := s.loadedManifest(); m != nil {
		return m, nil
	}
	// Noch keins da: einmal lesen – nach einem gerade laufenden Neuladen.
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if m := s.loadedManifest(); m != nil {
		return m, nil
	}
	m, err := config.LoadManifest(s.cfg.ManifestFile)
	if err != nil {
		s.log.Error("Versions-Manifest nicht lesbar", "err", err)
		return nil, &httpError{http.StatusServiceUnavailable, "Versions-Manifest auf dem Koordinator ist nicht lesbar"}
	}
	m = s.checkManifest(m)
	s.setManifest(m)
	return m, nil
}

func (s *Server) loadedManifest() *api.Manifest {
	s.manifestMu.RLock()
	defer s.manifestMu.RUnlock()
	return s.manifest
}

// CheckStarter prüft den Manifest-Eintrag starter: Release-Version, URL
// wanderwelt:<datei>, SHA-256, und die Datei in ArtifactDir(dataDir) hat
// diesen Hash (und die Größe size, wenn angegeben). Ergebnis ist der Befund
// fürs Log ("": in Ordnung).
func CheckStarter(dataDir string, r *api.StarterRelease) string {
	if _, ok := api.ReleaseVersion(r.Version); !ok {
		return fmt.Sprintf("version %q ist keine Release-Version MAJOR.MINOR.PATCH", r.Version)
	}
	scheme, name, ok := strings.Cut(r.URL, ":")
	if !ok || !strings.EqualFold(scheme, ArtifactScheme) || !ValidArtifactName(name) {
		return fmt.Sprintf("url %q: erwartet %s:<datei> – der Starter lädt sich nur vom Koordinator", r.URL, ArtifactScheme)
	}
	want, ok := r.StarterSHA256()
	if !ok {
		return fmt.Sprintf("sha256 %q: erwartet 64 Hex-Zeichen", r.SHA256)
	}
	if r.Size < 0 {
		return fmt.Sprintf("size %d ist negativ", r.Size)
	}
	path := filepath.Join(ArtifactDir(dataDir), name)
	fi, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	if !fi.Mode().IsRegular() {
		return path + " ist keine Datei"
	}
	if r.Size > 0 && fi.Size() != r.Size {
		return fmt.Sprintf("%s hat %d Bytes, im Manifest steht size %d", path, fi.Size(), r.Size)
	}
	got, err := fileHash(path, "sha256:")
	if err != nil {
		return err.Error()
	}
	if got != "sha256:"+want {
		return fmt.Sprintf("%s hat %s, im Manifest steht sha256 %q", path, got, r.SHA256)
	}
	return ""
}
