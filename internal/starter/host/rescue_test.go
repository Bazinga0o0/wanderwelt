package host

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorcon/rcon"
	"github.com/gorcon/rcon/rcontest"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// ---- Absturz-Rettung: verwaister Server ----

// startStoppableOrphan startet einen Dauerprozess als „verwaisten Server“,
// der sauber (Exit-Code 0) endet, sobald stop aufgerufen wird – wie ein
// Minecraft-Server nach "stop". o.marker passt zu ihm.
func startStoppableOrphan(t *testing.T) (o *orphan, stop func()) {
	t.Helper()
	cmd := exec.Command(testExe(t))
	cmd.Env = append(os.Environ(), helperEnv+"=stdin")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o = &orphan{cmd: cmd, exited: make(chan struct{})}
	go func() { cmd.Wait(); close(o.exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-o.exited
	})
	ref := identify(cmd.Process.Pid)
	if ref.Exe == "" || ref.Created == 0 {
		t.Fatalf("Prozess %d nicht erkennbar: %+v", cmd.Process.Pid, ref)
	}
	o.marker = marker{PID: ref.PID, Exe: ref.Exe, Created: ref.Created, Java: ref.Exe}
	return o, func() { in.Close() }
}

// rconStub ist der RCON-Zugang des verwaisten Servers; onStop läuft bei
// "stop". Ergebnis sind die passenden Zeilen für server.properties.
func rconStub(t *testing.T, onStop func()) string {
	t.Helper()
	rs := rcontest.NewServer(
		rcontest.SetSettings(rcontest.Settings{Password: "rcon-pw"}),
		rcontest.SetCommandHandler(func(c *rcontest.Context) {
			if c.Request().Body() == "stop" {
				onStop()
			}
			rcon.NewPacket(rcon.SERVERDATA_RESPONSE_VALUE, c.Request().ID, "Stopping the server").WriteTo(c.Conn())
		}),
	)
	t.Cleanup(rs.Close)
	_, port, err := net.SplitHostPort(rs.Addr())
	if err != nil {
		t.Fatal(err)
	}
	return "enable-rcon=true\nrcon.port=" + port + "\nrcon.password=rcon-pw\n"
}

// Der Starter ist abgestürzt, Java lief weiter: Endet der verwaiste Server
// nach "stop" über RCON von selbst, ist sein Stand sauber gespeichert. Er
// wird nicht verworfen, sondern auf der letzten Basis nachgereicht – auch
// während der tote Zuschlag der abgestürzten Sitzung noch gilt.
func TestRecoverOrphanStoppedCleanlyUploads(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Recover prüft Prozesse aus der Markierung nur unter Windows")
	}
	co := newCoord(t)
	head := co.seed(t, seedFiles)
	co.acquire(t, head, hostMachine) // Zuschlag der abgestürzten Sitzung
	dir := filepath.Join(group.Home(), "server")
	world := filepath.Join(dir, WorldName)
	saved := map[string]string{"level.dat": "beim stop gespeichert", "region/r.0.0.mca": "chunks"}
	writeTree(t, world, saved)
	o, stop := startStoppableOrphan(t)
	var stopped atomic.Bool
	writeTree(t, dir, map[string]string{"server.properties": rconStub(t, func() {
		stopped.Store(true)
		stop()
	})})
	if err := writeMarker(filepath.Join(dir, MarkerName), o.marker); err != nil {
		t.Fatal(err)
	}
	st := &group.State{MachineID: hostMachine, LastUploadedWorldID: head.WorldID, LastUploadedGeneration: head.Number}

	var notices []string
	err := RecoverWithCache(t.Context(), co.c, st, dir, nil, func(s string) { notices = append(notices, s) })
	if err != nil {
		t.Fatalf("Recover = %v, erwartet nachgereicht", err)
	}
	if !stopped.Load() || !o.gone(10*time.Second) {
		t.Fatalf("stop über RCON: %v, beendet: %v", stopped.Load(), o.gone(0))
	}
	if !slices.Equal(notices, []string{OrphanSavedNotice}) {
		t.Fatalf("Hinweise %q, erwartet %q", notices, OrphanSavedNotice)
	}
	commits := co.hostCommits()
	if len(commits) != 1 || commits[0].BaseWorldID != head.WorldID || commits[0].BaseGeneration != head.Number {
		t.Fatalf("Commits %+v, erwartet einen auf %s/%d", commits, head.WorldID, head.Number)
	}
	now := co.head(t)
	if now.Number != head.Number+1 || now.MachineID != hostMachine || genSHA(t, now, "level.dat") != sha(saved["level.dat"]) {
		t.Fatalf("Head %d von %s, erwartet den gesicherten Stand als %d", now.Number, now.MachineID, head.Number+1)
	}
	loaded, err := group.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	checkUploaded(t, st, now)
	checkUploaded(t, loaded, now)
	if _, err := os.Lstat(filepath.Join(dir, MarkerName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Markierungsdatei noch da: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(group.Home(), "verworfen-*")); len(matches) != 0 {
		t.Fatalf("verworfen: %v", matches)
	}
	if h := co.status(t).Host; h != nil {
		t.Fatalf("Zuschlag nicht zurückgegeben: %+v", h)
	}
}

// Reagiert der verwaiste Server nicht auf "stop" (endet nicht innerhalb von
// orphanGrace), wird er hart beendet und sein Stand verworfen, nie
// hochgeladen.
func TestRecoverOrphanIgnoringStopDiscarded(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Recover prüft Prozesse aus der Markierung nur unter Windows")
	}
	old := orphanGrace
	orphanGrace = 300 * time.Millisecond
	t.Cleanup(func() { orphanGrace = old })

	co := newCoord(t)
	o := startOrphan(t) // endet nicht von selbst
	var stopped atomic.Bool
	props := rconStub(t, func() { stopped.Store(true) })
	recoverOrphan(t, co, o.marker, props)
	if !stopped.Load() {
		t.Fatal("kein stop über RCON")
	}
	if !o.gone(10 * time.Second) {
		t.Fatal("verwaister Server läuft nach Recover noch")
	}
	if c := co.hostCommits(); len(c) != 0 {
		t.Fatalf("%d Commits, erwartet keinen", len(c))
	}
	if loaded, err := group.LoadState(); err != nil || loaded.PendingUpload {
		t.Fatalf("PendingUpload nach hartem Ende: %+v, %v", loaded, err)
	}
}

// ---- Absturz-Rettung: Fencing ----

// heldStop hält Stop des Fake-Servers an: Er endet erst mit finish –
// sauber, mit Speichern – oder hart, wenn sein Kontext vorher endet.
type heldStop struct {
	entered, gate chan struct{}
	once          sync.Once
}

// holdStop richtet heldStop ein (vor start).
func (e *hostEnv) holdStop() *heldStop {
	h := &heldStop{entered: make(chan struct{}), gate: make(chan struct{})}
	var in sync.Once
	e.srv.stopGate = h.gate
	e.srv.onStop = func() { in.Do(func() { close(h.entered) }) }
	return h
}

func (h *heldStop) finish() { h.once.Do(func() { close(h.gate) }) }

// untilFenced (nach start) stellt die Uhr bis zum Fencing bei 60 s (der
// Koordinator ist ab dem Serverstart weg) und wartet, bis Stop begonnen
// hat. Danach geht die Uhr weiter, während Stop läuft (drv.allow = 1).
func (h *heldStop) untilFenced(t *testing.T, e *hostEnv) {
	t.Helper()
	t.Cleanup(h.finish) // läuft vor dem Warten auf Run (Cleanups rückwärts)
	e.drv.advanceTo(59 * time.Second)
	e.drv.allow = 1
	e.drv.advanceTo(time.Minute)
	select {
	case <-h.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("Stop beginnt nicht")
	}
}

// Beim Fencing bekommt der Server fenceStopGrace (25 s ab dem Fencing) zum
// sauberen Stoppen. Endet er darin, wird sein Stand vorgemerkt, in der
// Sitzung aber nichts mehr hochgeladen; sonst wird er hart beendet und
// nichts vorgemerkt.
func TestFencingStopDeadline(t *testing.T) {
	t.Run("sauber innerhalb der Frist: vorgemerkt, kein Commit", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.srv.onStart = func() { e.co.down.Store(true) } // Fencing bei 60 s
		stop := e.holdStop()
		r := e.start(t)
		stop.untilFenced(t, e)
		e.drv.advanceTo(84 * time.Second) // 24 s nach dem Fencing speichert er noch
		if h := e.srv.hardTimes(); len(h) != 0 {
			t.Fatalf("hart beendet bei %v, vor Ablauf der Frist", h)
		}
		stop.finish() // jetzt endet er von selbst
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != time.Minute {
			t.Fatalf("Stop bei %v, erwartet genau einmal bei 60s", s)
		}
		if h := e.srv.hardTimes(); len(h) != 0 {
			t.Fatalf("hart beendet bei %v, erwartet sauber", h)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche, erwartet keinen", len(c))
		}
		checkPending(t, e.checkAfterRun(t), e.head.WorldID, e.head.Number)
	})

	t.Run("nicht fertig in der Frist: hart, nicht vorgemerkt", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.srv.onStart = func() { e.co.down.Store(true) }
		stop := e.holdStop() // endet nie von selbst
		r := e.start(t)
		stop.untilFenced(t, e)
		e.drv.advanceTo(84 * time.Second)
		select {
		case <-r.done:
			t.Fatalf("Run endet vor Ablauf der Frist: %v", r.err)
		default:
		}
		// Ab dem Ablauf der Frist steht die Uhr, bis Stop zurück ist.
		e.drv.allow = 0
		e.drv.advanceTo(10 * time.Minute)
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != time.Minute {
			t.Fatalf("Stop bei %v, erwartet genau einmal bei 60s", s)
		}
		// 25 s nach dem Fencing, deutlich vor dem Ablauf des Zuschlags (90 s
		// nach dem letzten Lebenszeichen bei 0 s).
		if h := e.srv.hardTimes(); len(h) != 1 || h[0] != 85*time.Second {
			t.Fatalf("hart beendet bei %v, erwartet genau einmal bei 1m25s", h)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche, erwartet keinen", len(c))
		}
		if _, err := os.Lstat(e.srv.marker); err != nil {
			t.Fatalf("Markierungsdatei weg nach hartem Ende: %v", err)
		}
		if _, running := e.tn.running(); running != 0 {
			t.Fatalf("%d tunnel-Prozesse laufen nach Run noch", running)
		}
		loaded, err := group.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		if e.d.State.PendingUpload || loaded.PendingUpload {
			t.Fatalf("PendingUpload nach hartem Ende: %+v / %+v", *e.d.State, *loaded)
		}
	})

	// Steht beim Fencing ein Zwischenstand zwischen SaveBarrier und release,
	// wartet Stop auf release. Kommt release erst nach der Frist (Stage hängt
	// und bemerkt den Abbruch nicht), beginnt Stop mit schon beendetem
	// Kontext: hart beendet, nichts vorgemerkt – auch wenn er sonst sauber
	// geendet hätte (stopGate fehlt, Stop wäre sofort fertig).
	t.Run("Zwischenstand gibt erst nach der Frist frei: hart, nicht vorgemerkt", func(t *testing.T) {
		e := newHostEnv(t, []string{"Alex"})
		e.srv.lateStop = true
		sg := newStageGate(t)
		sg.ignoreCancel = true
		e.d.stage = sg.stage
		r := e.start(t)
		e.drv.advanceTo(4*time.Minute + 25*time.Second)
		e.co.failHeartbeats.Store(true) // letztes erfolgreiches Lebenszeichen 4:20: Fencing 5:20, Frist bis 5:45
		e.drv.advanceTo(5*time.Minute - time.Second)
		e.drv.allow = 1 // ab 5:00 hängt der Zwischenstand in Stage
		e.drv.advanceTo(5 * time.Minute)
		sg.waitArrived(t)
		e.drv.advanceTo(5*time.Minute + 50*time.Second)
		if s := e.srv.stopTimes(); len(s) != 0 {
			t.Fatalf("Stop vor release des Zwischenstands: %v", s)
		}
		if !e.srv.isHolding() {
			t.Fatal("Zwischenstand steht nicht mehr zwischen SaveBarrier und release")
		}
		e.drv.resync()
		sg.openNow() // release erst jetzt, 5 s nach der Frist
		err := r.wait(t)
		if !errors.Is(err, ErrFenced) {
			t.Fatalf("Run = %v, erwartet ErrFenced", err)
		}
		if s := e.srv.stopTimes(); len(s) != 1 || s[0] != 5*time.Minute+50*time.Second {
			t.Fatalf("Stop bei %v, erwartet genau einmal bei 5m50s", s)
		}
		if h := e.srv.hardTimes(); len(h) != 1 || h[0] != 5*time.Minute+50*time.Second {
			t.Fatalf("hart beendet bei %v, erwartet genau einmal bei 5m50s", h)
		}
		if _, _, barriers, releases := e.srv.counts(); barriers != 1 || releases != 1 {
			t.Fatalf("SaveBarrier %d×, release %d×, erwartet je 1", barriers, releases)
		}
		if c := e.co.hostCommits(); len(c) != 0 {
			t.Fatalf("%d Commit-Versuche, erwartet keinen", len(c))
		}
		if _, err := os.Lstat(e.srv.marker); err != nil {
			t.Fatalf("Markierungsdatei weg nach hartem Ende: %v", err)
		}
		if _, running := e.tn.running(); running != 0 {
			t.Fatalf("%d tunnel-Prozesse laufen nach Run noch", running)
		}
		loaded, err := group.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		if e.d.State.PendingUpload || loaded.PendingUpload {
			t.Fatalf("PendingUpload nach hartem Ende: %+v / %+v", *e.d.State, *loaded)
		}
	})
}
