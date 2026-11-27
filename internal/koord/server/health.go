package server

// Gesundheit (nur Admins): GET /v1/health sammelt, wofür ein Admin sonst per
// SSH ins Log schaut – Version und Laufzeit, Speicherplatz, Weltstand, Host,
// Starter-Versionen, offene Beitrittsanfragen, Wartung, letzte Sicherung,
// letztes Aufräumen – und rechnet daraus Warnungen und die Ampel (Paket
// gesundheit). Die Starter-Versionen kommen mit Lebenszeichen und Bereitschaft
// (api.HeartbeatRequest/PresenceRequest.StarterVersion; ältere Starter senden
// sie nicht) und stehen nur im Speicher: Nach einem Neustart des Koordinators
// füllt sich die Liste neu, je nachdem, welche PCs sich melden.

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/gesundheit"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/sicherung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

const (
	// starterKeep: So lange bleibt ein gesehener Starter in der Liste.
	starterKeep = 30 * 24 * time.Hour
	// maxStarters begrenzt die Liste (ein PC je Mitglied und Gerät).
	maxStarters = 200
	// maxVersionLen begrenzt die gemeldete Starter-Version.
	maxVersionLen = 40
)

// healthState ist, was die Gesundheit über die Laufzeit merkt.
type healthState struct {
	started   time.Time
	version   string
	diskTotal func(dir string) (int64, bool)

	mu       sync.Mutex
	starters map[string]starterSeen
	prune    *api.HealthPrune
}

// starterSeen ist ein zuletzt gesehener PC (Schlüssel: E-Mail und PC-Kennung).
type starterSeen struct {
	email, machine, version string
	at                      time.Time
}

// initHealth setzt die Startzeit (aus der Uhr des Servers).
func (s *Server) initHealth() {
	s.health.started = s.now()
	s.health.diskTotal = diskTotal
	s.health.starters = map[string]starterSeen{}
}

// SetVersion setzt die Version des Koordinators für die Gesundheit (vor dem
// ersten Aufruf).
func (s *Server) SetVersion(v string) { s.health.version = v }

// validStarterVersion: kurz, nur Ziffern, Buchstaben und . + _ -.
func validStarterVersion(v string) bool {
	if v == "" || len(v) > maxVersionLen {
		return false
	}
	for _, r := range v {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune(".+_-", r)) {
			return false
		}
	}
	return true
}

// noteStarter merkt die Starter-Version eines PCs (Lebenszeichen,
// Bereitschaft). Ohne oder mit ungültiger Version tut es nichts.
func (s *Server) noteStarter(email, machine, version string) {
	if !validStarterVersion(version) || !validMachineID(machine) {
		return
	}
	now := s.now()
	h := &s.health
	h.mu.Lock()
	defer h.mu.Unlock()
	key := presenceKey(email, machine)
	if _, known := h.starters[key]; !known {
		for k, e := range h.starters {
			if now.Sub(e.at) > starterKeep {
				delete(h.starters, k)
			}
		}
		if len(h.starters) >= maxStarters {
			return
		}
	}
	h.starters[key] = starterSeen{email: email, machine: machine, version: version, at: now}
}

// notePrune merkt das Ergebnis des letzten Aufräumlaufs.
func (s *Server) notePrune(p api.HealthPrune) {
	s.health.mu.Lock()
	s.health.prune = &p
	s.health.mu.Unlock()
}

// healthStarters liefert die gesehenen PCs, die noch Mitglied sind, nach
// Name und PC-Kennung sortiert.
func (s *Server) healthStarters() []api.HealthStarter {
	now := s.now()
	s.health.mu.Lock()
	seen := make([]starterSeen, 0, len(s.health.starters))
	for _, e := range s.health.starters {
		if now.Sub(e.at) <= starterKeep {
			seen = append(seen, e)
		}
	}
	s.health.mu.Unlock()
	out := make([]api.HealthStarter, 0, len(seen))
	for _, e := range seen {
		m, ok := s.member(e.email)
		if !ok {
			continue
		}
		out = append(out, api.HealthStarter{MinecraftName: m.Minecraft, MachineID: e.machine, Version: e.version, Seen: e.at})
	}
	slices.SortFunc(out, func(a, b api.HealthStarter) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.MinecraftName), strings.ToLower(b.MinecraftName)), cmp.Compare(a.MachineID, b.MachineID))
	})
	return out
}

// health stellt die Gesundheit zusammen.
func (s *Server) healthSnapshot() api.Health {
	now := s.now()
	h := api.Health{
		Version: cmp.Or(s.health.version, "dev"),
		Started: s.health.started,
		UptimeS: max(int64(now.Sub(s.health.started)/time.Second), 0),
	}
	in := gesundheit.Eingabe{Now: now}

	if free, ok := s.diskFree(s.cfg.DataDir); ok {
		d := &api.HealthDisk{FreeBytes: free}
		if total, ok := s.health.diskTotal(s.cfg.DataDir); ok {
			d.TotalBytes = total
		}
		h.Disk = d
		in.FreiBekannt, in.Frei, in.Gesamt = true, d.FreeBytes, d.TotalBytes
	}
	used, over := s.st.Usage()
	h.StoreBytes, h.StoreKnown = used, s.st.UsageKnown()
	in.UeberBudget = over

	running := s.runningWorld()
	showWorld := cmp.Or(running, s.normWorld(""))
	h.WorldID, h.HeadGeneration, h.HeadCreated = s.st.HeadInfoOf(showWorld)
	if e, ok := s.st.GroupWorld(showWorld); ok {
		h.WorldName, h.WorldRunning = e.Name, e.ID == running
	}
	h.Worlds = len(s.st.GroupWorlds())

	if info, ok := s.lm.Info(); ok {
		h.Host = &api.HealthHost{
			MinecraftName: info.Host.MinecraftName,
			MachineID:     info.Host.MachineID,
			UploadOnly:    info.UploadOnly,
			LastBeat:      info.LastBeat,
		}
		in.Host = &gesundheit.Host{Name: info.Host.MinecraftName, LastBeat: info.LastBeat}
	}

	h.Starters = s.healthStarters()
	for _, st := range h.Starters {
		in.Starters = append(in.Starters, gesundheit.Starter{Name: st.MinecraftName, Version: st.Version})
	}
	s.manifestMu.RLock()
	if m := s.manifest; m != nil && m.Starter != nil {
		h.ManifestStarter = m.Starter.Version
	}
	s.manifestMu.RUnlock()
	in.ManifestStarter = h.ManifestStarter

	h.OpenJoins = int(s.openJoins.Load())
	in.OffeneBeitritte = h.OpenJoins
	h.Maintenance = s.wartungNotice()

	if s.cfg.DataDir != "" {
		if m, ok, err := sicherung.MarkeLesen(s.cfg.DataDir); err != nil {
			s.log.Error("Sicherungsmarke nicht lesbar – die letzte Sicherung gilt als unbekannt", "err", err)
		} else if ok {
			h.Backup = api.HealthBackup{Known: true, Time: m.Zeit, Target: m.Ziel}
			in.SicherungBekannt, in.Sicherung = true, m.Zeit
		}
	}

	s.health.mu.Lock()
	if p := s.health.prune; p != nil {
		c := *p
		h.Prune = &c
		in.AufraeumenFehler = c.Error
	}
	s.health.mu.Unlock()

	h.Warnings = gesundheit.Warnungen(in)
	h.Status = gesundheit.Ampel(h.Warnings)
	return h
}

// handleHealth liefert die Gesundheit (nur Admins).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !identityOf(r).member.Admin {
		writeError(w, http.StatusForbidden, "Die Gesundheit des Koordinators sehen nur Admins")
		return
	}
	writeJSON(w, http.StatusOK, s.healthSnapshot())
}

// pruneResult macht aus einem Aufräumlauf den Eintrag für die Gesundheit.
func pruneResult(at time.Time, probe bool, rep store.PruneReport, err error) api.HealthPrune {
	p := api.HealthPrune{Time: at.UTC(), Probe: probe}
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.Generations, p.Blobs, p.Bytes = rep.Generations, rep.Blobs, rep.Bytes
	return p
}
