package host

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
)

// ---- Spieler je Stand ----

// Jeder Stand nennt die Spieler, die seit dem vorigen Stand online waren
// (sortiert, jeder einmal), auch der Schluss-Stand – aber nur, wenn der
// Koordinator das Feld kennt: Ein neuer Koordinator antwortet auf das
// Lebenszeichen mit JSON, ein älterer mit 204 und lehnt "players" im
// Commit als unbekanntes Feld ab (400). Lehnt er doch ab, wird ohne
// Spielerliste neu eingereicht – kein Stand geht verloren – und in dieser
// Sitzung nicht mehr gesendet.
func TestCommitCarriesPlayers(t *testing.T) {
	// Zoe und Alex ab dem Start, Bob ab 1:00 (Abfrage um 1:00); Stand um
	// 5:00. Danach Bob (beim Stand online) und Carl ab 5:45; Ende um 6:00.
	snapPlayers := []string{"Alex", "Bob", "Zoe"}
	finalPlayers := []string{"Bob", "Carl"}
	run := func(t *testing.T, e *hostEnv) {
		t.Helper()
		r := e.start(t)
		e.drv.advanceTo(50 * time.Second)
		e.srv.setPlayers("Bob")
		e.drv.advanceTo(5*time.Minute + 30*time.Second)
		e.srv.setPlayers("Carl")
		e.drv.advanceTo(6 * time.Minute)
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
	}
	checkPlayers := func(t *testing.T, commits []api.CommitRequest, want ...[]string) {
		t.Helper()
		if len(commits) != len(want) {
			t.Fatalf("%d Commit-Versuche, erwartet %d", len(commits), len(want))
		}
		for i, c := range commits {
			if !slices.Equal(c.Players, want[i]) {
				t.Fatalf("Commit %d: Spieler %q, erwartet %q", i+1, c.Players, want[i])
			}
		}
	}

	t.Run("neuer Koordinator", func(t *testing.T) {
		e := newHostEnv(t, []string{"Zoe", "Alex"})
		e.co.hbResp.Store(&api.HeartbeatResponse{})
		run(t, e)
		n := e.head.Number
		commits := e.co.hostCommits()
		checkBases(t, commits, e.lease, e.head.WorldID, n, n+1)
		checkPlayers(t, commits, snapPlayers, finalPlayers)
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})

	t.Run("alter Koordinator (204): ohne Spielerliste", func(t *testing.T) {
		e := newHostEnv(t, []string{"Zoe", "Alex"})
		e.co.rejectPlayers.Store(true)
		run(t, e)
		n := e.head.Number
		commits := e.co.hostCommits()
		checkBases(t, commits, e.lease, e.head.WorldID, n, n+1)
		checkPlayers(t, commits, nil, nil)
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})

	t.Run("Koordinator lehnt players ab: ohne neu, danach nie mehr", func(t *testing.T) {
		e := newHostEnv(t, []string{"Zoe", "Alex"})
		e.co.hbResp.Store(&api.HeartbeatResponse{})
		e.co.rejectPlayers.Store(true)
		run(t, e)
		n := e.head.Number
		commits := e.co.hostCommits()
		// Zwischenstand: abgelehnt (400), ohne Spielerliste angenommen;
		// Schluss-Stand gleich ohne.
		checkBases(t, commits, e.lease, e.head.WorldID, n, n, n+1)
		checkPlayers(t, commits, snapPlayers, nil, nil)
		final := e.co.head(t)
		if final.Number != n+2 || final.MachineID != hostMachine {
			t.Fatalf("Head %d von %s, erwartet %d vom Host", final.Number, final.MachineID, n+2)
		}
		checkUploaded(t, e.checkAfterRun(t), final)
	})

	// Wer beim Stand online war, kann danach noch gespielt haben, auch wenn
	// die nächste Abfrage ihn nicht mehr sieht: Er steht auch im nächsten
	// Stand. Zwischenstände alle 100 s (Abfragen alle 15 s, also nicht
	// zugleich): Alex zuletzt um 1:30 gesehen, um 1:35 weg, Stände um 1:40
	// und 3:20, Ende um 3:30.
	t.Run("beim Stand online, danach weg: auch im nächsten Stand", func(t *testing.T) {
		co := newCoord(t, func(c *config.Config) { c.SnapshotInterval = 100 * time.Second })
		e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
		e.co.hbResp.Store(&api.HeartbeatResponse{})
		r := e.start(t)
		e.drv.advanceTo(95 * time.Second)
		e.srv.setPlayers()
		e.drv.advanceTo(210 * time.Second)
		r.cancel()
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v", err)
		}
		n := e.head.Number
		commits := e.co.hostCommits()
		checkBases(t, commits, e.lease, e.head.WorldID, n, n+1, n+2)
		checkPlayers(t, commits, []string{"Alex"}, []string{"Alex"}, nil)
	})
}

// ---- Bereitschaft ----

// Beginnt der Stop, meldet die Sitzung sofort ein Lebenszeichen mit
// ready=false, nicht erst im 10-s-Takt: Der Koordinator entzieht einem Host
// den Zuschlag, der nach einer Zurückhol-Anfrage zu lange „bereit“ meldet.
func TestReadyDropReportedAtOnce(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	e.srv.stopGate = make(chan struct{})
	r := e.start(t)
	e.drv.advanceTo(65 * time.Second)
	before := e.co.heartbeatReadies()
	if len(before) == 0 || !before[len(before)-1] {
		t.Fatalf("ready vor dem Ende %v, erwartet zuletzt true", before)
	}
	e.drv.resync()
	r.cancel()
	e.drv.allow = 1 // Stop hängt im stopGate
	e.drv.advanceTo(65 * time.Second)
	got := e.co.heartbeatReadies()
	if len(got) != len(before)+1 || got[len(got)-1] {
		t.Fatalf("ready bei 65 s %v nach %v, erwartet sofort ein Lebenszeichen mit false", got[len(before):], before)
	}
	e.drv.allow = 0
	e.drv.resync()
	close(e.srv.stopGate)
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
}

// ---- Zurückholen ----

// testNotice: „Chef“ holt den Stand von 11:05 UTC zurück.
func testNotice() *api.RestoreNotice {
	return &api.RestoreNotice{
		By:            "Chef",
		Target:        api.GenRef{WorldID: "w-alt", Number: 3},
		TargetCreated: time.Date(2026, 11, 22, 11, 5, 0, 0, time.UTC),
		RequestedAt:   t0,
		DelayS:        30,
	}
}

func firstAnnouncement(n *api.RestoreNotice) string {
	return "Admin " + n.By + " holt den Stand von " + n.TargetCreated.In(time.Local).Format("15:04") +
		" zurück – Neustart in 30 Sekunden"
}

const warnAnnouncement = "Neustart in 10 Sekunden"

func checkAnnouncements(t *testing.T, e *hostEnv, want ...timedText) {
	t.Helper()
	if got := e.srv.announcements(); !slices.Equal(got, want) {
		t.Fatalf("Ansagen bei %v: %+v, erwartet %+v", e.drv.now(), got, want)
	}
}

// checkRestoreErr: Run endet mit ErrRestore, und der Fehler trägt die
// Notice (für den Hinweis der App).
func checkRestoreErr(t *testing.T, err error, n *api.RestoreNotice) {
	t.Helper()
	if !errors.Is(err, ErrRestore) {
		t.Fatalf("Run = %v, erwartet ErrRestore", err)
	}
	re, ok := errors.AsType[*RestoreError](err)
	if !ok || re.Notice.By != n.By || re.Notice.Target != n.Target || !re.Notice.TargetCreated.Equal(n.TargetCreated) {
		t.Fatalf("Run = %#v, erwartet RestoreError mit %+v", err, *n)
	}
}

// Ein Lebenszeichen trägt die Rückhol-Notice: sofort Ansage, nach 20 s die
// zweite, nach 30 s sauberer Stopp, ein Schluss-Stand, Zuschlag zurück, und
// Run liefert ErrRestore.
func TestRestoreNoticeEndsSession(t *testing.T) {
	t.Run("während des Hostens", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		r := e.start(t)
		n := e.head.Number
		notice := testNotice()
		e.drv.advanceTo(55 * time.Second)
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		e.drv.advanceTo(60 * time.Second)
		first := timedText{60 * time.Second, firstAnnouncement(notice)}
		checkAnnouncements(t, e, first)
		e.drv.advanceTo(80*time.Second - time.Second)
		checkAnnouncements(t, e, first)
		e.drv.advanceTo(80 * time.Second)
		checkAnnouncements(t, e, first, timedText{80 * time.Second, warnAnnouncement})
		e.drv.advanceTo(90*time.Second - time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Server vor 90 s gestoppt: %v", s)
		}
		e.drv.advanceTo(90 * time.Second)
		checkRestoreErr(t, r.wait(t), notice)
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{90 * time.Second}) {
			t.Fatalf("Stop %v, erwartet einmal bei 90 s", s)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		final := e.co.head(t)
		if final.Number != n+1 || final.MachineID != hostMachine || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
			t.Fatalf("Head %d von %s, erwartet %d mit dem Stand nach Stop", final.Number, final.MachineID, n+1)
		}
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		checkUploaded(t, e.checkAfterRun(t), final)
		checkAnnouncements(t, e, first, timedText{80 * time.Second, warnAnnouncement})
	})

	t.Run("während des Starts: gleich danach sauber enden", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		notice := testNotice()
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		prepare := make(chan struct{})
		e.srv.onPrepare = func() { <-prepare }
		r := e.start(t)
		n := e.head.Number
		// Das erste Lebenszeichen (mit Notice) ist verarbeitet, der Start
		// hängt noch in Prepare.
		e.drv.allow = 1
		e.drv.advanceTo(0)
		e.drv.allow = 0
		e.drv.resync()
		close(prepare)
		checkRestoreErr(t, r.wait(t), notice)
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{0}) {
			t.Fatalf("Stop %v, erwartet einmal gleich nach dem Start", s)
		}
		checkAnnouncements(t, e) // der Server lief noch nicht, als die Notice kam
		if c := e.tn.count(); c != 0 {
			t.Fatalf("%d tunnel gestartet, erwartet keiner", c)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		checkUploaded(t, e.d.State, e.co.head(t))
	})

	// Der Server läuft, ist aber noch nicht bereit (tunnel steht noch keine
	// 3 s): Niemand kann verbunden sein. Sofort sauber enden, ohne Ansage und
	// ohne je „bereit“ zu melden – sonst meldete der Host gleich „bereit“,
	// und der Koordinator entzöge ihm nach seiner Frist den Zuschlag.
	t.Run("Server noch nicht bereit: sofort sauber enden", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		notice := testNotice()
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		g := newGate(t)
		e.co.hbGate.Store(g) // das erste Lebenszeichen hängt, bis der Server läuft
		r := e.start(t)
		n := e.head.Number
		g.waitArrived(t)
		e.drv.allow = 1
		e.drv.advanceTo(0)
		if _, starts, _, _ := e.srv.counts(); starts != 1 || e.tn.count() != 1 {
			t.Fatalf("Server %d× gestartet, %d tunnel; erwartet je 1", starts, e.tn.count())
		}
		e.co.hbGate.Store(nil)
		e.drv.allow = 0
		e.drv.resync()
		g.release()
		checkRestoreErr(t, r.wait(t), notice)
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{0}) {
			t.Fatalf("Stop %v, erwartet einmal sofort", s)
		}
		checkAnnouncements(t, e)
		if rd := e.co.heartbeatReadies(); slices.Contains(rd, true) {
			t.Fatalf("ready %v, erwartet nie true", rd)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		if h := e.co.status(t).Host; h != nil {
			t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
		}
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})
}

// Notice während eines Zwischenstands: genau ein Ende und ein Schluss-Stand;
// kommt sie, während die Sitzung schon endet, ändert sie nichts.
func TestRestoreNoticeDuringSnapshot(t *testing.T) {
	t.Run("Zwischenstand steht in Stage: verworfen", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		sg := newStageGate(t)
		e.d.stage = sg.stage
		notice := testNotice()
		r := e.start(t)
		n := e.head.Number
		e.drv.advanceTo(5*time.Minute - 5*time.Second)
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		e.drv.allow = 1 // ab 5:00 steht der Zwischenstand in Stage
		e.drv.advanceTo(5 * time.Minute)
		sg.waitArrived(t)
		e.drv.advanceTo(5*time.Minute + 30*time.Second)
		checkRestoreErr(t, r.wait(t), notice)
		checkAnnouncements(t, e, timedText{5 * time.Minute, firstAnnouncement(notice)},
			timedText{5*time.Minute + 20*time.Second, warnAnnouncement})
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{5*time.Minute + 30*time.Second}) {
			t.Fatalf("Stop %v, erwartet einmal bei 5:30", s)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
			t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1", barriers, releases)
		}
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})

	t.Run("Zwischenstand wird eingereicht: danach Schluss-Stand", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		notice := testNotice()
		g := newGate(t)
		e.co.commitGate.Store(g)
		r := e.start(t)
		n := e.head.Number
		e.drv.advanceTo(5*time.Minute - time.Second)
		e.drv.allow = 1 // ab 5:00 hängt der Commit des Zwischenstands
		e.drv.advanceTo(5 * time.Minute)
		g.waitArrived(t)
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		e.drv.advanceTo(5*time.Minute + 10*time.Second)
		e.drv.advanceTo(5*time.Minute + 40*time.Second)
		checkAnnouncements(t, e, timedText{5*time.Minute + 10*time.Second, firstAnnouncement(notice)},
			timedText{5*time.Minute + 30*time.Second, warnAnnouncement})
		if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{5*time.Minute + 40*time.Second}) {
			t.Fatalf("Stop %v, erwartet einmal bei 5:40", s)
		}
		e.co.commitGate.Store(nil)
		e.drv.allow = 0
		e.drv.resync()
		g.release()
		checkRestoreErr(t, r.wait(t), notice)
		// Erst der Zwischenstand, dann genau ein Schluss-Stand auf ihm.
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n, n+1)
		final := e.co.head(t)
		if final.Number != n+2 || genSHA(t, final, "level.dat") != sha(e.srv.saved()) {
			t.Fatalf("Head %d, erwartet %d mit dem Stand nach Stop", final.Number, n+2)
		}
		if s := e.srv.stopTimes(); len(s) != 1 {
			t.Fatalf("Stop %v, erwartet einmal", s)
		}
		checkUploaded(t, e.checkAfterRun(t), final)
	})

	t.Run("Sitzung endet schon: Notice ändert nichts", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.srv.stopGate = make(chan struct{})
		notice := testNotice()
		r := e.start(t)
		n := e.head.Number
		e.drv.advanceTo(time.Minute)
		e.drv.resync()
		r.cancel() // Stop hängt im stopGate
		e.drv.allow = 1
		e.drv.advanceTo(time.Minute)
		e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
		hb := e.co.heartbeats.Load()
		e.drv.advanceTo(time.Minute + 20*time.Second)
		if d := e.co.heartbeats.Load() - hb; d != 2 {
			t.Fatalf("%d Lebenszeichen mit Notice, erwartet 2", d)
		}
		e.drv.allow = 0
		e.drv.resync()
		close(e.srv.stopGate)
		// Das Ende kam vom Aufrufer, nicht vom Zurückholen: kein
		// ErrRestore (die App fragt danach nicht von selbst neu).
		if err := r.wait(t); err != nil {
			t.Fatalf("Run = %v, erwartet nil", err)
		}
		checkAnnouncements(t, e)
		if s := e.srv.stopTimes(); len(s) != 1 {
			t.Fatalf("Stop %v, erwartet einmal", s)
		}
		checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, n)
		checkUploaded(t, e.checkAfterRun(t), e.co.head(t))
	})
}

// Dieselbe Notice kommt mit jedem Lebenszeichen, bis der Zuschlag zurück
// ist: Sie löst nichts erneut aus – keine weitere Ansage, und die 30 s
// beginnen nicht von vorn. (Die Notice nennt hier keine Frist: Es gelten
// 30 s.)
func TestRestoreNoticeRepeated(t *testing.T) {
	e := newHostEnv(t, []string{"Alex"})
	notice := testNotice()
	notice.DelayS = 0
	r := e.start(t)
	e.drv.advanceTo(15 * time.Second)
	e.co.hbResp.Store(&api.HeartbeatResponse{Restore: notice})
	first := timedText{20 * time.Second, firstAnnouncement(notice)}
	for _, at := range []time.Duration{20 * time.Second, 30 * time.Second, 40*time.Second - time.Second} {
		hb := e.co.heartbeats.Load()
		e.drv.advanceTo(at)
		if at%(10*time.Second) == 0 && e.co.heartbeats.Load() == hb {
			t.Fatalf("kein Lebenszeichen bei %v", at)
		}
		checkAnnouncements(t, e, first)
	}
	// Eine Notice mit anderem Inhalt (sollte der Koordinator nie schicken)
	// ändert ebenfalls nichts mehr.
	other := testNotice()
	other.By = "Jemand"
	e.co.hbResp.Store(&api.HeartbeatResponse{Restore: other})
	e.drv.advanceTo(50*time.Second - time.Second)
	warn := timedText{40 * time.Second, warnAnnouncement}
	checkAnnouncements(t, e, first, warn)
	if s := e.srv.stopTimes(); len(s) != 0 {
		t.Fatalf("Server vor 50 s gestoppt: %v", s)
	}
	e.drv.advanceTo(50 * time.Second)
	checkRestoreErr(t, r.wait(t), notice)
	if s := e.srv.stopTimes(); !slices.Equal(s, []time.Duration{50 * time.Second}) {
		t.Fatalf("Stop %v, erwartet einmal bei 50 s", s)
	}
	checkAnnouncements(t, e, first, warn)
	checkBases(t, e.co.hostCommits(), e.lease, e.head.WorldID, e.head.Number)
}

// ---- Whitelist und Ops ----

// Ändert sich MembersVersion im Lebenszeichen, holt der Host die
// Host-Einstellungen neu und gleicht den laufenden Server an (Vergleich über
// die UUID, Befehle mit dem Namen): neue Mitglieder auf die Whitelist,
// entfernte herunter und hinaus, Ops dazu und weg. Gleiche Version: keine
// Befehle. Scheitert der Abruf, kommt der nächste Versuch mit dem nächsten
// Lebenszeichen.
func TestMembersVersionAppliesWhitelist(t *testing.T) {
	co := newCoord(t)
	p := func(id, name string) api.Player {
		return api.Player{UUID: "00000000-0000-4000-8000-0000000000" + id, Name: name}
	}
	alex, bea, carl, dora := p("0a", "Alex"), p("0b", "Bea"), p("0c", "Carl"), p("0d", "Dora")
	co.lists.Store(&hostLists{whitelist: []api.Player{alex, bea, carl}, ops: []api.Player{alex, bea}})
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 1})
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
	r := e.start(t)

	// Erste Version nach dem Start: abgeglichen mit dem Stand des Starts –
	// nichts zu tun.
	e.drv.advanceTo(30 * time.Second)
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q, erwartet keine", c)
	}
	gets := co.settingsGets.Load()

	// Mitglieder ändern sich, die Version (noch) nicht: nichts.
	// Ein Name wechselt bei gleicher UUID: kein Befehl.
	carlNeu := carl
	carlNeu.Name = "Carlo"
	co.lists.Store(&hostLists{whitelist: []api.Player{alex, carlNeu, dora}, ops: []api.Player{carlNeu}})
	e.drv.advanceTo(time.Minute)
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q bei gleicher Version, erwartet keine", c)
	}
	if g := co.settingsGets.Load(); g != gets {
		t.Fatalf("%d Abrufe der Host-Einstellungen bei gleicher Version, erwartet keinen", g-gets)
	}

	// Neue Version, der Abruf scheitert: nichts; das nächste Lebenszeichen
	// holt es nach.
	co.failSettings.Store(true)
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 2})
	e.drv.advanceTo(70 * time.Second)
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q ohne Host-Einstellungen, erwartet keine", c)
	}
	co.failSettings.Store(false)
	e.drv.advanceTo(80 * time.Second)
	want := []string{
		"whitelist add Dora",
		"op Carlo",
		"deop Alex",
		"deop Bea",
		"whitelist remove Bea",
		"kick Bea Du bist nicht mehr Mitglied der Gruppe.",
	}
	if c := e.srv.cmdsAfterStart(); !slices.Equal(c, want) {
		t.Fatalf("Befehle\n%q\nerwartet\n%q", c, want)
	}

	// Danach dieselbe Version: keine weiteren Befehle.
	e.drv.advanceTo(3 * time.Minute)
	if c := e.srv.cmdsAfterStart(); !slices.Equal(c, want) {
		t.Fatalf("Befehle %q, erwartet keine weiteren", c[len(want):])
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// Leere Whitelist in den Host-Einstellungen (die vorher nicht leer war):
// nicht angleichen, nächster Versuch beim nächsten Lebenszeichen. Den
// Spieler des Hosts selbst (Deps.OwnName, Groß-/Kleinschreibung egal)
// entfernt oder kickt der Abgleich nie.
func TestMembersSyncGuards(t *testing.T) {
	co := newCoord(t)
	p := func(id, name string) api.Player {
		return api.Player{UUID: "00000000-0000-4000-8000-0000000000" + id, Name: name}
	}
	alex, bea, carl := p("0a", "Alex"), p("0b", "Bea"), p("0c", "Carl")
	co.lists.Store(&hostLists{whitelist: []api.Player{alex, bea, carl}, ops: []api.Player{}})
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 1})
	e := newHostEnvOn(t, co, co.seed(t, seedFiles), []string{"Alex"})
	e.d.OwnName = "bea"
	r := e.start(t)
	e.drv.advanceTo(30 * time.Second)

	// Leere Whitelist mit neuer Version: keine Befehle; jedes weitere
	// Lebenszeichen versucht es erneut.
	co.lists.Store(&hostLists{whitelist: []api.Player{}, ops: []api.Player{}})
	co.hbResp.Store(&api.HeartbeatResponse{MembersVersion: 2})
	gets := co.settingsGets.Load()
	e.drv.advanceTo(50 * time.Second)
	if c := e.srv.cmdsAfterStart(); len(c) != 0 {
		t.Fatalf("Befehle %q bei leerer Whitelist, erwartet keine", c)
	}
	if g := co.settingsGets.Load() - gets; g != 2 {
		t.Fatalf("%d Abrufe bei 40 und 50 s, erwartet 2 (neuer Versuch je Lebenszeichen)", g)
	}

	// Bea (der Host) und Carl fallen weg: nur Carl wird entfernt und gekickt.
	co.lists.Store(&hostLists{whitelist: []api.Player{alex}, ops: []api.Player{}})
	e.drv.advanceTo(60 * time.Second)
	want := []string{"whitelist remove Carl", "kick Carl Du bist nicht mehr Mitglied der Gruppe."}
	if c := e.srv.cmdsAfterStart(); !slices.Equal(c, want) {
		t.Fatalf("Befehle %q, erwartet %q", c, want)
	}
	e.drv.advanceTo(90 * time.Second)
	if c := e.srv.cmdsAfterStart(); !slices.Equal(c, want) {
		t.Fatalf("Befehle %q, erwartet keine weiteren", c)
	}
	r.cancel()
	if err := r.wait(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}
