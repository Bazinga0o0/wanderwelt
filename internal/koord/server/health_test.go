package server_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/sicherung"
)

func (e *memberEnv) health(as who) (api.Health, int) {
	e.t.Helper()
	var h api.Health
	code := call(e.t, e.base, as, http.MethodGet, "/v1/health", nil, &h)
	return h, code
}

func hasWarning(h api.Health, level, part string) bool {
	for _, w := range h.Warnings {
		if w.Level == level && strings.Contains(w.Text, part) {
			return true
		}
	}
	return false
}

// GET /v1/health gibt es nur für Admins: Mitglieder ohne Admin-Recht und
// fremde Konten bekommen 403, ohne Identität 401.
func TestHealthNurFuerAdmins(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	if _, code := e.health(userA); code != http.StatusOK {
		t.Fatalf("Admin: %d", code)
	}
	for name, as := range map[string]who{"Mitglied": userB, "fremdes Konto": {"fremd@example.org", 909}} {
		if _, code := e.health(as); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	if _, code := e.health(who{}); code != http.StatusUnauthorized {
		t.Errorf("ohne Identität: %d, want 401", code)
	}
	// Wer das Admin-Recht verliert, verliert den Zugriff.
	if code := e.setRole(userA, emailB, true, false); code != http.StatusOK {
		t.Fatalf("Rolle: %d", code)
	}
	if _, code := e.health(userB); code != http.StatusOK {
		t.Errorf("neuer Admin: %d", code)
	}
}

// Die Gesundheit sammelt die Werte: Version und Laufzeit, Speicherplatz (aus
// der abstrahierten Abfrage, hier Fake), Weltstand, Host, Starter-Versionen
// aus Lebenszeichen und Bereitschaft, offene Beitrittsanfragen.
func TestHealthWerte(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.srv.SetVersion("0.9.1")
	e.srv.SetDiskFree(func(string) int64 { return 60 * gib })
	e.srv.SetDiskTotal(func(string) int64 { return 100 * gib })
	// Letzte Sicherung: gerade eben.
	if err := sicherung.MarkeSchreiben(dir, sicherung.Marke{Zeit: e.clk.Now(), Ziel: "/var/backup/x"}); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(90 * time.Minute)

	h, code := e.health(userA)
	if code != http.StatusOK {
		t.Fatalf("Gesundheit: %d", code)
	}
	if h.Version != "0.9.1" || h.UptimeS != 90*60 {
		t.Errorf("Version %q, Laufzeit %d s", h.Version, h.UptimeS)
	}
	if h.Disk == nil || h.Disk.FreeBytes != 60*gib || h.Disk.TotalBytes != 100*gib {
		t.Errorf("Speicherplatz: %+v", h.Disk)
	}
	if h.WorldID != "" || h.Host != nil || len(h.Starters) != 0 || h.OpenJoins != 0 || h.Maintenance != nil || h.Prune != nil {
		t.Errorf("leerer Koordinator: %+v", h)
	}
	if !h.Backup.Known || h.Backup.Target != "/var/backup/x" || !h.Backup.Time.Equal(e.clk.Now().Add(-90*time.Minute)) {
		t.Errorf("Sicherung: %+v", h.Backup)
	}
	if h.Status != api.HealthOK || len(h.Warnings) != 0 {
		t.Errorf("Ampel %q, Warnungen %+v", h.Status, h.Warnings)
	}

	// Host mit Starter-Version im Lebenszeichen.
	l := e.host()
	hb := api.HeartbeatRequest{LeaseID: l, MachineID: "pc-a", Players: []string{"Alice"}, Ready: true, StarterVersion: "0.1.4"}
	if code := call(t, e.base, userA, http.MethodPost, "/v1/lease/heartbeat", hb, nil); code != http.StatusOK {
		t.Fatalf("Lebenszeichen: %d", code)
	}
	// Bob meldet sich bereit (Bereitschaft) mit anderer Version.
	pr := api.PresenceRequest{MachineID: "pc-b", StarterVersion: "0.1.5"}
	if code := call(t, e.base, userB, http.MethodPost, "/v1/presence", pr, nil); code != http.StatusOK {
		t.Fatalf("Bereitschaft: %d", code)
	}
	// Ungültige Versionen (Leerzeichen, zu lang) werden ignoriert.
	pr = api.PresenceRequest{MachineID: "pc-c", StarterVersion: "0.1.5 <b>x</b>"}
	call(t, e.base, userC, http.MethodPost, "/v1/presence", pr, nil)
	pr = api.PresenceRequest{MachineID: "pc-c", StarterVersion: strings.Repeat("1", 41)}
	call(t, e.base, userC, http.MethodPost, "/v1/presence", pr, nil)
	// Ein älterer Starter sendet keine Version: kein Eintrag.
	call(t, e.base, userC, http.MethodPost, "/v1/presence", api.PresenceRequest{MachineID: "pc-c"}, nil)

	h, _ = e.health(userA)
	if h.Host == nil || h.Host.MinecraftName != "Alice" || h.Host.MachineID != "pc-a" || h.Host.UploadOnly || !h.Host.LastBeat.Equal(e.clk.Now()) {
		t.Errorf("Host: %+v (Uhr %v)", h.Host, e.clk.Now())
	}
	if len(h.Starters) != 2 {
		t.Fatalf("Starter: %+v", h.Starters)
	}
	if s := h.Starters[0]; s.MinecraftName != "Alice" || s.MachineID != "pc-a" || s.Version != "0.1.4" || !s.Seen.Equal(e.clk.Now()) {
		t.Errorf("erster Starter: %+v", s)
	}
	if s := h.Starters[1]; s.MinecraftName != "Bob" || s.Version != "0.1.5" {
		t.Errorf("zweiter Starter: %+v", s)
	}

	// Eine offene Beitrittsanfrage steht in Zahl und Warnungen (info; die
	// Ampel bleibt OK).
	if code := call(t, e.base, who{"neu@example.org", 777}, http.MethodPost, "/v1/join", api.JoinRequest{MinecraftName: "Dora"}, nil); code != http.StatusCreated {
		t.Fatalf("Beitrittsanfrage: %d", code)
	}
	h, _ = e.health(userA)
	if h.OpenJoins != 1 || !hasWarning(h, api.WarnInfo, "Beitrittsanfrage") || h.Status != api.HealthOK {
		t.Errorf("Beitrittsanfrage: %d offen, Ampel %q, %+v", h.OpenJoins, h.Status, h.Warnings)
	}

	// Kein Lebenszeichen seit 40 s (der Zuschlag gilt noch 50 s): Warnung.
	e.clk.Advance(40 * time.Second)
	h, _ = e.health(userA)
	if h.Status != api.HealthWarnung || !hasWarning(h, api.WarnWarnung, "Lebenszeichen") {
		t.Errorf("Host ohne Lebenszeichen: Ampel %q, %+v", h.Status, h.Warnings)
	}
	// Danach verfällt der Zuschlag: kein Host, keine Warnung.
	e.clk.Advance(60 * time.Second)
	e.srv.ExpireTick(t.Context())
	h, _ = e.health(userA)
	if h.Host != nil || hasWarning(h, api.WarnWarnung, "Lebenszeichen") {
		t.Errorf("nach dem Verfall: %+v %+v", h.Host, h.Warnings)
	}
}

// Speicherplatz, Sicherung, Starter-Versionen und Aufräumen führen zu den
// Warnungen der Spezifikation; die Ampel ist die höchste Stufe.
func TestHealthWarnungen(t *testing.T) {
	dir := t.TempDir()
	e := newMemberEnv(t, dir, gib)
	e.srv.SetDiskTotal(func(string) int64 { return 100 * gib })
	free := 60 * gib
	e.srv.SetDiskFree(func(string) int64 { return free })

	// Keine Sicherungsmarke: unbekannt.
	h, _ := e.health(userA)
	if h.Backup.Known || !hasWarning(h, api.WarnWarnung, "Sicherung unbekannt") || h.Status != api.HealthWarnung {
		t.Errorf("ohne Marke: %+v Ampel %q", h.Backup, h.Status)
	}
	// Marke von heute: Warnung weg.
	if err := sicherung.MarkeSchreiben(dir, sicherung.Marke{Zeit: e.clk.Now(), Ziel: "x"}); err != nil {
		t.Fatal(err)
	}
	if h, _ = e.health(userA); h.Status != api.HealthOK {
		t.Errorf("frische Sicherung: Ampel %q %+v", h.Status, h.Warnings)
	}
	// 7 Tage: noch in Ordnung, knapp darüber: Warnung.
	e.clk.Advance(7 * 24 * time.Hour)
	if h, _ = e.health(userA); h.Status != api.HealthOK {
		t.Errorf("Sicherung genau 7 Tage: Ampel %q %+v", h.Status, h.Warnings)
	}
	e.clk.Advance(time.Second)
	if h, _ = e.health(userA); !hasWarning(h, api.WarnWarnung, "7 Tage") {
		t.Errorf("Sicherung über 7 Tage: %+v", h.Warnings)
	}
	if err := sicherung.MarkeSchreiben(dir, sicherung.Marke{Zeit: e.clk.Now(), Ziel: "x"}); err != nil {
		t.Fatal(err)
	}

	// Freier Speicher: 14 % Warnung, unter 5 GB Fehler.
	free = 14 * gib
	if h, _ = e.health(userA); h.Status != api.HealthWarnung || !hasWarning(h, api.WarnWarnung, "14 %") {
		t.Errorf("14 %% frei: Ampel %q %+v", h.Status, h.Warnings)
	}
	free = 4 * gib
	if h, _ = e.health(userA); h.Status != api.HealthFehler || !hasWarning(h, api.WarnFehler, "4,0 GB") {
		t.Errorf("4 GB frei: Ampel %q %+v", h.Status, h.Warnings)
	}
	free = 60 * gib

	// Starter-Version unter der im Manifest: Hinweis.
	exe := []byte("MZ wanderwelt 0.2.0")
	writeFile(t, filepath.Join(server.ArtifactDir(dir), "wanderwelt-0.2.0.exe"), exe)
	e.srv.UseManifest(&api.Manifest{MinecraftVersion: "26.2", Starter: &api.StarterRelease{
		Version: "0.2.0", URL: "wanderwelt:wanderwelt-0.2.0.exe", SHA256: hexSHA256(exe), Size: int64(len(exe))}})
	pr := api.PresenceRequest{MachineID: "pc-b", StarterVersion: "0.1.5"}
	call(t, e.base, userB, http.MethodPost, "/v1/presence", pr, nil)
	h, _ = e.health(userA)
	if h.ManifestStarter != "0.2.0" || !hasWarning(h, api.WarnInfo, "Bob (0.1.5)") || h.Status != api.HealthOK {
		t.Errorf("alter Starter: %q Ampel %q %+v", h.ManifestStarter, h.Status, h.Warnings)
	}
	pr.StarterVersion = "0.2.0"
	call(t, e.base, userB, http.MethodPost, "/v1/presence", pr, nil)
	if h, _ = e.health(userA); hasWarning(h, api.WarnInfo, "Starter älter") {
		t.Errorf("Starter aktuell, trotzdem Hinweis: %+v", h.Warnings)
	}
}

// Das Ergebnis des letzten Aufräumens steht in der Gesundheit: normal,
// während einer Wartung als Probe.
func TestHealthAufraeumen(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), gib)
	h, _ := e.health(userA)
	if h.Prune != nil {
		t.Fatalf("vor dem ersten Lauf: %+v", h.Prune)
	}
	e.srv.PruneTick(t.Context())
	h, _ = e.health(userA)
	if h.Prune == nil || h.Prune.Probe || h.Prune.Error != "" || !h.Prune.Time.Equal(e.clk.Now()) {
		t.Fatalf("nach dem Lauf: %+v", h.Prune)
	}
	// Wartung: der nächste Lauf ist eine Probe.
	writeFile(t, filepath.Join(e.dir, "wartung.json"), []byte(`{"aktiv":true,"grund":"Update","since":"2026-11-22T12:00:00Z","stop_at":"2026-11-22T12:05:00Z"}`))
	e.srv.ReloadWartung()
	e.clk.Advance(time.Hour)
	e.srv.PruneTick(t.Context())
	h, _ = e.health(userA)
	if h.Prune == nil || !h.Prune.Probe || h.Maintenance == nil {
		t.Fatalf("Probe in der Wartung: %+v / %+v", h.Prune, h.Maintenance)
	}
}
