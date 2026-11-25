package server

// Host-Übergabe ohne Rauswurf.
//
// Starter melden alle 5 s ihre Bereitschaft (POST /v1/presence). Drückt der
// Host „Spiel für mich beenden“, während andere spielen, wählt der
// Koordinator mit POST /v1/handover/start den besten bereiten PC (Zustand
// „vorbereitung“: der alte Host speichert und lädt hoch). Mit
// POST /v1/handover/ready meldet der alte Host den hochgeladenen Stand;
// der Koordinator vergibt dem Kandidaten einen zweiten, wartenden
// Zuschlag (lease.GrantIncoming), setzt den Port-Halter eines Ersatz-Ports
// auf dessen Konto und hält ein Angebot für ihn bereit (Zustand „start“).
// Von da an reicht der alte Zuschlag nichts mehr ein. Meldet der wartende
// Zuschlag mit seinem Lebenszeichen ready, gilt die Übergabe als „bereit“:
// Der alte Host schickt die Spieler an Host:Ersatz-Port und gibt seinen
// Zuschlag zurück. Mit der Rückgabe (oder dem Ablauf, wenn der alte Host
// abgestürzt ist) wird der wartende Zuschlag befördert; der neue Host
// bekommt den Haupt-Port (MainPort im Lebenszeichen), den Ersatz-Port
// behält er bis zum Ende seines Zuschlags.
//
// Scheitert die Übergabe (5 min ohne ready nach dem Start, 5 min ohne
// „bereit“ nach dem Angebot, der wartende Zuschlag verfällt oder wird
// zurückgegeben), wird der wartende Zuschlag verworfen, der Ersatz-Port
// geräumt und der Zustand „gescheitert“ bleibt für den alten Host 2 min
// abrufbar; Commit ist für ihn wieder erlaubt. Bricht der alte Host die
// Übergabe selbst ab (POST /v1/handover/abort), wird sie wie ein Scheitern
// aufgeräumt, aber gleich vergessen.
//
// Die Übergabe und der Ersatz-Port des beförderten Hosts stehen auch auf der
// Platte (handover_state.go) und überstehen einen Neustart des Koordinators;
// die Fristen zählen danach neu. Ein auf der Platte gebliebener wartender
// Zuschlag ohne Übergabe (Datei verloren) wird verworfen. Der erste Takt
// räumt nur Ersatz-Ports, die keinem gültigen Zuschlag und keiner laufenden
// Übergabe gehören.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
)

const (
	// presenceFresh: So jung muss die Bereitschaft eines Kandidaten sein.
	presenceFresh = 15 * time.Second
	// presenceKeep: So lange merkt sich der Koordinator eine Bereitschaft.
	presenceKeep = time.Minute
	// handoverReadyTimeout: Vom Start bis ready des alten Hosts.
	handoverReadyTimeout = 5 * time.Minute
	// handoverBereitTimeout: Vom Angebot bis „neuer Host bereit“.
	handoverBereitTimeout = 5 * time.Minute
	// handoverFailedKeep: So lange bleibt „gescheitert“ abrufbar.
	handoverFailedKeep = 2 * time.Minute
)

// handover ist der Stand einer Übergabe. Er wird nie verändert, nur ganz
// ersetzt (Server.ho).
type handover struct {
	id    int64
	state string // HandoverPreparing, HandoverStarting oder HandoverFailed
	// Der alte Host.
	oldLease   int64
	oldMachine string
	from       api.HostInfo
	// Der Kandidat.
	to        lease.Candidate
	startedAt time.Time
	// Ab ready (Zustand start): der wartende Zuschlag und sein Ersatz-Port.
	inLease  int64
	port     int
	worldID  string
	headGen  int64
	offerAt  time.Time
	accepted bool // der wartende Zuschlag hat sein erstes Lebenszeichen geschickt
	ready    bool // der wartende Zuschlag meldete einen laufenden Server
	// Zustand failed.
	reason   string
	failedAt time.Time
}

func (h *handover) active() bool {
	return h != nil && (h.state == api.HandoverPreparing || h.state == api.HandoverStarting)
}

// replPort ist der Ersatz-Port eines beförderten Hosts: Er gehört ihm bis
// zum Ende seines Zuschlags.
type replPort struct {
	leaseID int64
	port    int
}

// presenceEntry ist die letzte Bereitschaft eines PCs.
type presenceEntry struct {
	cand   lease.Candidate
	joined bool
	at     time.Time
}

func candHost(c lease.Candidate) api.HostInfo {
	return api.HostInfo{Email: c.Email, MinecraftName: c.MinecraftName}
}

func presenceKey(email, machine string) string { return strings.ToLower(email) + "\x00" + machine }

// handoverState beschreibt h für den Starter; mit addr auch die Adresse
// des Ersatz-Ports (nur für den alten Host).
func (s *Server) handoverState(h *handover, withAddr bool) *api.HandoverState {
	if h == nil {
		return nil
	}
	st := &api.HandoverState{HandoverID: h.id, State: h.state, Reason: h.reason, To: *publicHost(candHost(h.to))}
	if h.state == api.HandoverStarting && h.ready {
		st.State = api.HandoverReady
		if withAddr {
			st.Address = handoverAddress(s.cfg.MinecraftAddress, h.port)
		}
	}
	return st
}

// handoverAddress ist der Host-Teil von addr mit dem Ersatz-Port.
func handoverAddress(addr string, port int) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// handoverActive meldet, ob eine Übergabe läuft (nicht: gescheitert).
func (s *Server) handoverActive() bool { return s.ho.Load().active() }

// handoverStepLocked treibt die Übergabe voran: Zeitgrenzen, Scheitern,
// Beförderung, aufräumen. Aufruf unter phMu, von jedem Ereignis, das den
// Zuschlag ändert (Takt, Rückgabe, Spielanfrage).
func (s *Server) handoverStepLocked(ctx context.Context) {
	now := s.now()
	_, curLease, haveMain := s.lm.Current()
	_, inOK := s.lm.Incoming()

	// Der Ersatz-Port eines beförderten Hosts endet mit dessen Zuschlag.
	if r := s.repl.Load(); r != nil && (!haveMain || curLease != r.leaseID) {
		s.hoDirty[r.port] = true
		s.setReplLocked(nil)
	}

	h := s.ho.Load()
	switch {
	case h == nil && inOK, h != nil && h.state == api.HandoverFailed && inOK:
		// Wartender Zuschlag ohne laufende Übergabe (Neustart des
		// Koordinators, fehlgeschlagenes Verwerfen): nie behalten.
		s.log.Warn("wartender Zuschlag ohne laufende Übergabe verworfen")
		if err := s.lm.DropIncoming(); err != nil {
			s.log.Error("wartenden Zuschlag nicht verworfen", "err", err)
		}
	case h != nil && h.state == api.HandoverFailed && now.Sub(h.failedAt) > handoverFailedKeep:
		s.setHoLocked(nil)
	}

	if h.active() {
		mainOK := haveMain && curLease == h.oldLease
		switch h.state {
		case api.HandoverPreparing:
			switch {
			case !mainOK:
				s.log.Info("Übergabe beendet: der alte Host hat den Zuschlag vor ready abgegeben", "id", h.id)
				s.setHoLocked(nil)
			case now.Sub(h.startedAt) > handoverReadyTimeout:
				s.failHandoverLocked(h, "Der alte Host hat den Stand nicht rechtzeitig hochgeladen")
			}
		case api.HandoverStarting:
			switch {
			// Gibt der alte Host ab, nachdem ready angenommen wurde, wird der
			// wartende auch dann befördert, wenn er noch nicht "bereit" meldete:
			// Der Übergabe-Stand ist der Schluss-Stand des alten Hosts (sein
			// Commit ist seit ready gesperrt, der Starter stoppt im Rückfall ohne
			// neuen Stand), so übernimmt der neue Host nahtlos. Vor ready gibt es
			// keinen wartenden Zuschlag, die Übergabe endet ohne Beförderung.
			case !mainOK && inOK:
				s.promoteLocked(ctx, h)
			case !mainOK:
				s.log.Info("Übergabe beendet: alter und wartender Zuschlag sind weg", "id", h.id)
				s.hoDirty[h.port] = true
				s.setHoLocked(nil)
			case !inOK:
				s.failHandoverLocked(h, "Der neue Host ist nicht mehr da")
			case !h.ready && now.Sub(h.offerAt) > handoverBereitTimeout:
				s.failHandoverLocked(h, "Der neue Host wurde nicht rechtzeitig bereit")
			}
		}
	}
	s.clearHandoverPortsLocked(ctx)
}

// failHandoverLocked beendet die Übergabe als gescheitert: wartender
// Zuschlag weg, Ersatz-Port zum Räumen vorgemerkt. Aufruf unter phMu.
func (s *Server) failHandoverLocked(h *handover, reason string) {
	s.log.Warn("Übergabe gescheitert", "id", h.id, "grund", reason)
	if err := s.lm.DropIncoming(); err != nil {
		// Der nächste Schritt versucht es erneut (Zustand gescheitert).
		s.log.Error("wartenden Zuschlag nicht verworfen", "err", err)
	}
	if h.port != 0 {
		s.hoDirty[h.port] = true
	}
	f := *h
	f.state, f.reason, f.failedAt = api.HandoverFailed, reason, s.now()
	f.ready, f.accepted = false, false
	s.setHoLocked(&f)
}

// promoteLocked befördert den wartenden Zuschlag: Haupt-Port auf sein Konto,
// dann lease.Promote. Misslingt der Port-Halter, bleibt alles stehen; der
// nächste Schritt versucht es erneut. Aufruf unter phMu.
func (s *Server) promoteLocked(ctx context.Context, h *handover) {
	in, ok := s.lm.Incoming()
	if !ok {
		return
	}
	if err := s.ph.Set(ctx, s.cfg.PortHolder.Port, in.AccountID); err != nil {
		s.needClear = true // Wirkung unbekannt
		s.log.Error("Haupt-Port für den neuen Host nicht gesetzt, neuer Versuch beim nächsten Takt",
			"lease", in.LeaseID, "konto", in.AccountID, "err", err)
		return
	}
	promoted, err := s.lm.Promote()
	if err != nil || !promoted {
		s.needClear = true
		s.log.Error("wartender Zuschlag nicht befördert", "lease", in.LeaseID, "befördert", promoted, "err", err)
		return
	}
	s.needClear = false
	s.log.Info("Übergabe abgeschlossen: wartender Zuschlag befördert", "id", h.id, "lease", in.LeaseID, "host", in.Host.Email)
	if old := s.repl.Load(); old != nil {
		s.hoDirty[old.port] = true
	}
	s.setReplLocked(&replPort{leaseID: in.LeaseID, port: h.port})
	s.setHoLocked(nil)
}

// clearHandoverPortsLocked räumt die vorgemerkten Ersatz-Ports, soweit sie
// nicht gerade benutzt werden. Aufruf unter phMu.
func (s *Server) clearHandoverPortsLocked(ctx context.Context) {
	if len(s.hoDirty) == 0 {
		return
	}
	inUse := map[int]bool{}
	if r := s.repl.Load(); r != nil {
		inUse[r.port] = true
	}
	if h := s.ho.Load(); h.active() && h.port != 0 {
		inUse[h.port] = true
	}
	for p := range s.hoDirty {
		if inUse[p] {
			continue
		}
		if err := s.ph.Clear(ctx, p); err != nil {
			s.log.Error("Ersatz-Port nicht geräumt, neuer Versuch beim nächsten Takt", "port", p, "err", err)
			continue
		}
		delete(s.hoDirty, p)
	}
}

// handoverSettle ist handoverStepLocked für Anfragen.
func (s *Server) handoverSettle(ctx context.Context) {
	s.phMu.Lock()
	defer s.phMu.Unlock()
	s.handoverStepLocked(ctx)
}

// isIncoming meldet, ob leaseID der gültige wartende Zuschlag des Kontos
// von id ist.
func (s *Server) isIncoming(id identity, leaseID int64) bool {
	in, ok := s.lm.Incoming()
	return ok && in.LeaseID == leaseID && strings.EqualFold(in.Host.Email, id.member.Email)
}

// handoverTouch merkt das Lebenszeichen des wartenden Zuschlags: das erste
// (Angebot erledigt) und das erste mit ready (Übergabe „bereit“).
func (s *Server) handoverTouch(ctx context.Context, leaseID int64, ready bool) {
	want := func(h *handover) bool {
		return h != nil && h.state == api.HandoverStarting && h.inLease == leaseID &&
			(!h.accepted || (ready && !h.ready))
	}
	if !want(s.ho.Load()) {
		return
	}
	s.phMu.Lock()
	defer s.phMu.Unlock()
	h := s.ho.Load()
	if !want(h) {
		return
	}
	c := *h
	c.accepted = true
	if ready {
		c.ready = true
		s.log.Info("Übergabe: neuer Host ist bereit", "id", h.id)
	}
	s.setHoLocked(&c)
}

// --- Bereitschaft ---

// handlePresence merkt die Bereitschaft des PCs und antwortet mit einem
// offenen Angebot für genau diesen PC.
func (s *Server) handlePresence(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.PresenceRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	if !validMachineID(req.MachineID) {
		writeError(w, http.StatusBadRequest, "ungültige machine_id")
		return
	}
	if sc := req.Score; sc.UploadMbps < 0 || sc.CPUScore < 0 || sc.RAMFreeMB < 0 || sc.RAMTotalMB < 0 || sc.XmxMB < 0 {
		writeError(w, http.StatusBadRequest, "Messwerte mit negativen Zahlen")
		return
	}
	now := s.now()
	s.presMu.Lock()
	for k, e := range s.presence {
		if now.Sub(e.at) > presenceKeep {
			delete(s.presence, k)
		}
	}
	s.presence[presenceKey(id.member.Email, req.MachineID)] = presenceEntry{
		cand: lease.Candidate{
			Email:     id.member.Email,
			MachineID: req.MachineID,
			AccountID: id.accountID,
			Score:     req.Score,
		},
		joined: req.Joined,
		at:     now,
	}
	s.presMu.Unlock()

	var resp api.PresenceResponse
	if h := s.ho.Load(); h != nil && h.state == api.HandoverStarting && !h.accepted &&
		strings.EqualFold(h.to.Email, id.member.Email) && h.to.MachineID == req.MachineID {
		from := *publicHost(h.from)
		resp.Handover = &api.HandoverOffer{
			HandoverID:       h.id,
			LeaseID:          h.inLease,
			WorldID:          h.worldID,
			HeadGeneration:   h.headGen,
			Port:             h.port,
			From:             from,
			MinecraftAddress: s.cfg.MinecraftAddress,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// pickCandidateLocked wählt den besten Kandidaten für die Übergabe von
// host (Info des Haupt-Zuschlags). Aufruf unter phMu.
func (s *Server) pickCandidateLocked(host lease.Info) (lease.Candidate, bool) {
	now := s.now()
	players := s.lm.Players()
	inGame := func(name string) bool {
		for _, p := range players {
			if strings.EqualFold(p, name) {
				return true
			}
		}
		return false
	}
	s.presMu.Lock()
	keys := make([]string, 0, len(s.presence))
	for k := range s.presence {
		keys = append(keys, k)
	}
	slices.Sort(keys) // feste Reihenfolge: bei Gleichstand gewinnt ein fester PC
	entries := make([]presenceEntry, 0, len(keys))
	for _, k := range keys {
		entries = append(entries, s.presence[k])
	}
	s.presMu.Unlock()

	profs := s.loadProfiles() // einmal lesen statt je Kandidat
	var cands []lease.Candidate
	for _, e := range entries {
		c := e.cand
		if now.Sub(e.at) > presenceFresh || c.MachineID == host.Host.MachineID {
			continue
		}
		m, ok := s.member(c.Email)
		if !ok {
			continue
		}
		c.MinecraftName = m.Minecraft
		if e.joined && (m.Minecraft == "" || !inGame(m.Minecraft)) {
			continue // spielt nicht (mehr) mit
		}
		if profs[strings.ToLower(c.Email)][c.MachineID].KeineUebernahme {
			continue
		}
		cands = append(cands, c)
	}
	if i := lease.Best(cands); i >= 0 {
		return cands[i], true
	}
	return lease.Candidate{}, false
}

// --- Start ---

// handleHandoverStart wählt den Kandidaten und beginnt die Übergabe.
func (s *Server) handleHandoverStart(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.HandoverStartRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	resp, err := func() (api.HandoverStartResponse, error) {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		ctx := context.WithoutCancel(r.Context())
		s.handoverStepLocked(ctx)
		info, err := s.mainOf(id, req.LeaseID, req.MachineID)
		if err != nil {
			return api.HandoverStartResponse{}, err
		}
		switch {
		case len(s.cfg.PortHolder.HandoverPorts) == 0:
			return api.HandoverStartResponse{}, &httpError{http.StatusConflict,
				"Host-Übergabe ist auf diesem Koordinator nicht eingerichtet (port_holder.handover_ports fehlt)"}
		case s.handoverActive():
			return api.HandoverStartResponse{}, &httpError{http.StatusConflict, "Es läuft schon eine Host-Übergabe"}
		case s.pending.Load() != nil:
			return api.HandoverStartResponse{}, &httpError{http.StatusConflict, "Ein Zurückholen steht aus – keine Host-Übergabe möglich"}
		}
		cand, ok := s.pickCandidateLocked(info)
		if !ok {
			return api.HandoverStartResponse{}, &httpError{http.StatusConflict, "Kein Mitspieler-PC ist bereit, das Hosten zu übernehmen"}
		}
		s.hoSeq++
		h := &handover{
			id:         s.hoSeq,
			state:      api.HandoverPreparing,
			oldLease:   info.LeaseID,
			oldMachine: info.Host.MachineID,
			from:       info.Host,
			to:         cand,
			startedAt:  s.now(),
		}
		s.setHoLocked(h)
		s.log.Info("Übergabe gestartet", "id", h.id, "von", info.Host.Email, "an", cand.Email, "machine", cand.MachineID)
		return api.HandoverStartResponse{HandoverID: h.id, To: *publicHost(candHost(cand))}, nil
	}()
	if err != nil {
		s.restoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mainOf liefert den Haupt-Zuschlag, wenn leaseID und machineID zu ihm
// passen und er dem Konto von id gehört und einen Server hat (409 sonst).
func (s *Server) mainOf(id identity, leaseID int64, machineID string) (lease.Info, error) {
	info, ok := s.lm.Info()
	if !ok || info.LeaseID != leaseID || info.Host.MachineID != machineID || !strings.EqualFold(info.Host.Email, id.member.Email) {
		return lease.Info{}, &httpError{http.StatusConflict, "lease lost"}
	}
	if info.UploadOnly {
		return lease.Info{}, &httpError{http.StatusConflict, "Dieser Zuschlag hat keinen Server, der sich übergeben ließe"}
	}
	return info, nil
}

// --- Ready ---

// handleHandoverReady nimmt die Meldung des alten Hosts an, dass der
// Übergabe-Stand hochgeladen ist, und stellt den wartenden Zuschlag, den
// Ersatz-Port und das Angebot bereit.
func (s *Server) handleHandoverReady(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.HandoverReadyRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	err := func() error {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		ctx := context.WithoutCancel(r.Context())
		s.handoverStepLocked(ctx)
		h := s.ho.Load()
		if h == nil || h.id != req.HandoverID || h.state != api.HandoverPreparing {
			return &httpError{http.StatusConflict, "Keine passende Host-Übergabe (abgelaufen oder gescheitert)"}
		}
		info, err := s.mainOf(id, req.LeaseID, req.MachineID)
		if err != nil || info.LeaseID != h.oldLease {
			return &httpError{http.StatusConflict, "lease lost"}
		}
		// Ab hier reicht der alte Zuschlag nichts mehr ein; Commit prüft
		// den Zustand unter commitMu, darum ist der Head hier endgültig.
		s.commitMu.Lock()
		defer s.commitMu.Unlock()
		worldID, head, err := s.head()
		if err != nil {
			return err
		}
		if req.WorldID != worldID || req.Generation != head {
			return &httpError{http.StatusConflict, fmt.Sprintf(
				"Der gemeldete Stand %q/%d ist nicht der aktuelle (%q/%d)", req.WorldID, req.Generation, worldID, head)}
		}
		g, err := s.lm.GrantIncoming(h.to)
		if errors.Is(err, lease.ErrBusy) || errors.Is(err, lease.ErrLeaseLost) {
			return &httpError{http.StatusConflict, "Der wartende Zuschlag ließ sich nicht vergeben"}
		}
		if err != nil {
			return err
		}
		port := s.pickReplPortLocked(info.LeaseID)
		if err := s.ph.Set(ctx, port, h.to.AccountID); err != nil {
			s.hoDirty[port] = true // Wirkung unbekannt
			if derr := s.lm.DropIncoming(); derr != nil {
				s.log.Error("wartenden Zuschlag nicht verworfen", "err", derr)
			}
			f := *h
			f.state, f.reason, f.failedAt = api.HandoverFailed, "Ersatz-Port für den neuen Host nicht gesetzt", s.now()
			s.setHoLocked(&f)
			s.log.Error("Ersatz-Port nicht gesetzt, Übergabe gescheitert", "port", port, "konto", h.to.AccountID, "err", err)
			code := http.StatusServiceUnavailable
			if errors.Is(err, portholder.ErrUnknownAccount) {
				code = http.StatusForbidden
			}
			return &httpError{code, "Ersatz-Port nicht gesetzt: " + err.Error()}
		}
		delete(s.hoDirty, port)
		n := *h
		n.state = api.HandoverStarting
		n.inLease, n.port = g.LeaseID, port
		n.worldID, n.headGen = worldID, head
		n.offerAt = s.now()
		s.setHoLocked(&n)
		s.log.Info("Übergabe: Angebot bereit", "id", h.id, "lease", g.LeaseID, "port", port)
		return nil
	}()
	if err != nil {
		if _, ok := errors.AsType[*httpError](err); !ok {
			s.internal(w, "Übergabe", err)
			return
		}
		s.restoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleHandoverAbort: Der alte Host bricht seine Übergabe ab und hostet
// weiter (der Übergabe-Stand ließ sich nicht bereitstellen oder einreichen).
// Wartender Zuschlag und Ersatz-Port werden wie bei einem Scheitern geräumt;
// die Übergabe ist danach weg, eine neue darf beginnen. Gibt es keine
// passende Übergabe (mehr), ist das kein Fehler (204).
func (s *Server) handleHandoverAbort(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.HandoverAbortRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	err := func() error {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		ctx := context.WithoutCancel(r.Context())
		s.handoverStepLocked(ctx)
		info, err := s.mainOf(id, req.LeaseID, req.MachineID)
		if err != nil {
			return err
		}
		h := s.ho.Load()
		if !h.active() || h.id != req.HandoverID || h.oldLease != info.LeaseID {
			return nil
		}
		s.log.Info("Übergabe vom alten Host abgebrochen", "id", h.id)
		s.failHandoverLocked(h, "Der alte Host hat die Übergabe abgebrochen")
		s.setHoLocked(nil)
		s.clearHandoverPortsLocked(ctx)
		return nil
	}()
	if err != nil {
		s.restoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pickReplPortLocked wählt den Ersatz-Port: den, den der Haupt-Zuschlag
// nicht als eigenen Ersatz-Port hält. Aufruf unter phMu.
func (s *Server) pickReplPortLocked(mainLease int64) int {
	ports := s.cfg.PortHolder.HandoverPorts
	held := s.repl.Load()
	for _, p := range ports {
		if held != nil && held.leaseID == mainLease && held.port == p {
			continue
		}
		return p
	}
	return ports[0]
}
