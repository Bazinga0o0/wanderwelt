// Package serverbefehl prüft, was ein Admin über den Reiter „Server“ an den
// laufenden Minecraft-Server schicken darf: freie Befehle, Spielregeln und
// Spielernamen. Koordinator und Host benutzen dasselbe Paket; der Host prüft
// jeden Auftrag noch einmal, bevor er ihn per RCON ausführt.
//
// Der Befehlsfilter ist eine Positivliste der Vanilla-Befehle von Minecraft
// 26.2 (am Server mit „help“ geprüft). Mod-Befehle und alles Unbekannte sind
// abgelehnt. Befehle, die den Ablauf des Starters (Speichern, Stände,
// Übergabe) stören, Dateien am Host schreiben, ins Netz gehen oder eine
// zweite Wahrheit neben den Listen des Koordinators schaffen, stehen nicht
// auf der Liste; für sie gibt es einen eigenen Ablehnungsgrund.
package serverbefehl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Grenzen.
const (
	// MaxBefehl: so viele Bytes darf ein Befehl lang sein.
	MaxBefehl = 256
	// MaxAntwort: so viele Bytes einer Antwort gehen zurück ins Fenster.
	MaxAntwort = 4 << 10
)

// erlaubt sind die Wurzeln der Vanilla-Befehle von 26.2, die ein Admin
// schicken darf (genau diese Schreibweise; Brigadier unterscheidet Groß und
// Klein).
var erlaubt = map[string]bool{
	"advancement": true, "attribute": true, "banlist": true, "bossbar": true,
	"clear": true, "clone": true, "damage": true, "data": true, "dialog": true,
	"effect": true, "enchant": true, "execute": true, "experience": true,
	"fill": true, "fillbiome": true, "forceload": true, "function": true,
	"gamemode": true, "gamerule": true, "give": true, "help": true, "item": true,
	"kick": true, "kill": true, "list": true, "locate": true, "loot": true,
	"me": true, "msg": true, "particle": true, "place": true, "playsound": true,
	"random": true, "recipe": true, "return": true, "ride": true, "rotate": true,
	"say": true, "schedule": true, "scoreboard": true, "seed": true,
	"setblock": true, "setworldspawn": true, "spawnpoint": true, "spectate": true,
	"spreadplayers": true, "stopsound": true, "stopwatch": true, "summon": true,
	"swing": true, "tag": true, "team": true, "teammsg": true, "tell": true,
	"tellraw": true, "teleport": true, "time": true, "title": true, "tm": true,
	"tp": true, "trigger": true, "version": true, "w": true, "waypoint": true,
	"weather": true, "worldborder": true, "xp": true,
}

// gesperrt nennt für bekannte, aber abgelehnte Befehle den Grund.
var gesperrt = map[string]string{
	"stop":     "stört Speichern und Stände des Starters (Hosten beenden geht über „Spiel für mich beenden“)",
	"save-all": "stört Speichern und Stände des Starters",
	"save-off": "stört Speichern und Stände des Starters",
	"save-on":  "stört Speichern und Stände des Starters",
	"reload":   "stört den Ablauf des Servers",
	"restart":  "stört Speichern und Stände des Starters",
	"tick":     "stört die Übergabe des Hostens",
	"transfer": "stört die Übergabe des Hostens",
	"datapack": "lädt die Welt neu und kann Dateien am Host schreiben",
	"debug":    "schreibt Dateien am Host",
	"perf":     "schreibt Dateien am Host",
	"jfr":      "schreibt Dateien am Host",
	"test":     "kann Dateien am Host schreiben",

	"fetchprofile": "fragt Mojang aus dem Server heraus",

	"whitelist": "Whitelist bitte über die Liste oben ändern",
	"op":        "Ops bitte über die Liste oben ändern",
	"deop":      "Ops bitte über die Liste oben ändern",
	"ban":       "Banns bitte über die Liste oben ändern",
	"pardon":    "Banns bitte über die Liste oben ändern",
	"ban-ip":    "IP-Banns gibt es in wanderwelt nicht",
	"pardon-ip": "IP-Banns gibt es in wanderwelt nicht",

	"difficulty":      "die Schwierigkeit gilt aus den Welt-Einstellungen (Reiter „Welt“)",
	"defaultgamemode": "der Spielmodus gilt aus den Welt-Einstellungen (Reiter „Welt“)",
	"setidletimeout":  "schreibt server.properties am Host",
}

// ErrAbgelehnt steckt in jedem Fehler von Pruefe.
var ErrAbgelehnt = errors.New("Befehl abgelehnt")

// Pruefe prüft einen Befehl aus dem Fenster und liefert ihn so, wie er an
// RCON geht (ohne führendes „/“ und ohne Leerzeichen an den Enden). Der
// Fehler ist ein deutscher Text für den Admin und wrappt ErrAbgelehnt.
func Pruefe(befehl string) (string, error) {
	if !utf8.ValidString(befehl) {
		return "", ablehnen("kein gültiger Text (UTF-8)")
	}
	if len(befehl) > MaxBefehl {
		return "", ablehnen(fmt.Sprintf("zu lang (höchstens %d Zeichen)", MaxBefehl))
	}
	for _, r := range befehl {
		if r != ' ' && (unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) ||
			zeilentrenner(r) || r == utf8.RuneError) {
			return "", ablehnen("enthält Steuer- oder unsichtbare Zeichen")
		}
	}
	b := strings.Trim(befehl, " ")
	b = strings.TrimPrefix(b, "/")
	if b == "" {
		return "", ablehnen("leer")
	}
	if err := pruefeTeile(strings.Fields(b), 0); err != nil {
		return "", err
	}
	return b, nil
}

// pruefeTeile prüft den Befehl aus den Teilen t (an Leerzeichen getrennt).
// Bei execute und return ist jeder Rest nach einem „run“ wieder ein Befehl
// und wird ebenso geprüft – welches „run“ Brigadier nimmt, spielt so keine
// Rolle.
func pruefeTeile(t []string, tiefe int) error {
	if len(t) == 0 {
		return ablehnen("nach „run“ fehlt der Befehl")
	}
	if tiefe > 8 {
		return ablehnen("zu tief verschachtelt")
	}
	wurzel := t[0]
	if grund, ok := gesperrt[strings.ToLower(wurzel)]; ok {
		return ablehnen(fmt.Sprintf("„%s“ ist hier gesperrt: %s", wurzel, grund))
	}
	if !erlaubt[wurzel] {
		return ablehnen(fmt.Sprintf("„%s“ ist kein erlaubter Minecraft-Befehl", kurz(wurzel)))
	}
	if wurzel != "execute" && wurzel != "return" {
		return nil
	}
	for i := 1; i < len(t); i++ {
		if t[i] == "run" {
			if err := pruefeTeile(t[i+1:], tiefe+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// zeilentrenner: U+2028 und U+2029 (Zeilen- und Absatztrenner).
func zeilentrenner(r rune) bool { return r == 0x2028 || r == 0x2029 }

func ablehnen(grund string) error {
	return fmt.Errorf("%w: %s", ErrAbgelehnt, grund)
}

// kurz kürzt einen Teil für die Meldung auf 32 Zeichen.
func kurz(s string) string {
	if utf8.RuneCountInString(s) <= 32 {
		return s
	}
	r := []rune(s)
	return string(r[:32]) + "…"
}

// Antwort macht aus der Antwort des Servers Text fürs Fenster: höchstens
// MaxAntwort Bytes (ohne ein Zeichen zu zerteilen, dann mit „ …“),
// gültiges UTF-8, Steuerzeichen außer dem Zeilenumbruch werden Leerzeichen.
func Antwort(s string) string {
	s = strings.ToValidUTF8(s, "?")
	gekuerzt := false
	if len(s) > MaxAntwort {
		cut := MaxAntwort
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s, gekuerzt = s[:cut], true
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || zeilentrenner(r) {
			return ' '
		}
		return r
	}, s)
	if gekuerzt {
		s += " …"
	}
	return s
}

// nameRE: ein heutiger Minecraft-Java-Name.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

// GueltigerName meldet, ob s ein heutiger Minecraft-Name ist (3–16 Zeichen
// aus Buchstaben, Ziffern und „_“). Nur solche Namen nimmt der Reiter
// „Server“ neu auf.
func GueltigerName(s string) bool { return nameRE.MatchString(s) }

var (
	regelRE = regexp.MustCompile(`^([a-z0-9_.-]{1,32}:)?[a-z0-9_./-]{1,64}$`)
	wertRE  = regexp.MustCompile(`^(true|false|-?[0-9]{1,10})$`)
)

// PruefeSpielregel prüft Name und Wert einer Spielregel (Fehler auf
// Deutsch, wrappt ErrAbgelehnt); ob es die Regel gibt und der Wert passt,
// entscheidet der Server.
func PruefeSpielregel(name, wert string) error {
	if !regelRE.MatchString(name) {
		return ablehnen("ungültiger Name einer Spielregel")
	}
	if !wertRE.MatchString(wert) {
		return ablehnen("der Wert ist true, false oder eine ganze Zahl")
	}
	return nil
}

// SpielregelBefehl ist der Befehl, der die Spielregel setzt (vorher mit
// PruefeSpielregel geprüft).
func SpielregelBefehl(name, wert string) (string, error) {
	if err := PruefeSpielregel(name, wert); err != nil {
		return "", err
	}
	return "gamerule " + name + " " + wert, nil
}

// spielregelListeRE findet die Namen in der Antwort auf „help gamerule“
// („/gamerule keep_inventory [<value>]“; auch „/gamerule minecraft:…“).
var spielregelListeRE = regexp.MustCompile(`/gamerule ([a-z0-9_.:/-]+) \[<value>\]`)

// SpielregelNamen liest die Namen aus der Antwort auf „help gamerule“:
// sortiert nach Auftreten, ohne Dubletten, „minecraft:“ entfällt (beide
// Schreibweisen stehen in der Hilfe). Höchstens 300.
func SpielregelNamen(hilfe string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range spielregelListeRE.FindAllStringSubmatch(hilfe, -1) {
		n := strings.TrimPrefix(m[1], "minecraft:")
		if seen[n] || !regelRE.MatchString(n) {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) == 300 {
			break
		}
	}
	return out
}

// spielregelWertRE liest „Gamerule keep_inventory is currently set to: false“.
var spielregelWertRE = regexp.MustCompile(`^Gamerule (\S+) is currently set to: (\S+)$`)

// SpielregelWert liest den Wert aus der Antwort auf „gamerule <name>“; ok
// false, wenn die Antwort nicht so aussieht.
func SpielregelWert(antwort string) (wert string, ok bool) {
	m := spielregelWertRE.FindStringSubmatch(strings.TrimSpace(antwort))
	if m == nil || !wertRE.MatchString(m[2]) {
		return "", false
	}
	return m[2], true
}

// SpielregelGesetzt meldet, ob die Antwort auf „gamerule <name> <wert>“ das
// Setzen bestätigt („Gamerule … is now set to: …“).
func SpielregelGesetzt(antwort string) bool {
	a := strings.TrimSpace(antwort)
	return strings.HasPrefix(a, "Gamerule ") && strings.Contains(a, " is now set to: ")
}
