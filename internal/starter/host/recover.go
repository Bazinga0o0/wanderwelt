package host

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorcon/rcon"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/worldsync"
)

// orphanGrace: so lange darf ein verwaister Server nach "stop" über RCON
// zum Speichern und Beenden brauchen, bevor er hart beendet wird (im Test
// kürzer).
var orphanGrace = 2 * time.Minute

const (
	// orphanKillWait: so lange auf das Ende nach dem harten Beenden warten.
	orphanKillWait = 10 * time.Second
	rconTimeout    = 10 * time.Second
)

// OrphanSavedNotice ist der Hinweis, wenn Recover den Stand eines nach
// einem Absturz des Starters weiterlaufenden Servers gesichert hat. Ob er
// danach hochgeladen oder verworfen wird, sagt er nicht: Der Aufrufer zeigt
// ihn zusammen mit dem Ergebnis von Recover.
const OrphanSavedNotice = "Der Server lief nach einem Absturz des Starters noch weiter; er wurde sauber gestoppt und sein Stand gesichert."

// ErrPendingLater: Der nachzureichende Stand baut noch auf dem Head auf,
// gerade kann ihn aber niemand annehmen (jemand hostet, eine Wahl läuft).
// Er bleibt vorgemerkt; später erneut versuchen.
var ErrPendingLater = errors.New("der Stand wartet noch auf das Nachreichen")

// ErrMaybeRunning: Ein Server- oder tunnel-Prozess aus der Markierung lässt
// sich nicht prüfen (etwa Zugriff verweigert) und läuft vielleicht noch. Die
// Welt bleibt dann unberührt; nach einem Neustart des PCs ist die Lage klar.
var ErrMaybeRunning = errors.New("der Minecraft-Server von vorhin läuft vielleicht noch – die Welt bleibt unberührt. " +
	"Bitte den PC neu starten (oder java.exe im Task-Manager beenden) und dann noch einmal")

// errNoProcess: Es gibt keinen Prozess mit dieser PID (mehr).
var errNoProcess = errors.New("Prozess läuft nicht")

// openProc öffnet einen fremden Prozess; im Test austauschbar.
var openProc = openProcess

// keepDiscarded: So viele verworfene Stände (Dir()/verworfen-*) bleiben
// liegen; ältere werden beim Verwerfen gelöscht.
const keepDiscarded = 2

// DiscardedError meldet, dass ein lokaler Stand nicht hochgeladen, sondern
// nach Path (Dir()/verworfen-<Zeit>) verschoben wurde. Das ist kein
// Fehlschlag: Der Starter zeigt den Hinweis und macht weiter.
type DiscardedError struct {
	Path   string
	Reason string
}

func (e *DiscardedError) Error() string {
	return fmt.Sprintf("lokaler Stand verworfen (%s), gesichert unter %s", e.Reason, e.Path)
}

// Recover räumt beim Start des Starters auf, bevor irgendetwas anderes
// läuft (es darf keine Sitzung mit demselben Home() laufen):
//
//   - Steht die Markierung <dir>/.wanderwelt-running noch, wurde der Server
//     nicht sauber gestoppt. Läuft er sogar noch (der Starter ist
//     abgestürzt, Java nicht), bekommt er "stop" über RCON – aber nur, wenn
//     PID, Programmpfad und Startzeit zur Markierung passen. Endet er
//     daraufhin innerhalb von orphanGrace von selbst, ist sein Stand sauber
//     gespeichert: Er wird auf der Basis LastUploaded… vorgemerkt und wie
//     jeder vorgemerkte Stand nachgereicht. Sonst (er lief nicht mehr, kein
//     RCON, keine Reaktion) wird er hart beendet und die Welt nach
//     Dir()/verworfen-<Zeit> verschoben, nie hochgeladen
//     (*DiscardedError).
//   - Ist ein sauber gestoppter Stand vorgemerkt (PendingUpload), holt
//     Recover einen Zuschlag zum Nachreichen auf seiner Basis. Bekommt es
//     ihn, wird der Stand bereitgestellt, hochgeladen und eingereicht und
//     der Zuschlag zurückgegeben. Hat seitdem jemand anderes einen Stand
//     eingereicht, wird verworfen (*DiscardedError); ist die Basis noch der
//     Head, aber gerade kein Zuschlag frei, bleibt er vorgemerkt
//     (ErrPendingLater).
//
// dir ist der Server-Ordner (leer: DefaultDir()).
func Recover(ctx context.Context, c *client.Client, st *group.State, dir string) error {
	return RecoverWithCache(ctx, c, st, dir, nil, nil)
}

// RecoverWithCache ist Recover mit dem Hash-Cache hc des Starters: Ein
// Prozess benutzt für Vorladen, Host-Sitzung, Nachreichen und Welt
// übernehmen eine einzige Instanz (eine zweite, später gespeicherte, würde
// die Einträge der ersten überschreiben). nil: Recover lädt den Cache
// selbst. notice (darf nil sein) bekommt Hinweise für den Nutzer, die kein
// Fehler sind: OrphanSavedNotice, bevor der gesicherte Stand eines
// verwaisten Servers nachgereicht wird. notice läuft in der Goroutine des
// Aufrufers, noch während RecoverWithCache läuft.
func RecoverWithCache(ctx context.Context, c *client.Client, st *group.State, dir string, hc *worldsync.HashCache, notice func(string)) error {
	if st == nil {
		return errors.New("host: Recover ohne Zustand")
	}
	if dir == "" {
		dir = DefaultDir()
	}
	world := filepath.Join(dir, WorldName)

	present, err := markerPresent(filepath.Join(dir, MarkerName))
	if err != nil {
		return err
	}
	if present {
		saved, err := rescueDirty(dir, st)
		if err != nil {
			return err
		}
		if !saved {
			return nil
		}
		// Ab hier wie jeder vorgemerkte Stand: Prüfungen, dann Nachreichen.
		if notice != nil {
			notice(OrphanSavedNotice)
		}
	}

	// Ein abgebrochener Download hinterlässt einen gemischten Weltordner: nie
	// hochladen. Ohne Vormerkung gibt es nichts nachzureichen; Run lädt vor
	// dem Start ohnehin neu (und verwendet dabei vorhandene Segmente wieder).
	// Mit Vormerkung wird der Stand verworfen.
	incomplete := worldsync.DownloadIncomplete(world)
	if !st.PendingUpload {
		if incomplete {
			log.Printf("host: Download in %s nicht abgeschlossen; die Welt wird vor dem nächsten Hosten neu geladen", world)
		}
		return nil
	}
	if incomplete {
		return discardPending(st, world, "ein Download in den Weltordner wurde nicht abgeschlossen")
	}
	if !worldPresent(world) {
		return discardPending(st, world, "der lokale Stand ist unvollständig")
	}
	if c == nil {
		return errors.New("host: kein Koordinator zum Nachreichen")
	}
	return uploadPending(ctx, c, st, dir, hc, false)
}

// ---- Markierung ----

// marker ist der Inhalt der Markierungsdatei.
type marker struct {
	PID     int       `json:"pid,omitempty"`
	Exe     string    `json:"exe,omitempty"`        // Programm des Server-Prozesses, wie Windows es meldet
	Created int64     `json:"created_ns,omitempty"` // Startzeit des Server-Prozesses (Unix-Nanosekunden)
	Java    string    `json:"java,omitempty"`       // Java aus deps
	Tunnel  *procRef  `json:"tunnel,omitempty"`     // zuletzt gestarteter tunnel
	Tunnel2 *procRef  `json:"tunnel2,omitempty"`    // zweiter tunnel (Haupt-Port nach einer Übergabe)
	Written time.Time `json:"written"`
}

// procRef kennzeichnet einen Prozess eindeutig: PID, Programm (wie Windows
// es meldet) und Startzeit (Unix-Nanosekunden).
type procRef struct {
	PID     int    `json:"pid"`
	Exe     string `json:"exe,omitempty"`
	Created int64  `json:"created_ns,omitempty"`
}

func writeMarker(path string, m marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

func readMarker(path string) (marker, error) {
	var m marker
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return marker{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func markerPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// removeMarker löscht die Markierung; wie renameRetry wird kurz wiederholt,
// wenn ein Virenscanner oder Indexdienst sie gerade offen hält.
func removeMarker(path string) error {
	var err error
	for i := range 10 {
		if err = os.Remove(path); err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}

// ---- Verwerfen ----

// discardDirty: Die Markierung steht noch. Einen verwaisten Server aus der
// Markierung beenden, die Welt verschieben, Markierung und PendingUpload
// löschen. Ergebnis nil, wenn es keine Welt gab. Auch ein Server, der nach
// "stop" sauber endet, kostet hier den Stand (nur Recover rettet ihn, siehe
// rescueDirty).
func discardDirty(dir string, st *group.State, reason string) (*DiscardedError, error) {
	if _, err := stopMarked(dir); err != nil {
		return nil, err
	}
	return discardMarked(dir, st, reason)
}

// rescueDirty: Die Markierung steht noch. tunnel und einen verwaisten
// Server aus der Markierung beenden. Hat sich der Server nach "stop" von
// selbst beendet, ist sein Stand sauber gespeichert: Er wird auf der Basis
// LastUploaded… vorgemerkt, dann wird die Markierung gelöscht (saved).
// Sonst wird verworfen wie in discardDirty; ein *DiscardedError kommt dann
// als Fehler.
func rescueDirty(dir string, st *group.State) (saved bool, err error) {
	clean, err := stopMarked(dir)
	if err != nil {
		return false, err
	}
	if !clean {
		de, err := discardMarked(dir, st, "der Server wurde nicht sauber gestoppt")
		if err != nil {
			return false, err
		}
		if de != nil {
			return false, de
		}
		return false, nil
	}
	// Wie nach einem sauberen Stop in der Sitzung: erst vormerken, dann die
	// Markierung löschen. Bei einem Absturz dazwischen wird verworfen, nie
	// Halbes hochgeladen; lässt sich die Markierung nicht löschen, ebenso
	// (beim nächsten Versuch).
	st.PendingUpload = true
	if err := st.Save(); err != nil {
		return false, fmt.Errorf("Stand des verwaisten Servers vormerken: %w", err)
	}
	if err := removeMarker(filepath.Join(dir, MarkerName)); err != nil {
		return false, fmt.Errorf("Markierungsdatei nach dem Stop des verwaisten Servers löschen: %w", err)
	}
	log.Printf("host: Stand des verwaisten Servers gesichert und auf %s/%d vorgemerkt, er wird nachgereicht",
		st.LastUploadedWorldID, st.LastUploadedGeneration)
	return true, nil
}

// stopMarked beendet tunnel und Server aus der Markierung, falls sie noch
// laufen. clean: Der Server lief noch und hat sich nach "stop" von selbst
// beendet (terminateOrphan).
func stopMarked(dir string) (clean bool, err error) {
	m, err := readMarker(filepath.Join(dir, MarkerName))
	if err != nil {
		log.Printf("host: Markierungsdatei unlesbar, kein Server-Prozess bekannt: %v", err)
	}
	// Erst tunnel: Er gibt den Server öffentlich frei.
	for _, ref := range []*procRef{m.Tunnel, m.Tunnel2} {
		if ref != nil {
			if err := terminateTunnel(*ref, m.Written); err != nil {
				return false, err
			}
		}
	}
	return terminateOrphan(dir, m)
}

// discardMarked verschiebt die Welt, nachdem stopMarked tunnel und Server
// beendet hat, und löscht Markierung und PendingUpload. Ergebnis nil, wenn
// es keine Welt gab.
func discardMarked(dir string, st *group.State, reason string) (*DiscardedError, error) {
	path := filepath.Join(dir, MarkerName)
	dest, err := discardWorld(filepath.Join(dir, WorldName))
	if err != nil {
		return nil, err
	}
	if err := removeMarker(path); err != nil {
		return nil, err
	}
	st.PendingUpload = false
	if err := st.Save(); err != nil {
		return nil, err
	}
	if dest == "" {
		return nil, nil
	}
	log.Printf("host: lokaler Stand verworfen (%s), gesichert unter %s", reason, dest)
	return &DiscardedError{Path: dest, Reason: reason}, nil
}

// discardPending legt einen vorgemerkten Stand beiseite und löscht
// PendingUpload.
func discardPending(st *group.State, world, reason string) error {
	dest, err := discardWorld(world)
	if err != nil {
		return err
	}
	st.PendingUpload = false
	if err := st.Save(); err != nil {
		return err
	}
	if dest == "" {
		return nil
	}
	log.Printf("host: lokaler Stand verworfen (%s), gesichert unter %s", reason, dest)
	return &DiscardedError{Path: dest, Reason: reason}
}

// discardWorld verschiebt den Weltordner nach Dir()/verworfen-<Ortszeit>
// und liefert das Ziel ("" wenn es keine Welt gab). Danach bleiben nur die
// keepDiscarded neuesten verworfenen Stände liegen.
func discardWorld(world string) (string, error) {
	if _, err := os.Lstat(world); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	home := group.Dir()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", err
	}
	base := filepath.Join(home, "verworfen-"+time.Now().Format("20060102-150405"))
	dest := base
	for i := 2; ; i++ {
		if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dest = fmt.Sprintf("%s-%d", base, i)
	}
	if err := renameRetry(world, dest); err != nil {
		return "", fmt.Errorf("Welt nach %s verschieben: %w", dest, err)
	}
	pruneDiscarded(home, keepDiscarded, filepath.Base(dest))
	return dest, nil
}

// pruneDiscarded löscht in home alle verworfen-<Zeit>[-n] bis auf die keep
// neuesten (nach Zeit im Namen, bei gleicher Zeit nach n). newest, der
// gerade verworfene Stand, zählt immer als neuester: Die Zeit im Namen ist
// Ortszeit und kann nach der Zeitumstellung im Herbst (oder gegenüber den
// UTC-Namen älterer Versionen) vor älteren Ständen liegen. Fehler werden
// nur geloggt.
func pruneDiscarded(home string, keep int, newest string) {
	entries, err := os.ReadDir(home)
	if err != nil {
		log.Printf("host: verworfene Stände aufräumen: %v", err)
		return
	}
	type item struct {
		name, stamp string
		n           int
	}
	var items []item
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), "verworfen-")
		if !ok || !e.IsDir() || e.Name() == newest {
			continue
		}
		it := item{name: e.Name(), stamp: rest, n: 1}
		if len(rest) > len("20060102-150405") && rest[len("20060102-150405")] == '-' {
			if n, err := strconv.Atoi(rest[len("20060102-150405")+1:]); err == nil {
				it.stamp, it.n = rest[:len("20060102-150405")], n
			}
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b item) int {
		return cmp.Or(strings.Compare(a.stamp, b.stamp), cmp.Compare(a.n, b.n))
	})
	if newest != "" {
		items = append(items, item{name: newest})
	}
	for len(items) > keep {
		p := filepath.Join(home, items[0].name)
		if err := os.RemoveAll(p); err != nil {
			log.Printf("host: alten verworfenen Stand %s nicht gelöscht: %v", p, err)
		} else {
			log.Printf("host: alten verworfenen Stand %s gelöscht (es bleiben die %d neuesten)", p, keep)
		}
		items = items[1:]
	}
}

// worldPresent: Im Weltordner liegt eine level.dat.
func worldPresent(world string) bool {
	info, err := os.Stat(filepath.Join(world, "level.dat"))
	return err == nil && info.Mode().IsRegular()
}

// ---- Verwaister Server ----

// terminateOrphan beendet den Server aus der Markierung, falls er noch
// läuft: erst "stop" über RCON (mit dem Passwort aus server.properties),
// nach orphanGrace hart. Jeder andere Prozess bleibt unberührt: Die PID
// muss laufen, ihr Programm muss das aus der Markierung sein und, falls
// bekannt, ihre Startzeit passen (sonst wurde die PID neu vergeben). Ein
// Fehler heißt: Der Server läuft vielleicht noch, die Welt nicht anfassen.
//
// clean: Der Server lief noch und hat sich nach "stop" innerhalb von
// orphanGrace von selbst und ohne Fehler (Exit-Code 0, wie bei Stop in
// mcserver) beendet – er hat dabei alles gespeichert. Lief er nicht mehr,
// musste er hart beendet werden oder endete er mit Fehler, ist clean false.
func terminateOrphan(dir string, m marker) (clean bool, err error) {
	p, err := openVerified(procRef{PID: m.PID, Exe: cmp.Or(m.Exe, m.Java), Created: m.Created}, "Server", m.Written)
	if err != nil {
		return false, err
	}
	if p == nil {
		return false, nil
	}
	defer p.close()

	log.Printf("host: verwaister Minecraft-Server läuft noch (PID %d), wird beendet", m.PID)
	if err := stopViaRCON(dir); err != nil {
		log.Printf("host: stop über RCON nicht möglich: %v", err)
	} else if p.wait(orphanGrace) {
		if code, err := p.exitCode(); err != nil || code != 0 {
			log.Printf("host: verwaister Server hat sich nach stop beendet, aber nicht sauber (Exit-Code %d, %v)", code, err)
			return false, nil
		}
		log.Printf("host: verwaister Server hat sich nach stop von selbst beendet, sein Stand ist gespeichert")
		return true, nil
	} else {
		log.Printf("host: verwaister Server reagiert nicht auf stop")
	}
	if err := p.kill(); err != nil && p.running() {
		return false, fmt.Errorf("verwaisten Minecraft-Server (PID %d) beenden: %w", m.PID, err)
	}
	if !p.wait(orphanKillWait) {
		return false, fmt.Errorf("verwaister Minecraft-Server (PID %d) endet nicht", m.PID)
	}
	log.Printf("host: verwaister Server hart beendet")
	return false, nil
}

// terminateTunnel beendet den tunnel aus der Markierung, falls er noch
// läuft und Programm und Startzeit passen (sonst bleibt der Prozess
// unberührt). tunnel hält keinen Zustand und wird sofort beendet.
func terminateTunnel(ref procRef, written time.Time) error {
	p, err := openVerified(ref, "tunnel", written)
	if err != nil {
		return err
	}
	if p == nil {
		return nil
	}
	defer p.close()
	log.Printf("host: verwaister tunnel läuft noch (PID %d), wird beendet", ref.PID)
	if err := p.kill(); err != nil && p.running() {
		return fmt.Errorf("verwaisten tunnel (PID %d) beenden: %w", ref.PID, err)
	}
	if !p.wait(orphanKillWait) {
		return fmt.Errorf("verwaister tunnel (PID %d) endet nicht", ref.PID)
	}
	return nil
}

// openVerified öffnet den Prozess ref.PID, wenn er läuft und genau der aus
// der Markierung ist: gleiches Programm und, falls bekannt, gleiche
// Startzeit (sonst wurde die PID neu vergeben). Sonst nil – der Prozess
// wird dann nie angefasst. Solange der Handle offen ist, kann Windows die
// PID nicht neu vergeben; Prüfen, Warten und Beenden treffen denselben.
//
// Lässt sich die PID nicht öffnen, obwohl es sie gibt (etwa Zugriff
// verweigert), ist der Fehler ErrMaybeRunning (fail closed) – außer die
// Markierung (written) ist älter als der letzte Start des PCs: Dann läuft
// nichts mehr aus ihr.
func openVerified(ref procRef, what string, written time.Time) (*procHandle, error) {
	if ref.PID <= 0 || ref.Exe == "" {
		return nil, nil
	}
	p, err := openProc(ref.PID)
	if errors.Is(err, errNoProcess) {
		return nil, nil
	}
	if err != nil {
		if boot := bootTime(); !boot.IsZero() && !written.IsZero() && written.Before(boot) {
			log.Printf("host: PID %d (%s) nicht prüfbar (%v), die Markierung stammt aber von vor dem Neustart des PCs", ref.PID, what, err)
			return nil, nil
		}
		log.Printf("host: PID %d (%s aus der Markierung) lässt sich nicht prüfen: %v", ref.PID, what, err)
		return nil, fmt.Errorf("%w (%s, PID %d: %v)", ErrMaybeRunning, what, ref.PID, err)
	}
	exe, err := p.exe()
	switch {
	case !p.running():
	case err != nil || !samePath(exe, ref.Exe):
		log.Printf("host: PID %d ist nicht der %s aus der Markierung (%q), bleibt unberührt", ref.PID, what, exe)
	case ref.Created != 0 && !sameCreated(p, ref.Created):
		log.Printf("host: PID %d wurde neu vergeben, bleibt unberührt", ref.PID)
	default:
		return p, nil
	}
	p.close()
	return nil, nil
}

func sameCreated(p *procHandle, want int64) bool {
	created, err := p.created()
	return err == nil && created == want
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// stopViaRCON schickt "stop" an den Server, der zu dir/server.properties
// gehört.
func stopViaRCON(dir string) error {
	props, err := readProperties(filepath.Join(dir, "server.properties"))
	if err != nil {
		return err
	}
	pw := props["rcon.password"]
	if !strings.EqualFold(props["enable-rcon"], "true") || pw == "" {
		return errors.New("RCON ist nicht eingerichtet")
	}
	addr := net.JoinHostPort("127.0.0.1", cmp.Or(props["rcon.port"], "25575"))
	conn, err := rcon.Dial(addr, pw, rcon.SetDialTimeout(rconTimeout), rcon.SetDeadline(rconTimeout))
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Execute("stop")
	return err
}

// readProperties liest die einfachen Schlüssel=Wert-Zeilen einer
// server.properties.
func readProperties(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			props[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return props, nil
}

// ---- Nachreichen ----

// uploadPending reicht den vorgemerkten Stand nach, wenn seine Basis noch
// der Head ist; adopted: die Basis wurde schon einmal übernommen.
func uploadPending(ctx context.Context, c *client.Client, st *group.State, dir string, hc *worldsync.HashCache, adopted bool) error {
	world := filepath.Join(dir, WorldName)
	baseWorld, baseGen := st.LastUploadedWorldID, st.LastUploadedGeneration
	resp, err := c.Play(ctx, api.PlayRequest{
		MachineID:      st.MachineID,
		UploadOnly:     true,
		BaseWorldID:    baseWorld,
		BaseGeneration: baseGen,
	})
	if err != nil {
		return fmt.Errorf("Stand nachreichen: %w", err)
	}
	if resp.Role != api.RoleHost {
		if resp.WorldID == baseWorld && resp.HeadGeneration == baseGen {
			return fmt.Errorf("%w: %s", ErrPendingLater, resp.Reason)
		}
		// Ist der Head der eigene Commit direkt auf der Basis (beim Fencing
		// oder Stop noch unterwegs, oder die Antwort ging verloren), baut der
		// lokale Stand auf ihm auf: übernehmen und auf ihm nachreichen.
		if !adopted {
			head, err := c.Head(ctx)
			if err != nil {
				return fmt.Errorf("Stand nachreichen: %w", err)
			}
			if ownCommitOn(head, st.MachineID, st) {
				log.Printf("host: Head %s/%d ist der eigene, noch nicht vermerkte Commit – Basis übernommen", head.WorldID, head.Number)
				st.LastUploadedWorldID, st.LastUploadedGeneration = head.WorldID, head.Number
				if err := st.Save(); err != nil {
					return err
				}
				return uploadPending(ctx, c, st, dir, hc, true)
			}
		}
		return discardPending(st, world, fmt.Sprintf("seitdem wurde weitergespielt (Head %s/%d, der Stand baut auf %s/%d auf)",
			resp.WorldID, resp.HeadGeneration, baseWorld, baseGen))
	}

	gen, err := commitPending(ctx, c, resp.LeaseID, st, dir, hc)
	releaseLease(ctx, c, resp.LeaseID, st.MachineID)
	if err != nil {
		return fmt.Errorf("Stand nachreichen: %w", err)
	}
	st.LastUploadedWorldID, st.LastUploadedGeneration = gen.WorldID, gen.Number
	st.PendingUpload = false
	if err := st.Save(); err != nil {
		return err
	}
	log.Printf("host: nachgereichter Stand ist %s/%d", gen.WorldID, gen.Number)
	if f := recoveredHook.Load(); f != nil {
		(*f)(Committed{
			Dir: filepath.Join(dir, StagingName), WorldID: gen.WorldID, Generation: gen.Number,
			MinecraftVersion: gen.MinecraftVersion, At: time.Now(),
		})
	}
	return nil
}

// recoveredHook: siehe SetRecoveredHook.
var recoveredHook atomic.Pointer[func(Committed)]

// SetRecoveredHook setzt den Haken, der nach einem nachgereichten Stand (aus
// Recover) läuft, mit dem Bereitstellungsordner dieses Stands; nil entfernt
// ihn. Der Haken läuft in der Goroutine von Recover, nachdem dieses den Platz
// (HoldSync) freigegeben hat; Recover läuft nur, solange keine Sitzung die
// Welt hält. Er muss schnell zurückkehren (Deps.OnCommitted).
func SetRecoveredHook(f func(Committed)) {
	if f == nil {
		recoveredHook.Store(nil)
		return
	}
	recoveredHook.Store(&f)
}

// commitPending stellt die Welt bereit und reicht sie mit dem Zuschlag
// lease ein; Lebenszeichen halten den Zuschlag so lange.
func commitPending(ctx context.Context, c *client.Client, lease int64, st *group.State, dir string, hc *worldsync.HashCache) (*api.Generation, error) {
	ctx, lost := context.WithCancelCause(ctx)
	defer lost(nil)
	stop := keepAlive(ctx, c, lease, st.MachineID, lost)
	defer stop()

	head, err := c.Head(ctx)
	if err != nil {
		return nil, err
	}
	if head == nil || head.WorldID != st.LastUploadedWorldID || head.Number != st.LastUploadedGeneration {
		return nil, errors.New("der Head ist nicht mehr die Basis des Stands")
	}
	// Nie gleichzeitig mit Download (syncSlot): Recover läuft vor jeder
	// Sitzung, der Platz sichert es trotzdem.
	unlock, err := holdSync(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Ein Cache für Bereitstellen und Manifest: Was die Sitzung schon
	// gehasht und zerlegt hat, wird übernommen.
	if hc == nil {
		hc = worldsync.LoadHashCache()
	}
	staging := filepath.Join(dir, StagingName)
	if err := worldsync.StageCachedContext(ctx, filepath.Join(dir, WorldName), staging, hc); err != nil {
		return nil, err
	}
	files, err := worldsync.BuildManifest(staging, hc)
	if err != nil {
		return nil, err
	}
	gen, err := worldsync.UploadAndCommit(ctx, c, staging, api.CommitRequest{
		LeaseID:        lease,
		MachineID:      st.MachineID,
		BaseWorldID:    head.WorldID,
		BaseGeneration: head.Number,
		// Gehostet wurde die Version der Basis (der Host hostet nur die
		// Manifest-Version, und die Basis stammt aus derselben Sitzung).
		MinecraftVersion: head.MinecraftVersion,
		Files:            files,
	})
	if err != nil {
		if cause := context.Cause(ctx); ctx.Err() != nil && cause != nil {
			return nil, cause
		}
		return nil, err
	}
	return gen, nil
}

// keepAlive schickt alle 10 s ein Lebenszeichen (ohne Spieler), bis stop
// aufgerufen wird. Lehnt der Koordinator ab, bricht lost die Arbeit ab.
func keepAlive(ctx context.Context, c *client.Client, lease int64, machine string, lost context.CancelCauseFunc) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(HeartbeatInterval)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			hctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
			_, err := c.Heartbeat(hctx, api.HeartbeatRequest{LeaseID: lease, MachineID: machine, Players: []string{}})
			cancel()
			if errors.Is(err, client.ErrLeaseLost) {
				lost(fmt.Errorf("Zuschlag zum Nachreichen verloren: %w", err))
				return
			}
			if err != nil {
				log.Printf("host: Lebenszeichen beim Nachreichen: %v", err)
			}
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

// ---- Dateien ----

// writeFileAtomic schreibt data über eine Temp-Datei neben path.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = renameRetry(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// renameRetry wiederholt das Umbenennen kurz: Unter Windows halten
// Virenscanner oder Indexdienst eine Datei manchmal einen Moment offen.
func renameRetry(from, to string) error {
	var err error
	for i := range 10 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}
