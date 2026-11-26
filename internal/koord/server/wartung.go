package server

// Wartung vom Server aus (Paket wartung).
//
// „wanderwelt-koordinator wartung start“ schreibt den Zustand nach
// data_dir/wartung.json; der Koordinator liest ihn in jedem Takt und bei
// SIGHUP (und beim Start, so übersteht die Wartung einen Neustart). Ab dem
// Beginn der Wartung bekommt jedes Spielen „denied“ mit dem Grund (auch
// Hochladen-Zuschläge und Host-Übergaben werden abgelehnt), Status und
// Lebenszeichen des Hosts tragen api.Maintenance. Der Host sagt an, stoppt
// zu StopAt sauber (Schluss-Stand, Zuschlag zurück) und bekommt keine
// Übergabe. Meldet er nach StopAt + wartungGrace noch einen laufenden
// Server (Ansage ignoriert, etwa ein älterer Starter), wird ihm wie beim
// Zurückholen der Zuschlag entzogen; ein Zuschlag nur zum Hochladen bleibt,
// bis er endet. Im Takt schreibt der Koordinator außerdem den Bericht
// (wartung-bericht.json), auf den „wartung start“ wartet.
//
// Solange die Wartung läuft, räumt der stündliche Aufräumlauf nur als Probe
// auf (er loggt, was er löschen würde): Der erste Start nach einem Update
// soll mit neuen Aufbewahrungsregeln nie unbemerkt Stände wegnehmen.
// Löschen gibt es erst wieder nach „wartung ende“.

import (
	"cmp"
	"context"
	"errors"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// wartungGrace: So lange nach StopAt darf der Host für Stopp, Schluss-Stand
// und Rückgabe brauchen, bevor ihm der Zuschlag entzogen wird.
const wartungGrace = 60 * time.Second

// ReloadWartung liest den Wartungszustand neu (SIGHUP).
func (s *Server) ReloadWartung() {
	s.phMu.Lock()
	defer s.phMu.Unlock()
	s.readWartungLocked()
}

// readWartungLocked gleicht den Zustand im Speicher mit wartung.json ab.
// Eine kaputte Datei lässt den bisherigen Zustand stehen (und wird einmal
// je Fehlertext geloggt). Aufruf unter phMu (oder im Aufbau).
func (s *Server) readWartungLocked() {
	z, err := wartung.Laden(s.cfg.DataDir)
	if err != nil {
		if msg := err.Error(); msg != s.wartungErr {
			s.wartungErr = msg
			s.log.Error("WARTUNGSZUSTAND NICHT LESBAR – der bisherige gilt weiter", "err", err)
		}
		return
	}
	s.wartungErr = ""
	old := s.wartung.Load()
	switch {
	case z.Aktiv && (old == nil || *old != z):
		s.wartung.Store(&z)
		s.log.Info("WARTUNG", "grund", z.Grund, "seit", z.Since, "stopp", z.StopAt)
	case !z.Aktiv && old != nil:
		s.wartung.Store(nil)
		wartung.BerichtLoeschen(s.cfg.DataDir)
		s.log.Info("WARTUNG BEENDET – Spielen ist wieder möglich, Aufräumen löscht wieder")
	}
}

// wartungNotice liefert die Wartung für Status und Lebenszeichen (nil ohne).
func (s *Server) wartungNotice() *api.Maintenance {
	z := s.wartung.Load()
	if z == nil {
		return nil
	}
	return &api.Maintenance{
		Reason:     z.Grund,
		StopAt:     z.StopAt,
		Since:      z.Since,
		RemainingS: int64(z.Rest(s.now()) / time.Second),
	}
}

// wartungReason ist der Grund, aus dem Spielen während der Wartung
// abgelehnt wird ("" ohne Wartung).
func (s *Server) wartungReason() string {
	z := s.wartung.Load()
	if z == nil {
		return ""
	}
	return "Wartung am Server (" + z.Grund + ") – Spielen ist gleich wieder möglich"
}

// wartungTickLocked: im Takt (ExpireTick). Liest den Zustand, entzieht
// einem Host, der nicht gestoppt hat, den Zuschlag und schreibt den
// Bericht. Aufruf unter phMu.
func (s *Server) wartungTickLocked(ctx context.Context) {
	s.readWartungLocked()
	z := s.wartung.Load()
	if z == nil {
		return
	}
	now := s.now()
	info, held := s.lm.Info()
	if held && !info.UploadOnly && info.Ready && !now.Before(z.StopAt.Add(wartungGrace)) {
		if err := s.lm.Release(info.LeaseID, info.Host.MachineID); err != nil && !errors.Is(err, lease.ErrLeaseLost) {
			s.log.Error("Zuschlag für die Wartung nicht entzogen, neuer Versuch beim nächsten Takt", "lease", info.LeaseID, "err", err)
		} else {
			s.log.Warn("HOST HAT DIE WARTUNG NICHT UMGESETZT – Zuschlag entzogen",
				"lease", info.LeaseID, "host", info.Host.Email, "nachfrist", wartungGrace)
			s.needClear = true
			s.clearLocked(ctx)
			info, held = s.lm.Info()
		}
	}
	b := wartung.Bericht{
		Time:      now.UTC(),
		Since:     z.Since,
		Uploads:   s.st.OpenUploads(),
		Uebergabe: s.handoverActive(),
	}
	if held {
		b.Zuschlag, b.NurUpload = cmp.Or(info.Host.MinecraftName, "ein PC"), info.UploadOnly
	}
	b.Bereit = z.Phase(now) == wartung.Gestoppt && !held && b.Uploads == 0 && !b.Uebergabe
	if err := wartung.BerichtSchreiben(s.cfg.DataDir, b); err != nil {
		s.log.Error("Wartungsbericht nicht geschrieben", "err", err)
	}
}
