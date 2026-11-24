package server

// Stände-Liste und Zurückholen.
//
// GET /v1/generations zeigt jedem Mitglied alle Welten mit ihren Ständen
// und die Belegung des Speichers. Mit POST /v1/restore holt ein Admin einen
// Stand zurück: Der Speicher legt dessen Inhalt als neuen Stand an, gelöscht
// wird nichts. Gibt es keinen Zuschlag, geschieht das sofort. Gibt es einen
// (auch einen nur zum Hochladen), steht das Zurückholen aus, bis genau
// dieser Zuschlag endet: Der Host erfährt es mit der Antwort auf sein
// Lebenszeichen, sagt es im Spiel an, reicht seinen Schluss-Stand ein und
// gibt den Zuschlag zurück; erst nach der Rückgabe (oder dem Ablauf, wenn
// der Host abgestürzt ist) wird zurückgeholt – auf seinem Schluss-Stand.
// Solange es aussteht, bekommen Spielanfragen „denied“, ein zweites
// Zurückholen 409, und Prune behält das Ziel (store.Pin). Meldet der Host
// nach restoreDelayS + restoreGrace noch einen laufenden Server (Ansage
// ignoriert, etwa ein älterer Starter), entzieht ihm der Koordinator den
// Zuschlag und holt trotzdem zurück; einen gestoppten Host, der seinen
// Schluss-Stand hochlädt, lässt er fertig werden. Das ausstehende
// Zurückholen steht nur im Speicher; ein Neustart des Koordinators
// verwirft es.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

const (
	// restoreDelayS: So viele Sekunden nach der Ansage im Spiel stoppt der
	// Host für ein Zurückholen.
	restoreDelayS = 30
	// restoreGrace: So lange nach der Ansage darf der Host für Stopp,
	// Schluss-Stand und Rückgabe brauchen, bevor ihm der Zuschlag entzogen
	// wird.
	restoreGrace = 60 * time.Second
	// reasonRestoring: Grund für "denied", solange ein Zurückholen aussteht.
	reasonRestoring = "Stand wird gerade zurückgeholt, gleich nochmal"
)

// pendingRestore ist ein ausstehendes Zurückholen. Es wird nie verändert,
// nur ganz ersetzt (Server.pending).
type pendingRestore struct {
	leaseID  int64        // der Zuschlag, auf dessen Ende es wartet
	deadline time.Time    // gilt er dann noch, wird er entzogen
	by       api.HostInfo // der Admin (E-Mail, Minecraft-Name)
	notice   api.RestoreNotice
}

// restoreDeadline ist die Frist für den Zuschlag, auf den ein Zurückholen
// ab from wartet.
func restoreDeadline(from time.Time) time.Time {
	return from.Add(restoreDelayS*time.Second + restoreGrace)
}

// httpError ist eine Ablehnung mit Status und Text für den Starter.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

// handleGenerations liefert alle Welten mit ihren Ständen (aktive zuerst,
// neueste Stände zuerst); den Host nennt sie mit seinem Minecraft-Namen.
func (s *Server) handleGenerations(w http.ResponseWriter, r *http.Request) {
	worlds, err := s.st.Worlds()
	if err != nil {
		s.internal(w, "Stände-Liste", err)
		return
	}
	names := map[string]string{}
	for _, m := range s.memberList() {
		names[strings.ToLower(m.Email)] = m.Minecraft
	}
	resp := api.GenerationsResponse{Worlds: make([]api.WorldInfo, 0, len(worlds))}
	for _, wm := range worlds {
		wi := api.WorldInfo{WorldID: wm.WorldID, Active: wm.Active, Generations: make([]api.GenerationInfo, 0, len(wm.Gens))}
		for _, g := range wm.Gens {
			wi.Generations = append(wi.Generations, api.GenerationInfo{
				WorldID:          g.WorldID,
				Number:           g.Number,
				Created:          g.Created,
				HostName:         names[strings.ToLower(g.HostEmail)],
				Players:          g.Players,
				Size:             g.Size,
				MinecraftVersion: g.MinecraftVersion,
				RestoredFrom:     g.RestoredFrom,
			})
		}
		resp.Worlds = append(resp.Worlds, wi)
	}
	info := s.storageInfo()
	resp.Storage = &info
	writeJSON(w, http.StatusOK, resp)
}

// storageInfo beschreibt die Belegung des Speichers: belegt und über dem
// Budget aus dem letzten Aufräumlauf (vor dem ersten: aus der Rechnung beim
// Start, store.RefreshUsage), frei auf dem Dateisystem von data_dir.
func (s *Server) storageInfo() api.StorageInfo {
	used, over := s.st.Usage()
	free, _ := s.diskFree(s.cfg.DataDir)
	return api.StorageInfo{
		UsedBytes:     used,
		BudgetBytes:   s.storageBudget(),
		DiskFreeBytes: free,
		OverBudget:    over,
	}
}

// storageBudget ist das wirksame Speicherbudget: aus settings.json in
// data_dir, sonst storage_budget aus der Konfiguration (members_api.go).
func (s *Server) storageBudget() int64 { return s.budget.Load() }

// handleRestore holt einen Stand zurück (nur Admins): sofort ohne Zuschlag,
// sonst nach dessen Ende. 404 unbekannter Stand; 409, wenn schon ein
// Zurückholen aussteht oder ein Server aus dem Manifest den Stand nicht
// laden darf (wie beim Hosten: api.CheckWorldVersion).
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	admin := identityOf(r).member
	if !admin.Admin {
		writeError(w, http.StatusForbidden, "Stände zurückholen dürfen nur Admins")
		return
	}
	var req api.RestoreRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	version, err := s.manifestVersion()
	if err != nil {
		s.restoreError(w, err)
		return
	}
	target := api.GenRef{WorldID: req.WorldID, Number: req.Number}
	by := api.HostInfo{Email: admin.Email, MinecraftName: admin.Minecraft}

	resp, err := func() (api.RestoreResponse, error) {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		s.settleRestoreLocked(context.WithoutCancel(r.Context()))
		s.handoverStepLocked(context.WithoutCancel(r.Context()))
		if s.handoverActive() {
			return api.RestoreResponse{}, &httpError{http.StatusConflict, "Eine Host-Übergabe läuft – bitte warten, bis sie abgeschlossen ist"}
		}
		if p := s.pending.Load(); p != nil {
			return api.RestoreResponse{}, &httpError{http.StatusConflict, fmt.Sprintf(
				"%s holt schon den Stand %s/%d zurück – bitte warten, bis der Host neu gestartet ist",
				cmp.Or(p.notice.By, "Ein Admin"), p.notice.Target.WorldID, p.notice.Target.Number)}
		}
		// Erst pinnen, dann lesen: Was danach noch da ist, räumt Prune nicht
		// mehr weg, solange das Zurückholen aussteht.
		s.st.Pin(target)
		g, err := s.st.Generation(target.WorldID, target.Number)
		if err == nil {
			err = versionRefusal(target, g.MinecraftVersion, version)
		}
		if err != nil {
			s.st.Pin()
			return api.RestoreResponse{}, err
		}
		s.log.Info("Zurückholen angefordert", "welt", target.WorldID, "generation", target.Number, "admin", admin.Email)

		if _, leaseID, ok := s.lm.Current(); ok {
			now := s.now()
			s.pending.Store(&pendingRestore{leaseID: leaseID, deadline: restoreDeadline(now), by: by, notice: api.RestoreNotice{
				By:            admin.Minecraft, // leer: die Ansage sagt „Ein Admin“
				Target:        target,
				TargetCreated: g.Created,
				RequestedAt:   now.UTC(),
				DelayS:        restoreDelayS,
			}})
			s.log.Info("Zurückholen wartet auf das Ende des Zuschlags", "lease", leaseID)
			return api.RestoreResponse{Pending: true}, nil
		}
		ng, err := s.applyRestoreLocked(target, by)
		if err != nil {
			return api.RestoreResponse{}, err
		}
		return api.RestoreResponse{Applied: true, New: &api.GenRef{WorldID: ng.WorldID, Number: ng.Number}}, nil
	}()
	if err != nil {
		s.restoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// versionRefusal lehnt das Zurückholen von target ab (409), wenn ein Server
// der Manifest-Version hosted den mit world gespeicherten Stand nicht laden
// darf – dieselbe Regel wie beim Hosten.
func versionRefusal(target api.GenRef, world, hosted string) error {
	switch err := api.CheckWorldVersion(world, hosted); {
	case errors.Is(err, api.ErrVersionsIncomparable):
		return &httpError{http.StatusConflict, fmt.Sprintf(
			"der Stand %s/%d ist mit Minecraft %s gespeichert, gehostet wird %s – die Versionen lassen sich nicht vergleichen, Zurückholen abgelehnt",
			target.WorldID, target.Number, world, hosted)}
	case err != nil:
		return &httpError{http.StatusConflict, fmt.Sprintf(
			"der Stand %s/%d ist mit Minecraft %s gespeichert, gehostet wird nur %s – ein älterer Server würde ihn beschädigen, Zurückholen abgelehnt",
			target.WorldID, target.Number, world, hosted)}
	}
	return nil
}

// restoreError antwortet mit der Ablehnung aus err oder dem passenden
// Status zu einem Speicherfehler.
func (s *Server) restoreError(w http.ResponseWriter, err error) {
	if he, ok := errors.AsType[*httpError](err); ok {
		writeError(w, he.code, he.msg)
		return
	}
	s.storeError(w, "Zurückholen", err)
}

// manifestVersion liefert die Minecraft-Version des Versions-Manifests; ohne
// lesbares Manifest lässt sich kein Stand prüfen (503).
func (s *Server) manifestVersion() (string, error) {
	m, herr := s.currentManifest()
	if herr != nil {
		return "", herr
	}
	return m.MinecraftVersion, nil
}

// settleRestoreLocked wendet ein ausstehendes Zurückholen an, sobald der
// Zuschlag, auf den es wartet, nicht mehr gilt (zurückgegeben, abgelaufen
// oder vom selben PC neu angefragt). Gilt inzwischen ein anderer Zuschlag
// (nur in einem engen Wettlauf mit dem Ablauf möglich), wartet es auf
// dessen Ende (mit neuer Frist), und dessen Host bekommt die Ansage.
//
// Meldet der Host nach Ablauf der Frist (restoreDelayS + restoreGrace) noch
// einen laufenden Server (ready aus dem Lebenszeichen, lease.Info.Ready),
// setzt er die Ansage nicht um (etwa ein älterer Starter): Der Zuschlag
// wird entzogen – das nächste Lebenszeichen bekommt „lease lost“, der Host
// stoppt ohne Schluss-Stand –, der Port-Halter geräumt und zurückgeholt.
// Hat er gestoppt (ready false, Schluss-Stand läuft), schweigt er (Absturz)
// oder hält er nur einen Zuschlag zum Hochladen, wird nichts entzogen; dann
// endet der Zuschlag wie sonst durch Rückgabe oder Ablauf.
//
// Schlägt das Anwenden fehl – auch weil das Ziel fehlt –, wird es
// verworfen: Niemand soll dauerhaft „gleich nochmal“ hören. Aufruf unter
// phMu.
func (s *Server) settleRestoreLocked(ctx context.Context) {
	p := s.pending.Load()
	if p == nil {
		return
	}
	info, ok := s.lm.Info()
	switch {
	case ok && info.LeaseID == p.leaseID:
		if s.now().Before(p.deadline) || !info.Ready || info.UploadOnly {
			return
		}
		if err := s.lm.Release(info.LeaseID, info.Host.MachineID); err != nil && !errors.Is(err, lease.ErrLeaseLost) {
			s.log.Error("Zuschlag für das Zurückholen nicht entzogen, neuer Versuch beim nächsten Takt", "lease", info.LeaseID, "err", err)
			return
		}
		s.log.Warn("HOST HAT DAS ZURÜCKHOLEN NICHT UMGESETZT – Zuschlag entzogen",
			"lease", info.LeaseID, "host", info.Host.Email, "frist", restoreDelayS*time.Second+restoreGrace)
		s.needClear = true
		s.clearLocked(ctx)
	case ok:
		moved := *p
		moved.leaseID, moved.deadline = info.LeaseID, restoreDeadline(s.now())
		s.pending.Store(&moved)
		s.log.Warn("Zurückholen wartet jetzt auf das Ende eines neuen Zuschlags", "alt", p.leaseID, "lease", info.LeaseID)
		return
	}
	s.pending.Store(nil)
	t := p.notice.Target
	if _, err := s.applyRestoreLocked(t, p.by); err != nil {
		msg := "ZURÜCKHOLEN FEHLGESCHLAGEN UND VERWORFEN"
		if errors.Is(err, store.ErrNotFound) {
			msg = "ZURÜCKHOLEN VERWORFEN – den Stand gibt es nicht mehr"
		}
		s.log.Error(msg, "welt", t.WorldID, "generation", t.Number, "admin", p.by.Email, "err", err)
	}
}

// applyRestoreLocked legt den Inhalt von target als neuen Head an und gibt
// das Ziel für Prune wieder frei. Aufruf unter phMu.
func (s *Server) applyRestoreLocked(target api.GenRef, by api.HostInfo) (*api.Generation, error) {
	g, err := s.st.Restore(target, by, s.now())
	s.st.Pin()
	if err != nil {
		return nil, err
	}
	s.log.Info("Zurückholen angewandt", "von_welt", target.WorldID, "von_generation", target.Number,
		"welt", g.WorldID, "generation", g.Number, "admin", by.Email)
	return g, nil
}

// restoreBlocksPlay wendet ein ausstehendes Zurückholen an, wenn sein
// Zuschlag schon geendet hat, und meldet sonst den Grund, aus dem eine
// Spielanfrage abgelehnt wird ("" = keiner).
func (s *Server) restoreBlocksPlay(ctx context.Context) string {
	s.phMu.Lock()
	defer s.phMu.Unlock()
	s.settleRestoreLocked(ctx)
	if s.pending.Load() != nil {
		return reasonRestoring
	}
	return ""
}

// restoreNotice liefert die Ansage des ausstehenden Zurückholens (nil, wenn
// keins aussteht). Ein angenommenes Lebenszeichen kommt immer vom gültigen
// Zuschlag, und auf dessen Ende wartet ein ausstehendes Zurückholen – oder
// gleich danach, falls sein alter Zuschlag gerade erst abgelaufen ist
// (settleRestoreLocked). Darum bekommt jedes Lebenszeichen die Ansage.
func (s *Server) restoreNotice() *api.RestoreNotice {
	p := s.pending.Load()
	if p == nil {
		return nil
	}
	n := p.notice
	return &n
}
