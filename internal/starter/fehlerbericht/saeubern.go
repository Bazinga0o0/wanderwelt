// Package fehlerbericht sammelt, was beim Suchen eines Fehlers hilft, ohne
// Geheimnisse preiszugeben: die zentrale Säuberung (Saeubern), die
// wanderwelt-Logzeilen, die mit der Welt zum nächsten Host wandern
// (wanderwelt-logs/ im Weltordner), und den Fehlerbericht als Zip.
//
// Alles, was die Maschine verlässt oder in einen Stand der Welt geschrieben
// wird, geht durch Saeubern.
package fehlerbericht

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Entfernt ist der Ersatz für alles, was Saeubern wegnimmt.
const Entfernt = "[entfernt]"

// Saeuberer entfernt Geheimnisse und persönliche Pfade aus Text. Er ist
// zustandslos nach der Erzeugung und von mehreren Goroutinen benutzbar.
type Saeuberer struct {
	home  string   // Benutzerordner dieses PCs ("" = unbekannt)
	user  string   // Windows-Benutzername ("" = unbekannt)
	known []string // Geheimnisse, die wörtlich entfernt werden (Token, RCON-Passwort)

	homeRe  *regexp.Regexp // alle Schreibweisen von home
	homeKey string         // letzter Teil von home, klein (Vorprüfung)
	userRe  *regexp.Regexp // user als eigenes Wort
}

// NeuerSaeuberer baut einen Säuberer für den Benutzerordner home und den
// Benutzernamen user (beides darf leer sein). geheim sind Werte, die in
// jeder Form wörtlich entfernt werden, etwa das Token der Gruppendatei;
// kürzere als 6 Zeichen werden ignoriert (sie träfen zu viel
// Unbeteiligtes).
func NeuerSaeuberer(home, user string, geheim ...string) *Saeuberer {
	s := &Saeuberer{home: home, user: user}
	for _, g := range geheim {
		if len(g) >= 6 {
			s.known = append(s.known, g)
		}
	}
	// Längere zuerst, damit ein Geheimnis, das in einem anderen steckt, nicht
	// ein Stück davon übrig lässt.
	for i := range s.known {
		for j := i + 1; j < len(s.known); j++ {
			if len(s.known[j]) > len(s.known[i]) {
				s.known[i], s.known[j] = s.known[j], s.known[i]
			}
		}
	}
	if h := strings.TrimRight(home, `\/`); len(h) > 3 {
		// Jede Schreibweise: \, \\ (JSON), / und beliebig gemischt, Groß-/
		// Kleinschreibung egal (Windows).
		parts := strings.FieldsFunc(h, func(r rune) bool { return r == '\\' || r == '/' })
		if len(parts) > 0 {
			s.homeKey = strings.ToLower(parts[len(parts)-1])
		}
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		pre := ""
		if strings.HasPrefix(h, `\\`) || strings.HasPrefix(h, "//") {
			pre = `[\\/]{2}`
		} else if strings.HasPrefix(h, `\`) || strings.HasPrefix(h, "/") {
			pre = `[\\/]`
		}
		s.homeRe = regexp.MustCompile(`(?i)` + pre + strings.Join(parts, `[\\/]+`) + `([\\/]|$|[^\p{L}\p{N}_.-])`)
	}
	if u := strings.TrimSpace(user); len(u) >= 3 && !allgemeinerName(u) {
		s.user = u
		s.userRe = regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(u) + `($|[^\p{L}\p{N}_])`)
	}
	return s
}

// allgemeinerName: Benutzernamen, die auch sonst im Text vorkommen.
func allgemeinerName(u string) bool {
	switch strings.ToLower(u) {
	case "user", "users", "admin", "administrator", "public", "default", "system", "benutzer", "gast", "guest", "root", "wanderwelt":
		return true
	}
	return false
}

var (
	standardOnce sync.Once
	standard     *Saeuberer
)

// Standard liefert den Säuberer dieses PCs: Benutzerordner und
// -name aus der Umgebung.
func Standard(geheim ...string) *Saeuberer {
	home, _ := os.UserHomeDir()
	if h := os.Getenv("USERPROFILE"); h != "" {
		home = h
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" && home != "" {
		user = filepath.Base(home)
	}
	return NeuerSaeuberer(home, user, geheim...)
}

// Saeubern entfernt aus text Geheimnisse (Tokens, Passwörter, Webhooks,
// Zugangsdaten in URLs, Base64-Blöcke, Gruppencodes, Spieler-IP-Adressen)
// und ersetzt den Windows-Benutzernamen in Pfaden durch %USERPROFILE%. Es
// arbeitet Zeile für Zeile, behält Zeilenumbrüche und ist idempotent. Im
// Zweifel wird eher mehr entfernt: Harmloses (Zeitangaben, Versionen,
// Hashes, Adressen ohne Zugangsdaten) bleibt aber unverändert.
//
// Diese Fassung kennt nur den Benutzer dieses PCs; Geheimnisse, die nur dieser
// PC kennt (Token der Gruppendatei), gibt NeuerSaeuberer mit.
func Saeubern(text string) string {
	standardOnce.Do(func() { standard = Standard() })
	return standard.Text(text)
}

// Text säubert text (siehe Saeubern).
func (s *Saeuberer) Text(text string) string {
	if text == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = s.Zeile(l)
	}
	return strings.Join(lines, "\n")
}

// langeZeile: Zeilen darüber werden stückweise gesäubert (die Muster sollen
// nie auf eine riesige Zeile losgehen); Geheimnisse enthalten keine
// Leerzeichen, an denen geschnitten wird.
const langeZeile = 8 << 10

// Zeile säubert eine einzelne Zeile (ohne Zeilenumbruch; ein \r am Ende
// bleibt erhalten).
func (s *Saeuberer) Zeile(l string) string {
	if l == "" {
		return l
	}
	if len(l) <= langeZeile {
		return s.zeile(l)
	}
	var b strings.Builder
	for len(l) > 0 {
		n := min(len(l), langeZeile/2)
		if n < len(l) {
			// An einem Leerzeichen in der zweiten Hälfte des Stücks schneiden,
			// sonst hart (auf einer Zeichengrenze).
			if i := strings.LastIndexAny(l[n/2:n], " \t"); i >= 0 {
				n = n/2 + i + 1
			} else {
				for n > 0 && !utf8.RuneStart(l[n]) {
					n--
				}
			}
		}
		b.WriteString(s.zeile(l[:n]))
		l = l[n:]
	}
	return b.String()
}

func (s *Saeuberer) zeile(l string) string {
	for _, k := range s.known {
		if strings.Contains(l, k) {
			l = strings.ReplaceAll(l, k, Entfernt)
		}
	}
	l = s.pfade(l)
	lo := strings.ToLower(l)
	// Jedes Muster läuft nur, wenn die Zeile sein Stichwort enthält: Die
	// meisten Zeilen eines Logs gehen so ohne ein einziges Regex durch.
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(lo, x) {
				return true
			}
		}
		return false
	}
	if has("webhooks/", "hooks.slack.com") {
		l = reWebhook.ReplaceAllString(l, "[Webhook-URL entfernt]")
	}
	if strings.Contains(l, "://") && strings.Contains(l, "@") {
		l = reUserinfo.ReplaceAllString(l, "${1}"+Entfernt+"@")
	}
	if has("cookie") {
		l = reCookie.ReplaceAllString(l, "${1}${2}"+Entfernt)
	}
	if has("authorization") {
		l = reAuthHeader.ReplaceAllString(l, "${1}${2}${3}"+Entfernt)
	}
	if has("bearer", "basic ") {
		l = replaceFunc(reBearer, l, func(m []string) string {
			if !tokenartig(m[2]) {
				return m[0]
			}
			return m[1] + " " + Entfernt
		})
	}
	if has("token", "secret", "passw", "pwd", "kennwort", "credential", "key") && has(":", "=") {
		l = replaceFunc(reSchluesselWert, l, func(m []string) string {
			wert := m[3]
			switch {
			case strings.HasPrefix(wert, "[") && strings.HasSuffix(wert, "entfernt]"):
				return m[0] // schon gesäubert (idempotent)
			case len(wert) >= 2 && (wert[0] == '"' || wert[0] == '\''):
				return m[1] + m[2] + string(wert[0]) + Entfernt + string(wert[0])
			}
			return m[1] + m[2] + Entfernt
		})
	}
	if strings.Contains(l, "--") {
		l = reKommandozeile.ReplaceAllString(l, "${1}"+Entfernt)
	}
	if strings.Contains(l, "eyJ") {
		l = reJWT.ReplaceAllString(l, "[JWT entfernt]")
	}
	if has("ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "sk-", "akia") || langerBlock(l, 50) {
		l = reBekannteToken.ReplaceAllString(l, Entfernt)
	}
	if strings.Contains(l, "w1.") {
		l = reGruppencode.ReplaceAllString(l, "w1."+Entfernt)
	}
	if has("token", "secret", "geheim") {
		l = replaceFunc(reDateiToken, l, func(m []string) string {
			if !strings.ContainsAny(m[2], "0123456789") {
				return m[0]
			}
			return m[1] + Entfernt
		})
	}
	if slashZiffer(l) {
		l = replaceFunc(reSpielerIP, l, func(m []string) string {
			if m[1] == "127.0.0.1" || m[1] == "0.0.0.0" {
				return m[0]
			}
			return "/[IP entfernt]"
		})
	}
	if langerBlock(l, 40) {
		for _, re := range []*regexp.Regexp{reBase64, reBase64Url} {
			l = replaceFunc(re, l, func(m []string) string {
				if base64artig(m[0]) {
					return "[Base64 entfernt]"
				}
				return m[0]
			})
		}
	}
	return l
}

// pfade ersetzt den Benutzerordner und den Benutzernamen.
func (s *Saeuberer) pfade(l string) string {
	if s.homeRe == nil && s.userRe == nil && !strings.Contains(l, "sers") && !strings.Contains(l, "SERS") {
		return l
	}
	lo := strings.ToLower(l)
	if s.homeRe != nil && strings.Contains(lo, s.homeKey) {
		// Das Zeichen hinter dem Ordner bleibt stehen.
		l = s.homeRe.ReplaceAllString(l, "%USERPROFILE%${1}")
		lo = strings.ToLower(l)
	}
	if strings.Contains(lo, "users") {
		l = replaceFunc(reUserPfad, l, func(m []string) string {
			name := m[1] + m[3] // entweder mit Trennzeichen (m[2]) dahinter oder ohne
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "public", "default", "all users", "default user":
				return m[0]
			}
			return "%USERPROFILE%" + m[2]
		})
		lo = strings.ToLower(l)
	}
	if s.userRe != nil && strings.Contains(lo, strings.ToLower(s.user)) {
		for range 4 { // Treffer können einander benachbaren Platz nehmen
			n := s.userRe.ReplaceAllString(l, "${1}%USERNAME%${2}")
			if n == l {
				break
			}
			l = n
		}
	}
	return l
}

// langerBlock: Die Zeile enthält mindestens n Zeichen am Stück aus dem
// Base64-Alphabet (auch URL-sicher, mit Punkt für Tokens).
func langerBlock(l string, n int) bool {
	run := 0
	for i := 0; i < len(l); i++ {
		c := l[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '+' || c == '/' || c == '-' || c == '_' || c == '.' {
			run++
			if run >= n {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// slashZiffer: Ein "/" steht unmittelbar vor einer Ziffer (Ort von
// IP-Adressen wie /1.2.3.4:5).
func slashZiffer(l string) bool {
	for i := 0; i+1 < len(l); i++ {
		if l[i] == '/' && l[i+1] >= '0' && l[i+1] <= '9' {
			return true
		}
	}
	return false
}

// replaceFunc ersetzt alle Treffer von re in s durch f(Teiltreffer); m[0] ist
// der ganze Treffer. (regexp bietet das nur ohne Teiltreffer an.)
func replaceFunc(re *regexp.Regexp, s string, f func(m []string) string) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range idx {
		m := make([]string, len(loc)/2)
		for i := range m {
			if loc[2*i] >= 0 {
				m[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(f(m))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// tokenartig: Ein Wort hinter „Bearer“ sieht wie ein Token aus (nicht wie
// „Bearer of bad news“): mindestens 8 Zeichen und eine Ziffer oder ein
// Sonderzeichen, oder mindestens 20 Zeichen.
func tokenartig(w string) bool {
	if len(w) >= 20 {
		return true
	}
	return len(w) >= 8 && strings.ContainsAny(w, "0123456789._~+/=-")
}

// base64artig: ein langer Block aus Base64-Zeichen mit Groß- und
// Kleinbuchstaben und Ziffern, der weder reines Hex (Hash) noch ein
// Pfad ist.
func base64artig(b string) bool {
	var gross, klein, ziffer bool
	hex := true
	slash := 0
	for _, r := range b {
		switch {
		case r >= 'A' && r <= 'Z':
			gross = true
		case r >= 'a' && r <= 'z':
			klein = true
		case r >= '0' && r <= '9':
			ziffer = true
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			hex = false
		}
		if r == '/' {
			slash++
		}
	}
	if hex || slash*8 > len(b) {
		return false
	}
	return gross && klein && ziffer
}

var (
	reWebhook = regexp.MustCompile(`(?i)https?://(?:[a-z0-9-]+\.)?(?:discord(?:app)?\.com/api/(?:v\d+/)?webhooks|hooks\.slack\.com/services)/[^\s"'<>]+`)

	// Zugangsdaten in URLs: https://nutzer:passwort@host
	reUserinfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s"'<>]+@`)

	reCookie     = regexp.MustCompile(`(?i)\b(set-cookie|cookie)(\s*:\s*)[^\r\n]+`)
	reAuthHeader = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization)(["']?\s*[:=]\s*["']?)((?:(?:bearer|basic|digest|token)\s+)?)[^\s"',;]+`)
	reBearer     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+([A-Za-z0-9._~+/=-]+)`)

	// token=…, "password": "…", rcon.password=… usw.
	reSchluesselWert = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|secret|passw(?:or)?d|passwort|pwd|passphrase|kennwort|credential|(?:api|access|private|secret|auth|signing|session)[_-]?key)[A-Za-z0-9_.-]*)(["']?\s*[:=]\s*)(\[[^\]]*entfernt\]|"[^"]*"|'[^']*'|[^\s"',;&})\]]+)`)

	reKommandozeile = regexp.MustCompile(`(?i)(--(?:access-?token|token|password|passwd|secret|api-?key)(?:\s+|=))(\S+)`)

	reJWT           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)
	reBekannteToken = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|[MNO][A-Za-z0-9_-]{23,25}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,})`)

	// Gruppencodes (group/joinsecret.go): enthalten nur öffentliche Adressen,
	// zeigen aber Fremden den Weg zum Beitritt.
	reGruppencode = regexp.MustCompile(`\bw1\.[A-Za-z0-9_-]{8,}`)

	// Dateinamen mit Token: token-AbC12345…, secret_…
	reDateiToken = regexp.MustCompile(`(?i)\b((?:token|secret|geheim)[-_=])([A-Za-z0-9]{8,})`)

	// Spieler-IP-Adressen im Minecraft-Log: Name[/1.2.3.4:5555], /1.2.3.4:5555
	reSpielerIP = regexp.MustCompile(`/((?:\d{1,3}\.){3}\d{1,3})(?::\d{2,5})?\b`)

	reBase64    = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)
	reBase64Url = regexp.MustCompile(`[A-Za-z0-9_-]{40,}`)

	// C:\Users\<Name>\ (auch / und \\), der Name darf Leerzeichen enthalten,
	// wenn danach noch ein Trennzeichen folgt.
	reUserPfad = regexp.MustCompile(`(?i)[A-Za-z]:[\\/]+Users[\\/]+(?:([^\\/:*?"<>|\r\n]{1,40})([\\/])|([^\\/:*?"<>|\s]+))`)
)
