package store

// Weltliste der Gruppe (current.json, Format 2).
//
// Eine Gruppe hat eine oder mehrere Welten (Gruppenwelten, z. B. „Survival“
// und „Creative“). Jede Welt hat eine stabile Kennung w<N>, einen Namen,
// einen Spielmodus und eine Schwierigkeit (beides darf leer sein: dann gilt
// world_settings des Koordinators) und zeigt auf genau eine Weltkennung
// des Speichers (StoreWorld, worlds/<id>) mit dem Head dieser Weltkennung.
// Ältere Weltkennungen derselben Welt (Archive nach „Welt übernehmen“ oder
// Zurückholen) stehen in Previous. Gleichzeitig läuft nur eine Welt; das
// regelt der Zuschlag (lease), nicht der Speicher.
//
// Die erste Welt der Liste ist die Welt für alles, was keine Welt nennt
// (ältere Starter, alte Methoden Head/Commit).
//
// Gelöscht wird eine Welt nur als Vermerk (Deleted): ihre Stände bleiben
// liegen, bis das Aufräumen sie unter Platzdruck als Erste wegnimmt
// (Prune). Ihre Kennung wird nie wieder vergeben.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// DefaultWorldName ist der Name der ersten Welt, wenn sie der Speicher
// selbst anlegt (Migration, erster Commit).
const DefaultWorldName = "Welt"

// MaxWorldNameLen: längster Name einer Welt in Zeichen.
const MaxWorldNameLen = 40

// ErrDeleted: Die Welt (oder die Weltkennung) gehört zu einer Welt, die ein
// Admin gelöscht hat.
var ErrDeleted = fmt.Errorf("%w: die Welt wurde gelöscht", ErrConflict)

// ErrLastWorld: Die letzte Welt einer Gruppe lässt sich nicht löschen.
var ErrLastWorld = fmt.Errorf("%w: die letzte Welt der Gruppe lässt sich nicht löschen", ErrConflict)

// WorldEntry ist eine Welt der Liste (siehe Paketkommentar).
type WorldEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Gamemode   string `json:"gamemode"`
	Difficulty string `json:"difficulty"`
	// Chat-Verlauf der Welt (Server-Mod chatverlauf): ChatOff = aus,
	// ChatMiB = Größengrenze (0 = Standard). Ältere current.json ohne die
	// Felder = an, Standardgröße.
	ChatOff    bool      `json:"chat_off,omitempty"`
	ChatMiB    int       `json:"chat_mib,omitempty"`
	Created    time.Time `json:"created"`
	StoreWorld string    `json:"store_world"` // Weltkennung; "": noch leer
	Head       int64     `json:"head"`
	Previous   []string  `json:"previous,omitempty"`
	Deleted    time.Time `json:"deleted,omitzero"`
}

// Empty: die Welt hat noch nie einen Stand bekommen.
func (e WorldEntry) Empty() bool { return e.StoreWorld == "" }

func (e WorldEntry) clone() WorldEntry {
	e.Previous = slices.Clone(e.Previous)
	return e
}

// rebind lässt die Welt auf die Weltkennung store mit Head head zeigen; die
// bisherige Weltkennung wird Archiv der Welt.
func (e *WorldEntry) rebind(store string, head int64) {
	if e.StoreWorld != "" && e.StoreWorld != store {
		e.Previous = append(e.Previous, e.StoreWorld)
	}
	e.Previous = slices.DeleteFunc(e.Previous, func(p string) bool { return p == store })
	e.StoreWorld, e.Head = store, head
}

// Spielmodi und Schwierigkeiten, die der Host in server.properties schreiben
// darf ("" = nichts schreiben).
var (
	gamemodes    = api.Gamemodes
	difficulties = api.Difficulties
)

// ValidGamemode meldet, ob s ein erlaubter Spielmodus ist ("" ist erlaubt).
func ValidGamemode(s string) bool { return s == "" || slices.Contains(gamemodes, s) }

// ValidDifficulty meldet, ob s eine erlaubte Schwierigkeit ist ("" ist erlaubt).
func ValidDifficulty(s string) bool { return s == "" || slices.Contains(difficulties, s) }

// CleanWorldName prüft und bereinigt den Namen einer Welt: ohne Leerraum am
// Rand, 1 bis MaxWorldNameLen Zeichen, gültiges UTF-8, keine Steuer- oder
// Formatzeichen.
func CleanWorldName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", fmt.Errorf("%w: Der Name der Welt fehlt", ErrInvalid)
	case !utf8.ValidString(name):
		return "", fmt.Errorf("%w: Der Name der Welt ist kein gültiger Text", ErrInvalid)
	case utf8.RuneCountInString(name) > MaxWorldNameLen:
		return "", fmt.Errorf("%w: Der Name der Welt ist länger als %d Zeichen", ErrInvalid, MaxWorldNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return "", fmt.Errorf("%w: Der Name der Welt enthält Steuerzeichen", ErrInvalid)
		}
	}
	return name, nil
}

func checkProps(gamemode, difficulty string) error {
	if !ValidGamemode(gamemode) {
		return fmt.Errorf("%w: Spielmodus %q (erlaubt: survival, creative, adventure)", ErrInvalid, gamemode)
	}
	if !ValidDifficulty(difficulty) {
		return fmt.Errorf("%w: Schwierigkeit %q (erlaubt: peaceful, easy, normal, hard)", ErrInvalid, difficulty)
	}
	return nil
}

// ValidChatMiB meldet, ob n eine erlaubte Größengrenze des Chat-Verlaufs ist
// (0 = Standard).
func ValidChatMiB(n int) bool { return n == 0 || (n >= api.ChatMiBMin && n <= api.ChatMiBMax) }

func checkChat(mib int) error {
	if !ValidChatMiB(mib) {
		return fmt.Errorf("%w: Größengrenze des Chat-Verlaufs %d MiB (erlaubt: %d bis %d, 0 = Standard)",
			ErrInvalid, mib, api.ChatMiBMin, api.ChatMiBMax)
	}
	return nil
}

// validEntryID: Kennung einer Welt der Liste (w<Ziffern>).
func validEntryID(s string) bool {
	if len(s) < 2 || len(s) > 12 || s[0] != 'w' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// checkCurrent prüft eine gelesene current.json.
func checkCurrent(c current) error {
	seen := map[string]bool{}
	stores := map[string]string{}
	for _, e := range c.Worlds {
		if !validEntryID(e.ID) {
			return fmt.Errorf("Welt-Kennung %q", e.ID)
		}
		if seen[e.ID] {
			return fmt.Errorf("Welt-Kennung %q doppelt", e.ID)
		}
		seen[e.ID] = true
		if e.StoreWorld == "" {
			if e.Head != 0 {
				return fmt.Errorf("Welt %s: Head %d ohne Weltkennung", e.ID, e.Head)
			}
		} else if !validWorldID(e.StoreWorld) || e.Head < 1 {
			return fmt.Errorf("Welt %s: Weltkennung %q, Head %d", e.ID, e.StoreWorld, e.Head)
		}
		if !ValidGamemode(e.Gamemode) || !ValidDifficulty(e.Difficulty) {
			return fmt.Errorf("Welt %s: Spielmodus %q oder Schwierigkeit %q", e.ID, e.Gamemode, e.Difficulty)
		}
		if !ValidChatMiB(e.ChatMiB) {
			return fmt.Errorf("Welt %s: Größengrenze des Chat-Verlaufs %d MiB", e.ID, e.ChatMiB)
		}
		for _, id := range append([]string{e.StoreWorld}, e.Previous...) {
			if id == "" {
				continue
			}
			if !validWorldID(id) {
				return fmt.Errorf("Welt %s: Weltkennung %q", e.ID, id)
			}
			if other, dup := stores[id]; dup && other != e.ID {
				return fmt.Errorf("Weltkennung %s gehört zu %s und %s", id, other, e.ID)
			}
			stores[id] = e.ID
		}
		if n, err := strconv.Atoi(e.ID[1:]); err != nil || n >= c.Next {
			return fmt.Errorf("Zähler %d ist nicht größer als die Welt %s", c.Next, e.ID)
		}
	}
	return nil
}

// --- Zugriff (Aufrufer halten s.mu) ---

// live liefert die Welt world der Liste ("": die erste), nil wenn es sie nicht
// gibt oder sie gelöscht ist. Der Zeiger gilt nur unter s.mu und nur bis zur
// nächsten Änderung von s.cur.
func (s *Store) live(world string) *WorldEntry {
	for i := range s.cur.Worlds {
		e := &s.cur.Worlds[i]
		if e.Deleted.IsZero() && (world == "" || e.ID == world) {
			return e
		}
	}
	return nil
}

func (s *Store) liveEntries() []WorldEntry {
	var out []WorldEntry
	for _, e := range s.cur.Worlds {
		if e.Deleted.IsZero() {
			out = append(out, e)
		}
	}
	return out
}

// entryOfStore liefert die Welt (auch eine gelöschte), zu der die
// Weltkennung gehört, oder nil.
func (s *Store) entryOfStore(id string) *WorldEntry {
	for i := range s.cur.Worlds {
		e := &s.cur.Worlds[i]
		if e.StoreWorld == id || slices.Contains(e.Previous, id) {
			return e
		}
	}
	return nil
}

// ownerOfStore: wie entryOfStore, aber ErrNotFound für eine unbekannte und
// ErrDeleted für eine Weltkennung einer gelöschten Welt.
func (s *Store) ownerOfStore(id string) (*WorldEntry, error) {
	e := s.entryOfStore(id)
	switch {
	case e == nil:
		return nil, fmt.Errorf("%w: Welt %s", ErrNotFound, id)
	case !e.Deleted.IsZero():
		return nil, fmt.Errorf("%w (Weltkennung %s, Welt %s)", ErrDeleted, id, e.ID)
	}
	return e, nil
}

// cloneCur kopiert die Weltliste, damit eine Änderung erst nach dem
// erfolgreichen Schreiben von current.json gilt.
func (s *Store) cloneCur() current {
	c := current{Next: s.cur.Next, Worlds: make([]WorldEntry, len(s.cur.Worlds))}
	for i, e := range s.cur.Worlds {
		c.Worlds[i] = e.clone()
	}
	return c
}

func (c *current) entry(id string) *WorldEntry {
	for i := range c.Worlds {
		if c.Worlds[i].ID == id {
			return &c.Worlds[i]
		}
	}
	return nil
}

// newEntry legt in c den Eintrag für eine neue, leere Welt an (Zähler
// hochzählen); der Aufrufer hängt ihn an c.Worlds.
func newEntry(c *current, name, gamemode, difficulty string, now time.Time) WorldEntry {
	if c.Next < 1 {
		c.Next = 1
	}
	for c.entry("w"+strconv.Itoa(c.Next)) != nil {
		c.Next++
	}
	id := "w" + strconv.Itoa(c.Next)
	c.Next++
	return WorldEntry{ID: id, Name: name, Gamemode: gamemode, Difficulty: difficulty, Created: now.UTC()}
}

// --- Verwaltung ---

// GroupWorlds liefert die Welten der Gruppe in der Reihenfolge der Liste.
func (s *Store) GroupWorlds() []WorldEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.liveEntries()
	for i := range out {
		out[i] = out[i].clone()
	}
	return out
}

// GroupWorld liefert die Welt id ("": die erste).
func (s *Store) GroupWorld(id string) (WorldEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.live(id); e != nil {
		return e.clone(), true
	}
	return WorldEntry{}, false
}

// OwnerOfStore liefert die Welt der Liste, zu der die Weltkennung gehört
// (auch als Archiv), sonst ok == false; deleted: die Welt wurde gelöscht.
func (s *Store) OwnerOfStore(id string) (e WorldEntry, deleted, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o := s.entryOfStore(id); o != nil {
		return o.clone(), !o.Deleted.IsZero(), true
	}
	return WorldEntry{}, false, false
}

func (s *Store) nameTaken(name, except string) bool {
	for _, e := range s.liveEntries() {
		if e.ID != except && strings.EqualFold(e.Name, name) {
			return true
		}
	}
	return false
}

// AddGroupWorld legt eine neue, leere Welt an (ans Ende der Liste). Der Name
// muss je Gruppe eindeutig sein (ohne Groß-/Kleinschreibung), sonst
// ErrConflict.
func (s *Store) AddGroupWorld(name, gamemode, difficulty string, now time.Time) (WorldEntry, error) {
	return s.AddGroupWorldChat(name, gamemode, difficulty, false, 0, now)
}

// AddGroupWorldChat wie AddGroupWorld, mit Chat-Einstellung (chatOff, chatMiB;
// 0 = Standard).
func (s *Store) AddGroupWorldChat(name, gamemode, difficulty string, chatOff bool, chatMiB int, now time.Time) (WorldEntry, error) {
	name, err := CleanWorldName(name)
	if err != nil {
		return WorldEntry{}, err
	}
	if err := checkProps(gamemode, difficulty); err != nil {
		return WorldEntry{}, err
	}
	if err := checkChat(chatMiB); err != nil {
		return WorldEntry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameTaken(name, "") {
		return WorldEntry{}, fmt.Errorf("%w: Es gibt schon eine Welt „%s“", ErrConflict, name)
	}
	next := s.cloneCur()
	e := newEntry(&next, name, gamemode, difficulty, now)
	e.ChatOff, e.ChatMiB = chatOff, chatMiB
	next.Worlds = append(next.Worlds, e)
	if err := s.setCurrent(next); err != nil {
		return WorldEntry{}, err
	}
	return e.clone(), nil
}

// WorldPatch ändert Felder einer Welt; nil lässt ein Feld unverändert.
type WorldPatch struct {
	Name       *string
	Gamemode   *string
	Difficulty *string
	ChatOff    *bool
	ChatMiB    *int
}

// UpdateGroupWorld ändert Name und Eigenschaften der Welt id. Die Welt
// selbst (Stände, Weltkennung) bleibt unberührt.
func (s *Store) UpdateGroupWorld(id string, p WorldPatch) (WorldEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.live(id)
	if id == "" || cur == nil {
		return WorldEntry{}, fmt.Errorf("%w: Welt %q", ErrNotFound, id)
	}
	e := cur.clone()
	if p.Name != nil {
		n, err := CleanWorldName(*p.Name)
		if err != nil {
			return WorldEntry{}, err
		}
		if s.nameTaken(n, e.ID) {
			return WorldEntry{}, fmt.Errorf("%w: Es gibt schon eine Welt „%s“", ErrConflict, n)
		}
		e.Name = n
	}
	if p.Gamemode != nil {
		e.Gamemode = *p.Gamemode
	}
	if p.Difficulty != nil {
		e.Difficulty = *p.Difficulty
	}
	if p.ChatOff != nil {
		e.ChatOff = *p.ChatOff
	}
	if p.ChatMiB != nil {
		e.ChatMiB = *p.ChatMiB
	}
	if err := checkProps(e.Gamemode, e.Difficulty); err != nil {
		return WorldEntry{}, err
	}
	if err := checkChat(e.ChatMiB); err != nil {
		return WorldEntry{}, err
	}
	next := s.cloneCur()
	*next.entry(id) = e
	if err := s.setCurrent(next); err != nil {
		return WorldEntry{}, err
	}
	return e.clone(), nil
}

// DeleteGroupWorld löscht die Welt id aus der Liste (nur als Vermerk, die
// Stände bleiben bis zum Aufräumen unter Platzdruck). Die letzte Welt lässt
// sich nicht löschen (ErrLastWorld). Ob die Welt gerade läuft, prüft der
// Aufrufer.
func (s *Store) DeleteGroupWorld(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || s.live(id) == nil {
		return fmt.Errorf("%w: Welt %q", ErrNotFound, id)
	}
	if len(s.liveEntries()) < 2 {
		return ErrLastWorld
	}
	next := s.cloneCur()
	next.entry(id).Deleted = now.UTC()
	if err := s.setCurrent(next); err != nil {
		return err
	}
	delete(s.heads, id)
	return nil
}
