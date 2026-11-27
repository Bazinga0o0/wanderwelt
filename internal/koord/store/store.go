// Package store ist der Speicher für Stände auf dem Koordinator.
//
// Jede Datei einer Welt liegt inhaltsadressiert als Blob unter
// blobs/ab/<sha256>. Eine zerlegte Datei (Regionsdatei) hat stattdessen ein
// Rezept (api.EncodeRecipe), selbst ein Blob, das ihre Segment-Blobs listet;
// einen Blob der ganzen Datei gibt es dann nicht. Ein angenommener Stand ist
// eine Generation (Manifest aus Pfad, Größe, Hash und gegebenenfalls
// Rezept) unter worlds/<weltID>/generations/<n>.json.
// Welche Welt aktiv ist und welche Generation ihr Head ist, steht in
// current.json; alle anderen Welten sind Archive. Angefangene Uploads liegen
// in tmp/.
//
// Veröffentlicht wird immer über Temp-Datei und Rename: erst die
// Generationsdatei, danach current.json. Ein Absturz dazwischen hinterlässt
// höchstens eine Generationsdatei über dem Head; die zeigt Generation nie
// an, und Open räumt sie weg. Ein Blob wird erst nach geprüftem Hash
// sichtbar.
//
// Zurückholen (Restore) legt den Inhalt eines älteren Stands als neue
// Generation an, auch aus einem Archiv, das dabei wieder die aktive Welt
// wird; gelöscht wird dabei nichts. Für die Stände-Liste (Worlds) behält der
// Speicher die Angaben jeder einmal gelesenen Generation im Speicher.
//
// Ein Store darf gleichzeitig benutzt werden, aber nur von einem Prozess.
package store

import (
	"bufio"
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Aufbewahrung der aktiven Welt (Prune).
const (
	KeepLast  = 24 // so viele letzte Generationen bleiben immer
	KeepHours = 48 // für so viele UTC-Stunden bleibt die letzte Generation der Stunde
	KeepDays  = 30 // für so viele UTC-Tage bleibt die letzte Generation des Tages
)

// partMaxAge: Nach dieser Zeit ohne neuen Teil räumt Prune einen
// angefangenen Upload weg.
const partMaxAge = 24 * time.Hour

// tmpPrefix kennzeichnet Temp-Dateien neben current.json und den
// Generationsdateien.
const tmpPrefix = ".tmp-"

var (
	// ErrConflict: Die Basis passt nicht zum Head, oder derselbe Blob wird
	// gerade schon hochgeladen.
	ErrConflict = errors.New("Konflikt mit dem aktuellen Stand")
	// ErrInvalid: ungültige Anfrage – Pfad, Hash, Größe, fehlender Blob,
	// Teil außer der Reihe oder Inhalt passt nicht zum Hash.
	ErrInvalid = errors.New("ungültige Anfrage")
	// ErrNotFound: Blob, Generation oder Welt gibt es nicht.
	ErrNotFound = errors.New("nicht vorhanden")
	// ErrMissingBlob: Commit nennt einen Blob, den es (noch) nicht gibt. Ist
	// zugleich ErrInvalid; anders als bei einem kaputten Manifest hilft hier
	// Nachladen und erneut Einreichen.
	ErrMissingBlob = fmt.Errorf("%w: Blob fehlt", ErrInvalid)
	// ErrTooLarge: Die Anfrage enthält mehr, als erlaubt ist (PutBlobs:
	// mehr Rahmen als maxFrames).
	ErrTooLarge = errors.New("Anfrage zu groß")
)

// current ist der Inhalt von current.json (Format 2): die Weltliste der
// Gruppe (welten.go). Format 1 (eine Welt: world_id und head) migriert
// format.go.
type current struct {
	Next   int          `json:"next"` // Zähler für neue Welt-Kennungen w<N>
	Worlds []WorldEntry `json:"worlds"`
}

// Store ist der Speicher für Blobs und Generationen.
type Store struct {
	dir    string
	budget int64

	mu  sync.Mutex
	cur current // Weltliste; ohne Einträge: noch keine Welt
	// heads: Generation Head je Welt der Liste (Schlüssel: Welt-Kennung),
	// fehlt bei einer leeren Welt.
	heads map[string]*api.Generation
	// fresh: Blobs, die seit dem letzten Commit hochgeladen oder über
	// Missing als vorhanden gemeldet wurden. Der Aufräumlauf lässt sie
	// stehen, auch wenn noch keine Generation sie nennt – sonst könnte er
	// einem langen Upload die Blobs unter den Füßen wegziehen.
	fresh map[string]struct{}
	// uploading: Blobs, deren Teile gerade geschrieben werden.
	uploading map[string]bool
	// meta: Angaben zu jeder schon gelesenen oder angelegten Generation
	// (Generationen ändern sich nie; Prune entfernt weggeräumte).
	meta map[genKey]GenMeta
	// pinned: Generationen, die Prune zusätzlich immer behält (Pin).
	pinned map[genKey]bool
	// used, over: Belegung und Über-Budget aus dem letzten Prune (Usage).
	used int64
	over bool
	// usageKnown: used stammt aus einem Prune oder RefreshUsage.
	usageKnown bool
}

// genKey bezeichnet eine Generation.
type genKey struct {
	worldID string
	n       int64
}

// GenMeta sind die Angaben zu einer Generation für die Stände-Liste.
type GenMeta struct {
	WorldID          string
	Number           int64
	Created          time.Time
	HostEmail        string
	Players          []string
	Size             int64 // Summe der Dateigrößen
	MinecraftVersion string
	RestoredFrom     *api.GenRef
}

// WorldMeta ist eine Weltkennung mit den Angaben zu ihren Generationen.
// Active: sie ist die aktuelle Weltkennung einer Welt der Liste (GroupWorld,
// GroupName), sonst ein Archiv (GroupWorld nennt dann die Welt, zu der das
// Archiv gehört, sofern bekannt).
type WorldMeta struct {
	WorldID    string
	Active     bool
	GroupWorld string
	GroupName  string
	Gens       []GenMeta // neueste zuerst
}

// Open öffnet den Speicher in dir und legt die Ordner bei Bedarf an. budget
// ist das Platzbudget in Bytes für alle Blobs, die Generationen nennen.
func Open(dir string, budget int64) (*Store, error) {
	if budget <= 0 {
		return nil, fmt.Errorf("store: Budget muss positiv sein, ist %d", budget)
	}
	// Zuerst die Format-Version: Ein zu neuer Speicher wird nicht angefasst.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := migrate(dir); err != nil {
		return nil, err
	}
	for _, sub := range []string{"blobs", "tmp", "worlds"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	s := &Store{
		dir:       dir,
		budget:    budget,
		fresh:     map[string]struct{}{},
		uploading: map[string]bool{},
		meta:      map[genKey]GenMeta{},
		heads:     map[string]*api.Generation{},
	}

	data, err := os.ReadFile(filepath.Join(dir, "current.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	var cur current
	if err := json.Unmarshal(data, &cur); err != nil {
		return nil, fmt.Errorf("store: current.json: %w", err)
	}
	if err := checkCurrent(cur); err != nil {
		return nil, fmt.Errorf("store: current.json ungültig: %w", err)
	}
	for _, e := range cur.Worlds {
		if e.StoreWorld == "" {
			continue
		}
		if e.Deleted.IsZero() {
			g, err := s.readGeneration(e.StoreWorld, e.Head)
			if err != nil {
				return nil, fmt.Errorf("store: Head der Welt %s aus current.json: %w", e.ID, err)
			}
			s.heads[e.ID] = g
		}
		// Generationen über dem Head wurden nie veröffentlicht (Absturz
		// zwischen Generationsdatei und current.json).
		nums, _, err := s.scanGenerations(e.StoreWorld)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		for _, n := range nums {
			if n > e.Head {
				if err := os.Remove(s.genPath(e.StoreWorld, n)); err != nil {
					return nil, fmt.Errorf("store: halbe Generation entfernen: %w", err)
				}
			}
		}
	}
	s.cur = cur
	return s, nil
}

// Missing liefert die Hashes aus hashes, für die es keinen Blob gibt
// (ungültige Hashes eingeschlossen). Vorhandene merkt sich der Speicher bis
// zum nächsten Commit als gebraucht.
func (s *Store) Missing(hashes []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	missing := []string{}
	for _, h := range hashes {
		if validSHA(h) && isFile(s.blobPath(h)) {
			s.fresh[h] = struct{}{}
			continue
		}
		missing = append(missing, h)
	}
	return missing
}

// PutBlobPart hängt einen Teil des Blobs sha an, der bei Byte start beginnt;
// total ist die Gesamtgröße. Teile kommen der Reihe nach: start 0 fängt neu
// an, jeder weitere Teil muss genau dort beginnen, wo der vorige endete. Ist
// total erreicht, wird der Hash geprüft und der Blob atomar abgelegt
// (complete == true). Passt der Hash nicht, ist der Teil weg und der Fehler
// ErrInvalid. Gibt es den Blob schon, ist complete sofort true.
func (s *Store) PutBlobPart(sha string, start, total int64, r io.Reader) (complete bool, err error) {
	if !validSHA(sha) {
		return false, fmt.Errorf("%w: Hash %q", ErrInvalid, sha)
	}
	if start < 0 || total < 0 || start > total {
		return false, fmt.Errorf("%w: Teil ab %d bei Gesamtgröße %d", ErrInvalid, start, total)
	}

	s.mu.Lock()
	if isFile(s.blobPath(sha)) {
		s.fresh[sha] = struct{}{}
		s.mu.Unlock()
		return true, nil
	}
	if s.uploading[sha] {
		s.mu.Unlock()
		return false, fmt.Errorf("%w: Blob %s wird gerade schon hochgeladen", ErrConflict, sha)
	}
	s.uploading[sha] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.uploading, sha)
		s.mu.Unlock()
	}()

	part := s.partPath(sha)
	f, err := openPart(part, start)
	if err != nil {
		return false, err
	}
	// Ein Byte über total hinaus lesen, um zu viel gesendete Daten zu erkennen.
	n, err := io.Copy(f, io.LimitReader(r, total-start+1))
	size := start + n
	if err == nil && size == total {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// Der Teil bleibt liegen; der Starter fängt mit start 0 neu an.
		return false, fmt.Errorf("store: Teil von %s schreiben: %w", sha, err)
	}
	if size > total {
		os.Remove(part)
		return false, fmt.Errorf("%w: mehr als %d Bytes für Blob %s", ErrInvalid, total, sha)
	}
	if size < total {
		return false, nil
	}
	if err := verifyHash(part, sha); err != nil {
		os.Remove(part)
		return false, err
	}

	// Unter der Sperre, damit kein Aufräumlauf zwischen Ablegen und Merken
	// den frischen Blob für unreferenziert hält.
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := s.blobPath(sha)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	if err := os.Rename(part, dst); err != nil {
		return false, fmt.Errorf("store: Blob %s ablegen: %w", sha, err)
	}
	syncDir(filepath.Dir(dst))
	s.fresh[sha] = struct{}{}
	return true, nil
}

// OpenBlob öffnet den Blob sha zum Lesen und liefert seine Größe.
func (s *Store) OpenBlob(sha string) (io.ReadSeekCloser, int64, error) {
	if !validSHA(sha) {
		return nil, 0, fmt.Errorf("%w: Hash %q", ErrInvalid, sha)
	}
	f, err := os.Open(s.blobPath(sha))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, fmt.Errorf("%w: Blob %s", ErrNotFound, sha)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("store: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("store: %w", err)
	}
	return f, info.Size(), nil
}

// Head liefert die Weltkennung und die neueste Generation der ersten Welt
// der Liste (so liest ein älterer Starter ohne Weltauswahl); ohne Welt ist
// gen nil. HeadOf nimmt die Welt.
func (s *Store) Head() (worldID string, gen *api.Generation, err error) {
	return s.HeadGenOf("")
}

// HeadGenOf liefert Weltkennung und neueste Generation der Welt world ("":
// die erste); ohne Stand (leere oder fehlende Welt) ist gen nil.
func (s *Store) HeadGenOf(world string) (worldID string, gen *api.Generation, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.live(world)
	if e == nil || s.heads[e.ID] == nil {
		return "", nil, nil
	}
	return e.StoreWorld, cloneGen(s.heads[e.ID]), nil
}

// HeadRef liefert Weltkennung und Head-Nummer der ersten Welt ("" und 0
// ohne Stand), ohne die Generation zu kopieren.
func (s *Store) HeadRef() (worldID string, n int64) {
	return s.HeadRefOf("")
}

// HeadRefOf ist HeadRef für die Welt world ("": die erste).
func (s *Store) HeadRefOf(world string) (worldID string, n int64) {
	worldID, n, _ = s.HeadInfoOf(world)
	return worldID, n
}

// HeadInfo ist HeadRef samt Zeit des Heads (Nullzeit ohne Welt).
func (s *Store) HeadInfo() (worldID string, n int64, created time.Time) {
	return s.HeadInfoOf("")
}

// HeadInfoOf ist HeadInfo für die Welt world ("": die erste).
func (s *Store) HeadInfoOf(world string) (worldID string, n int64, created time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.live(world)
	if e == nil || e.StoreWorld == "" {
		return "", 0, time.Time{}
	}
	if g := s.heads[e.ID]; g != nil {
		created = g.Created
	}
	return e.StoreWorld, e.Head, created
}

// Pin legt die Generationen fest, die Prune zusätzlich immer behält, auch
// über dem Budget – das Ziel eines ausstehenden Zurückholens. Jeder Aufruf
// ersetzt die bisherigen; ohne Argument behält Prune nichts zusätzlich. Nach
// der Rückkehr von Pin läuft kein Prune mehr, das sie nicht kennt.
func (s *Store) Pin(refs ...api.GenRef) {
	pinned := make(map[genKey]bool, len(refs))
	for _, r := range refs {
		pinned[genKey{r.WorldID, r.Number}] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinned = pinned
}

// Usage meldet aus dem letzten Prune, wie viele Bytes die Blobs der
// behaltenen Generationen belegen (jeder Blob einmal gezählt) und ob das
// trotz Aufräumen über dem Budget lag. Vor dem ersten Prune oder RefreshUsage 0 und false.
func (s *Store) Usage() (used int64, overBudget bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.over
}

// SetBudget setzt das Platzbudget für die folgenden Aufräumläufe (Prune,
// RefreshUsage). Über-Budget (Usage) gilt bis dahin vorläufig aus der
// zuletzt ermittelten Belegung.
func (s *Store) SetBudget(budget int64) error {
	if budget <= 0 {
		return fmt.Errorf("store: Budget muss positiv sein, ist %d", budget)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.budget = budget
	s.over = s.used > budget
	return nil
}

// UsageKnown meldet, ob die Belegung (Usage) schon ermittelt ist – durch
// Prune oder RefreshUsage.
func (s *Store) UsageKnown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usageKnown
}

// Budget liefert das Platzbudget.
func (s *Store) Budget() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.budget
}

// BlobSizes liefert die Größen der Blobs hashes in derselben Reihenfolge (-1
// für fehlende) und die fehlenden Hashes, jeden einmal. Ein ungültiger Hash
// ist ErrInvalid.
func (s *Store) BlobSizes(hashes []string) (sizes []int64, missing []string, err error) {
	sizes = make([]int64, len(hashes))
	seen := map[string]bool{}
	for i, h := range hashes {
		if !validSHA(h) {
			return nil, nil, fmt.Errorf("%w: Hash %q", ErrInvalid, h)
		}
		info, err := os.Stat(s.blobPath(h))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			sizes[i] = -1
			if !seen[h] {
				seen[h] = true
				missing = append(missing, h)
			}
		case err != nil:
			return nil, nil, fmt.Errorf("store: %w", err)
		default:
			sizes[i] = info.Size()
		}
	}
	return sizes, missing, nil
}

// PutBlobs legt die Blobs aus r ab, Rahmen für Rahmen: 32 Byte SHA-256
// (roh), uint64 big-endian Größe, Daten (api.BatchFrameHeader). Jeder Rahmen
// wird gegen seinen Hash geprüft und atomar abgelegt, sobald er vollständig
// ist; Rahmen vor einem Fehler bleiben. Abgeschnittene Rahmen, Größen über
// math.MaxInt64 und falsche Hashes sind ErrInvalid; mehr als maxFrames
// Rahmen sind ErrTooLarge (die ersten maxFrames bleiben). stored zählt die
// angenommenen Rahmen (auch Blobs, die es schon gab).
func (s *Store) PutBlobs(r io.Reader, maxFrames int) (stored int, err error) {
	br := bufio.NewReaderSize(r, 64<<10)
	dirs := map[string]bool{}
	defer func() {
		// Einmal je Präfixordner statt nach jedem Umbenennen.
		for d := range dirs {
			syncDir(d)
		}
	}()
	var head [api.BatchFrameHeader]byte
	for {
		if _, err := io.ReadFull(br, head[:]); err != nil {
			if err == io.EOF {
				return stored, nil
			}
			if err == io.ErrUnexpectedEOF {
				return stored, fmt.Errorf("%w: Rahmenkopf abgeschnitten", ErrInvalid)
			}
			return stored, err
		}
		if stored == maxFrames {
			return stored, fmt.Errorf("%w: mehr als %d Rahmen", ErrTooLarge, maxFrames)
		}
		sha := hex.EncodeToString(head[:32])
		size := binary.BigEndian.Uint64(head[32:])
		if size > math.MaxInt64 {
			return stored, fmt.Errorf("%w: Rahmen %s mit Größe %d", ErrInvalid, sha, size)
		}
		dir, err := s.putFrame(sha, int64(size), br)
		if err != nil {
			return stored, err
		}
		if dir != "" {
			dirs[dir] = true
		}
		stored++
	}
}

// putFrame schreibt size Bytes aus r in eine Temp-Datei, prüft den Hash und
// legt den Blob sha ab. dir ist der Präfixordner, wenn ein neuer Blob
// entstand.
func (s *Store) putFrame(sha string, size int64, r io.Reader) (dir string, err error) {
	// Gibt es den Blob schon, nur den Rahmen prüfen, ohne Temp-Datei und
	// Sync; der Blob gilt bis zum nächsten Commit als gebraucht.
	s.mu.Lock()
	exists := isFile(s.blobPath(sha))
	if exists {
		s.fresh[sha] = struct{}{}
	}
	s.mu.Unlock()
	if exists {
		return "", checkFrame(sha, size, r, io.Discard)
	}

	f, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "batch-*.tmp")
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := checkFrame(sha, size, r, f); err != nil {
		return "", err
	}
	// Daten vor dem Umbenennen dauerhaft: Ein umbenannter, aber leerer Blob
	// nach einem Absturz sähe für Missing vorhanden aus.
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dst := s.blobPath(sha)
	s.fresh[sha] = struct{}{}
	if isFile(dst) {
		os.Remove(tmp)
		return "", nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", fmt.Errorf("store: Blob %s ablegen: %w", sha, err)
	}
	return filepath.Dir(dst), nil
}

// checkFrame kopiert die size Bytes eines Rahmens aus r nach w und prüft
// Länge und Hash.
func checkFrame(sha string, size int64, r io.Reader, w io.Writer) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(r, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("%w: Rahmen %s abgeschnitten (%d von %d Bytes)", ErrInvalid, sha, n, size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("%w: Rahmen %s hat den Hash %s", ErrInvalid, sha, got)
	}
	return nil
}

// Commit nimmt einen Stand an und macht ihn zum neuen Head.
//
// Geprüft wird: Jeder Pfad ist gültig (api.ValidPath) und kommt nur einmal
// vor, auch ohne Rücksicht auf Groß/Klein; kein Pfad ist zugleich Ordner
// eines anderen; jeder Hash ist ein SHA-256 in Kleinbuchstaben; jeder Blob
// einer Datei ohne Rezept ist so groß wie angegeben. Jedes Rezept, das nicht
// schon mit derselben Größe im Head steht, wird gelesen: höchstens
// api.MaxRecipeSize Bytes, gültiges Format, Segmentsumme genau die Größe der
// Datei, jedes Segment vorhanden und so groß wie angegeben. Sonst ErrInvalid;
// fehlt ein Blob (Rezept oder Segment), ist der Fehler ErrMissingBlob (auch
// ErrInvalid).
//
// Ohne NewWorld muss der Stand auf dem Head der aktiven Welt aufbauen:
// BaseWorldID ist die aktive Welt und BaseGeneration ihr Head, solange es
// keine Welt gibt "" und 0 – sonst ErrConflict. Die Welt zählt mit, weil
// Generationsnummern in jeder Welt bei 1 beginnen. Mit NewWorld zählt die
// Basis nicht: Der Stand wird Generation 1 einer neuen Welt, die bisherige
// bleibt als Archiv. Ohne jede Welt legt auch ein Commit mit leerer Basis
// die erste Welt an.
//
// Mehrere Welten: Commit gilt für die erste Welt der Liste (wie vor der
// Weltliste), CommitTo für die Welt world. „Basis“ und „aktive Welt“ sind
// dann Weltkennung und Head dieser Welt; NewWorld ersetzt die Weltkennung
// dieser Welt (die bisherige bleibt Archiv der Welt).
func (s *Store) Commit(req api.CommitRequest, host api.HostInfo, now time.Time) (*api.Generation, error) {
	return s.CommitTo("", req, host, now)
}

// CommitTo nimmt einen Stand für die Welt world ("": die erste) an, siehe
// Commit. Eine Welt, die es nicht gibt, ist ErrNotFound (nur ohne jede Welt
// legt ein Commit an die erste Welt „Welt“ neu an).
func (s *Store) CommitTo(world string, req api.CommitRequest, host api.HostInfo, now time.Time) (*api.Generation, error) {
	files, err := checkFiles(req.Files)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	e := s.live(world)
	if e == nil && (world != "" || len(s.liveEntries()) > 0) {
		return nil, fmt.Errorf("%w: Welt %q", ErrNotFound, world)
	}
	var curWorld string
	var curHead int64
	if e != nil {
		curWorld, curHead = e.StoreWorld, e.Head
	}
	if !req.NewWorld && (req.BaseWorldID != curWorld || req.BaseGeneration != curHead) {
		return nil, fmt.Errorf("%w: Basis %q/%d, Head ist %q/%d",
			ErrConflict, req.BaseWorldID, req.BaseGeneration, curWorld, curHead)
	}
	// Rezepte des Heads sind geprüft, und Prune hält ihre Segmente, solange
	// der Head steht.
	known := map[string]int64{} // Rezept → Größe der Datei
	if e != nil && s.heads[e.ID] != nil {
		for _, f := range s.heads[e.ID].Files {
			if f.Recipe != "" {
				known[f.Recipe] = f.Size
			}
		}
	}
	sizes := map[string]int64{} // schon geprüfte Blobs → Größe
	for _, f := range files {
		if f.Recipe == "" {
			if err := s.checkBlob(sizes, f.SHA256, f.Size, f.Path); err != nil {
				return nil, err
			}
			continue
		}
		if size, ok := known[f.Recipe]; ok && size == f.Size {
			continue
		}
		if err := s.checkRecipe(sizes, f); err != nil {
			return nil, err
		}
		known[f.Recipe] = f.Size
	}

	g := &api.Generation{
		Number:           curHead + 1,
		Parent:           curHead,
		WorldID:          curWorld,
		Created:          now.UTC(),
		HostEmail:        host.Email,
		MachineID:        cmp.Or(host.MachineID, req.MachineID),
		LeaseID:          req.LeaseID,
		MinecraftVersion: req.MinecraftVersion,
		Files:            files,
		Players:          slices.Clone(req.Players),
	}
	next := s.cloneCur()
	if e == nil {
		// Ohne jede Welt: die erste Welt der Gruppe.
		ne := newEntry(&next, DefaultWorldName, "", "", now)
		e = &ne
		next.Worlds = append(next.Worlds, ne)
	}
	if req.NewWorld || curWorld == "" {
		id, err := s.createWorld(now)
		if err != nil {
			return nil, err
		}
		g.WorldID, g.Number, g.Parent = id, 1, 0
		next.entry(e.ID).rebind(id, 1)
		if err := s.publish(g, next); err != nil {
			os.RemoveAll(s.worldDir(id))
			return nil, err
		}
	} else {
		next.entry(e.ID).Head = g.Number
		if err := s.publish(g, next); err != nil {
			return nil, err
		}
	}

	// Alles, was dieser Stand braucht, ist jetzt referenziert.
	clear(s.fresh)
	return cloneGen(g), nil
}

// checkBlob prüft, dass der Blob sha existiert (sonst ErrMissingBlob) und
// size Bytes hat (sonst ErrInvalid); sizes merkt sich geprüfte Blobs.
func (s *Store) checkBlob(sizes map[string]int64, sha string, size int64, path string) error {
	have, ok := sizes[sha]
	if !ok {
		info, err := os.Stat(s.blobPath(sha))
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s für %s", ErrMissingBlob, sha, path)
		}
		if err != nil {
			return fmt.Errorf("store: %w", err)
		}
		have = info.Size()
		sizes[sha] = have
	}
	if have != size {
		return fmt.Errorf("%w: %s: Blob %s soll %d Bytes haben, hat %d", ErrInvalid, path, sha, size, have)
	}
	return nil
}

// checkRecipe liest und prüft das Rezept von f und seine Segmente.
func (s *Store) checkRecipe(sizes map[string]int64, f api.FileEntry) error {
	rc, _, err := s.OpenBlob(f.Recipe)
	if errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: Rezept %s für %s", ErrMissingBlob, f.Recipe, f.Path)
	}
	if err != nil {
		return err
	}
	defer rc.Close()
	// Höchstens ein Byte mehr als ein Rezept haben darf; ParseRecipe lehnt
	// alles Längere ab.
	data, err := io.ReadAll(io.LimitReader(rc, api.MaxRecipeSize+1))
	if err != nil {
		return fmt.Errorf("store: Rezept %s: %w", f.Recipe, err)
	}
	segs, err := api.ParseRecipe(data)
	if err != nil {
		return fmt.Errorf("%w: Rezept %s für %s: %v", ErrInvalid, f.Recipe, f.Path, err)
	}
	if total, ok := api.RecipeSize(segs); !ok || total != f.Size {
		return fmt.Errorf("%w: Segmente im Rezept von %s ergeben nicht %d Bytes", ErrInvalid, f.Path, f.Size)
	}
	for _, seg := range segs {
		if err := s.checkBlob(sizes, seg.SHA256, seg.Size, f.Path); err != nil {
			return err
		}
	}
	return nil
}

// Generation liest Generation n der Welt worldID (auch aus einem Archiv).
func (s *Store) Generation(worldID string, n int64) (*api.Generation, error) {
	if !validWorldID(worldID) || n < 1 {
		return nil, fmt.Errorf("%w: Generation %q/%d", ErrInvalid, worldID, n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Über dem Head einer Welt der Liste gibt es nur nie veröffentlichte Stände.
	if e := s.entryOfStore(worldID); e != nil && e.Deleted.IsZero() && e.StoreWorld == worldID && n > e.Head {
		return nil, fmt.Errorf("%w: Generation %s/%d", ErrNotFound, worldID, n)
	}
	return s.readGeneration(worldID, n)
}

// Activate macht die Weltkennung worldID (ein Archiv) wieder zur Weltkennung
// ihrer Welt; ihr Head ist ihre neueste Generation. Die bisherige
// Weltkennung dieser Welt wird Archiv. Gehört die Weltkennung zu keiner Welt
// der Liste, ist das ErrNotFound; zu einer gelöschten Welt ErrDeleted.
func (s *Store) Activate(worldID string) error {
	if !validWorldID(worldID) {
		return fmt.Errorf("%w: Welt %q", ErrInvalid, worldID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.ownerOfStore(worldID)
	if err != nil {
		return err
	}
	if e.StoreWorld == worldID {
		return nil
	}
	g, err := s.newestGeneration(worldID)
	if err != nil {
		return err
	}
	next := s.cloneCur()
	next.entry(e.ID).rebind(worldID, g.Number)
	if err := s.setCurrent(next); err != nil {
		return err
	}
	s.heads[e.ID] = g
	return nil
}

// newestGeneration liest die neueste Generation der Welt worldID – bei
// einem Archiv dessen Head. Eine Welt ohne Generation ist ErrNotFound.
func (s *Store) newestGeneration(worldID string) (*api.Generation, error) {
	nums, _, err := s.scanGenerations(worldID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if len(nums) == 0 {
		return nil, fmt.Errorf("%w: Welt %s", ErrNotFound, worldID)
	}
	return s.readGeneration(worldID, nums[len(nums)-1])
}

// Restore holt den Stand target zurück: Sein Inhalt wird als neue
// Generation Head seiner Welt (Elternteil ist deren bisheriger Head,
// RestoredFrom nennt target, Host ist by). Liegt target in einem Archiv,
// wird diese Weltkennung dabei wieder die ihrer Welt – wie bei Activate,
// ohne Neustart –, und die bisherige wird Archiv. Gelöscht wird nichts; ein
// Zurückholen lässt sich durch ein weiteres rückgängig machen.
//
// Geschrieben wird wie bei Commit unter derselben Sperre: erst die
// Generationsdatei, dann current.json; schlägt etwas fehl, bleibt alles
// beim Alten. Ein Absturz genau dazwischen lässt bei einem Archiv dort einen
// zusätzlichen Stand (Kopie von target) liegen; die Welt bleibt dann
// unverändert.
//
// Unbekannte Welt oder Generation (auch über dem Head einer Welt) ist
// ErrNotFound, eine Welt, die ein Admin gelöscht hat, ErrDeleted, eine
// ungültige Angabe ErrInvalid. Ob die Minecraft-Version des Stands passt,
// prüft der Aufrufer.
func (s *Store) Restore(target api.GenRef, by api.HostInfo, now time.Time) (*api.Generation, error) {
	if !validWorldID(target.WorldID) || target.Number < 1 {
		return nil, fmt.Errorf("%w: Stand %q/%d", ErrInvalid, target.WorldID, target.Number)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	e, err := s.ownerOfStore(target.WorldID)
	if err != nil {
		return nil, err
	}
	var head *api.Generation
	if target.WorldID == e.StoreWorld {
		if target.Number > e.Head {
			return nil, fmt.Errorf("%w: Generation %s/%d", ErrNotFound, target.WorldID, target.Number)
		}
		head = s.heads[e.ID]
	} else {
		h, err := s.newestGeneration(target.WorldID)
		if err != nil {
			return nil, err
		}
		head = h
	}
	if head == nil {
		return nil, fmt.Errorf("%w: Welt %s", ErrNotFound, target.WorldID)
	}
	src, err := s.readGeneration(target.WorldID, target.Number)
	if err != nil {
		return nil, err
	}

	// Die Blobs von src hält Prune, solange src steht; ab jetzt nennt sie
	// auch der neue Head.
	from := target
	g := &api.Generation{
		Number:           head.Number + 1,
		Parent:           head.Number,
		WorldID:          target.WorldID,
		Created:          now.UTC(),
		HostEmail:        by.Email,
		MachineID:        by.MachineID,
		MinecraftVersion: src.MinecraftVersion,
		Files:            slices.Clone(src.Files),
		RestoredFrom:     &from,
	}
	next := s.cloneCur()
	next.entry(e.ID).rebind(target.WorldID, g.Number)
	if err := s.publish(g, next); err != nil {
		return nil, err
	}
	return cloneGen(g), nil
}

// Worlds liefert alle Weltkennungen mit den Angaben zu ihren Generationen:
// die der Welten der Liste zuerst (in der Reihenfolge der Liste), danach
// die Archive, das zuletzt bespielte (Zeit seiner neuesten Generation)
// zuerst; je Weltkennung die neueste Generation zuerst. Weltkennungen ohne
// Generation und die gelöschter Welten fehlen.
//
// Die Angaben einer Generation werden beim ersten Bedarf aus ihrer Datei
// berechnet und danach im Speicher gehalten; eine Abfrage liest sonst nur
// die Ordner.
func (s *Store) Worlds() ([]WorldMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.dir, "worlds"))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	order := map[string]int{} // Weltkennung → Platz der Welt in der Liste
	for i, e := range s.liveEntries() {
		if e.StoreWorld != "" {
			order[e.StoreWorld] = i
		}
	}
	var out []WorldMeta
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !validWorldID(id) {
			continue
		}
		owner := s.entryOfStore(id)
		if owner != nil && !owner.Deleted.IsZero() {
			continue // Welt gelöscht
		}
		nums, _, err := s.scanGenerations(id)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		w := WorldMeta{WorldID: id}
		if owner != nil {
			w.GroupWorld, w.GroupName = owner.ID, owner.Name
			w.Active = owner.StoreWorld == id
		}
		for i := len(nums) - 1; i >= 0; i-- {
			if w.Active && nums[i] > owner.Head {
				continue // nie veröffentlicht
			}
			m, err := s.genMeta(id, nums[i])
			if err != nil {
				return nil, err
			}
			w.Gens = append(w.Gens, m)
		}
		if len(w.Gens) > 0 {
			out = append(out, w)
		}
	}
	slices.SortFunc(out, func(a, b WorldMeta) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		if a.Active {
			return cmp.Compare(order[a.WorldID], order[b.WorldID])
		}
		return cmp.Or(b.Gens[0].Created.Compare(a.Gens[0].Created), strings.Compare(b.WorldID, a.WorldID))
	})
	return out, nil
}

// genMeta liefert eine Kopie der Angaben zur Generation n der Welt
// worldID, beim ersten Mal aus ihrer Datei.
func (s *Store) genMeta(worldID string, n int64) (GenMeta, error) {
	k := genKey{worldID, n}
	m, ok := s.meta[k]
	if !ok {
		g, err := s.readGeneration(worldID, n)
		if err != nil {
			return GenMeta{}, err
		}
		m = metaOf(g)
		s.meta[k] = m
	}
	return m.clone(), nil
}

// metaOf berechnet die Angaben zur Generation g (als Kopie).
func metaOf(g *api.Generation) GenMeta {
	m := GenMeta{
		WorldID:          g.WorldID,
		Number:           g.Number,
		Created:          g.Created,
		HostEmail:        g.HostEmail,
		Players:          g.Players,
		MinecraftVersion: g.MinecraftVersion,
		RestoredFrom:     g.RestoredFrom,
	}
	for _, f := range g.Files {
		m.Size += f.Size
	}
	return m.clone()
}

func (m GenMeta) clone() GenMeta {
	m.Players = slices.Clone(m.Players)
	if m.RestoredFrom != nil {
		r := *m.RestoredFrom
		m.RestoredFrom = &r
	}
	return m
}

// Prune wendet die Aufbewahrung an und räumt danach Blobs weg, die keine
// Generation mehr nennt.
//
// Aktive Welt: Es bleiben die letzten KeepLast Generationen, für jede der
// letzten KeepHours UTC-Stunden (die Stunde von now eingeschlossen) die
// letzte Generation der Stunde und für jeden der letzten KeepDays UTC-Tage
// (der Tag von now eingeschlossen) die letzte Generation des Tages. Archive
// behalten alle Generationen.
//
// Budget: Belegen die Blobs aller behaltenen Generationen (jeder Blob
// einmal gezählt; bei Dateien mit Rezept das Rezept und seine Segmente) mehr
// als das Budget, fallen zuerst die ältesten Stunden- und Tagesstände der
// aktiven Welt, danach die ältesten Generationen der Archive – bis auf die
// jeweils neueste. Die letzten KeepLast der aktiven Welt fallen nie; reicht
// das nicht, bleibt der Speicher über dem Budget. Gepinnte Generationen
// (Pin) fallen nie. Belegung und Über-Budget merkt sich Prune für Usage.
//
// Ist eine Generation oder ein Rezept einer Generation nicht lesbar, bricht
// Prune ab, ohne etwas zu löschen. Rezepte werden einzeln gelesen, nie alle
// zugleich im Speicher gehalten.
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	plan, err := s.planPrune(now)
	if err != nil {
		return err
	}
	worlds, junk, keep, refs := plan.worlds, plan.junk, plan.keep, plan.refs

	// Erst die Generationen, dann die Blobs: So nennt keine Generation auf
	// der Platte je einen gelöschten Blob.
	// Die Angaben für Worlds gibt es danach genau für die Generationen, die
	// noch auf der Platte stehen.
	var errs []error
	meta := make(map[genKey]GenMeta, len(keep))
	for id, gens := range worlds {
		for _, g := range gens {
			if !keep[g] {
				err := os.Remove(s.genPath(id, g.Number))
				if err == nil || errors.Is(err, fs.ErrNotExist) {
					continue
				}
				errs = append(errs, err)
				refs.gen(g, +1) // bleibt liegen, also bleiben auch seine Blobs
			}
			k := genKey{id, g.Number}
			if m, ok := s.meta[k]; ok {
				meta[k] = m
			} else {
				meta[k] = metaOf(g)
			}
		}
	}
	s.meta = meta
	if refs.err == nil {
		s.used, s.over, s.usageKnown = refs.total, refs.total > s.budget, true
	}
	for _, p := range junk {
		if err := os.RemoveAll(p); err != nil {
			errs = append(errs, err)
		}
	}

	if refs.err != nil {
		return errors.Join(append(errs, refs.err)...)
	}
	blobRoot := filepath.Join(s.dir, "blobs")
	prefixes, err := os.ReadDir(blobRoot)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, p := range prefixes {
		if !p.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(blobRoot, p.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			sha := e.Name()
			if e.IsDir() || !validSHA(sha) || sha[:2] != p.Name() {
				continue // nichts anfassen, was der Speicher nicht selbst abgelegt hat
			}
			if _, ok := s.fresh[sha]; ok || refs.has(sha) {
				continue
			}
			if err := os.Remove(filepath.Join(blobRoot, p.Name(), sha)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}

	// Liegengebliebene Uploads.
	tmpDir := filepath.Join(s.dir, "tmp")
	parts, err := os.ReadDir(tmpDir)
	if err != nil {
		errs = append(errs, err)
	}
	for _, e := range parts {
		if sha, _ := strings.CutSuffix(e.Name(), ".part"); s.uploading[sha] {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > partMaxAge {
			if err := os.RemoveAll(filepath.Join(tmpDir, e.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// RefreshUsage rechnet die Aufbewahrung wie Prune durch, ohne etwas zu
// löschen, und merkt sich Belegung und Über-Budget für Usage – damit die
// Angaben nicht erst nach dem ersten (stündlichen) Prune stimmen. Weder
// Generationen noch Blobs, Temp-Dateien oder angefangene Uploads werden
// angefasst.
func (s *Store) RefreshUsage(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, err := s.planPrune(now)
	if err != nil {
		return err
	}
	s.used, s.over, s.usageKnown = plan.refs.total, plan.refs.total > s.budget, true
	return nil
}

// prunePlan ist die Aufbewahrungsrechnung von Prune: alle Generationen je
// Welt, liegengebliebene Temp-Dateien, die behaltenen Generationen und deren
// Blobs (refs.total ist ihre Belegung).
type prunePlan struct {
	worlds map[string][]*api.Generation
	junk   []string
	keep   map[*api.Generation]bool
	refs   *liveSet
}

// planPrune rechnet die Aufbewahrung samt Budget aus (siehe Prune), ohne
// etwas zu löschen. Aufruf unter s.mu.
func (s *Store) planPrune(now time.Time) (*prunePlan, error) {
	worlds, junk, err := s.loadAll()
	if err != nil {
		return nil, err
	}

	keep := map[*api.Generation]bool{}
	// activeExtras: Stunden- und Tagesstände der aktiven Welt außerhalb der
	// letzten KeepLast.
	// Jede Welt der Liste zählt als aktive Welt (mit ihrem Head); die
	// Weltkennungen gelöschter Welten fallen beim Budget zuerst, auch mit
	// ihrem neuesten Stand (deleted).
	activeHead := map[string]int64{}
	deletedStore := map[string]bool{}
	for _, e := range s.cur.Worlds {
		switch {
		case e.StoreWorld != "" && e.Deleted.IsZero():
			activeHead[e.StoreWorld] = e.Head
		case !e.Deleted.IsZero():
			deletedStore[e.StoreWorld] = true
			for _, p := range e.Previous {
				deletedStore[p] = true
			}
		}
	}
	var activeExtras, archived, deleted []*api.Generation
	for id, gens := range worlds { // gens ist nach Nummer sortiert
		head, isActive := activeHead[id]
		if !isActive {
			for _, g := range gens {
				keep[g] = true
			}
			if deletedStore[id] {
				deleted = append(deleted, gens...)
			} else {
				archived = append(archived, gens[:len(gens)-1]...)
			}
			continue
		}
		published := gens
		for len(published) > 0 && published[len(published)-1].Number > head {
			published = published[:len(published)-1]
		}
		for _, g := range published[max(0, len(published)-KeepLast):] {
			keep[g] = true
		}
		thisHour, today := hour(now), day(now)
		lastOfHour := map[int64]*api.Generation{}
		lastOfDay := map[int64]*api.Generation{}
		for _, g := range published { // aufsteigend: der letzte gewinnt
			if h := hour(g.Created); thisHour-h < KeepHours {
				lastOfHour[h] = g
			}
			if d := day(g.Created); today-d < KeepDays {
				lastOfDay[d] = g
			}
		}
		for _, last := range []map[int64]*api.Generation{lastOfHour, lastOfDay} {
			for _, g := range last {
				if !keep[g] {
					keep[g] = true
					activeExtras = append(activeExtras, g)
				}
			}
		}
	}
	// Gepinnte Generationen (Pin) bleiben immer, auch über dem Budget.
	isPinned := func(g *api.Generation) bool { return s.pinned[genKey{g.WorldID, g.Number}] }
	for _, gens := range worlds {
		for _, g := range gens {
			if isPinned(g) {
				keep[g] = true
			}
		}
	}
	activeExtras = slices.DeleteFunc(activeExtras, isPinned)
	archived = slices.DeleteFunc(archived, isPinned)
	deleted = slices.DeleteFunc(deleted, isPinned)
	byAge := func(a, b *api.Generation) int {
		return cmp.Or(a.Created.Compare(b.Created), strings.Compare(a.WorldID, b.WorldID), cmp.Compare(a.Number, b.Number))
	}
	slices.SortFunc(activeExtras, byAge)
	slices.SortFunc(archived, byAge)
	slices.SortFunc(deleted, byAge)

	// Referenzen der behaltenen Generationen; total zählt jeden Blob einmal.
	refs := newLiveSet(s)
	for g := range keep {
		refs.gen(g, +1)
	}
	for _, g := range slices.Concat(deleted, activeExtras, archived) {
		if refs.total <= s.budget || refs.err != nil {
			break
		}
		delete(keep, g)
		refs.gen(g, -1)
	}
	if refs.err != nil {
		return nil, refs.err
	}
	return &prunePlan{worlds: worlds, junk: junk, keep: keep, refs: refs}, nil
}

// liveSet zählt, welche Blobs die behaltenen Generationen brauchen:
// Ganzdatei-Blobs, Rezept-Blobs und die Segmente der Rezepte. Ein Rezept
// wird gelesen, wenn es zum ersten Mal gebraucht wird oder nicht mehr
// gebraucht wird; total ist die Größe aller gebrauchten Blobs, jeder einmal.
// Schlüssel sind rohe Hashes, damit große Welten wenig Speicher kosten.
type liveSet struct {
	s       *Store
	recipes map[[32]byte]int32 // Einträge, die das Rezept nennen
	blobs   map[[32]byte]int32 // Bezüge je Blob
	total   int64
	err     error // erstes nicht lesbares Rezept oder ungültiger Hash
}

func newLiveSet(s *Store) *liveSet {
	return &liveSet{s: s, recipes: map[[32]byte]int32{}, blobs: map[[32]byte]int32{}}
}

func shaKey(sha string) (k [32]byte, ok bool) {
	if !validSHA(sha) {
		return k, false
	}
	hex.Decode(k[:], []byte(sha))
	return k, true
}

func (l *liveSet) has(sha string) bool {
	k, ok := shaKey(sha)
	return ok && l.blobs[k] > 0
}

func (l *liveSet) blob(k [32]byte, size int64, delta int32) {
	before := l.blobs[k]
	n := before + delta
	switch {
	case before <= 0 && n > 0:
		l.total += size
	case before > 0 && n <= 0:
		l.total -= size
	}
	if n <= 0 {
		delete(l.blobs, k)
	} else {
		l.blobs[k] = n
	}
}

// gen zählt die Blobs der Generation g dazu (delta +1) oder weg (-1).
func (l *liveSet) gen(g *api.Generation, delta int32) {
	for _, f := range g.Files {
		if l.err != nil {
			return
		}
		if f.Recipe == "" {
			k, ok := shaKey(f.SHA256)
			if !ok {
				l.err = fmt.Errorf("store: Generation %s/%d nennt ungültigen Hash %q", g.WorldID, g.Number, f.SHA256)
				return
			}
			l.blob(k, f.Size, delta)
			continue
		}
		k, ok := shaKey(f.Recipe)
		if !ok {
			l.err = fmt.Errorf("store: Generation %s/%d nennt ungültiges Rezept %q", g.WorldID, g.Number, f.Recipe)
			return
		}
		before := l.recipes[k]
		n := before + delta
		if n <= 0 {
			delete(l.recipes, k)
		} else {
			l.recipes[k] = n
		}
		if (before > 0) != (n > 0) {
			l.walk(f.Recipe, k, delta)
		}
	}
}

// walk zählt das Rezept sha und seine Segmente dazu oder weg.
func (l *liveSet) walk(sha string, k [32]byte, delta int32) {
	rc, size, err := l.s.OpenBlob(sha)
	if err != nil {
		l.err = fmt.Errorf("store: Rezept %s nicht lesbar: %w", sha, err)
		return
	}
	defer rc.Close()
	l.blob(k, size, delta)
	err = api.WalkRecipe(rc, func(seg api.Segment) error {
		sk, _ := shaKey(seg.SHA256)
		l.blob(sk, seg.Size, delta)
		return nil
	})
	if err != nil {
		l.err = fmt.Errorf("store: Rezept %s nicht lesbar: %w", sha, err)
	}
}

// loadAll liest alle Generationen aller Welten, je Welt nach Nummer
// sortiert; Welten ohne Generation fehlen. junk sind liegengebliebene
// Temp-Dateien neben current.json und den Generationsdateien.
func (s *Store) loadAll() (worlds map[string][]*api.Generation, junk []string, err error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "worlds"))
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	worlds = map[string][]*api.Generation{}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !validWorldID(id) {
			continue
		}
		nums, tmps, err := s.scanGenerations(id)
		if err != nil {
			return nil, nil, fmt.Errorf("store: %w", err)
		}
		junk = append(junk, tmps...)
		for _, n := range nums {
			g, err := s.readGeneration(id, n)
			if err != nil {
				return nil, nil, err
			}
			worlds[id] = append(worlds[id], g)
		}
	}
	root, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	for _, e := range root {
		if strings.HasPrefix(e.Name(), tmpPrefix) {
			junk = append(junk, filepath.Join(s.dir, e.Name()))
		}
	}
	return worlds, junk, nil
}

// scanGenerations liefert die Nummern der Generationsdateien einer Welt
// (aufsteigend) und liegengebliebene Temp-Dateien. Eine unbekannte Welt hat
// keine Generationen.
func (s *Store) scanGenerations(worldID string) (nums []int64, tmps []string, err error) {
	dir := s.genDir(worldID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			tmps = append(tmps, filepath.Join(dir, name))
			continue
		}
		base, ok := strings.CutSuffix(name, ".json")
		n, err := strconv.ParseInt(base, 10, 64)
		if ok && err == nil && n >= 1 && strconv.FormatInt(n, 10) == base && !e.IsDir() {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	return nums, tmps, nil
}

func (s *Store) readGeneration(worldID string, n int64) (*api.Generation, error) {
	data, err := os.ReadFile(s.genPath(worldID, n))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: Generation %s/%d", ErrNotFound, worldID, n)
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	var g api.Generation
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("store: Generation %s/%d: %w", worldID, n, err)
	}
	if g.WorldID != worldID || g.Number != n {
		return nil, fmt.Errorf("store: Datei der Generation %s/%d enthält %s/%d", worldID, n, g.WorldID, g.Number)
	}
	return &g, nil
}

// publish schreibt die Generation g und macht sie danach zum Head – in
// dieser Reihenfolge, damit current.json nie auf eine fehlende oder halbe
// Generation zeigt.
//
// next ist der neue Inhalt von current.json, in dem die Welt von g schon auf
// g zeigt (Weltkennung und Head).
func (s *Store) publish(g *api.Generation, next current) error {
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	path := s.genPath(g.WorldID, g.Number)
	if err := writeAtomic(path, data); err != nil {
		return fmt.Errorf("store: Generation schreiben: %w", err)
	}
	if err := s.setCurrent(next); err != nil {
		os.Remove(path)
		return err
	}
	for _, e := range next.Worlds {
		if e.StoreWorld == g.WorldID && e.Deleted.IsZero() {
			s.heads[e.ID] = g
		}
	}
	s.meta[genKey{g.WorldID, g.Number}] = metaOf(g)
	return nil
}

func (s *Store) setCurrent(c current) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := writeAtomic(filepath.Join(s.dir, "current.json"), data); err != nil {
		return fmt.Errorf("store: current.json schreiben: %w", err)
	}
	s.cur = c
	return nil
}

// createWorld legt den Ordner einer neuen Welt an. Die ID beginnt mit der
// UTC-Zeit und ist darum nach Anlage sortierbar.
func (s *Store) createWorld(now time.Time) (string, error) {
	for range 10 {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("store: %w", err)
		}
		id := now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
		err := os.Mkdir(s.worldDir(id), 0o755)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err == nil {
			err = os.Mkdir(s.genDir(id), 0o755)
		}
		if err != nil {
			os.RemoveAll(s.worldDir(id))
			return "", fmt.Errorf("store: Welt anlegen: %w", err)
		}
		return id, nil
	}
	return "", errors.New("store: keine freie Welt-ID gefunden")
}

func (s *Store) blobPath(sha string) string {
	return filepath.Join(s.dir, "blobs", sha[:2], sha)
}

func (s *Store) partPath(sha string) string {
	return filepath.Join(s.dir, "tmp", sha+".part")
}

func (s *Store) worldDir(worldID string) string {
	return filepath.Join(s.dir, "worlds", worldID)
}

func (s *Store) genDir(worldID string) string {
	return filepath.Join(s.worldDir(worldID), "generations")
}

func (s *Store) genPath(worldID string, n int64) string {
	return filepath.Join(s.genDir(worldID), strconv.FormatInt(n, 10)+".json")
}

// checkFiles prüft die Dateiliste eines Commits und liefert sie nach Pfad
// sortiert als Kopie.
func checkFiles(in []api.FileEntry) ([]api.FileEntry, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: Stand ohne Dateien", ErrInvalid)
	}
	files := slices.Clone(in)
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if !api.ValidPath(f.Path) {
			return nil, fmt.Errorf("%w: Pfad %q", ErrInvalid, f.Path)
		}
		if !validSHA(f.SHA256) {
			return nil, fmt.Errorf("%w: Hash %q für %s", ErrInvalid, f.SHA256, f.Path)
		}
		if f.Size < 0 {
			return nil, fmt.Errorf("%w: Größe %d für %s", ErrInvalid, f.Size, f.Path)
		}
		if f.Recipe != "" && !validSHA(f.Recipe) {
			return nil, fmt.Errorf("%w: Rezept %q für %s", ErrInvalid, f.Recipe, f.Path)
		}
		// Windows unterscheidet Groß/Klein nicht; beim Auspacken wären das
		// dieselbe Datei.
		key := strings.ToLower(f.Path)
		if seen[key] {
			return nil, fmt.Errorf("%w: Pfad %q doppelt", ErrInvalid, f.Path)
		}
		seen[key] = true
	}
	// "a" und "a/b" zugleich: Ein Pfad kann nicht Datei und Ordner sein, so
	// ein Stand ließe sich nie wiederherstellen.
	for key := range seen {
		for i := 0; i < len(key); i++ {
			if key[i] == '/' && seen[key[:i]] {
				return nil, fmt.Errorf("%w: %q ist Datei und zugleich Ordner von %q", ErrInvalid, key[:i], key)
			}
		}
	}
	slices.SortFunc(files, func(a, b api.FileEntry) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// openPart öffnet die Teil-Datei für einen Teil ab start: start 0 fängt neu
// an, sonst muss die Datei genau start Bytes lang sein.
func openPart(path string, start int64) (*os.File, error) {
	if start == 0 {
		f, err := os.Create(path)
		if err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		return f, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: Teil ab %d, aber kein angefangener Upload", ErrInvalid, start)
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	end, err := f.Seek(0, io.SeekEnd)
	if err == nil && end != start {
		err = fmt.Errorf("%w: Teil ab %d, erwartet ab %d", ErrInvalid, start, end)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func verifyHash(path, sha string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sha {
		return fmt.Errorf("%w: Inhalt hat den Hash %s, nicht %s", ErrInvalid, got, sha)
	}
	return nil
}

// writeAtomic schreibt data über eine Temp-Datei im selben Ordner und
// benennt sie dann um: Leser sehen die alte oder die neue Datei, nie eine
// halbe.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
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
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir macht Renames im Ordner dauerhaft. Unter Windows lassen sich
// Ordner nicht syncen; dort bleibt es beim Versuch.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// validSHA: genau 64 Hex-Zeichen in Kleinbuchstaben.
func validSHA(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// validWorldID: 1–64 Zeichen aus Buchstaben, Ziffern, "-" und "_"; damit
// kann eine Welt-ID aus einer URL den Speicher nicht verlassen.
func validWorldID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// day ist der UTC-Kalendertag von t als fortlaufende Zahl.
func day(t time.Time) int64 { return floorDiv(t.Unix(), 86400) }

// hour ist die UTC-Stunde von t als fortlaufende Zahl.
func hour(t time.Time) int64 { return floorDiv(t.Unix(), 3600) }

// floorDiv teilt und rundet auch für negative a nach unten.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		q--
	}
	return q
}

func cloneGen(g *api.Generation) *api.Generation {
	c := *g
	c.Files = slices.Clone(g.Files)
	c.Players = slices.Clone(g.Players)
	if g.RestoredFrom != nil {
		r := *g.RestoredFrom
		c.RestoredFrom = &r
	}
	return &c
}
