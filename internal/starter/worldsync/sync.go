// Package worldsync gleicht die Minecraft-Welt mit dem Koordinator ab.
//
// Hochladen (Host, Zwischenstand alle paar Minuten und am Ende): Stage
// kopiert die geänderten Dateien der laufenden Welt in einen
// Bereitstellungsordner, BuildManifest hasht sie (mit Hash-Cache), Upload
// lädt nur die Blobs hoch, die dem Koordinator fehlen, und Commit reicht den
// Stand ein. UploadAndCommit fasst Upload und Commit zusammen und lädt nach,
// wenn der Koordinator beim Commit einen Blob vermisst;
// UploadAndCommitCounted meldet dazu die hochgeladenen Bytes.
//
// Herunterladen (neuer Host, Vorladen nach der Installation): Download baut
// den Stand einer Generation im Weltordner auf, lädt nur, was fehlt oder
// abweicht, und entfernt alles, was nicht im Manifest steht.
//
// Regionsdateien (*.mca) zerlegt BuildManifest in Segmente (siehe
// SplitRegion), jedes ein eigener Blob; das Manifest nennt nur das Rezept,
// ein Blob mit der Segmentliste. Upload lädt nur fehlende Segmente gebündelt
// hoch (nie die ganze Datei), Download holt nur Segmente, die der lokalen
// Datei fehlen, setzt zusammen und prüft den Hash der ganzen Datei.
//
// Temp-Dateien (.wanderwelt-*.tmp, auch Absturzreste im Weltordner) werden
// nie bereitgestellt und nie ins Manifest aufgenommen.
//
// Die Sperrdatei des laufenden Servers (session.lock im Weltordner) wird
// nirgends übertragen: Stage kopiert sie nicht, BuildManifest nennt sie
// nicht, Download schreibt und löscht sie nicht.
//
// Solange ein Download läuft, liegt im Weltordner die Markierung
// .wanderwelt-download; bricht er ab, bleibt sie liegen. Ein so markierter
// Ordner ist kein gültiger Stand: Stage lehnt ihn ab, DownloadIncomplete
// meldet ihn.
//
// Größe und Änderungszeit kommen immer aus os.Lstat und nie aus dem
// Ordnereintrag: Unter Windows zeigt der Ordnereintrag einer Datei, die
// gerade offen ist (der Server hält Regionsdateien offen), nach einem
// Überschreiben gleicher Größe noch die alte Zeit.
package worldsync

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	// lockFile ist die Sperrdatei des Servers im Wurzelordner der Welt.
	lockFile = "session.lock"
	// tmpPattern: Temp-Dateien neben dem Ziel (Stage, Download).
	tmpPattern = ".wanderwelt-*.tmp"
	// uploadWorkers: so viele Blobs gehen gleichzeitig hoch.
	uploadWorkers = 3
	// progressStep: Während einer Datei meldet Download den Fortschritt
	// spätestens nach so vielen Bytes.
	progressStep = 1 << 20

	// downloadMarker liegt während eines Downloads im Wurzelordner der Welt.
	downloadMarker = ".wanderwelt-download"

	// downloadWorkers: so viele Dateien lädt Download gleichzeitig.
	downloadWorkers = 4
	// recipeBatch: so viele Rezepte holt Download je Anfrage (höchstens
	// api.MaxRecipeSize Bytes je Rezept, zusammen unter
	// api.TransferMaxBytes).
	recipeBatch = api.TransferMaxBytes / api.MaxRecipeSize

	cacheFileName = "hashcache.json"
	// Version 2: Einträge mit Rezept statt Segmentliste.
	cacheVersion = 2
)

// ErrMissingBlobs: Der Koordinator hat einen Commit mit 412 abgelehnt, weil
// ihm ein genannter Blob fehlt (etwa nach einem Aufräumlauf). Fehlende Blobs
// nachladen und neu einreichen hilft; UploadAndCommit tut das einmal.
var ErrMissingBlobs = errors.New("dem Koordinator fehlen Blobs")

// ErrLocalRecipe: Upload konnte die lokale Kopie eines Rezepts nicht
// verwenden (fehlt, beschädigt oder passt nicht zur Dateigröße). Upload hat
// die Kopie gelöscht; der nächste BuildManifest zerlegt die Datei neu, der
// nächste Stand geht wieder durch.
var ErrLocalRecipe = errors.New("lokales Rezept unbrauchbar")

// ErrDownloadIncomplete: Stage lehnt einen Weltordner ab, in dem ein
// Download nicht abgeschlossen wurde (siehe DownloadIncomplete).
var ErrDownloadIncomplete = errors.New("Download im Weltordner nicht abgeschlossen; das ist kein gültiger Stand")

// ErrChangedWhileCopying: Eine Datei der Welt hat sich während des Kopierens
// in Stage geändert (der Server schrieb trotz save-off). Der
// Bereitstellungsordner ist dann kein stimmiger Stand; ein neuer Versuch nach
// erneutem Speichern kann gelingen.
var ErrChangedWhileCopying = errors.New("Datei hat sich beim Kopieren geändert")

// chtimes setzt die Änderungszeit einer bereitgestellten Datei (in Tests
// ersetzbar, um ein Dateisystem mit groben Zeiten nachzustellen).
var chtimes = os.Chtimes

// --- Hash-Cache ---

// HashCache merkt sich den SHA-256 von Dateien, bei Regionsdateien auch den
// Hash ihres Rezepts. Ein Eintrag gilt nur, wenn Pfad (absolut), Größe und
// Änderungszeit (auf die Nanosekunde) zur Datei passen; pro Pfad gibt es
// einen Eintrag. Er liegt in group.Home()/hashcache.json. Ein HashCache ist
// nebenläufig nutzbar; nil heißt: ohne Cache.
type HashCache struct {
	path string

	mu      sync.Mutex
	entries map[string]cacheEntry
	dirty   bool
}

type cacheEntry struct {
	Size   int64  `json:"size"`
	MTime  int64  `json:"mtime_ns"`
	SHA256 string `json:"sha256"`
	// Split: Die Datei wurde als Regionsdatei zerlegt, Recipe ist der Hash
	// ihres Rezepts (leer: nicht zerlegbar, ganze Datei). Einträge ohne
	// Split gelten für Regionsdateien nicht.
	Split  bool   `json:"split,omitempty"`
	Recipe string `json:"recipe,omitempty"`
}

// valid prüft einen geladenen Eintrag.
func (e cacheEntry) valid() bool {
	return e.Size >= 0 && validSHA(e.SHA256) && (e.Recipe == "" || e.Split && validSHA(e.Recipe))
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// LoadHashCache lädt den Hash-Cache aus group.Home()/hashcache.json. Fehlt
// die Datei oder ist sie unlesbar oder kaputt, fängt er leer an (ein Cache
// ist nie wichtiger als ein Neuberechnen).
func LoadHashCache() *HashCache {
	path := filepath.Join(group.Home(), cacheFileName)
	hc := &HashCache{path: path, entries: map[string]cacheEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return hc
	}
	if err != nil {
		log.Printf("worldsync: Hash-Cache nicht lesbar, fange leer an: %v", err)
		return hc
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != cacheVersion {
		log.Printf("worldsync: Hash-Cache %s kaputt oder veraltet, fange leer an: %v", path, err)
		return hc
	}
	for p, e := range f.Entries {
		if filepath.IsAbs(p) && e.valid() {
			hc.entries[p] = e
		}
	}
	return hc
}

// lookup liefert den gemerkten Eintrag von path, wenn Größe und
// Änderungszeit zu info passen.
func (hc *HashCache) lookup(path string, info fs.FileInfo) (cacheEntry, bool) {
	if hc == nil {
		return cacheEntry{}, false
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	e, ok := hc.entries[path]
	if !ok || e.Size != info.Size() || e.MTime != info.ModTime().UnixNano() {
		return cacheEntry{}, false
	}
	return e, true
}

// put merkt sich e (Hash, gegebenenfalls Rezept) für path mit Größe und
// Änderungszeit aus info.
func (hc *HashCache) put(path string, info fs.FileInfo, e cacheEntry) {
	if hc == nil {
		return
	}
	e.Size, e.MTime = info.Size(), info.ModTime().UnixNano()
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if old, ok := hc.entries[path]; !ok || old != e {
		hc.entries[path] = e
		hc.dirty = true
	}
}

// prune vergisst alle Einträge unter root, die nicht in keep stehen.
func (hc *HashCache) prune(root string, keep map[string]bool) {
	if hc == nil {
		return
	}
	prefix := strings.TrimRight(root, `\/`) + string(filepath.Separator)
	hc.mu.Lock()
	defer hc.mu.Unlock()
	for p := range hc.entries {
		if strings.HasPrefix(p, prefix) && !keep[p] {
			delete(hc.entries, p)
			hc.dirty = true
		}
	}
}

// recipes liefert die Rezepte, die Einträge des Caches nennen.
func (hc *HashCache) recipes() map[string]bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	out := map[string]bool{}
	for _, e := range hc.entries {
		if e.Recipe != "" {
			out[e.Recipe] = true
		}
	}
	return out
}

// Save schreibt den Cache atomar (Temp-Datei, dann Umbenennen), wenn sich
// etwas geändert hat.
func (hc *HashCache) Save() error {
	if hc == nil {
		return nil
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if !hc.dirty {
		return nil
	}
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Entries: hc.entries})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hc.path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(hc.path, data); err != nil {
		return err
	}
	hc.dirty = false
	return nil
}

// saveQuietly speichert den Cache; ein Fehler kostet nur Zeit beim nächsten
// Mal und wird geloggt.
func (hc *HashCache) saveQuietly() {
	if err := hc.Save(); err != nil {
		log.Printf("worldsync: Hash-Cache nicht gespeichert: %v", err)
	}
}

// --- Bereitstellen und Manifest ---

// Stage bringt den Bereitstellungsordner stagingDir auf den Stand der
// laufenden Welt worldDir: Dateien, die dort fehlen oder deren Größe oder
// Änderungszeit abweicht, werden kopiert (Temp-Datei, Änderungszeit der
// Quelle, Umbenennen); was es in der Welt nicht mehr gibt, wird entfernt.
// session.lock wird nicht kopiert. Symlinks, Junctions und andere
// Sonderdateien in der Welt sind ein Fehler, ebenso eine Welt mit der
// Markierung eines abgebrochenen Downloads (DownloadIncomplete): Sie ist kein
// gültiger Stand.
//
// Kann der Bereitstellungsordner die Änderungszeit der Quelle nicht genau
// speichern (grobe Zeitstempel wie FAT), scheitert Stage: Der Hash-Cache
// erkennt Änderungen an Größe und Änderungszeit, bei gerundeten Zeiten
// bliebe eine Änderung gleicher Größe unbemerkt.
//
// Stage darf nur zwischen SaveBarrier und release laufen (save-off und
// save-all flush, danach save-on): Nur dann schreibt der Server nicht in die
// Welt, und die Kopie ist ein stimmiger Stand. Ändert sich eine Datei
// trotzdem während des Kopierens, bricht Stage mit Fehler ab.
//
// Danach gehört stagingDir BuildManifest und Upload, bis der Stand
// eingereicht ist; den nächsten Stand erst danach bereitstellen.
//
// Temp-Dateien von wanderwelt (.wanderwelt-*.tmp) in der Welt werden
// übergangen. Liegt die Markierung eines abgebrochenen Downloads in der
// Welt, ist der Fehler ErrDownloadIncomplete; lässt sie sich nicht prüfen,
// kommt dieser Fehler.
func Stage(worldDir, stagingDir string) error {
	return StageCachedContext(context.Background(), worldDir, stagingDir, nil)
}

// StageContext ist Stage mit Abbruch: Endet ctx, hört es vor der nächsten
// Datei auf (die gerade kopierte ist dann vollständig und wieder
// geschlossen) und liefert context.Cause(ctx). Der Bereitstellungsordner ist
// danach kein stimmiger Stand und darf nicht hochgeladen werden; der nächste
// Aufruf gleicht ihn wieder ab.
func StageContext(ctx context.Context, worldDir, stagingDir string) error {
	return StageCachedContext(ctx, worldDir, stagingDir, nil)
}

// StageCached wie Stage; zusätzlich übernimmt hc für jede bereitgestellte
// Datei den gültigen Eintrag ihrer Quelle (gleiche Größe und
// Änderungszeit). So muss BuildManifest nach einem Download die Welt nicht
// neu hashen und zerlegen: Download hinterlegt Hash und Rezept jeder Datei.
func StageCached(worldDir, stagingDir string, hc *HashCache) error {
	return StageCachedContext(context.Background(), worldDir, stagingDir, hc)
}

// StageCachedContext ist StageCached mit Abbruch wie StageContext: ctx wird
// am Anfang und vor jeder zu kopierenden Datei geprüft; endet er, kommt
// context.Cause(ctx), und der Bereitstellungsordner darf nicht hochgeladen
// werden. Die Host-Sitzung ruft es mit demselben hc auf, den sie Download
// gegeben hat, damit der erste Stand nach einer Übergabe nicht die ganze
// Welt neu hasht und zerlegt.
func StageCachedContext(ctx context.Context, worldDir, stagingDir string, hc *HashCache) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	world, err := filepath.Abs(worldDir)
	if err != nil {
		return err
	}
	staging, err := filepath.Abs(stagingDir)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(world, downloadMarker)); err == nil {
		return fmt.Errorf("%w: %s", ErrDownloadIncomplete, world)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Markierung im Weltordner nicht prüfbar: %w", err)
	}
	if within(world, staging) || within(staging, world) {
		return fmt.Errorf("Weltordner %s und Bereitstellungsordner %s dürfen nicht ineinander liegen", world, staging)
	}
	files, dirs, err := scanWorld(world)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := requireRealDir(staging); err != nil {
		return err
	}

	// Erst aufräumen: Was im Bereitstellungsordner nicht genau so (Datei
	// bzw. Ordner) in der Welt steht, kommt weg – auch Reste abgebrochener
	// Kopien. Danach stehen keine Dateien mehr, wo Ordner hingehören.
	err = filepath.WalkDir(staging, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == staging {
			return nil
		}
		rel, err := slashRel(staging, path)
		if err != nil {
			return err
		}
		switch t := d.Type(); {
		case t == fs.ModeDir:
			if dirs[rel] {
				return nil
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return fs.SkipDir
		case t.IsRegular():
			if _, ok := files[rel]; ok {
				return nil
			}
		}
		return os.Remove(path)
	})
	if err != nil {
		return fmt.Errorf("Bereitstellungsordner aufräumen: %w", err)
	}

	for _, rel := range slices.Sorted(maps.Keys(files)) {
		info := files[rel]
		src := filepath.Join(world, filepath.FromSlash(rel))
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		cur, err := os.Lstat(dst)
		if err != nil || !cur.Mode().IsRegular() || !sameState(cur, info) {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if err := stageFile(src, dst, info); err != nil {
				return err
			}
			if cur, err = os.Lstat(dst); err != nil {
				return err
			}
		}
		// Die Bereitstellung ist inhaltsgleich mit der Quelle (gleiche Größe
		// und Änderungszeit, wie überall im Hash-Cache).
		if e, ok := hc.lookup(src, info); ok {
			hc.put(dst, cur, e)
		}
	}
	return nil
}

// scanWorld liest die Dateien (Pfad mit "/" → Lstat) und Ordner der Welt
// unter root, ohne session.lock.
func scanWorld(root string) (map[string]fs.FileInfo, map[string]bool, error) {
	if err := requireRealDir(root); err != nil {
		return nil, nil, err
	}
	files := map[string]fs.FileInfo{}
	dirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := slashRel(root, path)
		if err != nil {
			return err
		}
		switch t := d.Type(); {
		case t == fs.ModeDir:
			dirs[rel] = true
			return nil
		case !t.IsRegular():
			return fmt.Errorf("%s ist weder Datei noch Ordner (%v)", path, t)
		case isLockFile(rel), isTempName(d.Name()):
			return nil
		case !api.ValidPath(rel):
			return fmt.Errorf("%s: Dateiname lässt sich nicht übertragen", path)
		}
		info, err := os.Lstat(path) // nicht d.Info(), siehe Paketkommentar
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s ist keine reguläre Datei mehr", path)
		}
		files[rel] = info
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("Welt lesen: %w", err)
	}
	return files, dirs, nil
}

// stageFile kopiert src nach dst über eine Temp-Datei neben dst und setzt
// die Änderungszeit von src. before ist Lstat von src vor dem Kopieren.
func stageFile(src, dst string, before fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), tmpPattern)
	if err != nil {
		return err
	}
	tmp := out.Name()
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(tmp)
		}
	}()

	n, err := io.Copy(out, in)
	if err != nil {
		return fmt.Errorf("%s kopieren: %w", src, err)
	}
	after, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if n != before.Size() || !sameState(before, after) {
		return fmt.Errorf("%s hat sich beim Kopieren geändert (Stage nur zwischen SaveBarrier und release): %w", src, ErrChangedWhileCopying)
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := chtimes(tmp, time.Time{}, before.ModTime()); err != nil {
		return err
	}
	staged, err := os.Lstat(tmp)
	if err != nil {
		return err
	}
	if !staged.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%s: Der Bereitstellungsordner speichert die Änderungszeit nicht genau (%s statt %s); er muss auf NTFS liegen",
			dst, staged.ModTime().Format(time.RFC3339Nano), before.ModTime().Format(time.RFC3339Nano))
	}
	if err := renameRetry(tmp, dst); err != nil {
		return err
	}
	ok = true
	return nil
}

// BuildManifest liefert die Dateien in stagingDir als Manifest: Pfade
// relativ und mit "/", nach Pfad sortiert, ohne session.lock und
// Temp-Dateien (.wanderwelt-*.tmp). Regionsdateien (Pfad endet auf .mca),
// die SplitRegion in mehr als ein Segment zerlegt, bekommen ein Rezept; es
// wird lokal abgelegt (Upload liest es dort). Alle anderen Dateien und nicht
// lesbare Regionsdateien bleiben ein Blob. Hashes und Rezepte kommen aus hc,
// solange Größe und Änderungszeit passen (und das Rezept lokal unversehrt
// liegt: Hash und Format werden bei jedem Treffer geprüft), sonst
// werden sie berechnet und gemerkt. Der Cache wird danach gespeichert, und
// lokale Rezepte, die kein Eintrag mehr nennt, werden entfernt.
func BuildManifest(stagingDir string, hc *HashCache) ([]api.FileEntry, error) {
	root, err := filepath.Abs(stagingDir)
	if err != nil {
		return nil, err
	}
	if err := requireRealDir(root); err != nil {
		return nil, err
	}
	var files []api.FileEntry
	seen := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := slashRel(root, path)
		if err != nil {
			return err
		}
		switch t := d.Type(); {
		case t == fs.ModeDir:
			return nil
		case !t.IsRegular():
			return fmt.Errorf("%s ist weder Datei noch Ordner (%v)", path, t)
		case isLockFile(rel), isTempName(d.Name()):
			return nil
		case !api.ValidPath(rel):
			return fmt.Errorf("%s: Dateiname lässt sich nicht übertragen", path)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		f, err := describeFile(path, rel, info, hc)
		if err != nil {
			return err
		}
		seen[path] = true
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("Manifest bilden: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("Manifest bilden: %s enthält keine Dateien", root)
	}
	slices.SortFunc(files, func(a, b api.FileEntry) int { return strings.Compare(a.Path, b.Path) })
	hc.prune(root, seen)
	hc.saveQuietly()
	if hc != nil {
		pruneRecipes(hc.recipes())
	}
	return files, nil
}

// describeFile liefert den Manifest-Eintrag der Datei path (rel ist ihr
// Pfad mit "/", info Lstat davon), aus hc oder berechnet und gemerkt. Ein
// Treffer mit Rezept gilt nur, wenn die lokale Kopie des Rezepts unversehrt
// ist (Hash und Format); sonst wird neu zerlegt, und splitFile
// ersetzt die beschädigte Kopie.
func describeFile(path, rel string, info fs.FileInfo, hc *HashCache) (api.FileEntry, error) {
	f := api.FileEntry{Path: rel, Size: info.Size()}
	region := isRegion(rel) && info.Size() <= maxSplitSize
	if e, ok := hc.lookup(path, info); ok && (!region || e.Split && (e.Recipe == "" || recipeIntact(e.Recipe))) {
		f.SHA256 = e.SHA256
		if region {
			f.Recipe = e.Recipe
		}
		return f, nil
	}
	if !region {
		sha, err := hashFile(path, info)
		if err != nil {
			return f, err
		}
		hc.put(path, info, cacheEntry{SHA256: sha})
		f.SHA256 = sha
		return f, nil
	}
	whole, recipe, err := splitFile(path, info)
	if err != nil {
		return f, err
	}
	f.SHA256, f.Recipe = whole, recipe
	hc.put(path, info, cacheEntry{SHA256: whole, Split: true, Recipe: recipe})
	return f, nil
}

// --- Hochladen und Einreichen ---

// Upload lädt aus stagingDir hoch, was dem Koordinator für files fehlt:
//  1. Er fragt nach den Blobs der ganzen Dateien und den Rezepten.
//  2. Für Dateien, deren Rezept fehlt, fragt er nach ihren Segmenten (das
//     Rezept liegt lokal, siehe BuildManifest).
//  3. Fehlende Segmente gehen gebündelt hoch (POST /v1/blobs/batch, je
//     Anfrage höchstens api.TransferMaxBytes, die Datei einmal geöffnet),
//     danach die fehlenden Rezepte; fehlende ganze Dateien einzeln.
//
// Für eine zerlegte Datei geht nie ein Blob der ganzen Datei hoch. Jede
// Missing-Anfrage nennt höchstens client.MissingChunk Hashes. Höchstens drei
// Anfragen laufen gleichzeitig; beim ersten Fehler bricht Upload die übrigen
// ab. Der Koordinator prüft jeden Blob gegen seinen Hash, ein halber oder
// falscher Blob wird nie sichtbar.
func Upload(ctx context.Context, c *client.Client, stagingDir string, files []api.FileEntry) error {
	return upload(ctx, c, stagingDir, files, false, new(atomic.Int64))
}

// upload ist Upload; mit allRecipes fragt Schritt 2 nach den Segmenten
// aller Dateien mit Rezept, auch wenn das Rezept schon da ist (nach einem
// 412 könnte ein Segment fehlen). sent zählt die Bytes jedes vollständig
// hochgeladenen Blobs (Segmente, ganze Dateien, Rezepte).
func upload(ctx context.Context, c *client.Client, stagingDir string, files []api.FileEntry, allRecipes bool, sent *atomic.Int64) error {
	root, err := filepath.Abs(stagingDir)
	if err != nil {
		return err
	}
	var top []string
	topSeen := map[string]bool{}
	for _, f := range files {
		if !api.ValidPath(f.Path) || !validSHA(f.SHA256) || f.Size < 0 || f.Recipe != "" && !validSHA(f.Recipe) {
			return fmt.Errorf("Upload: ungültiger Manifest-Eintrag %+v", f)
		}
		h := cmp.Or(f.Recipe, f.SHA256)
		if !topSeen[h] {
			topSeen[h] = true
			top = append(top, h)
		}
	}
	missingTop, err := c.Missing(ctx, top)
	if err != nil {
		return fmt.Errorf("Upload: fehlende Blobs erfragen: %w", err)
	}
	lacks := map[string]bool{}
	for _, h := range missingTop {
		lacks[h] = true
	}

	// Schritt 2: Segmente der Dateien, deren Rezept fehlt.
	type recipeFile struct {
		f    api.FileEntry
		segs []api.Segment
	}
	var rfs []recipeFile
	var segHashes []string
	segSeen := map[string]bool{}
	recSeen := map[string]bool{}
	for _, f := range files {
		if f.Recipe == "" || !(allRecipes || lacks[f.Recipe]) {
			continue
		}
		segs, err := recipeFor(f.Recipe, f.Size, loadRecipe)
		if err != nil {
			// Selbstheilung: Die Kopie weg, dann gilt der Cache-Eintrag nicht
			// mehr und der nächste BuildManifest zerlegt die Datei neu. Dieser
			// Stand scheitert.
			if derr := dropRecipe(f.Recipe); derr != nil {
				log.Printf("worldsync: unbrauchbares lokales Rezept %s nicht gelöscht: %v", f.Recipe, derr)
			}
			return fmt.Errorf("Upload: %s: %w (gelöscht, der nächste Stand zerlegt die Datei neu): %w", f.Path, ErrLocalRecipe, err)
		}
		rfs = append(rfs, recipeFile{f, segs})
		for _, s := range segs {
			if !segSeen[s.SHA256] {
				segSeen[s.SHA256] = true
				segHashes = append(segHashes, s.SHA256)
			}
		}
	}
	missingSegs, err := c.Missing(ctx, segHashes)
	if err != nil {
		return fmt.Errorf("Upload: fehlende Segmente erfragen: %w", err)
	}
	for _, h := range missingSegs {
		lacks[h] = true
	}

	// Schritt 3a: Segmente gebündelt, je Datei; ganze Dateien einzeln.
	var jobs []func(context.Context) error
	queued := map[string]bool{}
	for _, rf := range rfs {
		var parts []segPart
		var off int64
		for _, s := range rf.segs {
			if lacks[s.SHA256] && !queued[s.SHA256] {
				queued[s.SHA256] = true
				parts = append(parts, segPart{s, off})
			}
			off += s.Size
		}
		path := filepath.Join(root, filepath.FromSlash(rf.f.Path))
		// Ein Segment über maxSegment passt in kein Paket: einzeln (nur aus
		// fremden oder alten Rezepten, splitFile bildet so etwas nicht).
		var packed []segPart
		for _, p := range parts {
			if p.Size > maxSegment {
				jobs = append(jobs, counted(sent, p.Size, func(ctx context.Context) error { return uploadSegment(ctx, c, path, rf.f.Size, p) }))
			} else {
				packed = append(packed, p)
			}
		}
		for _, batch := range packSegments(packed) {
			var n int64
			for _, p := range batch {
				n += p.Size
			}
			jobs = append(jobs, counted(sent, n, func(ctx context.Context) error { return uploadSegments(ctx, c, path, rf.f.Size, batch) }))
		}
	}
	for _, f := range files {
		if f.Recipe == "" && lacks[f.SHA256] && !queued[f.SHA256] {
			queued[f.SHA256] = true
			jobs = append(jobs, counted(sent, f.Size, func(ctx context.Context) error { return uploadBlob(ctx, c, root, f) }))
		}
	}
	if err := runJobs(ctx, uploadWorkers, jobs); err != nil {
		return fmt.Errorf("Upload: %w", err)
	}

	// Schritt 3b: erst danach die fehlenden Rezepte.
	var recipes []client.BlobFrame
	for _, rf := range rfs {
		if !lacks[rf.f.Recipe] || recSeen[rf.f.Recipe] {
			continue
		}
		recSeen[rf.f.Recipe] = true
		data, err := api.EncodeRecipe(rf.segs) // kanonisch: dieselben Bytes wie lokal
		if err != nil {
			return fmt.Errorf("Upload: %w", err)
		}
		recipes = append(recipes, client.BlobFrame{SHA256: rf.f.Recipe, Size: int64(len(data)), Data: bytes.NewReader(data)})
	}
	for batch := range slices.Chunk(recipes, recipeBatch) {
		if err := c.PutBlobs(ctx, batch); err != nil {
			return fmt.Errorf("Upload: Rezepte: %w", err)
		}
		for _, f := range batch {
			sent.Add(f.Size)
		}
	}
	return nil
}

// counted zählt n Bytes zu sent, wenn job gelingt.
func counted(sent *atomic.Int64, n int64, job func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := job(ctx); err != nil {
			return err
		}
		sent.Add(n)
		return nil
	}
}

// segPart ist ein Segment einer Datei ab Byte off.
type segPart struct {
	api.Segment
	off int64
}

// packSegments teilt parts in Pakete zu höchstens api.TransferMaxBytes
// (samt Rahmenköpfen) und api.FetchMaxBlobs Segmenten.
func packSegments(parts []segPart) [][]segPart {
	var out [][]segPart
	var cur []segPart
	var size int64
	for _, p := range parts {
		n := api.BatchFrameHeader + p.Size
		if len(cur) > 0 && (size+n > api.TransferMaxBytes || len(cur) == api.BatchMaxFrames) {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, p)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// uploadSegments lädt die Segmente parts der bereitgestellten Datei path
// (fileSize Bytes) in einer Batch-Anfrage hoch; die Datei wird einmal
// geöffnet und beim Senden gelesen.
func uploadSegments(ctx context.Context, c *client.Client, path string, fileSize int64, parts []segPart) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != fileSize {
		return fmt.Errorf("%s hat sich seit dem Manifest geändert", path)
	}
	frames := make([]client.BlobFrame, len(parts))
	for i, p := range parts {
		frames[i] = client.BlobFrame{SHA256: p.SHA256, Size: p.Size, Data: io.NewSectionReader(fh, p.off, p.Size)}
	}
	return c.PutBlobs(ctx, frames)
}

// uploadSegment lädt ein einzelnes Segment p der bereitgestellten Datei path
// (fileSize Bytes) über den Einzel-Endpunkt hoch, der keine Größengrenze
// hat.
func uploadSegment(ctx context.Context, c *client.Client, path string, fileSize int64, p segPart) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != fileSize {
		return fmt.Errorf("%s hat sich seit dem Manifest geändert", path)
	}
	return c.PutBlob(ctx, p.SHA256, io.NewSectionReader(fh, p.off, p.Size), p.Size)
}

// runJobs führt jobs mit höchstens workers gleichzeitig aus; der erste
// Fehler bricht den Rest ab.
func runJobs(ctx context.Context, workers int, jobs []func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	ch := make(chan func(context.Context) error)
	var wg sync.WaitGroup
	for range min(workers, len(jobs)) {
		wg.Go(func() {
			for job := range ch {
				if ctx.Err() != nil {
					continue
				}
				if err := job(ctx); err != nil {
					cancel(err)
				}
			}
		})
	}
feed:
	for _, job := range jobs {
		select {
		case ch <- job:
		case <-ctx.Done():
			break feed
		}
	}
	close(ch)
	wg.Wait()
	return context.Cause(ctx)
}

// uploadBlob lädt die ganze Datei f aus root als einen Blob hoch.
func uploadBlob(ctx context.Context, c *client.Client, root string, f api.FileEntry) error {
	path := filepath.Join(root, filepath.FromSlash(f.Path))
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return fmt.Errorf("%s hat sich seit dem Manifest geändert", path)
	}
	return c.PutBlob(ctx, f.SHA256, fh, f.Size)
}

// Commit reicht den Stand req ein. req trägt den Zuschlag und die Basis:
// BaseWorldID und BaseGeneration der Generation, auf der der Stand aufbaut
// (ohne Welt "" und 0). Ergebnis ist die neue Generation; Welt und Nummer
// daraus sind die Basis des nächsten Stands.
//
// 409 (Zuschlag verloren oder Basis falsch) → client.ErrLeaseLost,
// 412 (Blob fehlt) → ErrMissingBlobs, 400 (Manifest ungültig) →
// *client.StatusError.
func Commit(ctx context.Context, c *client.Client, req api.CommitRequest) (*api.Generation, error) {
	gen, err := c.Commit(ctx, req)
	if se, ok := errors.AsType[*client.StatusError](err); ok && se.StatusCode == http.StatusPreconditionFailed {
		return nil, fmt.Errorf("%w: %w", ErrMissingBlobs, err)
	}
	if err != nil {
		return nil, err
	}
	if gen == nil || gen.WorldID == "" || gen.Number < 1 {
		return nil, fmt.Errorf("Koordinator: Commit ohne gültige Generation beantwortet: %+v", gen)
	}
	return gen, nil
}

// UploadAndCommit lädt die fehlenden Blobs von req.Files aus stagingDir hoch
// und reicht req ein. Vermisst der Koordinator dabei einen Blob (412),
// prüft es die Segmente aller Dateien mit Rezept (auch wenn das Rezept da
// ist), lädt Fehlendes nach und reicht genau einmal neu ein. Schlägt der
// Upload fehl, wird nichts eingereicht.
func UploadAndCommit(ctx context.Context, c *client.Client, stagingDir string, req api.CommitRequest) (*api.Generation, error) {
	gen, _, err := UploadAndCommitCounted(ctx, c, stagingDir, req)
	return gen, err
}

// UploadAndCommitCounted ist UploadAndCommit und meldet zusätzlich, wie
// viele Bytes dabei hochgingen: nur die Blobs, die dem Koordinator fehlten
// (Segmente, ganze Dateien, Rezepte), nicht die Größe der Welt. Bei einem
// Fehler zählen die bis dahin vollständig hochgeladenen Blobs.
func UploadAndCommitCounted(ctx context.Context, c *client.Client, stagingDir string, req api.CommitRequest) (*api.Generation, int64, error) {
	var sent atomic.Int64
	if err := upload(ctx, c, stagingDir, req.Files, false, &sent); err != nil {
		return nil, sent.Load(), err
	}
	gen, err := Commit(ctx, c, req)
	if !errors.Is(err, ErrMissingBlobs) {
		return gen, sent.Load(), err
	}
	log.Printf("worldsync: Koordinator vermisst Blobs, prüfe alle Segmente und lade nach: %v", err)
	if err := upload(ctx, c, stagingDir, req.Files, true, &sent); err != nil {
		return nil, sent.Load(), err
	}
	gen, err = Commit(ctx, c, req)
	return gen, sent.Load(), err
}

// --- Herunterladen ---

// Download baut den Stand gen im Weltordner worldDir auf (der Server läuft
// dabei nicht). Erst wird das ganze Manifest geprüft; jeder Pfad, für den
// api.ValidPath falsch ist oder der außerhalb von worldDir landen würde,
// doppelte Pfade, Pfade, die zugleich Datei und Ordner wären, die
// Markierung .wanderwelt-download als Pfad, ungültige Hashes und ein
// Manifest ohne Dateien lassen Download ohne jede Änderung scheitern.
//
// Vor der ersten Änderung legt Download die Markierung .wanderwelt-download
// in worldDir an und entfernt sie erst, wenn alles geklappt hat; nach einem
// Fehler meldet DownloadIncomplete den Ordner als unvollständig.
//
// Dann entfernt Download alles, was nicht im Manifest steht (session.lock im
// Wurzelordner bleibt), samt Symlinks und Junctions, ohne ihnen zu folgen.
// Dateien mit passender Größe und passendem Hash (aus hc oder berechnet)
// bleiben. Für die übrigen:
//  1. Rezepte kommen aus der lokalen Ablage oder gebündelt vom Koordinator;
//     jedes muss die Dateigröße ergeben.
//  2. Liegt am Ziel schon eine Datei, wird sie zerlegt; ihre Segmente
//     werden wiederverwendet, geholt werden nur die übrigen, gebündelt
//     (POST /v1/blobs/fetch, je Anfrage höchstens api.TransferMaxBytes).
//  3. Die Datei entsteht in einer Temp-Datei neben dem Ziel, wird nach
//     Größe und SHA-256 der ganzen Datei geprüft und dann umbenannt.
//
// Dateien ohne Rezept kommen als ein Blob. Bis zu vier Dateien laden
// gleichzeitig. In ein Ziel wird nie direkt geschrieben, auch nicht über
// einen harten Link. Hash und Rezept jeder Datei landen in hc. Nur mit hc
// räumt Download danach die lokalen Rezepte auf (behalten werden die dieser
// Generation und die, die hc nennt).
//
// progress (falls nicht nil) bekommt geladene bzw. schon vorhandene Bytes
// und die Gesamtgröße, immer aus der Goroutine von Download.
//
// Der Aufrufer stellt sicher, dass worldDir keinen Stand mehr enthält, der
// noch eingereicht werden soll: Was nicht im Manifest steht, ist danach weg.
// Er stellt auch sicher, dass auf diesem PC währenddessen kein Stage,
// BuildManifest oder Upload läuft (gemeinsame Rezeptablage und Hash-Cache).
func Download(ctx context.Context, c *client.Client, gen *api.Generation, worldDir string, hc *HashCache, progress func(done, total int64)) error {
	if gen == nil {
		return errors.New("Download: keine Generation")
	}
	root, err := filepath.Abs(worldDir)
	if err != nil {
		return err
	}
	want, dirs, err := checkManifest(root, gen.Files)
	if err != nil {
		return fmt.Errorf("Download von Generation %s/%d: %w", gen.WorldID, gen.Number, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := requireRealDir(root); err != nil {
		return err
	}
	// Über Temp-Datei und Umbenennen: Ein Verweis an dieser Stelle wird
	// ersetzt, nicht verfolgt.
	marker := filepath.Join(root, downloadMarker)
	note := fmt.Sprintf("Download von Generation %s/%d nicht abgeschlossen\n", gen.WorldID, gen.Number)
	if err := writeFileAtomic(marker, []byte(note)); err != nil {
		return fmt.Errorf("Download: Markierung anlegen: %w", err)
	}
	if err := cleanWorld(root, want, dirs); err != nil {
		return fmt.Errorf("Download: Weltordner aufräumen: %w", err)
	}

	p := &progressCounter{fn: progress}
	for _, f := range want {
		p.total += f.Size
	}
	p.report()
	made := map[string]bool{}
	seen := map[string]bool{}
	var todo []api.FileEntry
	for _, rel := range slices.Sorted(maps.Keys(want)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := want[rel]
		target := filepath.Join(root, filepath.FromSlash(rel))
		seen[target] = true
		if err := ensureDirs(root, rel, made); err != nil {
			return fmt.Errorf("Download: %w", err)
		}
		current, err := upToDate(target, f, hc)
		if err != nil {
			return fmt.Errorf("Download: %w", err)
		}
		if current {
			p.add(f.Size)
			continue
		}
		todo = append(todo, f)
	}

	d := &downloader{c: c, root: root, hc: hc}
	if d.recipes, err = fetchRecipes(ctx, c, todo); err != nil {
		return fmt.Errorf("Download: %w", err)
	}
	if err := d.run(ctx, todo, p); err != nil {
		return fmt.Errorf("Download: %w", err)
	}
	p.report()
	hc.prune(root, seen)
	hc.saveQuietly()
	// Lokale Rezepte: nur die dieser Generation und die, die der Cache noch
	// nennt (wie nach BuildManifest); sonst sammeln sich auf einem PC, der
	// nur lädt, die Rezepte aller Stände und veralteter Dateien an. Nur mit
	// Cache: Ohne ihn weiß Download nicht, welche Rezepte eine Bereitstellung
	// noch braucht, und ließe deren Upload scheitern.
	if hc != nil {
		keep := hc.recipes()
		for _, f := range want {
			if f.Recipe != "" {
				keep[f.Recipe] = true
			}
		}
		pruneRecipes(keep)
	}
	if err := os.Remove(marker); err != nil {
		return fmt.Errorf("Download: Markierung entfernen: %w", err)
	}
	return nil
}

// DownloadIncomplete meldet, ob in worldDir ein Download angefangen, aber
// nicht abgeschlossen wurde (Markierung .wanderwelt-download). Der Inhalt
// ist dann kein gültiger Stand: nicht hochladen, sondern neu herunterladen.
// Lässt sich das nicht feststellen, ist die Antwort ebenfalls true.
func DownloadIncomplete(worldDir string) bool {
	_, err := os.Lstat(filepath.Join(worldDir, downloadMarker))
	return !errors.Is(err, fs.ErrNotExist)
}

// fetchRecipes liefert die Rezepte der Dateien todo: aus der lokalen Ablage,
// sonst gebündelt vom Koordinator (dann lokal abgelegt). Jedes muss die
// Größe seiner Datei ergeben.
func fetchRecipes(ctx context.Context, c *client.Client, todo []api.FileEntry) (map[string][]api.Segment, error) {
	out := map[string][]api.Segment{}
	var need []string
	asked := map[string]bool{}
	for _, f := range todo {
		if f.Recipe == "" || asked[f.Recipe] {
			continue
		}
		asked[f.Recipe] = true
		if segs, err := loadRecipe(f.Recipe); err == nil {
			out[f.Recipe] = segs
		} else {
			need = append(need, f.Recipe)
		}
	}
	for batch := range slices.Chunk(need, recipeBatch) {
		rc, err := c.FetchBlobs(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("Rezepte holen: %w", err)
		}
		err = func() error {
			defer rc.Close()
			for _, sha := range batch {
				var head [8]byte
				if _, err := io.ReadFull(rc, head[:]); err != nil {
					return fmt.Errorf("Rezept %s: %w", sha, err)
				}
				n, err := api.RecipeLen(head[:])
				if err != nil {
					return fmt.Errorf("Rezept %s: %w", sha, err)
				}
				data := make([]byte, n)
				copy(data, head[:])
				if _, err := io.ReadFull(rc, data[len(head):]); err != nil {
					return fmt.Errorf("Rezept %s: %w", sha, err)
				}
				segs, err := parseRecipe(sha, data)
				if err != nil {
					return err
				}
				if err := storeRecipe(sha, data); err != nil {
					log.Printf("worldsync: Rezept %s nicht lokal abgelegt: %v", sha, err)
				}
				out[sha] = segs
			}
			return expectEOF(rc)
		}()
		if err != nil {
			return nil, fmt.Errorf("Rezepte holen: %w", err)
		}
	}
	for _, f := range todo {
		if f.Recipe == "" {
			continue
		}
		if _, err := recipeFor(f.Recipe, f.Size, func(sha string) ([]api.Segment, error) { return out[sha], nil }); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return out, nil
}

// expectEOF prüft, dass r zu Ende ist.
func expectEOF(r io.Reader) error {
	var b [1]byte
	if n, err := r.Read(b[:]); n > 0 || err != io.EOF {
		if err != nil && err != io.EOF {
			return err
		}
		return errors.New("Antwort länger als erwartet")
	}
	return nil
}

// downloader lädt Dateien eines Downloads.
type downloader struct {
	c       *client.Client
	root    string
	hc      *HashCache
	recipes map[string][]api.Segment
}

// run lädt todo mit bis zu downloadWorkers Dateien gleichzeitig. Den
// Fortschritt meldet nur die aufrufende Goroutine.
func (d *downloader) run(ctx context.Context, todo []api.FileEntry, p *progressCounter) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan api.FileEntry)
	prog := make(chan int64, 64)
	report := func(n int64) { prog <- n }
	var wg sync.WaitGroup
	for range min(downloadWorkers, len(todo)) {
		wg.Go(func() {
			for f := range jobs {
				if ctx.Err() != nil {
					continue
				}
				if err := d.file(ctx, f, report); err != nil {
					cancel(fmt.Errorf("%s: %w", f.Path, err))
				}
			}
		})
	}
	go func() {
	feed:
		for _, f := range todo {
			select {
			case jobs <- f:
			case <-ctx.Done():
				break feed
			}
		}
		close(jobs)
		wg.Wait()
		close(prog)
	}()
	for n := range prog {
		p.add(n)
	}
	return context.Cause(ctx)
}

// file lädt eine Datei in eine Temp-Datei neben ihrem Ziel, prüft Größe und
// SHA-256 und benennt sie um.
func (d *downloader) file(ctx context.Context, f api.FileEntry, report func(int64)) error {
	target := filepath.Join(d.root, filepath.FromSlash(f.Path))
	tmp, err := os.CreateTemp(filepath.Dir(target), tmpPattern)
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()

	whole := sha256.New()
	out := &checkedWriter{w: tmp, limit: f.Size, h: whole, report: report}
	if f.Recipe == "" {
		// GetBlob prüft den Hash, out die Größe.
		if err := d.c.GetBlob(ctx, f.SHA256, out); err != nil {
			return err
		}
	} else if err := d.assemble(ctx, target, d.recipes[f.Recipe], out); err != nil {
		return err
	}
	if out.n != f.Size {
		return fmt.Errorf("%d Bytes geladen, erwartet %d", out.n, f.Size)
	}
	if got := hex.EncodeToString(whole.Sum(nil)); got != f.SHA256 {
		return fmt.Errorf("Inhalt hat den Hash %s, erwartet %s", got, f.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameRetry(name, target); err != nil {
		return err
	}
	ok = true
	if info, err := os.Lstat(target); err == nil {
		// Aus dem Rezept zusammengesetzt und gegen den Hash der ganzen Datei
		// geprüft: Das Rezept beschreibt genau diesen Inhalt.
		d.hc.put(target, info, cacheEntry{SHA256: f.SHA256, Split: f.Recipe != "", Recipe: f.Recipe})
	}
	return nil
}

// assemble schreibt die Segmente segs der Reihe nach nach out. Segmente,
// die die vorhandene Datei am Ziel schon enthält, kommen von dort, die
// übrigen gebündelt vom Koordinator; jedes wird gegen seinen Hash geprüft.
func (d *downloader) assemble(ctx context.Context, target string, segs []api.Segment, out io.Writer) error {
	have := map[string][]byte{}
	if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() && info.Size() <= maxSplitSize {
		if data, err := readStable(target, info); err == nil {
			if local, parts, ok := SplitRegion(data); ok {
				for i, s := range local {
					have[s.SHA256] = parts[i]
				}
			}
		}
	}
	var need []api.Segment
	queued := map[string]bool{}
	for _, s := range segs {
		if _, ok := have[s.SHA256]; ok || queued[s.SHA256] {
			continue
		}
		queued[s.SHA256] = true
		if s.Size <= maxSegment {
			need = append(need, s)
			continue
		}
		// Zu groß für eine gebündelte Antwort: einzeln, ohne Größengrenze.
		var buf bytes.Buffer
		w := &checkedWriter{w: &buf, limit: s.Size, h: sha256.New(), report: func(int64) {}}
		if err := d.c.GetBlob(ctx, s.SHA256, w); err != nil {
			return err
		}
		have[s.SHA256] = buf.Bytes()
	}
	for len(need) > 0 {
		n, size := 0, int64(0)
		for n < len(need) && n < api.FetchMaxBlobs && (n == 0 || size+need[n].Size <= api.TransferMaxBytes) {
			size += need[n].Size
			n++
		}
		if err := d.fetchSegments(ctx, need[:n], have); err != nil {
			return err
		}
		need = need[n:]
	}
	for _, s := range segs {
		b := have[s.SHA256]
		if int64(len(b)) != s.Size {
			return fmt.Errorf("Segment %s hat %d Bytes, im Rezept %d", s.SHA256, len(b), s.Size)
		}
		if _, err := out.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// fetchSegments holt segs in einer Anfrage und legt sie in have ab.
func (d *downloader) fetchSegments(ctx context.Context, segs []api.Segment, have map[string][]byte) error {
	hashes := make([]string, len(segs))
	for i, s := range segs {
		hashes[i] = s.SHA256
	}
	rc, err := d.c.FetchBlobs(ctx, hashes)
	if err != nil {
		return err
	}
	defer rc.Close()
	for _, s := range segs {
		b := make([]byte, s.Size)
		if _, err := io.ReadFull(rc, b); err != nil {
			return fmt.Errorf("Segment %s: %w", s.SHA256, err)
		}
		if got := sha256.Sum256(b); hex.EncodeToString(got[:]) != s.SHA256 {
			return fmt.Errorf("Segment %s: Inhalt hat einen anderen Hash", s.SHA256)
		}
		have[s.SHA256] = b
	}
	return expectEOF(rc)
}

// checkManifest prüft die Dateiliste und liefert die zu schreibenden
// Dateien (ohne session.lock) und alle Ordner darüber, jeweils Pfad mit "/".
func checkManifest(root string, files []api.FileEntry) (map[string]api.FileEntry, map[string]bool, error) {
	want := map[string]api.FileEntry{}
	lower := map[string]bool{}
	for _, f := range files {
		if !api.ValidPath(f.Path) || !filepath.IsLocal(filepath.FromSlash(f.Path)) {
			return nil, nil, fmt.Errorf("unzulässiger Pfad %q", f.Path)
		}
		if rel, err := filepath.Rel(root, filepath.Join(root, filepath.FromSlash(f.Path))); err != nil || !filepath.IsLocal(rel) {
			return nil, nil, fmt.Errorf("Pfad %q liegt außerhalb des Weltordners", f.Path)
		}
		if !validSHA(f.SHA256) {
			return nil, nil, fmt.Errorf("ungültiger Hash %q für %s", f.SHA256, f.Path)
		}
		if f.Recipe != "" && !validSHA(f.Recipe) {
			return nil, nil, fmt.Errorf("ungültiges Rezept %q für %s", f.Recipe, f.Path)
		}
		if f.Size < 0 {
			return nil, nil, fmt.Errorf("ungültige Größe %d für %s", f.Size, f.Path)
		}
		if first, _, _ := strings.Cut(f.Path, "/"); isMarker(first) {
			return nil, nil, fmt.Errorf("Pfad %q ist der Markierung vorbehalten", f.Path)
		}
		// Unter Windows wären das dieselbe Datei.
		key := strings.ToLower(f.Path)
		if lower[key] {
			return nil, nil, fmt.Errorf("Pfad %q doppelt", f.Path)
		}
		lower[key] = true
		if !isLockFile(f.Path) {
			want[f.Path] = f
		}
	}
	for key := range lower {
		for i := 0; i < len(key); i++ {
			if key[i] == '/' && lower[key[:i]] {
				return nil, nil, fmt.Errorf("%q ist Datei und zugleich Ordner von %q", key[:i], key)
			}
		}
	}
	if len(want) == 0 {
		return nil, nil, errors.New("Manifest ohne Dateien")
	}
	dirs := map[string]bool{}
	for p := range want {
		for i := 0; i < len(p); i++ {
			if p[i] == '/' {
				dirs[p[:i]] = true
			}
		}
	}
	return want, dirs, nil
}

// cleanWorld entfernt unter root alles, was nicht als Datei in want oder als
// Ordner in dirs vorkommt, außer session.lock und der Markierung des
// laufenden Downloads. Symlinks, Junctions und Sonderdateien werden selbst
// entfernt; ihnen wird nie gefolgt.
func cleanWorld(root string, want map[string]api.FileEntry, dirs map[string]bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := slashRel(root, path)
		if err != nil {
			return err
		}
		switch t := d.Type(); {
		case t == fs.ModeDir:
			if dirs[rel] {
				return nil
			}
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return fs.SkipDir
		case t.IsRegular():
			if _, ok := want[rel]; ok || isLockFile(rel) || isMarker(rel) {
				return nil
			}
		}
		return os.Remove(path)
	})
}

// ensureDirs legt die Ordner über der Datei rel unter root als echte Ordner
// an. Liegt an ihrer Stelle etwas anderes (Datei, Symlink, Junction), wird
// es entfernt, nicht verfolgt. made merkt sich geprüfte Ordner.
func ensureDirs(root, rel string, made map[string]bool) error {
	parts := strings.Split(rel, "/")
	dir := root
	for i, part := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, part)
		key := strings.Join(parts[:i+1], "/")
		if made[key] {
			continue
		}
		info, err := os.Lstat(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case isRealDir(info):
			made[key] = true
			continue
		default:
			if err := os.Remove(dir); err != nil {
				return err
			}
		}
		if err := os.Mkdir(dir, 0o755); err != nil {
			return err
		}
		made[key] = true
	}
	return nil
}

// upToDate meldet, ob target schon den Inhalt von f hat. Liegt dort etwas
// anderes als eine reguläre Datei, wird es entfernt. Eine Regionsdatei wird
// dabei auch zerlegt, damit hc ihr Rezept kennt.
func upToDate(target string, f api.FileEntry, hc *HashCache) (bool, error) {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case isRealDir(info):
		return false, os.RemoveAll(target)
	case !info.Mode().IsRegular():
		return false, os.Remove(target)
	case info.Size() != f.Size:
		return false, nil
	}
	region := isRegion(f.Path) && info.Size() <= maxSplitSize
	if e, ok := hc.lookup(target, info); ok && (!region || e.Split) {
		return e.SHA256 == f.SHA256, nil
	}
	if !region {
		sha, err := hashFile(target, info)
		if err != nil {
			return false, err
		}
		hc.put(target, info, cacheEntry{SHA256: sha})
		return sha == f.SHA256, nil
	}
	whole, recipe, err := splitFile(target, info)
	if err != nil {
		return false, err
	}
	hc.put(target, info, cacheEntry{SHA256: whole, Split: true, Recipe: recipe})
	return whole == f.SHA256, nil
}

// checkedWriter schreibt höchstens limit Bytes nach w, hasht in h mit und
// meldet jeden Schreibvorgang an report.
type checkedWriter struct {
	w      io.Writer
	limit  int64
	n      int64
	h      hash.Hash
	report func(int64)
}

func (cw *checkedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > cw.limit-cw.n {
		return 0, fmt.Errorf("Daten länger als die %d Bytes im Manifest", cw.limit)
	}
	n, err := cw.w.Write(b)
	cw.h.Write(b[:n])
	cw.n += int64(n)
	cw.report(int64(n))
	return n, err
}

// progressCounter ruft fn nach jeder Datei und spätestens alle progressStep
// Bytes.
type progressCounter struct {
	fn                func(done, total int64)
	done, total, last int64
}

func (p *progressCounter) add(n int64) {
	p.done += n
	if p.done-p.last >= progressStep {
		p.report()
	}
}

func (p *progressCounter) report() {
	p.last = p.done
	if p.fn != nil {
		p.fn(p.done, p.total)
	}
}

// --- Hilfen ---

// hashFile liefert den SHA-256 von path; before ist Lstat vor dem Lesen.
// Ändert sich die Datei dabei, ist das ein Fehler.
func hashFile(path string, before fs.FileInfo) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", fmt.Errorf("%s hashen: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if n != before.Size() || !sameState(before, after) {
		return "", fmt.Errorf("%s hat sich beim Hashen geändert", path)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readStable liest path ganz; before ist Lstat vor dem Lesen. Ändert sich
// die Datei dabei, ist das ein Fehler.
func readStable(path string, before fs.FileInfo) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, before.Size()+1))
	if err != nil {
		return nil, fmt.Errorf("%s lesen: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != before.Size() || !sameState(before, after) {
		return nil, fmt.Errorf("%s hat sich beim Lesen geändert", path)
	}
	return data, nil
}

// sameState: gleiche Größe und gleiche Änderungszeit.
func sameState(a, b fs.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// isRealDir: ein Ordner, kein Symlink und keine Junction (die meldet Go
// unter Windows als ModeIrregular).
func isRealDir(info fs.FileInfo) bool {
	return info.Mode().Type() == fs.ModeDir
}

func requireRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !isRealDir(info) {
		return fmt.Errorf("%s ist kein Ordner (oder ein Verweis)", path)
	}
	return nil
}

// isLockFile: rel (mit "/") ist die Sperrdatei im Wurzelordner der Welt.
func isLockFile(rel string) bool {
	return strings.EqualFold(rel, lockFile)
}

// isTempName: name ist eine Temp-Datei von wanderwelt (tmpPattern), etwa
// der Rest einer abgebrochenen Kopie oder eines abgebrochenen Downloads.
func isTempName(name string) bool {
	ok, _ := filepath.Match(tmpPattern, name)
	return ok
}

// isMarker: rel (mit "/") ist die Download-Markierung im Wurzelordner.
func isMarker(rel string) bool {
	return strings.EqualFold(rel, downloadMarker)
}

// within meldet, ob path gleich root ist oder darunter liegt (ohne
// Rücksicht auf Groß/Klein, wie unter Windows).
func within(root, path string) bool {
	rel, err := filepath.Rel(strings.ToLower(root), strings.ToLower(path))
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func slashRel(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// validSHA: genau 64 Hex-Zeichen in Kleinbuchstaben (wie der Koordinator).
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

// writeFileAtomic schreibt data über eine Temp-Datei neben path.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
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
