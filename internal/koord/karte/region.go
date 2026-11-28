package karte

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Speicherlayout (bestätigt an internal/starter/worldsync und
// internal/koord/store): Die Oberwelt liegt im Stand unter
// dimensions/minecraft/overworld/region/r.X.Z.mca. Eine Regionsdatei ist
// entweder ein einziger Blob (FileEntry.Recipe leer, Blob = SHA256) oder in
// Segmente zerlegt: FileEntry.Recipe nennt den Blob mit dem Rezept „WWR1“
// (api.ParseRecipe), und die Datei ist die Aneinanderreihung der Segment-Blobs
// in der Reihenfolge des Rezepts (Kopf von 8192 Byte, Chunk-Bereiche, Lücken,
// alles unkomprimiert). FileEntry.SHA256 ist in beiden Fällen der Hash der
// ganzen Datei.
const (
	// RegionPrefix ist der Ordner der Oberwelt im Stand.
	RegionPrefix = "dimensions/minecraft/overworld/region/"
	// maxCoordDigits: so viele Ziffern hat eine Regionskoordinate höchstens.
	maxCoordDigits = 9
)

// Blobs ist der Lesezugriff auf den Speicher (store.Store).
type Blobs interface {
	OpenBlob(sha string) (io.ReadSeekCloser, int64, error)
}

// Region ist die Position einer Regionsdatei (512×512 Blöcke).
type Region struct{ X, Z int }

// Name ist der Dateiname r.X.Z.mca.
func (r Region) Name() string { return "r." + strconv.Itoa(r.X) + "." + strconv.Itoa(r.Z) + ".mca" }

// parseCoord liest eine ganze Zahl streng: optionales „-“, 1 bis 9 Ziffern,
// keine führende Null (außer „0“ selbst), kein „-0“, kein „+“, keine Leerzeichen.
func parseCoord(s string) (int, bool) {
	digits := strings.TrimPrefix(s, "-")
	neg := len(digits) != len(s)
	if len(digits) == 0 || len(digits) > maxCoordDigits {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	if digits[0] == '0' && (len(digits) > 1 || neg) {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// parseRegionPath erkennt den Pfad einer Oberwelt-Regionsdatei im Stand.
func parseRegionPath(p string) (Region, bool) {
	name, ok := strings.CutPrefix(p, RegionPrefix)
	if !ok || strings.Contains(name, "/") {
		return Region{}, false
	}
	name, ok = strings.CutSuffix(name, ".mca")
	if !ok {
		return Region{}, false
	}
	rest, ok := strings.CutPrefix(name, "r.")
	if !ok {
		return Region{}, false
	}
	// „r.-1.-2“: die Trennung ist der Punkt nach der ersten Zahl.
	i := strings.IndexByte(rest, '.')
	if i < 0 {
		return Region{}, false
	}
	x, ok1 := parseCoord(rest[:i])
	z, ok2 := parseCoord(rest[i+1:])
	if !ok1 || !ok2 {
		return Region{}, false
	}
	return Region{x, z}, true
}

// Regionen liefert die Oberwelt-Regionsdateien eines Stands. Pfade, die nicht
// genau dem Muster entsprechen (andere Ordner, andere Dimensionen, Unfug),
// zählen nicht.
func Regionen(files []api.FileEntry) map[Region]api.FileEntry {
	out := map[Region]api.FileEntry{}
	for _, f := range files {
		if r, ok := parseRegionPath(f.Path); ok {
			out[r] = f
		}
	}
	return out
}

// Geaenderte bestimmt, welche Regionen seit dem zuletzt gerenderten Stand
// (alt: Region → Hash der ganzen Datei) neu zu rendern sind. Das ist genau
// die Menge der Regionen, deren Segmente sich geändert haben: Ein Rezept
// listet Größe und Hash aller Segmente, und der Hash der ganzen Datei ändert
// sich, sobald ein Segment anders ist; ein Chunk-Blob, der nur in der Datei
// wandert, ändert den Hash der Datei ebenfalls, aber das Rendern ist dann
// auch nicht überflüssig (der Kopf ist anders). alle ist wahr, wenn nicht
// nachgeführt werden kann: kein alter Stand (nil), oder eine Region ist
// verschwunden (das Programm kann keine Kacheln streichen). Dann sind alle
// Regionen geändert. Das Ergebnis ist sortiert.
func Geaenderte(alt map[Region]string, neu map[Region]api.FileEntry) (changed []Region, alle bool) {
	alle = alt == nil
	for r := range alt {
		if _, ok := neu[r]; !ok {
			alle = true
		}
	}
	for r, f := range neu {
		if alle || alt[r] != f.SHA256 {
			changed = append(changed, r)
		}
	}
	sortRegions(changed)
	return changed, alle
}

func sortRegions(rs []Region) {
	slices.SortFunc(rs, func(a, b Region) int {
		if a.X != b.X {
			return a.X - b.X
		}
		return a.Z - b.Z
	})
}

// MitNachbarn liefert die Regionen aus changed und ihre acht Nachbarn, soweit
// sie in have vorhanden sind (das Programm schattiert am Rand mit dem
// Nachbarn), sortiert.
func MitNachbarn(changed []Region, have map[Region]api.FileEntry) []Region {
	set := map[Region]bool{}
	for _, r := range changed {
		for dx := -1; dx <= 1; dx++ {
			for dz := -1; dz <= 1; dz++ {
				n := Region{r.X + dx, r.Z + dz}
				if _, ok := have[n]; ok {
					set[n] = true
				}
			}
		}
	}
	out := make([]Region, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sortRegions(out)
	return out
}

// regionLine ist eine Zeile der Datei für --only-regions.
func regionLine(r Region) string { return strconv.Itoa(r.X) + " " + strconv.Itoa(r.Z) + "\n" }

// stateKey und parseStateKey: Schlüssel „X Z“ in state.json.
func stateKey(r Region) string { return strconv.Itoa(r.X) + " " + strconv.Itoa(r.Z) }

func parseStateKey(k string) (Region, bool) {
	a, b, ok := strings.Cut(k, " ")
	if !ok {
		return Region{}, false
	}
	x, ok1 := parseCoord(a)
	z, ok2 := parseCoord(b)
	return Region{x, z}, ok1 && ok2
}

// ErrRegionKaputt: Eine Regionsdatei lässt sich nicht aus dem Speicher
// zusammensetzen (Rezept oder Blob unlesbar, Größe oder Hash passen nicht).
var ErrRegionKaputt = errors.New("Regionsdatei nicht aus dem Speicher zusammensetzbar")

// Zusammensetzen schreibt die Regionsdatei f aus ihren Blobs nach w und prüft
// Länge und Hash der ganzen Datei gegen f. Es setzt nur aneinander (wie
// worldsync.JoinSegments), Chunks werden nicht gelesen. ctx bricht zwischen
// zwei Segmenten ab.
func Zusammensetzen(ctx context.Context, q Blobs, f api.FileEntry, w io.Writer) error {
	h := sha256.New()
	out := io.MultiWriter(w, h)
	var total int64
	copyBlob := func(sha string, want int64) error {
		rc, size, err := q.OpenBlob(sha)
		if err != nil {
			return err
		}
		defer rc.Close()
		if want >= 0 && size != want {
			return fmt.Errorf("Blob %s hat %d statt %d Bytes", sha, size, want)
		}
		n, err := io.Copy(out, rc)
		total += n
		return err
	}
	var err error
	if f.Recipe == "" {
		err = copyBlob(f.SHA256, f.Size)
	} else {
		var segs []api.Segment
		if segs, err = readRecipe(q, f.Recipe); err == nil {
			if sum, ok := api.RecipeSize(segs); !ok || sum != f.Size {
				err = fmt.Errorf("Rezept %s ergibt nicht %d Bytes", f.Recipe, f.Size)
			}
			for _, s := range segs {
				if err != nil {
					break
				}
				if err = ctx.Err(); err == nil {
					err = copyBlob(s.SHA256, s.Size)
				}
			}
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: %v", ErrRegionKaputt, f.Path, err)
	}
	if total != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("%w: %s: Länge oder Hash der zusammengesetzten Datei passen nicht", ErrRegionKaputt, f.Path)
	}
	return nil
}

// readRecipe liest und prüft das Rezept sha.
func readRecipe(q Blobs, sha string) ([]api.Segment, error) {
	rc, _, err := q.OpenBlob(sha)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, api.MaxRecipeSize+1))
	if err != nil {
		return nil, err
	}
	return api.ParseRecipe(data)
}

// RegionDatei setzt die Region r (Datei f) im Ordner dir als r.X.Z.mca
// zusammen. Der Dateiname entsteht nur aus den geprüften Zahlen.
func RegionDatei(ctx context.Context, q Blobs, f api.FileEntry, r Region, dir string) (err error) {
	file, err := os.OpenFile(filepath.Join(dir, r.Name()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := file.Close(); err == nil {
			err = cerr
		}
	}()
	return Zusammensetzen(ctx, q, f, file)
}
