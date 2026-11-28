package fenster

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

func beispielServerListen(laeuft bool) *api.ServerLists {
	return &api.ServerLists{
		Whitelist: []api.ListPlayer{{Name: "Lukas", UUID: "a", Member: true}, {Name: "Gast1", UUID: "g"}, {Name: "Anna", UUID: "b", Member: true}},
		Ops:       []api.ListPlayer{{Name: "Lukas", UUID: "a", Member: true}, {Name: "Gast1", UUID: "g"}},
		Bans:      []api.ListBan{{Name: "Troll", UUID: "t", By: "Lukas", Since: time.Now().Add(-time.Hour)}, {Name: "Anna", UUID: "b", Member: true}},
		Running:   laeuft,
	}
}

// Den Reiter „Server“ gibt es nur für Admins: Ein Mitglied fällt auf Start
// zurück, und in der Reiterleiste steht er nur für Admins.
func TestReiterServerNurAdmins(t *testing.T) {
	jetzt := time.Now()
	var ops op.Ops
	for _, admin := range []bool{true, false} {
		a := ansicht{s: schnappschuss{verb: verbunden, z: beispielZustand(jetzt, admin, true),
			server: serverDaten{l: beispielServerListen(true), ab: jetzt}}, reiter: reiterServer}
		u := testOberflaeche(t, false, a, jetzt)
		u.layout(testKontext(&ops, image.Pt(900, 620), 1, jetzt))
		if got := u.reiter == reiterServer; got != admin {
			t.Errorf("admin=%v: Reiter bleibt %v", admin, u.reiter)
		}
	}
	if !nurAdmin(reiterServer) || reiterNamen[reiterServer] != "Server" || reiterServer != reiterZustand+1 {
		t.Errorf("Reiter Server: nurAdmin %v, Name %q, Stelle %d von %d (soll hinter Zustand stehen)", nurAdmin(reiterServer), reiterNamen[reiterServer], reiterServer, anzahlReiter)
	}
	// Klick auf den Reiter wirkt für ein Mitglied nicht.
	a := ansicht{s: schnappschuss{verb: verbunden, z: beispielZustand(jetzt, false, true)}, reiter: reiterStart}
	u := testOberflaeche(t, false, a, jetzt)
	u.tabs[reiterServer].Click()
	u.layout(testKontext(&ops, image.Pt(900, 620), 1, jetzt))
	if u.reiter == reiterServer {
		t.Error("Mitglied kommt per Klick in den Reiter „Server“")
	}
}

func TestServerZeilen(t *testing.T) {
	l := beispielServerListen(true)
	wl := whitelistZeilen(l)
	if len(wl) != 3 || wl[0].name != "Anna" || wl[1].name != "Lukas" || wl[2].name != "Gast1" {
		t.Fatalf("Whitelist-Reihenfolge: %+v", wl)
	}
	if wl[0].entfernbar || wl[1].entfernbar || !wl[2].entfernbar || wl[0].zusatz != "Mitglied" || wl[2].zusatz != "Gast" {
		t.Fatalf("Whitelist: Mitglieder hier nicht entfernbar, Gäste schon: %+v", wl)
	}
	for _, z := range opsZeilen(l) {
		if !z.entfernbar {
			t.Errorf("Op %s nicht entziehbar", z.name)
		}
	}
	bs := bannZeilen(l, time.Now())
	if len(bs) != 2 || bs[0].name != "Anna" || !strings.Contains(bs[0].zusatz, "Mitglied") || !strings.Contains(bs[1].zusatz, "von Lukas") {
		t.Fatalf("Banns: %+v", bs)
	}
	if befehlsAntwort(api.ServerCommandResult{OK: true}) == "" || befehlsAntwort(api.ServerCommandResult{Output: "x"}) != "x" {
		t.Error("befehlsAntwort")
	}
}

// Die Ansicht zeichnet in allen Zuständen ohne Absturz.
func TestServerZeichnet(t *testing.T) {
	jetzt := time.Date(2026, 11, 27, 14, 0, 0, 0, time.Local)
	regeln := &api.Gamerules{Rules: []api.Gamerule{{Name: "keep_inventory", Value: "false"}, {Name: "random_tick_speed", Value: "3"}}}
	for name, g := range map[string]serverDaten{
		"laden":         {},
		"fehler":        {err: "Der Koordinator ist zu alt für diese Ansicht", ab: jetzt},
		"laeuft":        {l: beispielServerListen(true), ab: jetzt},
		"laeuft-nicht":  {l: beispielServerListen(false), ab: jetzt},
		"leer":          {l: &api.ServerLists{}, ab: jetzt},
		"regeln":        {l: beispielServerListen(true), ab: jetzt, regeln: regeln},
		"regeln-laedt":  {l: beispielServerListen(true), ab: jetzt, regelnLaedt: true},
		"regeln-fehler": {l: beispielServerListen(true), ab: jetzt, regelnErr: "Der Host hat nicht rechtzeitig geantwortet"},
	} {
		for _, dunkel := range []bool{false, true} {
			for _, breite := range []int{520, 1100} {
				a := ansicht{s: schnappschuss{verb: verbunden, runde: 1, z: beispielZustand(jetzt, true, true), server: g}, reiter: reiterServer,
					vorher: func(u *oberflaeche, _ time.Time) { u.srv.befAntwort = "There are 0 of a max of 20 players online: " }}
				u := testOberflaeche(t, dunkel, a, jetzt)
				var ops op.Ops
				// Zweimal: beim zweiten Bild sind Schalter und Felder angelegt.
				for range 2 {
					if d := u.layout(testKontext(&ops, image.Pt(breite, 700), 1, jetzt)); d.Size.X != breite {
						t.Errorf("%s: Größe %v", name, d.Size)
					}
				}
				if u.reiter != reiterServer {
					t.Errorf("%s: Reiter gewechselt", name)
				}
			}
		}
	}
}

// serverFake liefert Listen und zählt die Abrufe.
type serverFake struct {
	*aktionsQuelle
	mu    sync.Mutex
	admin bool
	n     int
	l     *api.ServerLists
	ant   map[string]any // Aktion → letzte Anfrage
}

func (q *serverFake) Zustand(context.Context) (*fensterapi.Zustand, error) {
	return beispielZustand(time.Now(), q.admin, true), nil
}

func (q *serverFake) ServerListen(context.Context) (*api.ServerLists, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.n++
	l := *q.l
	return &l, nil
}

func (q *serverFake) Spielregeln(context.Context) (*api.Gamerules, error) {
	return &api.Gamerules{Rules: []api.Gamerule{{Name: "keep_inventory", Value: "true"}}}, nil
}

func (q *serverFake) AktionAntwort(_ context.Context, name string, body, out any) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ant == nil {
		q.ant = map[string]any{}
	}
	q.ant[name] = body
	if r, ok := out.(*api.ServerCommandResult); ok {
		*r = api.ServerCommandResult{OK: true, Output: "erledigt"}
	}
	return nil
}

func (q *serverFake) abrufe() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n
}

// Listen nur bei offenem Reiter und nur für Admins.
func TestDatenServerNurAdmins(t *testing.T) {
	for _, admin := range []bool{true, false} {
		q := &serverFake{aktionsQuelle: &aktionsQuelle{}, admin: admin, l: beispielServerListen(true)}
		d := neueDaten(func() error { return nil }, nil)
		d.takt = 20 * time.Millisecond
		d.verbinde = func() (quelle, error) { return q, nil }
		ctx, cancel := context.WithCancel(context.Background())
		go d.lauf(ctx)
		time.Sleep(160 * time.Millisecond)
		if n := q.abrufe(); n != 0 {
			t.Fatalf("admin=%v: auf dem Reiter Start %d Abrufe", admin, n)
		}
		d.setzeReiter(reiterServer)
		if admin {
			warteAuf(t, time.Second, "Listen geholt", func() bool { return q.abrufe() == 1 })
			if s := d.lesen(); s.server.l == nil || len(s.server.l.Bans) != 2 {
				t.Fatalf("Schnappschuss: %+v", s.server)
			}
		} else {
			time.Sleep(160 * time.Millisecond)
			if n := q.abrufe(); n != 0 {
				t.Fatalf("Nicht-Admin: %d Abrufe", n)
			}
		}
		cancel()
	}
}

// Ein Befehl aus dem Feld geht als Aktion „befehl“ hinaus; ein gesperrter
// gar nicht (Hinweis im Fenster).
func TestServerBefehlSenden(t *testing.T) {
	jetzt := time.Now()
	q := &serverFake{aktionsQuelle: &aktionsQuelle{}, admin: true, l: beispielServerListen(true)}
	a := ansicht{s: schnappschuss{verb: verbunden, runde: 1, z: beispielZustand(jetzt, true, true),
		server: serverDaten{l: beispielServerListen(true), ab: jetzt}}, reiter: reiterServer}
	u := testOberflaeche(t, false, a, jetzt)
	u.d.c = q
	var ops op.Ops
	u.layout(testKontext(&ops, image.Pt(900, 700), 1, jetzt))

	u.srv.befEd.SetText("stop")
	u.srv.befSenden.Click()
	u.layout(testKontext(&ops, image.Pt(900, 700), 1, jetzt))
	if !u.srv.befFehler || !strings.Contains(u.srv.befAntwort, "gesperrt") {
		t.Fatalf("stop: %q", u.srv.befAntwort)
	}
	u.srv.befEd.SetText("/say hallo")
	u.srv.befSenden.Click()
	u.layout(testKontext(&ops, image.Pt(900, 700), 1, jetzt))
	warteAuf(t, time.Second, "Antwort", func() bool {
		u.layout(testKontext(&ops, image.Pt(900, 700), 1, jetzt))
		return u.srv.befAntwort == "erledigt"
	})
	q.mu.Lock()
	defer q.mu.Unlock()
	if b, ok := q.ant[fensterapi.AktionBefehl].(api.ServerCommand); !ok || b.Command != "say hallo" {
		t.Fatalf("Aktion: %#v", q.ant)
	}
}
