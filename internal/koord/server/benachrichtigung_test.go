package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/benachrichtigung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
)

const geheimerWebhook = "https://discord.com/api/webhooks/123456789012345678/GEHEIMES-TOKEN-abcdefghijklmnopqrstuvwxyz0123456789"

type nachrichten struct {
	mu   sync.Mutex
	text []string
}

func (n *nachrichten) senden(t string) { n.mu.Lock(); n.text = append(n.text, t); n.mu.Unlock() }
func (n *nachrichten) alle() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.text...)
}

// Die Webhook-Adresse erfährt nie jemand: Status (auch für Gäste), Info,
// Gesundheit und Mitglieder enthalten sie nicht; der Status sagt nur „an“.
func TestStatusNenntNurDassDiscordAnIst(t *testing.T) {
	for _, mitWebhook := range []bool{false, true} {
		dir := t.TempDir()
		cfg := &config.Config{
			DataDir: dir, MinecraftAddress: "mc.example.org:15565", StorageBudget: 1 << 30,
			Members: []config.Member{{Email: emailA, Minecraft: "Alice", Admin: true}, {Email: emailB, Minecraft: "Bob"}},
			Gruppe:  config.Gruppe{Titel: "Wandergruppe"},
		}
		cfg.Auth.Mode = "dev"
		cfg.PortHolder.Port = mcPort
		if mitWebhook {
			cfg.DiscordWebhook = geheimerWebhook
		}
		clk := &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
		lm, err := lease.New(filepath.Join(dir, "lease.json"), clk)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
		if err != nil {
			t.Fatal(err)
		}
		srv := server.New(cfg, lm, st, &fakePortHolder{}, fakeUUIDs{"Alice": uuidA, "Bob": uuidB})
		srv.SetNow(clk.Now)
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		t.Cleanup(srv.Wait)

		var status api.Status
		if code := call(t, ts.URL, userB, http.MethodGet, "/v1/status", nil, &status); code != http.StatusOK {
			t.Fatalf("Status: %d", code)
		}
		if status.DiscordChannel != mitWebhook {
			t.Errorf("Webhook %v: discord_channel %v", mitWebhook, status.DiscordChannel)
		}
		for _, path := range []string{"/v1/status", "/v1/info", "/v1/me", "/v1/members", "/v1/health", "/v1/worlds", "/v1/stats"} {
			for _, as := range []who{userA, userB} {
				req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
				setIdentity(req, as)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(body), "GEHEIMES-TOKEN") || strings.Contains(string(body), "123456789012345678") || strings.Contains(string(body), "webhook") {
					t.Errorf("%s (%s): enthält die Webhook-Adresse: %s", path, as.email, body)
				}
				if !mitWebhook && strings.Contains(string(body), "discord_channel") && path == "/v1/status" {
					t.Errorf("ohne Webhook darf discord_channel fehlen: %s", body)
				}
			}
		}
	}
}

// Ereignisse des Koordinators gehen im Takt an Discord – ohne Flut beim
// Start und ohne E-Mail-Adressen.
func TestTaktMeldetEreignisseAnDiscord(t *testing.T) {
	e := newMemberEnv(t, t.TempDir(), 1<<30)
	n := &nachrichten{}
	e.srv.SetBenachrichtigung(benachrichtigung.Neu(benachrichtigung.Optionen{
		Gruppe: "Wandergruppe", Senden: n.senden, Anlauf: 20 * time.Second,
	}))
	tick := func(d time.Duration) {
		e.clk.Advance(d)
		e.srv.ExpireTick(t.Context())
	}
	tick(0) // Vergleichsstand
	host := e.host()
	tick(5 * time.Second) // in der Anlaufzeit: Stand nachführen, nichts melden
	if got := n.alle(); len(got) != 0 {
		t.Fatalf("Nachrichten in der Anlaufzeit: %q", got)
	}
	tick(20 * time.Second)
	e.heartbeat(host, "Alice", "Bob")
	tick(5 * time.Second)
	// Host war schon da (Anlaufzeit), „bereit“ kommt jetzt: Online-Meldung, keine Spieler-Flut.
	got := n.alle()
	if len(got) != 1 || got[0] != "Wandergruppe: Die Welt ist online (Host Alice)." {
		t.Fatalf("Nachrichten: %q", got)
	}
	e.heartbeat(host, "Alice", "Bob", "Dora")
	tick(5 * time.Second)
	tick(10 * time.Second)
	got = n.alle()
	if len(got) != 2 || got[1] != "Wandergruppe: Dora ist beigetreten." {
		t.Fatalf("Nachrichten: %q", got)
	}
	for _, g := range got {
		if strings.Contains(g, "example.org") || strings.Contains(g, "@") {
			t.Errorf("E-Mail-Adresse in der Nachricht: %s", g)
		}
	}
}
