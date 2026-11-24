// Package deps holt, was der Host braucht (Java, Fabric-Server-Launcher,
// Mods, tunnel.exe), prüft dabei die Hashes aus dem Versions-Manifest und
// pflegt das Launcher-Profil "wanderwelt" (launcher.go).
//
// Quellen: https-, http- und file-URLs sowie "wanderwelt:<name>" – die Datei
// liefert der Koordinator selbst aus (GET /v1/artifacts/<name>, über den
// angemeldeten Client).
//
// Ablage: <Home>/deps/<name>/… – je Artefakt ein eigener Ordner, der nur
// den aktuellen Stand enthält. Einzeldateien liegen direkt darin, Zip-Archive
// (JRE, tunnel als Release-Zip) werden in <name>/<hash16>/ entpackt; eine
// Markierungsdatei mit dem Hash, die zuletzt geschrieben wird, zeigt an, dass
// das Entpacken vollständig war.
package deps

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Paths sind die fertigen Pfade nach Ensure.
type Paths struct {
	Java            string // bin/java.exe des JRE
	FabricServerJar string
	Tunnel          string
	Mods            []string // in Manifest-Reihenfolge, ohne übersprungene
}

// ArtifactSource liefert Artefakte mit der URL wanderwelt:<name> vom
// Koordinator (*client.Client); size ist -1, wenn unbekannt.
type ArtifactSource interface {
	Artifact(ctx context.Context, name string) (body io.ReadCloser, size int64, err error)
}

// coordinatorScheme ist das URL-Schema für Artefakte vom Koordinator.
const coordinatorScheme = "wanderwelt"

const (
	userAgent = "wanderwelt-starter"
	// markerFile steht im Entpack-Ordner und enthält den Hash des Archivs.
	markerFile = ".wanderwelt-hash"
	// maxExtract begrenzt, was ein Archiv entpackt auf die Platte bringen
	// darf (das JRE braucht rund 150 MB).
	maxExtract int64 = 2 << 30
	// stallTimeout bricht einen Download ab, der so lange keine Daten liefert.
	stallTimeout = 60 * time.Second
)

var httpClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}()

// Ensure sorgt dafür, dass alles aus dem Manifest unter <Home>/deps liegt,
// und liefert die Pfade. Vorhandenes mit passendem Hash wird nicht neu
// geladen. Optionale Mods, die fehlschlagen, werden nur geloggt; C2ME wird
// nur geladen, wenn profile.C2ME gesetzt ist. src lädt Einträge mit der URL
// wanderwelt:<name> (nil: solche Einträge scheitern). progress (darf nil
// sein) wird nur bei echten Downloads aufgerufen; total ist -1, wenn die
// Größe unbekannt ist.
//
// Ensure entfernt ältere Stände in den Artefakt-Ordnern und darf deshalb
// nicht laufen, während ein Server aus diesen Dateien läuft.
func Ensure(ctx context.Context, m *api.Manifest, profile api.Profile, src ArtifactSource, progress func(name string, done, total int64)) (*Paths, error) {
	if m == nil {
		return nil, errors.New("deps: kein Manifest")
	}
	if err := checkNames(m); err != nil {
		return nil, err
	}
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	f := &fetcher{base: filepath.Join(group.Home(), "deps"), progress: progress, src: src}

	var p Paths
	var err error

	if !isZip(m.Java) {
		return nil, fmt.Errorf("deps: %s: das JRE muss als Zip vorliegen", m.Java.Name)
	}
	if p.Java, err = f.ensureArchive(ctx, m.Java, "bin/"+exeName("java")); err != nil {
		return nil, err
	}
	if p.FabricServerJar, err = f.ensureFile(ctx, m.FabricServer, jarName(m.FabricServer)); err != nil {
		return nil, err
	}
	if isZip(m.Tunnel) {
		p.Tunnel, err = f.ensureArchive(ctx, m.Tunnel, exeName("tunnel"))
	} else {
		p.Tunnel, err = f.ensureFile(ctx, m.Tunnel, exeName("tunnel"))
	}
	if err != nil {
		return nil, err
	}

	for _, mod := range m.Mods {
		if isC2ME(mod.Name) && !profile.C2ME {
			continue
		}
		mp, err := f.ensureFile(ctx, mod, jarName(mod))
		if err != nil {
			if mod.Optional && ctx.Err() == nil {
				log.Printf("deps: optionaler Mod %s fehlt, Server läuft ohne ihn: %v", mod.Name, err)
				continue
			}
			return nil, err
		}
		p.Mods = append(p.Mods, mp)
	}
	return &p, nil
}

// FetchFile legt das Artefakt a als dir/fileName ab (Selbst-Update des
// Starters): Liegt die Datei dort schon mit passendem Hash, wird nichts
// geladen; sonst lädt sie wie Ensure (wanderwelt:<name> über src) in eine
// temporäre Datei in dir, prüft den Hash und benennt erst dann um. size > 0
// ist die erwartete Größe in Bytes: Eine andere Größe ist ein Fehler, und
// mehr als size Bytes werden gar nicht erst geschrieben. Sonst begrenzt
// maxBytes (> 0) den Download. Andere Dateien in dir bleiben liegen.
func FetchFile(ctx context.Context, a api.Artifact, src ArtifactSource, dir, fileName string, size, maxBytes int64) (string, error) {
	w, err := parseHash(a.Hash)
	if err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	if !validName(fileName) {
		return "", fmt.Errorf("deps: %s: ungültiger Dateiname %q", a.Name, fileName)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	target := filepath.Join(dir, fileName)
	if fileMatches(target, w) && (size <= 0 || fileSize(target) == size) {
		return target, nil
	}
	f := &fetcher{base: dir, progress: func(string, int64, int64) {}, src: src, size: size, limit: maxBytes}
	tmp, err := f.download(ctx, a, w, dir)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	return target, nil
}

// ErrMismatch: Die geladene Datei passt nicht zu Hash oder Größe aus dem
// Manifest (oder ist größer als erlaubt) – ein neuer Versuch mit demselben
// Eintrag hilft nicht. Prüfen mit errors.Is.
var ErrMismatch = errors.New("Datei passt nicht zum Manifest")

type mismatchError struct{ msg string }

func (e *mismatchError) Error() string        { return e.msg }
func (e *mismatchError) Is(target error) bool { return target == ErrMismatch }

func mismatchf(format string, args ...any) error {
	return &mismatchError{msg: fmt.Sprintf(format, args...)}
}

// fileSize ist die Größe von p (-1, wenn nicht lesbar).
func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// Names liefert die Namen der Artefakte, die Ensure für profile prüft, in
// seiner Reihenfolge (für einen Gesamtfortschritt).
func Names(m *api.Manifest, profile api.Profile) []string {
	if m == nil {
		return nil
	}
	names := []string{m.Java.Name, m.FabricServer.Name, m.Tunnel.Name}
	for _, mod := range m.Mods {
		if isC2ME(mod.Name) && !profile.C2ME {
			continue
		}
		names = append(names, mod.Name)
	}
	return names
}

// isC2ME erkennt den Mod C2ME am Namen im Manifest ("c2me-fabric").
func isC2ME(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "c2me")
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// checkNames prüft, dass jeder Name ein einfacher, eindeutiger Ordnername
// ist; er wird zu <Home>/deps/<name>.
func checkNames(m *api.Manifest) error {
	all := append([]api.Artifact{m.Java, m.FabricServer, m.Tunnel}, m.Mods...)
	seen := make(map[string]bool, len(all))
	for _, a := range all {
		if !validName(a.Name) {
			return fmt.Errorf("deps: ungültiger Artefakt-Name %q", a.Name)
		}
		key := strings.ToLower(a.Name)
		if seen[key] {
			return fmt.Errorf("deps: Artefakt-Name %q steht doppelt im Manifest", a.Name)
		}
		seen[key] = true
	}
	return nil
}

// validName lässt nur einfache Datei- bzw. Ordnernamen zu.
func validName(s string) bool {
	if s == "" || len(s) > 128 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
}

// urlBase ist der letzte Pfadteil der URL ("" bei Fehlern), bei
// wanderwelt:<name> der Name.
func urlBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if strings.EqualFold(u.Scheme, coordinatorScheme) {
		return u.Opaque
	}
	return path.Base(u.Path)
}

func isZip(a api.Artifact) bool {
	return strings.HasSuffix(strings.ToLower(urlBase(a.URL)), ".zip")
}

// jarName ist der Dateiname aus der URL, wenn er ein brauchbarer .jar-Name
// ist, sonst <name>.jar (etwa beim Fabric-Launcher, dessen URL auf /jar endet).
func jarName(a api.Artifact) string {
	b := urlBase(a.URL)
	if validName(b) && strings.HasSuffix(strings.ToLower(b), ".jar") {
		return b
	}
	return a.Name + ".jar"
}

// ---- Hashes ----

type wantHash struct {
	algo string // "sha256" | "sha512"
	hex  string // klein geschrieben
}

func parseHash(s string) (wantHash, error) {
	algo, hx, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return wantHash{}, fmt.Errorf("Hash %q: erwartet \"sha256:<hex>\" oder \"sha512:<hex>\"", s)
	}
	algo, hx = strings.ToLower(algo), strings.ToLower(hx)
	var n int
	switch algo {
	case "sha256":
		n = sha256.Size * 2
	case "sha512":
		n = sha512.Size * 2
	default:
		return wantHash{}, fmt.Errorf("Hash %q: unbekanntes Verfahren %q", s, algo)
	}
	if len(hx) != n {
		return wantHash{}, fmt.Errorf("Hash %q: %s braucht %d Hex-Zeichen", s, algo, n)
	}
	if _, err := hex.DecodeString(hx); err != nil {
		return wantHash{}, fmt.Errorf("Hash %q: kein Hex", s)
	}
	return wantHash{algo: algo, hex: hx}, nil
}

func (w wantHash) String() string { return w.algo + ":" + w.hex }

func (w wantHash) new() hash.Hash {
	if w.algo == "sha512" {
		return sha512.New()
	}
	return sha256.New()
}

// fileMatches meldet, ob die Datei existiert und den erwarteten Hash hat.
func fileMatches(p string, w wantHash) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	h := w.new()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == w.hex
}

// ---- Laden ----

type fetcher struct {
	base     string
	progress func(name string, done, total int64)
	src      ArtifactSource
	// size > 0: erwartete Größe (FetchFile); limit > 0: höchstens so viele
	// Bytes laden. 0: wie das Manifest sagt bzw. ohne Grenze.
	size  int64
	limit int64
}

// dir legt <base>/<name> an und liefert den Pfad.
func (f *fetcher) dir(a api.Artifact) (string, error) {
	d := filepath.Join(f.base, a.Name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	return d, nil
}

// ensureFile legt das Artefakt als <dir>/<fileName> ab.
func (f *fetcher) ensureFile(ctx context.Context, a api.Artifact, fileName string) (string, error) {
	w, err := parseHash(a.Hash)
	if err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	d, err := f.dir(a)
	if err != nil {
		return "", err
	}
	target := filepath.Join(d, fileName)
	if fileMatches(target, w) {
		log.Printf("deps: %s vorhanden, Hash passt", a.Name)
		removeOthers(d, fileName)
		return target, nil
	}

	tmp, err := f.download(ctx, a, w, d)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	removeOthers(d, fileName)
	return target, nil
}

// ensureArchive entpackt das Zip-Artefakt nach <dir>/<hash16> und liefert
// den Pfad von find (mit "/" getrennt, relativ zum Entpack-Ordner oder zu
// dessen einzigem Unterordner, wie beim JRE).
func (f *fetcher) ensureArchive(ctx context.Context, a api.Artifact, find string) (string, error) {
	w, err := parseHash(a.Hash)
	if err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	d, err := f.dir(a)
	if err != nil {
		return "", err
	}
	key := w.hex[:16]
	root := filepath.Join(d, key)

	if b, err := os.ReadFile(filepath.Join(root, markerFile)); err == nil && strings.TrimSpace(string(b)) == w.String() {
		if p, err := findIn(root, find); err == nil {
			log.Printf("deps: %s vorhanden, Hash passt", a.Name)
			removeOthers(d, key)
			return p, nil
		}
	}

	tmp, err := f.download(ctx, a, w, d)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)

	// Unvollständige Reste (ohne Markierung) weg, dann neu entpacken; die
	// Markierung kommt zuletzt.
	if err := os.RemoveAll(root); err != nil {
		return "", fmt.Errorf("deps: %s: alter Entpack-Ordner: %w", a.Name, err)
	}
	if err := unzip(tmp, root); err != nil {
		os.RemoveAll(root)
		return "", fmt.Errorf("deps: %s: entpacken: %w", a.Name, err)
	}
	p, err := findIn(root, find)
	if err != nil {
		os.RemoveAll(root)
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(p, 0o755); err != nil {
			return "", fmt.Errorf("deps: %s: %w", a.Name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, markerFile), []byte(w.String()+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	removeOthers(d, key)
	return p, nil
}

// download lädt a.URL in eine temporäre Datei in dir und prüft den Hash.
// Bei Erfolg liefert es den Pfad der temporären Datei.
func (f *fetcher) download(ctx context.Context, a api.Artifact, w wantHash, dir string) (string, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	log.Printf("deps: lade %s von %s", a.Name, a.URL)
	src, total, err := f.open(ctx, a.URL)
	if err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	// Liefert die Quelle stallTimeout lang nichts, wird abgebrochen.
	stall := time.AfterFunc(stallTimeout, func() {
		cancel(fmt.Errorf("%s lang keine Daten", stallTimeout))
	})
	defer stall.Stop()

	// Mehr als erwartet (size) oder erlaubt (limit) wird nicht geschrieben.
	limit := f.limit
	if f.size > 0 {
		limit = f.size
		if total >= 0 && total != f.size {
			return "", mismatchf("deps: %s: der Koordinator meldet %d Bytes, erwartet %d", a.Name, total, f.size)
		}
	}
	h := w.new()
	var done int64
	f.progress(a.Name, 0, total)
	buf := make([]byte, 64<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if limit > 0 && done+int64(n) > limit {
				return "", mismatchf("deps: %s: Download größer als %d Bytes", a.Name, limit)
			}
			stall.Reset(stallTimeout)
			if _, err := tmp.Write(buf[:n]); err != nil {
				return "", fmt.Errorf("deps: %s: %w", a.Name, err)
			}
			h.Write(buf[:n])
			done += int64(n)
			f.progress(a.Name, done, total)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if c := context.Cause(ctx); c != nil {
				rerr = c
			}
			return "", fmt.Errorf("deps: %s: Download: %w", a.Name, rerr)
		}
	}
	if total >= 0 && done != total {
		return "", fmt.Errorf("deps: %s: Download unvollständig (%d von %d Bytes)", a.Name, done, total)
	}
	if f.size > 0 && done != f.size {
		return "", mismatchf("deps: %s: Download hat %d Bytes, erwartet %d", a.Name, done, f.size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != w.hex {
		return "", mismatchf("deps: %s: Hash passt nicht (erwartet %s, erhalten %s:%s)", a.Name, w, w.algo, got)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("deps: %s: %w", a.Name, err)
	}
	ok = true
	return tmp.Name(), nil
}

// open öffnet eine https-, http-, file- oder wanderwelt-URL und liefert die
// Größe (-1, wenn unbekannt).
func (f *fetcher) open(ctx context.Context, raw string) (io.ReadCloser, int64, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("URL %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case coordinatorScheme:
		name := u.Opaque
		if !validArtifactName(name) {
			return nil, 0, fmt.Errorf("URL %q: erwartet %s:<name> mit einfachem Dateinamen (Buchstaben, Ziffern, . _ -)", raw, coordinatorScheme)
		}
		if f.src == nil {
			return nil, 0, fmt.Errorf("URL %q: Artefakte vom Koordinator brauchen eine Verbindung zum Koordinator", raw)
		}
		body, size, err := f.src.Artifact(ctx, name)
		if err != nil {
			return nil, 0, err
		}
		return body, size, nil
	case "https", "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, 0, fmt.Errorf("%s: HTTP %s", raw, resp.Status)
		}
		return resp.Body, resp.ContentLength, nil
	case "file":
		p, err := filePath(u)
		if err != nil {
			return nil, 0, err
		}
		fh, err := os.Open(p)
		if err != nil {
			return nil, 0, err
		}
		fi, err := fh.Stat()
		if err != nil {
			fh.Close()
			return nil, 0, err
		}
		if !fi.Mode().IsRegular() {
			fh.Close()
			return nil, 0, fmt.Errorf("%s ist keine Datei", p)
		}
		return &ctxReader{ctx: ctx, r: fh}, fi.Size(), nil
	case "":
		return nil, 0, fmt.Errorf("URL %q: keine URL (leer oder ohne Schema)", raw)
	default:
		return nil, 0, fmt.Errorf("URL %q: Schema %q wird nicht unterstützt", raw, u.Scheme)
	}
}

// filePath macht aus file:///C:/x/y.exe bzw. file:///home/x den lokalen Pfad;
// file://localhost/… ist erlaubt, andere Hosts nicht.
func filePath(u *url.URL) (string, error) {
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", fmt.Errorf("file-URL mit Host %q wird nicht unterstützt", u.Host)
	}
	p := u.Path
	if p == "" {
		p = u.Opaque
	}
	if p == "" {
		return "", errors.New("file-URL ohne Pfad")
	}
	// "/C:/…" → "C:/…"
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}

// validArtifactName: wie auf dem Koordinator – 1–128 Zeichen aus
// Buchstaben, Ziffern, ".", "_" und "-", nicht "." oder "..".
func validArtifactName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 128 {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// ctxReader bricht das Lesen einer lokalen Datei ab, wenn ctx endet.
type ctxReader struct {
	ctx context.Context
	r   io.ReadCloser
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func (c *ctxReader) Close() error { return c.r.Close() }

// removeOthers entfernt alles in dir außer keep (ältere Stände, Reste
// abgebrochener Downloads). Fehler werden übergangen.
func removeOthers(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("deps: alter Stand %s nicht entfernt: %v", filepath.Join(dir, e.Name()), err)
		}
	}
}

// ---- Zip ----

// unzip entpackt zipPath nach dest. Einträge mit absoluten Pfaden, "..",
// Laufwerksangaben oder Backslashes sowie Links werden abgelehnt.
func unzip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var total int64
	for _, zf := range r.File {
		rel, err := safeRel(zf.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case mode.IsRegular():
			n, err := extractFile(zf, target, maxExtract-total)
			if err != nil {
				return err
			}
			total += n
		default:
			return fmt.Errorf("Eintrag %q: Typ %v wird nicht unterstützt", zf.Name, mode.Type())
		}
	}
	return nil
}

// extractFile schreibt einen Zip-Eintrag nach target, höchstens limit Bytes.
func extractFile(zf *zip.File, target string, limit int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, err
	}
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	perm := fs.FileMode(0o644)
	if zf.Mode()&0o111 != 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("Archiv entpackt mehr als %d MiB", maxExtract>>20)
	}
	return n, nil
}

// safeRel prüft einen Zip-Eintragsnamen und liefert ihn bereinigt ("" für
// den Wurzeleintrag).
func safeRel(name string) (string, error) {
	bad := func() (string, error) { return "", fmt.Errorf("unsicherer Pfad im Archiv: %q", name) }
	if name == "" || strings.ContainsAny(name, `\:`) || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return bad()
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return bad()
	}
	return clean, nil
}

// findIn sucht rel (mit "/" getrennt) in root und in dessen direkten
// Unterordnern.
func findIn(root, rel string) (string, error) {
	try := func(dir string) (string, bool) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Stat(p)
		return p, err == nil && fi.Mode().IsRegular()
	}
	if p, ok := try(root); ok {
		return p, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			if p, ok := try(filepath.Join(root, e.Name())); ok {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s nicht im Archiv gefunden", rel)
}
