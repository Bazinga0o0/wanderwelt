package host

// Aufträge aus dem Reiter „Server“: Solange der Server läuft, holt die
// Sitzung Aufträge der Admins beim Koordinator ab (Long-Poll), führt sie
// über eine eigene RCON-Verbindung aus (mcserver.Server.Isolated) und meldet
// das Ergebnis. Jeder Auftrag wird hier noch einmal mit internal/serverbefehl
// geprüft; ausgeführt wird nur, was der Minecraft-Server selbst ausführt –
// keine Shell, keine Aktion des Starters.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
)

const (
	// jobTimeout begrenzt einen Auftrag (der Admin wartet höchstens 15 s).
	jobTimeout = 12 * time.Second
	// jobResultTimeout begrenzt das Melden des Ergebnisses.
	jobResultTimeout = 10 * time.Second
	// jobRetryMax: längste Pause nach gescheiterten Abrufen.
	jobRetryMax = 30 * time.Second
	// jobLeaseWait: Pause, wenn der Koordinator den Zuschlag (noch) nicht als
	// Haupt-Zuschlag kennt (wartender Zuschlag einer Übergabe).
	jobLeaseWait = 5 * time.Second
)

// textUebergabe: Antwort auf Aufträge während einer Übergabe.
const textUebergabe = "Gerade läuft eine Übergabe des Hostens – bitte gleich noch einmal."

// startJobs startet den Abruf der Aufträge (einmal, sobald der Server läuft).
func (s *Session) startJobs() {
	if s.jobsCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.work)
	s.jobsCancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.jobLoop(ctx)
	}()
}

// jobsStep läuft in jedem Schritt der Schleife: Läuft der Server nicht mehr
// weiter (Ende, Fencing, Absturz), endet der Abruf; während einer Übergabe
// werden Aufträge abgelehnt.
func (s *Session) jobsStep() {
	s.jobsPaused.Store(s.ho != nil || s.hoPending)
	if s.jobsCancel != nil && !s.active() {
		s.jobsCancel()
		s.jobsCancel = nil
	}
}

// jobLoop holt Aufträge, bis ctx endet. Ein älterer Koordinator kennt den
// Endpunkt nicht: dann endet sie still.
func (s *Session) jobLoop(ctx context.Context) {
	fails := 0
	pause := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for ctx.Err() == nil {
		jobs, err := s.d.Client.HostJobs(ctx, s.d.LeaseID, s.d.MachineID)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, client.ErrNotSupported):
			log.Printf("host: der Koordinator kennt keine Aufträge (älter) – Befehle aus dem Reiter „Server“ gibt es nicht")
			return
		case errors.Is(err, client.ErrLeaseLost):
			// Wartender Zuschlag einer Übergabe, oder verloren – das
			// entscheidet das Lebenszeichen.
			if !pause(jobLeaseWait) {
				return
			}
			continue
		case err != nil:
			fails++
			if fails == 1 || fails%10 == 0 {
				log.Printf("host: Aufträge holen gescheitert (%d. Mal): %v", fails, err)
			}
			if !pause(min(time.Duration(fails)*2*time.Second, jobRetryMax)) {
				return
			}
			continue
		}
		fails = 0
		for _, j := range jobs {
			res := s.execJob(ctx, j)
			rctx, cancel := context.WithTimeout(ctx, jobResultTimeout)
			if err := s.d.Client.HostJobResult(rctx, res); err != nil && ctx.Err() == nil {
				log.Printf("host: Ergebnis von Auftrag %d nicht gemeldet: %v", j.ID, err)
			}
			cancel()
		}
	}
}

// execJob führt einen Auftrag aus und liefert sein Ergebnis.
func (s *Session) execJob(ctx context.Context, j api.HostJob) api.HostJobResult {
	res := api.HostJobResult{LeaseID: s.d.LeaseID, MachineID: s.d.MachineID, ID: j.ID}
	if s.jobsPaused.Load() {
		res.Output = textUebergabe
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	switch j.Kind {
	case api.JobCommand:
		cmd, err := serverbefehl.Pruefe(j.Command)
		if err != nil {
			log.Printf("host: Admin-Befehl abgelehnt: %v", err)
			res.Output = err.Error()
			return res
		}
		log.Printf("host: Admin-Befehl: %s", cmd)
		var out string
		err = s.d.Server.Isolated(ctx, func(run func(string) (string, error)) error {
			var err error
			out, err = run(cmd)
			return err
		})
		if err != nil {
			res.Output = "Der Server hat den Befehl nicht beantwortet: " + err.Error()
			return res
		}
		res.OK, res.Output = true, serverbefehl.Antwort(out)
	case api.JobGamerule:
		cmd, err := serverbefehl.SpielregelBefehl(j.Name, j.Value)
		if err != nil {
			res.Output = err.Error()
			return res
		}
		log.Printf("host: Spielregel: %s", cmd)
		var out string
		err = s.d.Server.Isolated(ctx, func(run func(string) (string, error)) error {
			var err error
			out, err = run(cmd)
			return err
		})
		if err != nil {
			res.Output = "Der Server hat nicht geantwortet: " + err.Error()
			return res
		}
		res.OK, res.Output = serverbefehl.SpielregelGesetzt(out), serverbefehl.Antwort(out)
	case api.JobGamerules:
		rules, err := s.readGamerules(ctx)
		if err != nil {
			res.Output = "Die Spielregeln ließen sich nicht lesen: " + err.Error()
			return res
		}
		res.OK, res.Rules = true, rules
	default:
		res.Output = fmt.Sprintf("Unbekannter Auftrag %q – bitte wanderwelt aktualisieren.", j.Kind)
	}
	return res
}

// readGamerules liest Namen („help gamerule“) und Werte („gamerule <name>“)
// der Spielregeln über eine Verbindung.
func (s *Session) readGamerules(ctx context.Context) ([]api.Gamerule, error) {
	var rules []api.Gamerule
	err := s.d.Server.Isolated(ctx, func(run func(string) (string, error)) error {
		help, err := run("help gamerule")
		if err != nil {
			return err
		}
		names := serverbefehl.SpielregelNamen(help)
		if len(names) == 0 {
			return errors.New("der Server nennt keine Spielregeln")
		}
		for _, n := range names {
			out, err := run("gamerule " + n)
			if err != nil {
				return err
			}
			if v, ok := serverbefehl.SpielregelWert(out); ok {
				rules = append(rules, api.Gamerule{Name: n, Value: v})
			}
		}
		return nil
	})
	return rules, err
}
