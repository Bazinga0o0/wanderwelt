package host

// Übergabe der laufenden Welt an einen anderen PC, ohne die Spieler
// hinauszuwerfen.
//
// Alter Host (Deps.Handover):
//
//	HandoverStart → tick freeze, save-off, Modi merken, save-all flush,
//	alle Spectator, Ansage → Stand bereitstellen, hochladen, einreichen,
//	HandoverReady → warten (Spielerabfrage alle 2 s, Neue werden Spectator)
//	→ Koordinator meldet "bereit": transfer <host> <port> @a, warten bis
//	keiner mehr da ist (höchstens 30 s) → Server stoppen ohne weiteren
//	Stand, Zuschlag zurückgeben.
//
// Scheitert etwas (kein "bereit" in 6 Minuten, "gescheitert", Fehler beim
// Einfrieren, Hochladen oder Melden, Ende verlangt): Modi zurück, tick
// unfreeze, save-on, Ansage, dann das normale Ende (Schluss-Stand, Upload,
// Rückgabe). Gab es keinen Kandidaten (409 auf HandoverStart), endet die
// Sitzung gleich normal. Der Server steht erst nach HandoverStart still,
// ein Ende vor dem Einfrieren braucht also keine Rückabwicklung.
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
	"log"
	"maps"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
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
)

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
	frozen  bool // Einfrieren fertig: neue Spieler werden Spectator

	// failReason: Die Übergabe ist gescheitert; läuft noch ein Schritt,
	// zieht er die Folgen, sobald er zurückkommt.
	failReason string
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
		log.Printf("host: Ende (%s) wartet, bis die Übergabe zurückgebaut ist", why)
	default:
		s.hoFail("Ende verlangt: " + why)
	}
	return true
}

// hoBegin: HandoverStart beim Koordinator.
func (s *Session) hoBegin() {
	h := &handover{stage: hoStarting, modes: map[string]string{}, pending: map[string]bool{}}
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
	to := h.to
	s.launch(func() func() {
		modes, err := s.freezeOp(to)
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

// freezeOp: die Befehle der Sperre in der festen Reihenfolge. Läuft in einer
// eigenen Goroutine. modes enthält auch bei einem Fehler, was bis dahin
// erfasst war.
func (s *Session) freezeOp(to string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(s.work, handoverFreezeTimeout)
	defer cancel()
	modes := map[string]string{}
	run := func(cmd string) error {
		if _, err := s.d.Server.Command(ctx, cmd); err != nil {
			return fmt.Errorf("%s: %w", cmd, err)
		}
		return nil
	}
	if err := run("tick freeze"); err != nil {
		return modes, err
	}
	if err := run("save-off"); err != nil {
		return modes, err
	}
	names, err := s.d.Server.Players(ctx)
	if err != nil {
		return modes, fmt.Errorf("Spielerliste: %w", err)
	}
	var valid []string
	for _, n := range names {
		if !commandName(n) {
			log.Printf("host: %q ist kein gültiger Minecraft-Name – bleibt ohne Befehl", n)
			continue
		}
		valid = append(valid, n)
	}
	for _, n := range valid {
		modes[n] = s.playerMode(ctx, n)
	}
	// Erst speichern (Stand mit den echten Modi), dann Spectator: Der
	// Übergabe-Stand enthält nie Spectator.
	if err := run("save-all flush"); err != nil {
		return modes, err
	}
	for _, n := range valid {
		if err := run("gamemode spectator " + n); err != nil {
			return modes, err
		}
	}
	who := "einen anderen PC"
	if commandName(to) {
		who = to
	}
	if err := run(tellraw(fmt.Sprintf("Die Welt wird an %s übergeben – kurz warten …", who))); err != nil {
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
	h.frozen, h.stage = true, hoUploading
	log.Printf("host: Welt eingefroren (%d Spieler als Spectator)", len(modes))
	s.nextPlayers = s.clk.Now() // Spielerabfrage gleich, dann alle 2 s
	b := s.base()
	players := s.takeSeen()
	send := s.commitPlayers(players)
	id := h.id
	s.launch(func() func() {
		res := s.handoverUploadOp(id, b, send)
		res.up.players = players
		return func() { s.onHoUploaded(res) }
	})
}

type hoUpload struct {
	up       uploadResult
	readyErr error
}

// handoverUploadOp: bereitstellen, hochladen, einreichen, HandoverReady
// melden. Der Server steht (save-off und save-all flush sind erledigt).
func (s *Session) handoverUploadOp(id int64, b baseRef, players []string) (res hoUpload) {
	began := time.Now()
	func() {
		unlock, err := holdSync(s.work)
		if err != nil {
			res.up.err = fmt.Errorf("Übergabe-Stand: %w", err)
			return
		}
		defer unlock()
		t := time.Now()
		if err := s.restage(s.work); err != nil {
			res.up.err = fmt.Errorf("Übergabe-Stand bereitstellen: %w", err)
			return
		}
		stageTook := time.Since(t)
		res.up = s.uploadStaged(s.work, b, players)
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
	if h.stage == hoWaiting {
		s.hoFallback()
	}
}

// hoFallback: Die Übergabe ist gescheitert.
//
// Vor dem gelungenen Commit: Modi zurück, auftauen, Speichern an, Ansage –
// dann endet die Sitzung normal (Schluss-Stand, Upload, Rückgabe).
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
	log.Printf("host: Übergabe gescheitert (%s) – Modi zurück, Welt wird aufgetaut, dann normales Ende", h.failReason)
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
	reason := h.failReason
	s.launch(func() func() {
		s.restoreOp(modes)
		return func() {
			s.ho = nil
			s.requestEnd("Übergabe gescheitert: " + reason)
		}
	})
}

// restoreOp macht die Sperre rückgängig. Läuft in einer eigenen Goroutine;
// ein Fehler bei einem Befehl hält die übrigen nicht auf.
func (s *Session) restoreOp(modes map[string]string) {
	ctx, cancel := context.WithTimeout(s.work, handoverFreezeTimeout)
	defer cancel()
	run := func(cmd string) {
		if _, err := s.d.Server.Command(ctx, cmd); err != nil {
			log.Printf("host: Rückfall: %q nicht ausgeführt: %v", cmd, err)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(modes)) {
		if commandName(n) {
			run("gamemode " + modes[n] + " " + n)
		}
	}
	run("tick unfreeze")
	run("save-on")
	run(tellraw("Übergabe gescheitert – Server stoppt"))
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
		for _, n := range names {
			if _, known := h.modes[n]; known || h.pending[n] {
				continue
			}
			if !commandName(n) {
				log.Printf("host: %q ist kein gültiger Minecraft-Name – bleibt ohne Befehl", n)
				continue
			}
			h.pending[n] = true
			s.launch(func() func() {
				ctx, cancel := context.WithTimeout(s.work, handoverCmdTimeout)
				mode := s.playerMode(ctx, n)
				_, err := s.d.Server.Command(ctx, "gamemode spectator "+n)
				cancel()
				return func() {
					if err != nil {
						log.Printf("host: %s nicht auf Spectator gesetzt: %v", n, err)
					} else {
						log.Printf("host: %s kam während der Übergabe dazu – Spectator (vorher %s)", n, mode)
					}
					if s.ho != nil {
						s.ho.modes[n] = mode
						delete(s.ho.pending, n)
						if s.ho.restoreWait && len(s.ho.pending) == 0 {
							s.ho.restoreWait = false
							s.hoRestore()
						}
					}
				}
			})
		}
	}
}
