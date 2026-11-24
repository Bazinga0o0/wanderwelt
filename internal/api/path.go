package api

import (
	"strings"
	"unicode/utf8"
)

// ValidPath meldet, ob p als Pfad einer Weltdatei erlaubt ist (FileEntry.Path).
// Erlaubt sind nur relative, mit "/" getrennte Pfade, die sich unter Windows
// verlustfrei als Datei anlegen lassen:
//   - nicht leer, gültiges UTF-8, keine Steuerzeichen;
//   - kein führendes oder doppeltes "/", keine Teile "." oder "..";
//   - keines der Zeichen \ : < > " | ? * (":" schließt auch Laufwerke und
//     Windows-Datenströme aus);
//   - kein Teil endet auf "." oder Leerzeichen (Windows schneidet das ab, "a."
//     wäre dieselbe Datei wie "a");
//   - kein Teil heißt wie ein Gerät (CON, PRN, AUX, NUL, COM0–9, LPT0–9),
//     Groß/Klein egal, auch mit Endung ("nul.txt").
//
// Starter und Koordinator prüfen mit derselben Regel.
func ValidPath(p string) bool {
	if p == "" || !utf8.ValidString(p) {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\:<>"|?*`, r) {
			return false
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return false
		}
		if reservedName(seg) {
			return false
		}
	}
	return true
}

// reservedName meldet, ob seg unter Windows ein Gerät bezeichnet: Der Teil
// vor dem ersten Punkt (ohne Leerzeichen am Ende) ist CON, PRN, AUX, NUL,
// COM0–9 oder LPT0–9.
func reservedName(seg string) bool {
	stem, _, _ := strings.Cut(seg, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL":
		return true
	}
	return len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '0' && stem[3] <= '9'
}
