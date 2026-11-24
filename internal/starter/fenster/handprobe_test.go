package fenster

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// TestHandprobeAttrappe spielt eine laufende Instanz für die Handprobe des
// Fensters: control.json in WANDERWELT_HOME, Beispieldaten, alle 12 s ein
// Hinweis, Aktionen verändern den Zustand (Spielen mit Fortschritt,
// Beenden, Profil, Autostart, Rollen, Zurückholen, Speicher). Nur mit
// WANDERWELT_FENSTER_HANDPROBE=<Sekunden>; ohne Steuerung durch Tests.
func TestHandprobeAttrappe(t *testing.T) {
	sek, _ := strconv.Atoi(os.Getenv("WANDERWELT_FENSTER_HANDPROBE"))
	if sek <= 0 {
		t.Skip("WANDERWELT_FENSTER_HANDPROBE nicht gesetzt")
	}
	home := os.Getenv("WANDERWELT_HOME")
	if home == "" {
		t.Fatal("WANDERWELT_HOME fehlt")
	}
	a := starteAttrappe(t, home)
	a.mu.Lock()
	a.protokoll = func(format string, args ...any) {
		t.Logf(time.Now().Format("15:04:05.000 ")+format, args...)
	}
	seq := int64(1)
	fortschritt := -1
	a.beiAktion = func(a *attrappe, name string, body []byte) {
		z := &a.z
		switch name {
		case "play":
			z.Busy, z.Phase, z.Detail = "Spielen", "Startet", "lädt die Welt: 0 %"
			z.Status.Host, z.Status.Players = nil, nil
			fortschritt = 0
		case "beenden":
			z.Hosting, z.Phase, z.Detail, z.HostAddr = false, "Bereit", "", ""
			z.Status.Host, z.Status.Players, z.Status.HostReady = nil, nil, false
		case "profil":
			json.Unmarshal(body, &z.Profil)
		case "autostart":
			var b struct{ An bool }
			json.Unmarshal(body, &b)
			z.Autostart = b.An
		case "rolle":
			var r rolleAuftrag
			json.Unmarshal(body, &r)
			for i := range a.members {
				if a.members[i].Email == r.Email {
					a.members[i].Admin, a.members[i].Op = r.Admin, r.Op
				}
			}
		case "zurueckholen":
			var r api.RestoreRequest
			json.Unmarshal(body, &r)
			z.Status.RestorePending = &api.RestoreNotice{By: "Lukas", Target: api.GenRef{WorldID: r.WorldID, Number: r.Number},
				TargetCreated: time.Now().Add(-2 * time.Hour), RequestedAt: time.Now(), DelayS: 30}
			go func() {
				time.Sleep(20 * time.Second)
				a.mu.Lock()
				a.z.Status.RestorePending = nil
				a.mu.Unlock()
			}()
		case "speicher":
			var b api.BudgetRequest
			json.Unmarshal(body, &b)
			a.staende.Storage.BudgetBytes = b.BudgetBytes
		}
	}
	a.mu.Unlock()
	t.Logf("Attrappe läuft auf %s (Home %s) für %d s", a.srv.URL, home, sek)

	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	ende := time.After(time.Duration(sek) * time.Second)
	for n := 1; ; n++ {
		select {
		case <-ende:
			return
		case <-tick.C:
		}
		a.mu.Lock()
		z := &a.z
		if fortschritt >= 0 {
			fortschritt += 10
			if fortschritt <= 100 {
				z.Detail = "lädt die Welt: " + strconv.Itoa(fortschritt) + " %"
			} else {
				fortschritt = -1
				z.Busy, z.Phase, z.Detail, z.Hosting = "", "Hostet", "", true
				z.HostAddr = "wanderwelt.example.de"
				z.Status.Host = &api.HostInfo{Email: "lukas@example.de", MinecraftName: "Lukas"}
				z.Status.HostReady, z.Status.Players = true, []string{"Lukas"}
				z.Status.HeadCreated = time.Now()
				seq++
				z.Hinweise = append(z.Hinweise, fensterapi.Hinweis{Seq: seq, Zeit: time.Now(), Art: "info",
					Text: "Du hostest – verbinde dich mit wanderwelt.example.de (Profil wanderwelt)."})
			}
		}
		if n%10 == 0 {
			t.Logf("%s Zustand bisher %d-mal abgefragt", time.Now().Format("15:04:05.000"), a.zaehler["/v1/zustand"])
		}
		if n%8 == 0 {
			seq++
			art, text := "info", "Jonas ist dem Spiel beigetreten."
			if seq%3 == 0 {
				art, text = "fehler", "Status: Der Koordinator antwortet nicht (Zeitüberschreitung)."
			}
			z.Hinweise = append(z.Hinweise, fensterapi.Hinweis{Seq: seq, Zeit: time.Now(), Art: art, Text: text})
			if len(z.Hinweise) > 20 {
				z.Hinweise = z.Hinweise[len(z.Hinweise)-20:]
			}
			a.log += time.Now().Format("2006/01/02 15:04:05") + " Hinweis: " + text + "\n"
		}
		a.mu.Unlock()
	}
}
