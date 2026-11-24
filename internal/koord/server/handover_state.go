package server

// Persistenz der Host-Übergabe: DataDir/handover.json hält den Ersatz-Port
// des beförderten Hosts (repl) und eine laufende oder eben gescheiterte
// Übergabe. Nach einem Neustart des Koordinators wird beides geladen; die
// Fristen zählen ab dem Laden neu (volle Frist). Geschrieben wird atomar bei
// jeder Änderung, unter phMu.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
)

type hoFile struct {
	Repl     *replDisk `json:"repl,omitempty"`
	Handover *hoDisk   `json:"handover,omitempty"`
}

type replDisk struct {
	LeaseID int64 `json:"lease_id"`
	Port    int   `json:"port"`
}

type candDisk struct {
	Email         string `json:"email"`
	MinecraftName string `json:"minecraft_name"`
	MachineID     string `json:"machine_id"`
	AccountID     int64  `json:"account_id"`
}

type hoDisk struct {
	ID         int64        `json:"id"`
	State      string       `json:"state"`
	OldLease   int64        `json:"old_lease"`
	OldMachine string       `json:"old_machine"`
	From       api.HostInfo `json:"from"`
	To         candDisk     `json:"to"`
	InLease    int64        `json:"in_lease,omitempty"`
	Port       int          `json:"port,omitempty"`
	WorldID    string       `json:"world_id,omitempty"`
	HeadGen    int64        `json:"head_gen,omitempty"`
	Accepted   bool         `json:"accepted,omitempty"`
	Ready      bool         `json:"ready,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

func (s *Server) hoPath() string { return filepath.Join(s.cfg.DataDir, "handover.json") }

// setHoLocked ersetzt die Übergabe und schreibt den Zustand. Aufruf unter
// phMu.
func (s *Server) setHoLocked(h *handover) {
	s.ho.Store(h)
	s.saveHoLocked()
}

// setReplLocked ersetzt den Ersatz-Port des beförderten Hosts und schreibt
// den Zustand. Aufruf unter phMu.
func (s *Server) setReplLocked(r *replPort) {
	s.repl.Store(r)
	s.saveHoLocked()
}

// saveHoLocked schreibt den Zustand; ein Fehler wird nur geloggt (die
// Übergabe läuft im Speicher weiter).
func (s *Server) saveHoLocked() {
	var f hoFile
	if r := s.repl.Load(); r != nil {
		f.Repl = &replDisk{LeaseID: r.leaseID, Port: r.port}
	}
	if h := s.ho.Load(); h != nil {
		f.Handover = &hoDisk{
			ID: h.id, State: h.state, OldLease: h.oldLease, OldMachine: h.oldMachine, From: h.from,
			To:      candDisk{h.to.Email, h.to.MinecraftName, h.to.MachineID, h.to.AccountID},
			InLease: h.inLease, Port: h.port, WorldID: h.worldID, HeadGen: h.headGen,
			Accepted: h.accepted, Ready: h.ready, Reason: h.reason,
		}
	}
	var err error
	if f.Repl == nil && f.Handover == nil {
		if err = os.Remove(s.hoPath()); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	} else {
		var data []byte
		if data, err = json.MarshalIndent(f, "", "  "); err == nil {
			err = writeFileAtomic(s.hoPath(), data)
		}
	}
	if err != nil {
		s.log.Error("Übergabe-Zustand nicht gespeichert", "err", err)
	}
}

// loadHo lädt den Zustand beim Start (vor dem ersten Aufruf). Fristen
// beginnen ab jetzt.
func (s *Server) loadHo() {
	data, err := os.ReadFile(s.hoPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Error("Übergabe-Zustand nicht lesbar, ignoriert", "err", err)
		}
		return
	}
	var f hoFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.log.Error("Übergabe-Zustand kaputt, ignoriert", "err", err)
		return
	}
	now := s.now()
	if f.Repl != nil {
		s.repl.Store(&replPort{leaseID: f.Repl.LeaseID, port: f.Repl.Port})
	}
	if d := f.Handover; d != nil {
		h := &handover{
			id: d.ID, state: d.State, oldLease: d.OldLease, oldMachine: d.OldMachine, from: d.From,
			to: lease.Candidate{Email: d.To.Email, MinecraftName: d.To.MinecraftName,
				MachineID: d.To.MachineID, AccountID: d.To.AccountID},
			startedAt: now, offerAt: now, failedAt: now,
			inLease: d.InLease, port: d.Port, worldID: d.WorldID, headGen: d.HeadGen,
			accepted: d.Accepted, ready: d.Ready, reason: d.Reason,
		}
		s.ho.Store(h)
		s.hoSeq = max(s.hoSeq, h.id)
		s.log.Info("Übergabe nach Neustart geladen", "id", h.id, "zustand", h.state)
	}
}
