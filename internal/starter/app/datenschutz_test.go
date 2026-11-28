package app

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Datenschutz und Kompatibilität: Der Koordinator nennt die Adresse des Hosts
// nur dem Host selbst. Ältere Starter (0.1.2 bis 0.1.9) vergleichen
// Status.Host.Email mit ihrer eigenen Adresse (play.go watchHost, tray.go
// ownPlayers); das bleibt damit richtig: Der Host sieht seine Adresse
// (gleich: „ich hoste“), jeder andere eine leere (ungleich: „ein anderer
// hostet“). Die Prüfung läuft gegen einen echten Koordinator.
func TestAltstarterHostVergleich(t *testing.T) {
	const alice, bob = "alice@example.org", "bob@example.org"
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, StorageBudget: 1 << 30, MinecraftAddress: "mc.example.org:15565",
		Members: []config.Member{
			{Email: alice, Minecraft: "Alice", Admin: true},
			{Email: bob, Minecraft: "Bob"},
		}}
	cfg.Auth.Mode = "dev"
	lm, err := lease.New(filepath.Join(dir, "lease.json"), server.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"), cfg.StorageBudget)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(cfg, lm, st, portholder.Noop(), nil))
	defer ts.Close()
	cA := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: alice})
	cB := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: bob})
	ctx := t.Context()

	resA, err := cA.Play(ctx, api.PlayRequest{MachineID: "pc-a", UploadOnly: true})
	if err != nil || resA.Role != api.RoleHost {
		t.Fatalf("Alice: %+v, %v", resA, err)
	}
	meA, err := cA.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	meB, err := cB.Me(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stA, err := cA.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stB, err := cB.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Der Vergleich der älteren Starter, wörtlich (tray.go ownPlayers vor dem
	// Datenschutz; play.go: EqualFold(st.Host.Email, hostEmail)).
	alt := func(s *api.Status, me *api.Me) bool {
		return s != nil && s.Host != nil && me != nil && me.Email != "" && strings.EqualFold(s.Host.Email, me.Email)
	}
	if !alt(stA, meA) {
		t.Errorf("älterer Starter: Alice sieht sich nicht als Host (Host %+v, Me %+v)", stA.Host, meA)
	}
	if alt(stB, meB) {
		t.Errorf("älterer Starter: Bob hält sich für den Host (Host %+v)", stB.Host)
	}
	if stB.Host == nil || stB.Host.Email != "" || stB.Host.MinecraftName != "Alice" {
		t.Fatalf("Bobs Status: Host %+v, erwartet Alice ohne Adresse", stB.Host)
	}
	if b, _ := json.Marshal(stB); strings.Contains(string(b), alice) {
		t.Errorf("Bobs Status nennt Alices Adresse: %s", b)
	}
	// Älterer Starter als Mitspieler: gleich (leer == leer) heißt „derselbe
	// Host“, kein Host heißt „weg“.
	resB, err := cB.Play(ctx, api.PlayRequest{MachineID: "pc-b"})
	if err != nil || resB.Host == nil {
		t.Fatalf("Bob: %+v, %v", resB, err)
	}
	if !strings.EqualFold(stB.Host.Email, resB.Host.Email) {
		t.Errorf("älterer Mitspieler: Host-Adresse im Status %q, in der Spielantwort %q, erwartet gleich", stB.Host.Email, resB.Host.Email)
	}

	// Neuer Starter: gleiche Antworten.
	if !stA.Host.IstIch(meA) || stB.Host.IstIch(meB) || !stB.Host.IstIch(meA) {
		t.Errorf("IstIch: A %v, B %v, B über Alices Me %v", stA.Host.IstIch(meA), stB.Host.IstIch(meB), stB.Host.IstIch(meA))
	}
	stA.Players = []string{"Alice", "Bob"}
	stB.Players = stA.Players
	if got := ownPlayers(stA, meA); len(got) != 2 {
		t.Errorf("ownPlayers für den Host: %v", got)
	}
	if got := ownPlayers(stB, meB); got != nil {
		t.Errorf("ownPlayers für Bob: %v", got)
	}
	// Neuer Mitspieler: erkennt über die Kennung denselben Host und einen
	// anderen, obwohl nie eine Adresse da ist.
	hostB := *resB.Host
	other := *stB.Host
	other.ID = "ffffffffffffffff"
	steps := []statusStep{
		{st: &api.Status{Host: stB.Host, HostReady: true}},
		{st: &api.Status{Host: &other, HostReady: true}},
	}
	s := &statusSeq{seq: steps}
	if got := watchHost(t.Context(), s.get, hostB, time.Millisecond, time.Hour, time.Hour, func(*api.Status) {}); got != hostGone {
		t.Errorf("watchHost bei anderem Host (nur Kennung) = %v, erwartet hostGone", got)
	}
	s = &statusSeq{seq: []statusStep{{st: &api.Status{Host: stB.Host, HostReady: true}}}}
	if got := watchHost(t.Context(), s.get, hostB, time.Millisecond, 20*time.Millisecond, time.Hour, func(*api.Status) {}); got != watchDone {
		t.Errorf("watchHost bei gleichem Host (nur Kennung) = %v, erwartet watchDone", got)
	}
}

// Der Vergleich über die Kennung, ohne Koordinator: Fälle, die ein älterer
// Koordinator (nur Adresse) und ein neuer (nur Kennung, Adresse nur beim
// Host) liefern.
func TestHostVergleichKennungUndAdresse(t *testing.T) {
	me := &api.Me{Email: "ich@example.org", ID: "1111111111111111"}
	for _, tc := range []struct {
		name string
		h    api.HostInfo
		ich  bool
	}{
		{"neuer Koordinator, ich", api.HostInfo{ID: "1111111111111111", Email: "ich@example.org"}, true},
		{"neuer Koordinator, ich ohne Adresse", api.HostInfo{ID: "1111111111111111"}, true},
		{"neuer Koordinator, anderer", api.HostInfo{ID: "2222222222222222"}, false},
		{"älterer Koordinator, ich", api.HostInfo{Email: "Ich@Example.org"}, true},
		{"älterer Koordinator, anderer", api.HostInfo{Email: "anderer@example.org"}, false},
		{"nichts bekannt", api.HostInfo{MinecraftName: "Ich"}, false},
	} {
		if got := tc.h.IstIch(me); got != tc.ich {
			t.Errorf("%s: IstIch = %v", tc.name, got)
		}
	}
	// Älterer Koordinator, Starter mit Kennung: Me ohne Kennung, Host mit
	// Adresse.
	if !(&api.HostInfo{Email: "ich@example.org"}).IstIch(&api.Me{Email: "ich@example.org"}) {
		t.Error("älterer Koordinator: Adresse sollte genügen")
	}
	// Ziel einer Übergabe: über die Kennung, sonst Adresse, sonst Name.
	for _, tc := range []struct {
		h, to api.HostInfo
		want  bool
	}{
		{api.HostInfo{ID: "a"}, api.HostInfo{ID: "a"}, true},
		{api.HostInfo{ID: "a", MinecraftName: "X"}, api.HostInfo{ID: "b", MinecraftName: "X"}, false},
		{api.HostInfo{Email: "a@x.org"}, api.HostInfo{Email: "A@x.org"}, true},
		{api.HostInfo{MinecraftName: "Bob"}, api.HostInfo{MinecraftName: "bob"}, true},
		{api.HostInfo{}, api.HostInfo{}, false},
	} {
		if got := isTarget(&tc.h, tc.to); got != tc.want {
			t.Errorf("isTarget(%+v, %+v) = %v", tc.h, tc.to, got)
		}
	}
}

// Hinweise: Ein eigenes Hosten erkennt der Starter an der Kennung (der
// Koordinator nennt nie eine fremde Adresse); ältere Koordinatoren liefern
// die Adresse.
func TestEigenesFilternMitKennung(t *testing.T) {
	me := &api.Me{Email: "ich@example.org", ID: "1111111111111111", MinecraftName: "Ich_Selbst"}
	if _, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: "1111111111111111"}, me); !skip {
		t.Error("eigenes Hosten (Kennung) wird gemeldet")
	}
	if _, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: "2222222222222222"}, me); skip {
		t.Error("fremdes Hosten (Kennung) wird verschluckt")
	}
	if _, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.Uebergabe, WerKey: "2222222222222222", NachKey: "1111111111111111"}, me); !skip {
		t.Error("eigene Übergabe (Kennung) wird gemeldet")
	}
	// Ohne Kennung und ohne Schlüssel (leer) nie „ich“.
	if _, skip := eigenesFiltern(ereignis.Ereignis{Art: ereignis.HostStartet, WerKey: ""}, &api.Me{}); skip {
		t.Error("leerer Schlüssel gilt als „ich“")
	}
}
