package server

// Statistik (Paket stats): Spielzeit, Hostzeit und Host-Sitzungen je
// Mitglied, gezählt aus den Lebenszeichen des Haupt-Zuschlags.

import (
	"cmp"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/stats"
)

// openStats öffnet data_dir/stats.json; geht das nicht, zählt der Server
// nur im Speicher weiter (Fehler im Log), statt nicht zu starten.
func (s *Server) openStats() {
	path := ""
	if s.cfg.DataDir != "" {
		path = filepath.Join(s.cfg.DataDir, "stats.json")
	}
	t, err := stats.Open(path, s.now())
	if err != nil {
		slog.Error("Statistik: stats.json nicht lesbar, es wird nur im Speicher gezählt", "err", err)
		t, _ = stats.Open("", s.now())
	}
	s.stats = t
}

// statsSample zählt ein Lebenszeichen des Haupt-Zuschlags leaseID (nach
// lm.Report); ein Zuschlag nur zum Hochladen zählt nicht (kein Server).
func (s *Server) statsSample(leaseID int64, host string, players []string) {
	if info, ok := s.lm.Info(); !ok || info.LeaseID != leaseID || info.UploadOnly {
		return
	}
	byName := map[string]string{}
	for _, m := range s.memberList() {
		if m.Minecraft != "" {
			byName[strings.ToLower(m.Minecraft)] = strings.ToLower(m.Email)
		}
	}
	var keys []string
	for _, p := range players {
		if k, ok := byName[strings.ToLower(p)]; ok {
			keys = append(keys, k)
		}
	}
	s.stats.Sample(s.now(), leaseID, host, keys)
}

// FlushStats schreibt die Statistik sofort (beim Beenden).
func (s *Server) FlushStats() {
	if err := s.stats.Flush(s.now()); err != nil {
		slog.Error("Statistik: stats.json nicht geschrieben", "err", err)
	}
}

// handleStats beantwortet GET /v1/stats für jedes Mitglied (Gäste lässt
// ServeHTTP nicht durch). Es erscheinen die heutigen Mitglieder mit
// Minecraft-Namen, mit dem Namen von heute (E-Mail-Adressen nie); Zahlen
// ausgetretener Mitglieder bleiben in der Datei, zählen aber weder in der
// Liste noch in HostTotalS.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	now := s.now()
	rep := s.stats.Report(now)
	members := slices.Clone(s.memberList())
	slices.SortFunc(members, func(a, b config.Member) int {
		return cmp.Compare(strings.ToLower(a.Minecraft), strings.ToLower(b.Minecraft))
	})
	period := func(sums map[string]stats.Totals) api.StatsPeriod {
		p := api.StatsPeriod{Members: []api.MemberStats{}}
		for _, m := range members {
			if m.Minecraft == "" {
				continue
			}
			x := sums[strings.ToLower(m.Email)]
			p.Members = append(p.Members, api.MemberStats{
				MinecraftName: m.Minecraft,
				PlayS:         x.PlayMS / 1000,
				HostS:         x.HostMS / 1000,
				HostSessions:  x.Sessions,
			})
			p.HostTotalS += x.HostMS / 1000
		}
		if p.HostTotalS > 0 {
			for i := range p.Members {
				p.Members[i].HostShare = float64(p.Members[i].HostS) / float64(p.HostTotalS)
			}
		}
		return p
	}
	out := api.Stats{Since: rep.Since, Total: period(rep.Total), Last30: period(rep.Last30)}
	// Gezählt wird die ganze Gruppe (nicht je Welt); die laufende Welt steht dabei.
	if e, ok := s.st.GroupWorld(s.runningWorld()); ok && s.runningWorld() != "" {
		out.RunningWorld = e.Name
	}
	writeJSON(w, http.StatusOK, out)
}
