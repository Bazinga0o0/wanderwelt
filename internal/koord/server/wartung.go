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
//
// Fortsetzen: Kam die Wartung bis zum Stopp, öffnet „wartung ende“ ein
// Fenster (wartung-fortsetzen.json, Status.Resume): Starter, die bei
// Wartungsbeginn hosteten oder mitspielten, bewerben sich nach 60 s alle
// gleichzeitig mit PlayRequest.Resume, und das gewöhnliche Sammelfenster
// wählt den besten PC. Nach 10 min verfällt das Fenster; Bewerbungen mit
// Resume ohne offenes Fenster werden abgelehnt.

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
		s.dropResumeLocked() // eine neue Wartung beendet ein altes Fenster
		s.log.Info("WARTUNG", "grund", z.Grund, "seit", z.Since, "stopp", z.StopAt)
	case !z.Aktiv && old != nil:
		// Erst das Fenster, dann das Ende der Wartung: Der Status zeigt nie
		// ohne Wartung und ohne Fenster (Starter verwerfen ihren Merker, wenn beides fehlt).
		s.openResumeLocked(*old)
		s.wartung.Store(nil)
		wartung.BerichtLoeschen(s.cfg.DataDir)
		s.log.Info("WARTUNG BEENDET – Spielen ist wieder möglich, Aufräumen löscht wieder")
	}
}

// openResumeLocked öffnet nach dem Ende der Wartung old das Fortsetzen-Fenster
// (nur, wenn sie bis zum Stopp kam) und speichert es. Aufruf unter phMu.
func (s *Server) openResumeLocked(old wartung.Zustand) {
	now := s.now()
	f, ok := wartung.NeuesFortsetzen(old, now)
	if !ok {
		return
	}
	if err := wartung.FortsetzenSpeichern(s.cfg.DataDir, f); err != nil {
		// Das Fenster gilt trotzdem im Speicher; nur ein Neustart vergisst es.
		s.log.Error("Fortsetzen-Fenster nicht gespeichert", "err", err)
	}
	s.resume.Store(&f)
	s.log.Info("FORTSETZEN-FENSTER OFFEN – wer bei der Wartung hostete oder mitspielte, bewirbt sich gemeinsam",
		"beginn", f.Opens, "verfall", f.Until)
}

// dropResumeLocked schließt das Fortsetzen-Fenster. Aufruf unter phMu (oder
// im Aufbau).
func (s *Server) dropResumeLocked() {
	if s.resume.Swap(nil) != nil {
		wartung.FortsetzenLoeschen(s.cfg.DataDir)
	}
}

// loadResumeLocked liest beim Start ein gespeichertes Fenster (es übersteht
// einen Neustart); ein verfallenes oder kaputtes wird gelöscht. Aufruf im
// Aufbau.
func (s *Server) loadResumeLocked() {
	f, ok, err := wartung.FortsetzenLaden(s.cfg.DataDir)
	switch {
	case err != nil:
		s.log.Error("Fortsetzen-Fenster nicht lesbar, es wird verworfen", "err", err)
		wartung.FortsetzenLoeschen(s.cfg.DataDir)
	case !ok:
	case s.wartung.Load() != nil || !f.Offen(s.now()):
		wartung.FortsetzenLoeschen(s.cfg.DataDir)
	default:
		s.resume.Store(&f)
	}
}

// resumeOpen: Es gibt ein offenes Fortsetzen-Fenster (ohne Wartung).
func (s *Server) resumeOpen() bool {
	f := s.resume.Load()
	return f != nil && s.wartung.Load() == nil && f.Offen(s.now())
}

// resumeNotice liefert das Fenster für den Status (nil ohne offenes).
func (s *Server) resumeNotice() *api.Resume {
	f := s.resume.Load()
	if f == nil || s.wartung.Load() != nil {
		return nil
	}
	now := s.now()
	if !f.Offen(now) {
		return nil
	}
	return &api.Resume{
		Since:    f.Since,
		OpensInS: int64(f.OpensIn(now) / time.Second),
		UntilInS: int64(f.UntilIn(now) / time.Second),
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
	if f := s.resume.Load(); f != nil && !f.Offen(s.now()) {
		s.dropResumeLocked() // das Fenster ist verfallen
		s.log.Info("Fortsetzen-Fenster verfallen")
	}
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
