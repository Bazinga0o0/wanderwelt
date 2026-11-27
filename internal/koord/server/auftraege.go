package server

// Befehlskanal Admin → Koordinator → Host (Reiter „Server“).
//
// Ein Admin schickt einen Befehl, eine Spielregel oder fragt die Spielregeln
// ab; der Koordinator prüft Recht und Inhalt (internal/serverbefehl),
// schreibt den Auftrag ins Log und reiht ihn für den gültigen Haupt-Zuschlag
// eines bereiten Hosts ein. Der Host holt Aufträge per Long-Poll ab
// (POST /v1/host/jobs), führt sie über eine eigene RCON-Verbindung aus und
// meldet das Ergebnis (POST /v1/host/jobs/result); der Admin-Aufruf wartet
// so lange, höchstens jobWait. Aufträge stehen nur im Speicher: Ein Neustart
// des Koordinators oder das Ende des Zuschlags verwirft sie.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
)

const (
	// jobPoll: So lange hält der Koordinator einen Abruf des Hosts offen,
	// wenn nichts ansteht.
	jobPoll = 20 * time.Second
	// maxJobs: höchstens so viele offene Aufträge (abgeholt oder nicht).
	maxJobs = 8
	// maxJobsPerPoll: höchstens so viele Aufträge je Abruf.
	maxJobsPerPoll = 4
	// maxJobBody: Ergebnis eines Auftrags (Spielregeln, Antwort).
	maxJobBody = 256 << 10
)

// jobWait: So lange wartet ein Admin auf das Ergebnis seines Auftrags (Tests
// verkürzen es).
var jobWait = 15 * time.Second

// errNotRunning: Es gibt keinen bereiten Host.
var errNotRunning = &httpError{http.StatusConflict, "Die Welt läuft gerade nicht – das geht nur, solange ein Host sie spielt"}

// job ist ein Auftrag samt Zuschlag, für den er gilt.
type job struct {
	api.HostJob
	lease int64
	taken bool
	done  chan api.HostJobResult // Puffer 1; geschlossen, wenn er verfällt
}

// jobQueue hält die offenen Aufträge.
type jobQueue struct {
	mu   sync.Mutex
	seq  int64
	jobs []*job
	wake chan struct{} // wird geschlossen und ersetzt, sobald ein Auftrag dazukommt
}

func newJobQueue() *jobQueue {
	return &jobQueue{seq: time.Now().UnixNano(), wake: make(chan struct{})}
}

// add reiht hj für lease ein; nil und 429, wenn zu viele offen sind.
func (q *jobQueue) add(lease int64, hj api.HostJob) (*job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) >= maxJobs {
		return nil, &httpError{http.StatusTooManyRequests, "Es warten schon zu viele Aufträge auf den Host – bitte gleich noch einmal"}
	}
	q.seq++
	hj.ID = q.seq
	j := &job{HostJob: hj, lease: lease, done: make(chan api.HostJobResult, 1)}
	q.jobs = append(q.jobs, j)
	close(q.wake)
	q.wake = make(chan struct{})
	return j, nil
}

// remove nimmt j heraus (Admin wartet nicht mehr).
func (q *jobQueue) remove(j *job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = slices.DeleteFunc(q.jobs, func(o *job) bool { return o == j })
}

// take holt die noch nicht abgeholten Aufträge von lease; wartet höchstens
// bis ctx endet, wenn keiner ansteht.
func (q *jobQueue) take(ctx context.Context, lease int64) []api.HostJob {
	for {
		q.mu.Lock()
		var out []api.HostJob
		for _, j := range q.jobs {
			if j.lease == lease && !j.taken && len(out) < maxJobsPerPoll {
				j.taken = true
				out = append(out, j.HostJob)
			}
		}
		wake := q.wake
		q.mu.Unlock()
		if len(out) > 0 {
			return out
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return nil
		}
	}
}

// finish übergibt das Ergebnis res an den wartenden Admin; nur für einen
// abgeholten Auftrag genau dieses Zuschlags.
func (q *jobQueue) finish(lease int64, res api.HostJobResult) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.IndexFunc(q.jobs, func(j *job) bool { return j.ID == res.ID && j.lease == lease && j.taken })
	if i < 0 {
		return false
	}
	j := q.jobs[i]
	q.jobs = slices.Delete(q.jobs, i, i+1)
	j.done <- res
	return true
}

// dropExcept lässt alle Aufträge verfallen, die nicht für lease gelten (0:
// alle); der wartende Admin erfährt es sofort.
func (q *jobQueue) dropExcept(lease int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = slices.DeleteFunc(q.jobs, func(j *job) bool {
		if j.lease == lease {
			return false
		}
		close(j.done)
		return true
	})
}

// currentLease ist der Haupt-Zuschlag eines bereiten Hosts (0: keiner).
func (s *Server) currentLease() int64 {
	info, ok := s.lm.Info()
	if !ok || !info.Ready || info.UploadOnly {
		return 0
	}
	return info.LeaseID
}

// runJob reiht hj für den bereiten Host ein und wartet auf das Ergebnis.
func (s *Server) runJob(ctx context.Context, hj api.HostJob) (api.HostJobResult, error) {
	lease := s.currentLease()
	if lease == 0 {
		return api.HostJobResult{}, errNotRunning
	}
	s.jobs.dropExcept(lease)
	j, err := s.jobs.add(lease, hj)
	if err != nil {
		return api.HostJobResult{}, err
	}
	t := time.NewTimer(jobWait)
	defer t.Stop()
	select {
	case res, ok := <-j.done:
		if !ok {
			return api.HostJobResult{}, &httpError{http.StatusConflict, "Die Welt läuft nicht mehr – der Auftrag ist verfallen"}
		}
		return res, nil
	case <-t.C:
	case <-ctx.Done():
	}
	s.jobs.remove(j)
	return api.HostJobResult{}, &httpError{http.StatusGatewayTimeout, "Der Host hat nicht rechtzeitig geantwortet – der Befehl ist vielleicht trotzdem ausgeführt worden"}
}

// hostOf prüft, dass die Anfrage vom gültigen Haupt-Zuschlag kommt (Konto
// und PC); sonst 409 wie beim Lebenszeichen.
func (s *Server) hostOf(w http.ResponseWriter, r *http.Request, lease int64, machine string) bool {
	if !s.isHost(identityOf(r), lease) || !s.lm.Valid(lease, machine) {
		writeError(w, http.StatusConflict, "lease lost")
		return false
	}
	return true
}

// handleHostJobs: Der Host holt Aufträge (Long-Poll bis jobPoll).
func (s *Server) handleHostJobs(w http.ResponseWriter, r *http.Request) {
	var req api.HostJobsRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	if !s.hostOf(w, r, req.LeaseID, req.MachineID) {
		return
	}
	s.jobs.dropExcept(req.LeaseID)
	ctx, cancel := context.WithTimeout(r.Context(), s.jobPoll)
	defer cancel()
	jobs := s.jobs.take(ctx, req.LeaseID)
	if jobs == nil {
		jobs = []api.HostJob{}
	}
	writeJSON(w, http.StatusOK, api.HostJobs{Jobs: jobs})
}

// handleHostJobResult: Der Host meldet das Ergebnis eines Auftrags.
func (s *Server) handleHostJobResult(w http.ResponseWriter, r *http.Request) {
	var res api.HostJobResult
	if !s.decode(w, r, maxJobBody, &res) {
		return
	}
	if !s.hostOf(w, r, res.LeaseID, res.MachineID) {
		return
	}
	if !s.jobs.finish(res.LeaseID, res) {
		writeError(w, http.StatusNotFound, "kein offener Auftrag mit dieser Kennung")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleServerCommand: Ein Admin schickt einen Befehl an den laufenden
// Server.
func (s *Server) handleServerCommand(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminOf(w, r, "Befehle an den Server schicken")
	if !ok {
		return
	}
	var req api.ServerCommand
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	cmd, err := serverbefehl.Pruefe(req.Command)
	if err != nil {
		s.log.Warn("Admin-Befehl abgelehnt", "admin", admin.Email, "admin_minecraft", admin.Minecraft,
			"befehl", shortCmd(req.Command), "grund", err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("Admin-Befehl", "admin", admin.Email, "admin_minecraft", admin.Minecraft, "befehl", cmd)
	res, err := s.runJob(r.Context(), api.HostJob{Kind: api.JobCommand, Command: cmd})
	if err != nil {
		s.log.Info("Admin-Befehl ohne Ergebnis", "admin", admin.Email, "befehl", cmd, "grund", err.Error())
		s.changeError(w, "Befehl", err)
		return
	}
	out := serverbefehl.Antwort(res.Output)
	s.log.Info("Admin-Befehl ausgeführt", "admin", admin.Email, "befehl", cmd, "ok", res.OK, "antwort_bytes", len(out))
	writeJSON(w, http.StatusOK, api.ServerCommandResult{OK: res.OK, Output: out})
}

// handleGamerules liefert die Spielregeln der laufenden Welt.
func (s *Server) handleGamerules(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminOf(w, r, "Spielregeln sehen"); !ok {
		return
	}
	res, err := s.runJob(r.Context(), api.HostJob{Kind: api.JobGamerules})
	if err != nil {
		s.changeError(w, "Spielregeln", err)
		return
	}
	if !res.OK {
		writeError(w, http.StatusBadGateway, "Der Host konnte die Spielregeln nicht lesen: "+serverbefehl.Antwort(res.Output))
		return
	}
	out := api.Gamerules{Rules: []api.Gamerule{}}
	for _, g := range res.Rules {
		// Was der Host meldet, geht nur geprüft ins Fenster.
		if serverbefehl.PruefeSpielregel(g.Name, g.Value) == nil && len(out.Rules) < 300 {
			out.Rules = append(out.Rules, g)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGameruleSet setzt eine Spielregel der laufenden Welt.
func (s *Server) handleGameruleSet(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminOf(w, r, "Spielregeln ändern")
	if !ok {
		return
	}
	var req api.GameruleSet
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	if err := serverbefehl.PruefeSpielregel(req.Name, req.Value); err != nil {
		s.log.Warn("Spielregel abgelehnt", "admin", admin.Email, "regel", shortCmd(req.Name), "wert", shortCmd(req.Value), "grund", err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.log.Info("Spielregel ändern", "admin", admin.Email, "admin_minecraft", admin.Minecraft, "regel", req.Name, "wert", req.Value)
	res, err := s.runJob(r.Context(), api.HostJob{Kind: api.JobGamerule, Name: req.Name, Value: req.Value})
	if err != nil {
		s.changeError(w, "Spielregel", err)
		return
	}
	out := serverbefehl.Antwort(res.Output)
	s.log.Info("Spielregel geändert", "admin", admin.Email, "regel", req.Name, "wert", req.Value, "ok", res.OK)
	writeJSON(w, http.StatusOK, api.ServerCommandResult{OK: res.OK, Output: out})
}

// shortCmd: ein abgelehnter Wert fürs Log, höchstens 300 Bytes (slog
// maskiert Steuerzeichen selbst).
func shortCmd(s string) string {
	if len(s) > 300 {
		return strings.ToValidUTF8(s[:300], "") + "…"
	}
	return s
}
