package benachrichtigung

import (
	"strings"
	"unicode"

	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
)

// zws ist das unsichtbare Leerzeichen (U+200B), das Erwähnungen und Links
// aufbricht, ohne sichtbar zu sein.
const zws = "​"

// maxNachricht ist die Obergrenze einer Nachricht in Zeichen (Discord
// erlaubt 2000).
const maxNachricht = 1500

// Saeubern macht aus fremdem Text (Spielername, Gruppentitel, Wartungsgrund)
// etwas, das in einer Discord-Nachricht nichts auslöst: eine Zeile von
// höchstens max Zeichen, ohne Steuer- und unsichtbare Formatzeichen (etwa
// Umkehrung der Schreibrichtung), mit entschärften Erwähnungen („@“ und „<“
// bekommen ein unsichtbares Leerzeichen dahinter, damit weder @everyone noch
// <@123> oder <#123> wirken), ohne Links („://“ aufgebrochen) und mit
// Rückstrich vor den Zeichen der Markdown-Formatierung.
//
// Zusätzlich verbietet jede Nachricht Erwähnungen über allowed_mentions
// (sender.go); das hier ist der zweite Riegel, damit auch nichts Sichtbares
// aussieht wie eine Erwähnung.
func Saeubern(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) {
			return -1 // unsichtbare Formatzeichen, auch ein eingeschmuggeltes U+200B
		}
		return r
	}, s)
	s = ereignis.Kurz(s, max)
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case '\\', '*', '_', '~', '`', '|', '>', '#', '[', ']':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '@', '<':
			b.WriteRune(r)
			b.WriteString(zws)
		case ':':
			b.WriteRune(r)
			if i+2 < len(rs) && rs[i+1] == '/' && rs[i+2] == '/' {
				b.WriteString(zws)
			}
		case '-', '+':
			if i == 0 { // Aufzählungszeichen am Anfang der Nachricht
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// kuerze begrenzt eine fertige Nachricht auf maxNachricht Zeichen.
func kuerze(s string) string {
	if r := []rune(s); len(r) > maxNachricht {
		return string(r[:maxNachricht-1]) + "…"
	}
	return s
}
