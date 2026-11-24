package worldsync

// Rezepte: Eine zerlegte Regionsdatei steht im Manifest mit dem Hash ihres
// Rezepts (api.EncodeRecipe), das die Segmente listet. Der Starter hält eine
// Kopie jedes Rezepts, das er gebildet oder geladen hat, unter
// group.Home()/recipes/<sha256>; Upload liest dort die Segmentliste,
// Download spart sich das erneute Laden.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// maxSegment: Segmente über dieser Größe passen nicht in eine gebündelte
// Anfrage (api.TransferMaxBytes samt Rahmenkopf).
var maxSegment int64 = api.TransferMaxBytes - api.BatchFrameHeader

func recipeDir() string { return filepath.Join(group.Home(), "recipes") }

func recipePath(sha string) string { return filepath.Join(recipeDir(), sha) }

// recipeIntact meldet, ob die lokale Kopie des Rezepts sha unversehrt ist
// (Hash und Format). Ein Rezept ist klein (höchstens api.MaxRecipeSize);
// die Prüfung kostet einmal Lesen und Hashen. Ob es zur Dateigröße passt,
// prüfen Upload (recipeFor) und der Koordinator beim Commit.
func recipeIntact(sha string) bool {
	_, err := loadRecipe(sha)
	return err == nil
}

// dropRecipe löscht die lokale Kopie des Rezepts sha (fehlt sie, ist nichts
// zu tun). Danach gilt kein Cache-Eintrag mit diesem Rezept mehr, und
// BuildManifest zerlegt die Datei neu.
func dropRecipe(sha string) error {
	if err := os.Remove(recipePath(sha)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// storeRecipe legt das Rezept data (Hash sha) lokal ab, falls es fehlt oder
// die lokale Kopie beschädigt ist.
func storeRecipe(sha string, data []byte) error {
	if _, err := loadRecipe(sha); err == nil {
		return nil
	}
	if err := os.MkdirAll(recipeDir(), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(recipePath(sha), data)
}

// loadRecipe liest das lokale Rezept sha und prüft Hash und Format.
func loadRecipe(sha string) ([]api.Segment, error) {
	f, err := os.Open(recipePath(sha))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, api.MaxRecipeSize+1))
	if err != nil {
		return nil, err
	}
	return parseRecipe(sha, data)
}

// parseRecipe prüft, dass data den Hash sha hat, und liest das Rezept.
func parseRecipe(sha string, data []byte) ([]api.Segment, error) {
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != sha {
		return nil, fmt.Errorf("Rezept %s: Inhalt hat einen anderen Hash", sha)
	}
	segs, err := api.ParseRecipe(data)
	if err != nil {
		return nil, fmt.Errorf("Rezept %s: %w", sha, err)
	}
	return segs, nil
}

// recipeFor liefert das Rezept einer Datei mit size Bytes und prüft, dass
// seine Segmente genau size ergeben.
func recipeFor(sha string, size int64, load func(string) ([]api.Segment, error)) ([]api.Segment, error) {
	segs, err := load(sha)
	if err != nil {
		return nil, err
	}
	if total, ok := api.RecipeSize(segs); !ok || total != size {
		return nil, fmt.Errorf("Rezept %s ergibt nicht die Dateigröße %d", sha, size)
	}
	return segs, nil
}

// pruneRecipes entfernt lokale Rezepte, die nicht in keep stehen, samt
// liegengebliebener Temp-Dateien.
func pruneRecipes(keep map[string]bool) {
	entries, err := os.ReadDir(recipeDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if keep[name] || !(validSHA(name) || strings.HasSuffix(name, ".tmp")) {
			continue
		}
		os.Remove(filepath.Join(recipeDir(), name))
	}
}

// splitFile liest die Regionsdatei path (before ist Lstat davon), hasht sie
// und zerlegt sie. Ergibt das mehr als ein Segment und ist keines größer als
// maxSegment, wird das Rezept lokal abgelegt und sein Hash geliefert; sonst
// ist recipe leer: Die Datei bleibt ein ganzer Blob, der ohne Größengrenze
// einzeln übertragen wird.
func splitFile(path string, before fs.FileInfo) (whole, recipe string, err error) {
	data, err := readStable(path, before)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	whole = hex.EncodeToString(sum[:])
	// Ein einziges Segment wäre die ganze Datei.
	segs, _, ok := SplitRegion(data)
	if !ok || len(segs) < 2 {
		return whole, "", nil
	}
	for _, s := range segs {
		if s.Size > maxSegment {
			return whole, "", nil
		}
	}
	rec, err := api.EncodeRecipe(segs)
	if err != nil {
		return "", "", err
	}
	rsum := sha256.Sum256(rec)
	recipe = hex.EncodeToString(rsum[:])
	if err := storeRecipe(recipe, rec); err != nil {
		return "", "", fmt.Errorf("Rezept ablegen: %w", err)
	}
	return whole, recipe, nil
}
