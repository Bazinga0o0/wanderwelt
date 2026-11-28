package server

// Karte (Karten-MVP): Die Endpunkte liefern den Kartenstand einer Welt der
// Gruppe aus (nur Mitglieder, wie alles unter /v1 außer dem Beitritt; die
// Mitgliedschaft prüft ServeHTTP). Erzeugung und Auslieferung stehen im
// Paket karte; hier hängen nur Weiche, Ratenbegrenzung, Status und
// Gesundheit daran.
//
//	GET /v1/karte/{welt}/manifest.json
//	GET /v1/karte/{welt}/t/{z}/{x}/{y}.png

import (
	"net/http"
	"strconv"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/karte"
)

// UseKarte hängt den Karten-Dienst an (vor dem ersten Aufruf); ohne ihn
// antworten die Karten-Endpunkte 404.
func (s *Server) UseKarte(d *karte.Dienst) { s.karte = d }

// karteInfo ist der Kartenstand der Welt für den Status (nil: keiner).
func (s *Server) karteInfo(world string) *api.KarteInfo {
	if s.karte == nil {
		return nil
	}
	return s.karte.Info(world)
}

// karteWelt prüft Dienst, Ratenbegrenzung und die Welt; false: schon
// beantwortet.
func (s *Server) karteWelt(w http.ResponseWriter, r *http.Request) (welt string, ok bool) {
	if s.karte == nil {
		writeError(w, http.StatusNotFound, "die Karte ist auf diesem Koordinator nicht eingerichtet")
		return "", false
	}
	if !s.karte.Erlaube(identityOf(r).member.Email) {
		w.Header().Set("Retry-After", strconv.Itoa(1))
		writeError(w, http.StatusTooManyRequests, "zu viele Karten-Anfragen – bitte einen Moment warten")
		return "", false
	}
	welt = r.PathValue("welt")
	if e, found := s.st.GroupWorld(welt); !found || welt == "" || e.ID != welt {
		writeError(w, http.StatusNotFound, "diese Welt gibt es nicht (mehr)")
		return "", false
	}
	return welt, true
}

func (s *Server) handleKarteManifest(w http.ResponseWriter, r *http.Request) {
	if welt, ok := s.karteWelt(w, r); ok {
		s.karte.Manifest(w, r, welt)
	}
}

func (s *Server) handleKarteKachel(w http.ResponseWriter, r *http.Request) {
	if welt, ok := s.karteWelt(w, r); ok {
		s.karte.Kachel(w, r, welt, r.PathValue("z"), r.PathValue("x"), r.PathValue("y"))
	}
}

// InWartung meldet, ob eine Wartung läuft (wartung.json aktiv). Der
// Karten-Dienst rendert dann nicht.
func (s *Server) InWartung() bool { return s.wartung.Load() != nil }
