package host

// Übergabe der laufenden Welt an einen anderen PC, ohne die Spieler
// hinauszuwerfen.
//
// Alter Host (Deps.Handover):
//
//	HandoverStart → tick freeze, save-off, Modi merken, save-all flush,
//	Ruhe abwarten (die Dateien der Welt ändern sich nicht mehr), Stand
//	bereitstellen (ändert sich dabei eine Datei: erneut speichern und bis zu
//	dreimal von vorn) → erst danach alle Spectator, Ansage → hochladen,
//	einreichen, HandoverReady → warten (Spielerabfrage alle 2 s, Neue werden
//	Spectator) → Koordinator meldet "bereit": transfer <host> <port> @a,
//	warten bis keiner mehr da ist (höchstens 30 s) → Server stoppen ohne
//	weiteren Stand, Zuschlag zurückgeben. Der Stand enthält so nie Spectator.
//
// Scheitert etwas VOR dem gelungenen Commit (Fehler beim Einfrieren,
// Bereitstellen, Hochladen, Einreichen oder Melden, "gescheitert" vom
// Koordinator): Modi zurück, tick unfreeze, save-on, Ansage, die Übergabe wird
// beim Koordinator abgebrochen (POST /v1/handover/abort) und die Sitzung
// hostet weiter; der Host darf es erneut versuchen. Nach dem gelungenen
// Commit (kein "bereit" in 7 Minuten, "gescheitert", transfer fehlgeschlagen)
// ist der Übergabe-Stand der Schluss-Stand: Server stoppt ohne neuen Stand.
// Wird ein Ende verlangt (Herunterfahren, Zurückholen …), endet die Sitzung
// nach dem Zurückbauen normal. Gab es keinen Kandidaten (409 auf
// HandoverStart), endet die Sitzung gleich normal. Der Server steht erst nach
// HandoverStart still, ein Ende vor dem Einfrieren braucht also keine
// Rückabwicklung.
//
// Neuer Host: Deps.TunnelPort (Ersatz-Port) und die Auswertung von MainPort
// im Lebenszeichen (host.go).
//
// Alles hier läuft in der Schleife der Sitzung; Befehle, Abfragen und der
// Upload laufen in Goroutinen (launch) und melden ihr Ergebnis zurück.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// Zeiten der Übergabe.
const (
	// HandoverPlayersInterval: Spielerabfrage während der Übergabe (neue
	// Spieler werden gleich Spectator, nach dem transfer wird auf ihr Ende
	// gewartet).
	HandoverPlayersInterval = 2 * time.Second
	// handoverReadyWait: so lange nach dem Einreichen auf "bereit" warten.
	handoverReadyWait = 7 * time.Minute
	// transferWait: so lange nach dem transfer auf das Ende der Spieler
	// warten.
	transferWait = 30 * time.Second

	handoverStartTimeout  = 30 * time.Second
	handoverFreezeTimeout = 2 * time.Minute
	handoverCmdTimeout    = 30 * time.Second
	handoverReadyTimeout  = 30 * time.Second

	// handoverStageTries: so oft wird das Bereitstellen versucht, wenn sich
	// beim Kopieren eine Datei ändert.
	handoverStageTries = 3
	// handoverAbortTries: so oft wird der Abbruch beim Koordinator versucht.
	handoverAbortTries = 3
)

// Zeiten vor dem Kopieren. Variablen, damit Tests sie kürzen können.
var (
	// handoverFlushPause: so lange mindestens nach save-all flush warten.
	handoverFlushPause = 2 * time.Second
	// handoverQuietFor: so lange müssen die Dateien der Welt unverändert sein
	// (Größe und Änderungszeit), bevor kopiert wird.
	handoverQuietFor = time.Second
	// handoverQuietPoll: Abstand der Prüfungen.
	handoverQuietPoll = 250 * time.Millisecond
	// handoverQuietMax: so lange wird höchstens auf Ruhe gewartet (danach
	// wird trotzdem kopiert; Stage erkennt eine Änderung).
	handoverQuietMax = 10 * time.Second
	// handoverStageRetryPause: Pause vor einem neuen Bereitstellen.
	handoverStageRetryPause = time.Second
	// handoverAbortPause: Pause zwischen den Versuchen des Abbruchs.
	handoverAbortPause = time.Second

	// handoverPrepareLimit: Gesamtfrist ab HandoverStart für Einfrieren, Ruhe,
	// Bereitstellen, Sperren und Hochladen. Danach wird die Vorbereitung
	// abgebrochen (der Host spielt weiter), damit der Stand nicht erst nach
	// der Frist des Koordinators (handoverCoordLimit) eingereicht wird.
	handoverPrepareLimit = 4 * time.Minute
	// handoverCoordLimit: Frist des Koordinators vom Start bis ready.
	handoverCoordLimit = 5 * time.Minute
	// handoverUploadReserve: so viel Zeit bis handoverCoordLimit muss vor dem
	// Sperren der Spieler noch bleiben (grobe Schätzung für Upload und Ready).
	handoverUploadReserve = time.Minute
)

// errPrepareLimit ist der Grund, wenn die Gesamtfrist der Vorbereitung abläuft.
var errPrepareLimit = errors.New("Frist der Vorbereitung abgelaufen")

type hoStage int

const (
	hoStarting  hoStage = iota // HandoverStart läuft
	hoFreezing                 // Einfrieren (Befehle) läuft
	hoUploading                // Stand wird bereitgestellt, hochgeladen, gemeldet
	hoWaiting                  // gemeldet, warten auf "bereit"
	hoTransfer                 // transfer geschickt (oder wird gerade geschickt)
	hoFallback                 // Rückfall läuft
)

// handover ist der Zustand einer Übergabe auf der Seite des alten Hosts.
type handover struct {
	stage hoStage
	id    int64
	to    string // Minecraft-Name des neuen Hosts

	// modes: Spielmodus (survival, creative, adventure, spectator) jedes
	// Spielers vor der Sperre; pending: Spieler, deren Modus gerade erfasst
	// wird.
	modes   map[string]string
	pending map[string]bool
	frozen  bool // alle sind Spectator: neue Spieler werden gleich Spectator

	// began: Beginn (Wanduhr) für die Fristen der Vorbereitung; ctx/cancel:
	// Kontext der Vorbereitung (Einfrieren, Ruhe, Bereitstellen, Sperren,
	// Hochladen); er endet mit der Frist und mit hoFail.
	began  time.Time
	ctx    context.Context
	cancel context.CancelCauseFunc
	// online: Wer bei der letzten Abfrage der Sperre online war (ein Spieler
	// mit bekanntem Modus, der wiederkommt, wird erneut Spectator).
	online map[string]bool

	// failReason: Die Übergabe ist gescheitert; läuft noch ein Schritt,
	// zieht er die Folgen, sobald er zurückkommt.
	failReason string
	// endWanted: Ein Ende wurde verlangt; nach dem Zurückbauen endet die
	// Sitzung, statt weiterzuhosten.
	endWanted bool
	// deadline: Ende des Wartens (hoWaiting: auf "bereit", hoTransfer: auf
	// das Ende der Spieler).
	deadline time.Time
	// sent: transfer ist angekommen (hoTransfer).
	sent bool
	// committed: Der Übergabe-Stand ist eingereicht (Commit gelungen).
	committed bool
	// restoreWait: Der Rückfall wartet auf Spieler, deren Modus gerade erfasst wird.
	restoreWait bool
}

// gameModes: Zahl aus playerGameType → Name für gamemode.
var gameModes = []string{"survival", "creative", "adventure", "spectator"}

var modeNumber = regexp.MustCompile(`(\d+)\D*$`)

// parseGameMode liest den Spielmodus aus der Antwort auf "data get entity
// <Name> playerGameType" ("Alex has the following entity data: 1").
func parseGameMode(resp string) (string, bool) {
	m := modeNumber.FindStringSubmatch(strings.TrimSpace(resp))
	if m == nil {
		return "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 || n >= len(gameModes) {
		return "", false
	}
	return gameModes[n], true
}

// playersEvery ist der Abstand der Spielerabfragen.
func (s *Session) playersEvery() time.Duration {
	if s.ho != nil {
		return HandoverPlayersInterval
	}
	return PlayersInterval
}

func (s *Session) onHandoverNote(state, to string) {
	if s.d.OnHandover != nil {
		s.d.OnHandover(state, to)
	}
}

// requestHandover: Deps.Handover hat gefeuert. Die Übergabe beginnt, sobald
// kein Zwischenstand mehr läuft (der Bereitstellungsordner gehört bis dahin
// ihm). Läuft der Server nicht (Start oder Ende), gibt es nichts zu
// übergeben: normales Ende.
func (s *Session) requestHandover() {
	if s.ho != nil || s.hoPending || s.handedOver {
		return
	}
	if !s.active() {
		log.Printf("host: Übergabe nicht möglich (Server läuft nicht oder die Sitzung endet schon)")
		if s.phase == phStarting {
			s.requestEnd("Hosten beendet (Server startet noch, keine Übergabe)")
		}
		return
	}
	log.Printf("host: Übergabe der Welt verlangt")
	s.hoPending = true
	s.hoStep(s.clk.Now())
}

// hoStep: fällige Schritte der Übergabe (nur bei laufendem Server).
func (s *Session) hoStep(now time.Time) {
	if !s.active() {
		s.hoPending = false
		return
	}
	if s.hoPending && !s.snapBusy && !s.snapHolding {
		s.hoPending = false
		s.hoBegin()
		return
	}
	h := s.ho
	if h == nil || h.deadline.IsZero() || now.Before(h.deadline) {
		return
	}
	switch h.stage {
	case hoWaiting:
		h.deadline = time.Time{}
		s.hoFail(fmt.Sprintf("nach %v noch nicht bereit", handoverReadyWait))
	case hoTransfer:
		h.deadline = time.Time{}
		s.hoFinish(fmt.Sprintf("nach %v waren noch Spieler da", transferWait))
	}
}

// hoWake meldet den nächsten Termin der Übergabe.
func (s *Session) hoWake(add func(time.Time)) {
	if h := s.ho; h != nil && (h.stage == hoWaiting || h.stage == hoTransfer) {
		add(h.deadline)
	}
}

// hoHold: Ein Ende wird verlangt, während eine Übergabe läuft. Der Server
// ist eingefroren (Spectator): Er darf so nicht enden, der Stand wäre voller
// Spectator. Die Übergabe wird abgebrochen und zurückgebaut; danach endet die
// Sitzung selbst. Nach dem transfer dagegen darf das Ende kommen (der Stand
// ist hochgeladen, das Ende ist dann ohnehin Ziel). true: Das Ende wartet.
func (s *Session) hoHold(why string) bool {
	h := s.ho
	if h == nil {
		return false
	}
	switch {
	case h.stage == hoTransfer && h.sent:
		return false
	case h.stage == hoFallback:
		h.endWanted = true
		log.Printf("host: Ende (%s) wartet, bis die Übergabe zurückgebaut ist", why)
	default:
		h.endWanted = true
		s.hoFail("Ende verlangt: " + why)
	}
	return true
}

// hoBegin: HandoverStart beim Koordinator.
func (s *Session) hoBegin() {
	h := &handover{stage: hoStarting, modes: map[string]string{}, pending: map[string]bool{}, began: time.Now()}
	s.ho = h
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, handoverStartTimeout)
		resp, err := s.d.Client.HandoverStart(ctx, s.d.LeaseID, s.d.MachineID)
		cancel()
		return func() { s.onHoStart(resp, err) }
	})
}

func (s *Session) onHoStart(resp *api.HandoverStartResponse, err error) {
	h := s.ho
	if h == nil {
		return
	}
	if err != nil || h.failReason != "" {
		s.ho = nil
		switch {
		case errors.Is(err, client.ErrHandoverBusy):
			// Eine frühere Übergabe hängt noch beim Koordinator (ihr Abbruch
			// kam nicht an): kein Grund, den Server zu stoppen.
			log.Printf("host: Übergabe gerade nicht möglich: %v", err)
			s.onHandoverNote(api.HandoverFailed, "")
			s.say("Übergabe gerade nicht möglich – bitte gleich nochmal")
			s.rearmHandover()
		case errors.Is(err, client.ErrNoHandover):
			log.Printf("host: keine Übergabe: %v", err)
			s.onHandoverNote("keiner da", "")
			s.requestEnd("keine Übergabe möglich")
		case err != nil:
			log.Printf("host: Übergabe nicht begonnen: %v", err)
			s.onHandoverNote("gescheitert", "")
			s.requestEnd("Übergabe nicht begonnen")
		default:
			// Beendet, bevor der Koordinator antwortete: Ohne ready läuft
			// seine Übergabe von selbst ab.
			s.requestEnd(h.failReason)
		}
		return
	}
	h.id, h.to = resp.HandoverID, resp.To.MinecraftName
	log.Printf("host: Übergabe %d an %s begonnen – Welt wird eingefroren", h.id, cmp.Or(h.to, "?"))
	s.onHandoverNote(api.HandoverPreparing, h.to)
	h.stage = hoFreezing
	ctx, cancel := context.WithCancelCause(s.work)
	dctx, dcancel := context.WithDeadlineCause(ctx, h.began.Add(handoverPrepareLimit), errPrepareLimit)
	h.ctx, h.cancel = dctx, func(cause error) { cancel(cause); dcancel() }
	s.launch(func() func() {
		modes, err := s.freezeOp(dctx)
		return func() { s.onHoFrozen(modes, err) }
	})
}

// command schickt einen Befehl in einer eigenen Goroutine (Fehler nur im
// Log).
func (s *Session) command(cmd string) {
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, handoverCmdTimeout)
		resp, err := s.d.Server.Command(ctx, cmd)
		cancel()
		return func() {
			if err != nil {
				log.Printf("host: Befehl %q nicht ausgeführt: %v", cmd, err)
				return
			}
			log.Printf("host: Befehl %q → %s", cmd, strings.TrimSpace(resp))
		}
	})
}

// tellraw baut den Befehl, der text allen Spielern zeigt.
func tellraw(text string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"text": text, "color": "yellow"})
	return "tellraw @a " + strings.TrimSpace(b.String())
}

// playerMode fragt den Spielmodus von name ab (Name muss gültig sein).
// Lässt er sich nicht lesen, gilt survival (und steht im Log).
func (s *Session) playerMode(ctx context.Context, name string) string {
	resp, err := s.d.Server.Command(ctx, "data get entity "+name+" playerGameType")
	if err == nil {
		if m, ok := parseGameMode(resp); ok {
			return m
		}
	}
	log.Printf("host: Spielmodus von %s nicht lesbar (%q, %v) – bei Rückfall survival", name, resp, err)
	return "survival"
}

// freezeOp: die Befehle der Sperre in der festen Reihenfolge: einfrieren,
// Speichern aus, Modi lesen, alles speichern. Spectator kommt erst nach dem
// Kopieren (lockOp). Läuft in einer eigenen Goroutine. modes enthält auch bei
// einem Fehler, was bis dahin erfasst war.
func (s *Session) freezeOp(parent context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(parent, handoverFreezeTimeout)
	defer cancel()
	modes := map[string]string{}
	if err := s.runCmd(ctx, "tick freeze"); err != nil {
		return modes, err
	}
	// Bis zum Spectator (nach dem Kopieren) sind die Spieler frei beweglich;
	// Änderungen in dieser Zeit können verloren gehen.
	if err := s.runCmd(ctx, tellraw("Welt wird gleich übergeben – kurz nicht bauen oder Truhen öffnen …")); err != nil {
		return modes, err
	}
	if err := s.runCmd(ctx, "save-off"); err != nil {
		return modes, err
	}
	names, err := s.d.Server.Players(ctx)
	if err != nil {
		return modes, fmt.Errorf("Spielerliste: %w", err)
	}
	for _, n := range names {
		if !commandName(n) {
			log.Printf("host: %q ist kein gültiger Minecraft-Name – bleibt ohne Befehl", n)
			continue
		}
		modes[n] = s.playerMode(ctx, n)
	}
	// Erst speichern, dann (nach dem Kopieren) Spectator: Der
	// Übergabe-Stand enthält nie Spectator.
	if err := s.runCmd(ctx, "save-all flush"); err != nil {
		return modes, err
	}
	return modes, nil
}

func (s *Session) runCmd(ctx context.Context, cmd string) error {
	if _, err := s.d.Server.Command(ctx, cmd); err != nil {
		return fmt.Errorf("%s: %w", cmd, err)
	}
	return nil
}

// lockOp sperrt die Spieler nach dem Kopieren: alle, die jetzt online sind,
// werden Spectator (wer seit der Modus-Erfassung dazukam, wird erst erfasst),
// dann die Ansage. known sind die schon erfassten Modi. Liefert die Modi
// aller so Gesperrten (auch bei einem Fehler, was bis dahin erfasst war).
func (s *Session) lockOp(parent context.Context, to string, known map[string]string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(parent, handoverFreezeTimeout)
	defer cancel()
	modes := map[string]string{}
	names, err := s.d.Server.Players(ctx)
	if err != nil {
		return modes, fmt.Errorf("Spielerliste: %w", err)
	}
	for _, n := range names {
		if !commandName(n) {
			log.Printf("host: %q ist kein gültiger Minecraft-Name – bleibt ohne Befehl", n)
			continue
		}
		if m, ok := known[n]; ok {
			modes[n] = m
		} else {
			modes[n] = s.playerMode(ctx, n)
		}
		if err := s.runCmd(ctx, "gamemode spectator "+n); err != nil {
			return modes, err
		}
	}
	who := "einen anderen PC"
	if commandName(to) {
		who = to
	}
	if err := s.runCmd(ctx, tellraw(fmt.Sprintf("Die Welt wird an %s übergeben – kurz warten …", who))); err != nil {
		return modes, err
	}
	return modes, nil
}

func (s *Session) onHoFrozen(modes map[string]string, err error) {
	h := s.ho
	if h == nil {
		return
	}
	for n, m := range modes {
		h.modes[n] = m
	}
	if err != nil {
		h.failReason = cmp.Or(h.failReason, fmt.Sprintf("Einfrieren: %v", err))
	}
	if h.failReason != "" {
		s.hoFallback()
		return
	}
	h.stage = hoUploading
	log.Printf("host: Welt eingefroren und gespeichert (%d Spieler erfasst) – Stand wird bereitgestellt", len(modes))
	b := s.base()
	players := s.takeSeen()
	send := s.commitPlayers(players)
	id, to, known := h.id, h.to, maps.Clone(h.modes)
	s.launch(func() func() {
		res := s.handoverUploadOp(h.ctx, h, id, to, known, b, send)
		res.up.players = players
		return func() { s.onHoUploaded(res) }
	})
}

type hoUpload struct {
	up       uploadResult
	modes    map[string]string // Modi der gesperrten Spieler
	readyErr error
}

// onHoLocked: Alle sind Spectator (aus der Goroutine des Stands, vor dessen
// Ergebnis): neue Spieler werden gleich Spectator.
func (s *Session) onHoLocked(h *handover, modes map[string]string) {
	if s.ho != h {
		return
	}
	h.online = map[string]bool{}
	for n, m := range modes {
		h.modes[n] = m
		h.online[n] = true
	}
	h.frozen = true
	log.Printf("host: Stand bereitgestellt, %d Spieler als Spectator", len(modes))
	s.nextPlayers = s.clk.Now() // Spielerabfrage gleich, dann alle 2 s
}

// sleepCtx wartet d oder bis ctx endet (false).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// worldState: Größe und Änderungszeit jeder Datei der Welt (Regionen und
// Daten aller Dimensionen, level.dat, playerdata …), ohne session.lock.
func worldState(root string) (map[string][2]int64, error) {
	out := map[string][2]int64{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // zwischen Lesen und Prüfen verschwunden
			}
			return err
		}
		if d.IsDir() || d.Name() == "session.lock" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		out[path] = [2]int64{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	return out, err
}

// waitQuiet wartet nach save-all flush: mindestens handoverFlushPause, dann
// bis die Dateien der Welt handoverQuietFor lang unverändert sind (Prüfung
// alle handoverQuietPoll, höchstens handoverQuietMax). Läuft die Zeit ab,
// geht es trotzdem weiter (Stage erkennt eine Änderung beim Kopieren).
func (s *Session) waitQuiet(ctx context.Context) error {
	if !sleepCtx(ctx, handoverFlushPause) {
		return context.Cause(ctx)
	}
	began := time.Now()
	prev, err := worldState(s.world)
	if err != nil {
		return fmt.Errorf("Welt prüfen: %w", err)
	}
	stableSince := time.Now()
	for {
		if !sleepCtx(ctx, handoverQuietPoll) {
			return context.Cause(ctx)
		}
		cur, err := worldState(s.world)
		if err != nil {
			return fmt.Errorf("Welt prüfen: %w", err)
		}
		now := time.Now()
		if maps.Equal(cur, prev) {
			if now.Sub(stableSince) >= handoverQuietFor {
				return nil
			}
		} else {
			prev, stableSince = cur, now
		}
		if now.Sub(began) >= handoverQuietMax {
			log.Printf("host: Welt wurde in %v nicht ruhig – es wird trotzdem kopiert", handoverQuietMax)
			return nil
		}
	}
}

// stageQuiet: auf Ruhe warten und bereitstellen. Ändert sich beim Kopieren
// eine Datei (worldsync.ErrChangedWhileCopying), wird erneut gespeichert und
// bis zu handoverStageTries-mal von vorn versucht; jeder andere Fehler gilt
// sofort.
func (s *Session) stageQuiet(ctx context.Context) error {
	for try := 1; ; try++ {
		if try > 1 {
			if !sleepCtx(ctx, handoverStageRetryPause) {
				return context.Cause(ctx)
			}
			cctx, cancel := context.WithTimeout(ctx, handoverFreezeTimeout)
			err := s.runCmd(cctx, "save-all flush")
			cancel()
			if err != nil {
				return err
			}
		}
		if err := s.waitQuiet(ctx); err != nil {
			return err
		}
		err := s.restage(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, worldsync.ErrChangedWhileCopying) || try >= handoverStageTries {
			return err
		}
		log.Printf("host: Übergabe-Stand: %v – Versuch %d von %d, neu speichern", err, try, handoverStageTries)
	}
}

// handoverUploadOp: bereitstellen (mit Wiederholungen), erst danach die
// Spieler sperren, hochladen, einreichen, HandoverReady melden. Der Server
// steht (save-off und save-all flush sind erledigt).
func (s *Session) handoverUploadOp(ctx context.Context, h *handover, id int64, to string, known map[string]string, b baseRef, players []string) (res hoUpload) {
	began := time.Now()
	func() {
		unlock, err := holdSync(ctx)
		if err != nil {
			res.up.err = fmt.Errorf("Übergabe-Stand: %w", err)
			return
		}
		defer unlock()
		t := time.Now()
		if err := s.stageQuiet(ctx); err != nil {
			res.up.err = fmt.Errorf("Übergabe-Stand bereitstellen: %w", err)
			return
		}
		stageTook := time.Since(t)
		// Spectator nur, wenn bis zur Frist des Koordinators noch Zeit für
		// Upload und Ready bleibt.
		if err := context.Cause(ctx); err != nil {
			res.up.err = err
			return
		}
		if left := time.Until(h.began.Add(handoverCoordLimit)); left < handoverUploadReserve {
			res.up.err = fmt.Errorf("zu wenig Zeit bis zur Frist des Koordinators (noch %v)", left.Round(time.Second))
			return
		}
		modes, err := s.lockOp(ctx, to, known)
		res.modes = modes
		if err != nil {
			res.up.err = fmt.Errorf("Spieler sperren: %w", err)
			return
		}
		s.post(func() { s.onHoLocked(h, modes) })
		res.up = s.uploadStaged(ctx, b, players)
		res.up.stats.stage, res.up.stats.total = stageTook, time.Since(began)
	}()
	if res.up.err != nil || res.up.gen == nil {
		return res
	}
	ctx, cancel := context.WithTimeout(s.work, handoverReadyTimeout)
	defer cancel()
	res.readyErr = s.d.Client.HandoverReady(ctx, api.HandoverReadyRequest{
		HandoverID: id,
		LeaseID:    s.d.LeaseID,
		MachineID:  s.d.MachineID,
		WorldID:    res.up.gen.WorldID,
		Generation: res.up.gen.Number,
	})
	return res
}

func (s *Session) onHoUploaded(res hoUpload) {
	h := s.ho
	r := res.up
	if h != nil && h.cancel != nil {
		h.cancel(nil) // die Vorbereitung ist vorbei
	}
	if h != nil {
		for n, m := range res.modes {
			h.modes[n] = m
		}
	}
	if r.refused {
		s.playersRefused = true
	}
	if r.adopted {
		s.setBase(r.base.world, r.base.gen, false)
	}
	if r.err == nil {
		s.setBase(r.gen.WorldID, r.gen.Number, true)
		if h != nil {
			h.committed = true
		}
		s.noteCommitted(r.gen, true)
		log.Printf("host: Übergabe-Stand %s/%d eingereicht", r.gen.WorldID, r.gen.Number)
		logStats(r.gen.Number, false, r.stats)
	} else {
		if r.uncertain {
			s.baseUncertain = true
		}
		// Was seit dem vorigen Stand gespielt wurde, ist nicht eingereicht.
		s.played = true
		for _, n := range r.players {
			s.seen[n] = true
		}
	}
	if h == nil {
		return
	}
	switch {
	case r.err != nil:
		h.failReason = cmp.Or(h.failReason, fmt.Sprintf("Übergabe-Stand nicht eingereicht: %v", r.err))
	case res.readyErr != nil:
		h.failReason = cmp.Or(h.failReason, fmt.Sprintf("Bereitschaft nicht gemeldet: %v", res.readyErr))
	}
	if h.failReason != "" {
		s.hoFallback()
		return
	}
	h.stage = hoWaiting
	h.deadline = s.clk.Now().Add(handoverReadyWait)
	log.Printf("host: Übergabe %d: Stand gemeldet, warten auf den neuen Host", h.id)
	s.onHandoverNote(api.HandoverStarting, h.to)
}

// onHandoverState wertet den Stand der Übergabe aus dem Lebenszeichen aus.
func (s *Session) onHandoverState(st api.HandoverState) {
	h := s.ho
	if h == nil || (st.HandoverID != 0 && h.id != 0 && st.HandoverID != h.id) {
		return
	}
	switch st.State {
	case api.HandoverFailed:
		if h.stage == hoTransfer {
			return // die Spieler sind schon unterwegs
		}
		s.hoFail("Koordinator meldet: gescheitert" + suffix(st.Reason))
	case api.HandoverReady:
		if h.stage == hoWaiting {
			s.hoTransfer(st.Address)
		}
	}
}

func suffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// hoFail: Die Übergabe ist gescheitert. Wartet die Sitzung nur, beginnt der
// Rückfall gleich; läuft ein Schritt, zieht er die Folgen bei seiner Rückkehr.
func (s *Session) hoFail(reason string) {
	h := s.ho
	if h == nil {
		return
	}
	h.failReason = cmp.Or(h.failReason, reason)
	log.Printf("host: Übergabe scheitert: %s", reason)
	if h.cancel != nil {
		h.cancel(errors.New(reason)) // Ruhe, Bereitstellen und Hochladen abbrechen
	}
	if h.stage == hoWaiting {
		s.hoFallback()
	}
}

// hoFallback: Die Übergabe ist gescheitert.
//
// Vor dem gelungenen Commit: Modi zurück, auftauen, Speichern an, Ansage, die
// Übergabe beim Koordinator abbrechen – dann hostet die Sitzung weiter (ist
// ein Ende verlangt, endet sie normal: Schluss-Stand, Upload, Rückgabe).
//
// Nach dem gelungenen Commit ist der Übergabe-Stand der Schluss-Stand (die
// Welt steht seitdem still, alle sind Spectator): Ansage, Server stoppen ohne
// neuen Stand, Zuschlag zurück (wie nach gelungener Übergabe).
func (s *Session) hoFallback() {
	h := s.ho
	if h == nil || h.stage == hoFallback {
		return
	}
	h.stage, h.deadline = hoFallback, time.Time{}
	s.onHandoverNote(api.HandoverFailed, h.to)
	if h.committed {
		log.Printf("host: Übergabe gescheitert (%s) – Server stoppt ohne neuen Stand (der Übergabe-Stand ist der Schluss-Stand)", h.failReason)
		reason := h.failReason
		s.launch(func() func() {
			ctx, cancel := context.WithTimeout(s.work, handoverCmdTimeout)
			if _, err := s.d.Server.Command(ctx, tellraw("Übergabe gescheitert – Server stoppt")); err != nil {
				log.Printf("host: Ansage nicht gesendet: %v", err)
			}
			cancel()
			return func() {
				s.handedOver = true
				s.ho = nil
				s.requestEnd("Übergabe gescheitert: " + reason)
			}
		})
		return
	}
	log.Printf("host: Übergabe gescheitert (%s) – Modi zurück, Welt wird aufgetaut", h.failReason)
	if !h.endWanted {
		// Der Abbruch beim Koordinator hängt nicht vom langsamen Rückbau ab.
		s.abortAtCoordinator(h.id)
	}
	if len(h.pending) > 0 {
		// Ein Spieler wird gerade Spectator; sein Modus kommt gleich.
		h.restoreWait = true
		return
	}
	s.hoRestore()
}

// hoRestore startet das Zurückbauen der Sperre mit den gemerkten Modi.
func (s *Session) hoRestore() {
	h := s.ho
	modes := maps.Clone(h.modes)
	msg := "Übergabe gescheitert – es geht normal weiter"
	if h.endWanted {
		msg = "Übergabe gescheitert – Server stoppt"
	}
	s.launch(func() func() {
		debts := s.restoreOp(modes, msg)
		return func() {
			for n, m := range debts {
				if s.modeDebt == nil {
					s.modeDebt = map[string]string{}
				}
				s.modeDebt[n] = m
			}
			s.onHoRestored(h)
		}
	})
}

// onHoRestored: Die Sperre ist zurückgebaut. Ein Ende, das inzwischen
// verlangt wurde, geht jetzt weiter; sonst hostet die Sitzung weiter.
func (s *Session) onHoRestored(h *handover) {
	if s.ho != h {
		return
	}
	s.ho = nil
	if h.endWanted {
		s.requestEnd("Übergabe gescheitert: " + h.failReason)
		return
	}
	if !s.active() {
		return
	}
	log.Printf("host: Übergabe %d abgebrochen – die Sitzung hostet weiter", h.id)
	// Nichts hält mehr die Leerlauf-Frist an; der Wunsch des Hosts, die
	// Übergabe zu wiederholen, wird wieder gehört.
	s.emptySince = time.Time{}
	s.nextPlayers = s.clk.Now()
	s.rearmHandover()
}

// rearmHandover macht das Übergabe-Signal wieder hörbar (die Schleife
// empfängt es nur einmal); ein Klick während der Übergabe zählt nicht.
func (s *Session) rearmHandover() {
	if s.d.Handover == nil {
		return
	}
	select {
	case <-s.d.Handover:
	default:
	}
	s.handoverCh = s.d.Handover
}

// abortAtCoordinator bricht die Übergabe id beim Koordinator ab (bis zu
// handoverAbortTries Versuche mit Pause). Läuft neben dem Rückbau.
func (s *Session) abortAtCoordinator(id int64) {
	if id == 0 {
		return
	}
	s.launch(func() func() {
		var err error
		for try := 1; try <= handoverAbortTries; try++ {
			if try > 1 && !sleepCtx(s.work, handoverAbortPause) {
				break
			}
			ctx, cancel := context.WithTimeout(s.work, handoverReadyTimeout)
			err = s.d.Client.HandoverAbort(ctx, api.HandoverAbortRequest{
				HandoverID: id, LeaseID: s.d.LeaseID, MachineID: s.d.MachineID,
			})
			cancel()
			if err == nil {
				break
			}
			log.Printf("host: Übergabe %d: Abbruch beim Koordinator, Versuch %d: %v", id, try, err)
		}
		return func() {
			if err != nil {
				log.Printf("host: Übergabe %d beim Koordinator nicht abgebrochen (seine Frist beendet sie): %v", id, err)
				return
			}
			log.Printf("host: Übergabe %d beim Koordinator abgebrochen", id)
		}
	})
}

// restoreOp macht die Sperre rückgängig. Läuft in einer eigenen Goroutine;
// ein Fehler bei einem Befehl hält die übrigen nicht auf. Wer gerade offline
// ist (ging während der Sperre), bekommt den Befehl nicht: Sein Modus kommt in
// das Ergebnis und wird gesetzt, sobald er wieder auf dem Server ist. Ob
// Minecraft den Modus eines als Spectator getrennten Spielers speichert, ist
// nicht geprüft; so oder so stimmt der Modus nach dem Wiedereinloggen.
func (s *Session) restoreOp(modes map[string]string, msg string) (debts map[string]string) {
	ctx, cancel := context.WithTimeout(s.work, handoverFreezeTimeout)
	defer cancel()
	run := func(cmd string) {
		if _, err := s.d.Server.Command(ctx, cmd); err != nil {
			log.Printf("host: Rückfall: %q nicht ausgeführt: %v", cmd, err)
		}
	}
	online := map[string]bool{}
	names, err := s.d.Server.Players(ctx)
	if err != nil {
		log.Printf("host: Rückfall: Spielerliste: %v – Modi werden allen geschickt", err)
	}
	for _, n := range names {
		online[n] = true
	}
	debts = map[string]string{}
	for _, n := range slices.Sorted(maps.Keys(modes)) {
		if !commandName(n) {
			continue
		}
		if err == nil && !online[n] {
			debts[n] = modes[n]
			continue
		}
		run("gamemode " + modes[n] + " " + n)
	}
	run("tick unfreeze")
	run("save-on")
	run(tellraw(msg))
	return debts
}

// applyModeDebt setzt den gemerkten Modus von Spielern, die nach dem
// Rückbau wieder auf dem Server sind.
func (s *Session) applyModeDebt(names []string) {
	for _, n := range names {
		mode, ok := s.modeDebt[n]
		if !ok {
			continue
		}
		delete(s.modeDebt, n)
		cmd := "gamemode " + mode + " " + n
		s.command(cmd)
	}
}

// validTransferTarget prüft host und Port aus der Adresse des Koordinators,
// bevor sie in einen Befehl kommen.
func validTransferTarget(address string) (host, port string, ok bool) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || len(host) > 253 {
		return "", "", false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '-') {
			return "", "", false
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", "", false
	}
	return host, port, true
}

// hoTransfer: Der neue Host ist bereit – alle Spieler werden zu ihm
// geschickt.
func (s *Session) hoTransfer(address string) {
	h := s.ho
	host, port, ok := validTransferTarget(address)
	if !ok {
		s.hoFail(fmt.Sprintf("ungültige Adresse des neuen Hosts %q", address))
		return
	}
	h.stage = hoTransfer
	log.Printf("host: Neuer Host bereit (%s) – Spieler werden hinübergeschickt", address)
	s.onHandoverNote(api.HandoverReady, h.to)
	cmd := "transfer " + host + " " + port + " @a"
	s.launch(func() func() {
		ctx, cancel := context.WithTimeout(s.work, handoverCmdTimeout)
		resp, err := s.d.Server.Command(ctx, cmd)
		cancel()
		return func() { s.onHoTransferred(cmd, resp, err) }
	})
}

func (s *Session) onHoTransferred(cmd, resp string, err error) {
	h := s.ho
	if h == nil || h.stage != hoTransfer {
		return
	}
	if err != nil {
		h.failReason = cmp.Or(h.failReason, fmt.Sprintf("transfer nicht ausgeführt: %v", err))
	}
	if h.failReason != "" {
		s.hoFallback()
		return
	}
	log.Printf("host: Befehl %q → %s", cmd, strings.TrimSpace(resp))
	h.sent = true
	s.handedOver = true
	h.deadline = s.clk.Now().Add(transferWait)
	s.nextPlayers = s.clk.Now()
}

// hoFinish: Die Spieler sind drüben (oder die Frist ist um): Server
// stoppen, ohne weiteren Stand.
func (s *Session) hoFinish(why string) {
	log.Printf("host: Übergabe abgeschlossen (%s) – Server wird ohne weiteren Stand gestoppt", why)
	s.ho = nil
	s.requestEnd("Welt übergeben")
}

// hoPlayers: Ergebnis einer Spielerabfrage während der Übergabe.
func (s *Session) hoPlayers(names []string) {
	h := s.ho
	switch h.stage {
	case hoTransfer:
		if h.sent && len(names) == 0 {
			s.hoFinish("alle Spieler sind drüben")
		}
	case hoUploading, hoWaiting:
		if !h.frozen {
			return
		}
		cur := map[string]bool{}
		for _, n := range names {
			cur[n] = true
			if h.pending[n] {
				continue
			}
			if !commandName(n) {
				log.Printf("host: %q ist kein gültiger Minecraft-Name – bleibt ohne Befehl", n)
				continue
			}
			_, known := h.modes[n]
			if known && h.online[n] {
				continue // schon gesperrt
			}
			h.pending[n] = true
			s.launch(func() func() {
				ctx, cancel := context.WithTimeout(s.work, handoverCmdTimeout)
				mode := ""
				if !known {
					mode = s.playerMode(ctx, n)
				}
				_, err := s.d.Server.Command(ctx, "gamemode spectator "+n)
				cancel()
				return func() {
					switch {
					case err != nil:
						log.Printf("host: %s nicht auf Spectator gesetzt: %v", n, err)
					case known:
						log.Printf("host: %s kam während der Übergabe zurück – wieder Spectator", n)
					default:
						log.Printf("host: %s kam während der Übergabe dazu – Spectator (vorher %s)", n, mode)
					}
					if s.ho != nil {
						if !known {
							s.ho.modes[n] = mode
						}
						delete(s.ho.pending, n)
						if s.ho.restoreWait && len(s.ho.pending) == 0 {
							s.ho.restoreWait = false
							s.hoRestore()
						}
					}
				}
			})
		}
		h.online = cur
	}
}
