package serverbefehl

import (
	"errors"
	"strings"
	"testing"
)

func TestPruefeErlaubt(t *testing.T) {
	for in, want := range map[string]string{
		"say Hallo":                          "say Hallo",
		"/time set day":                      "time set day",
		"  weather clear  ":                  "weather clear",
		"execute as @a run say hi":           "execute as @a run say hi",
		"execute as @a run execute run list": "execute as @a run execute run list",
		"return run say x":                   "return run say x",
		"gamerule keep_inventory true":       "gamerule keep_inventory true",
		"tellraw @a {\"text\":\"run stop\"}": "tellraw @a {\"text\":\"run stop\"}",
		"say Ä Ö ü ß – „Zitat“":              "say Ä Ö ü ß – „Zitat“",
		"kick Alex":                          "kick Alex",
		"banlist":                            "banlist",
		"say stop":                           "say stop", // „stop“ nur als Text, nicht als Befehl
		strings.Repeat("a", 0) + "list":      "list",
	} {
		got, err := Pruefe(in)
		if err != nil || got != want {
			t.Errorf("Pruefe(%q) = %q, %v; erwartet %q", in, got, err, want)
		}
	}
}

func TestPruefeGesperrt(t *testing.T) {
	for _, in := range []string{
		"stop", "/stop", " stop", "save-all", "save-all flush", "save-off", "save-on", "reload", "restart",
		"debug start", "perf start", "jfr start", "test create x", "datapack create a b", "datapack enable x",
		"tick freeze", "transfer example.org", "fetchprofile name Alex",
		"whitelist off", "whitelist add Alex", "op Alex", "deop Alex", "ban Alex", "pardon Alex", "ban-ip 1.2.3.4", "pardon-ip 1.2.3.4",
		"difficulty peaceful", "defaultgamemode creative", "setidletimeout 1",
		"STOP", "Stop", "minecraft:stop", "//stop", "/ stop",
		// über execute/return
		"execute run stop", "execute as @a run save-all", "execute as @a run execute run stop",
		"execute as run run stop", "return run stop", "execute run", "execute as @a at @s run whitelist off",
		"execute run say ok run stop", // ein späteres „run“ zählt ebenso
		// unbekannt oder Mods
		"polymer generate-pack", "graves list", "c2me", "lp user x", "unbekannt",
		// Leeres
		"", "   ", "/",
	} {
		got, err := Pruefe(in)
		if err == nil || !errors.Is(err, ErrAbgelehnt) {
			t.Errorf("Pruefe(%q) = %q, %v; erwartet Ablehnung", in, got, err)
		}
	}
}

func TestPruefeZeichen(t *testing.T) {
	for _, in := range []string{
		"say a\nstop", "say a\rb", "say a\tb", "say \x00", "say \x1b[31m", "say \x7f", "say \u0085x",
		"say \xff\xfe", // kein UTF-8
	} {
		if _, err := Pruefe(in); err == nil {
			t.Errorf("Pruefe(%q): erwartet Ablehnung", in)
		}
	}
	// Zeilen-/Absatztrenner, Breite null, Richtungswechsel, BOM, geschütztes
	// und ideografisches Leerzeichen.
	for _, r := range []rune{0x2028, 0x2029, 0x200b, 0x202e, 0xfeff, 0xa0, 0x3000} {
		in := "say a" + string(r) + "b"
		if _, err := Pruefe(in); err == nil {
			t.Errorf("Pruefe(%q): erwartet Ablehnung", in)
		}
	}
}

func TestPruefeLaenge(t *testing.T) {
	ok := "say " + strings.Repeat("x", MaxBefehl-4)
	if _, err := Pruefe(ok); err != nil {
		t.Fatalf("genau %d Bytes: %v", MaxBefehl, err)
	}
	if _, err := Pruefe(ok + "x"); err == nil {
		t.Fatalf("%d Bytes: erwartet Ablehnung", MaxBefehl+1)
	}
	// Bytes zählen, nicht Zeichen.
	umlaut := "say " + strings.Repeat("ä", (MaxBefehl-4)/2+1)
	if _, err := Pruefe(umlaut); err == nil {
		t.Fatalf("%d Bytes Umlaute: erwartet Ablehnung", len(umlaut))
	}
}

// Jede Wurzel der Positivliste ist ein Befehl von 26.2 und keine gesperrte.
func TestListenGetrennt(t *testing.T) {
	for w := range erlaubt {
		if _, ok := gesperrt[w]; ok {
			t.Errorf("%q steht in beiden Listen", w)
		}
	}
}

func TestAntwort(t *testing.T) {
	if got := Antwort("a\x1b[0mb\r\nc" + string(rune(0x202e)) + string(rune(0x2028))); got != "a [0mb \nc  " {
		t.Errorf("Antwort = %q", got)
	}
	lang := strings.Repeat("ä", MaxAntwort) // 2 Bytes je Zeichen
	got := Antwort(lang)
	if len(got) > MaxAntwort+len(" …") || !strings.HasSuffix(got, " …") {
		t.Errorf("gekürzt %d Bytes, Ende %q", len(got), got[len(got)-8:])
	}
	if strings.ContainsRune(got, '�') {
		t.Errorf("Zeichen zerteilt")
	}
	if got := Antwort(strings.Repeat("x", MaxAntwort)); got != strings.Repeat("x", MaxAntwort) {
		t.Errorf("genau %d Bytes dürfen nicht gekürzt werden", MaxAntwort)
	}
}

func TestGueltigerName(t *testing.T) {
	for _, n := range []string{"Alex", "abc", "Swaggolas_Claus", "a_b_c_d_e_f_g_h1"} {
		if !GueltigerName(n) {
			t.Errorf("%q sollte gültig sein", n)
		}
	}
	for _, n := range []string{"", "ab", "a_b_c_d_e_f_g_h12", "Alex Steve", "Alex;stop", "Älex", "Alex\n", "@a", "Alex-1", " Alex"} {
		if GueltigerName(n) {
			t.Errorf("%q sollte ungültig sein", n)
		}
	}
}

func TestSpielregel(t *testing.T) {
	if b, err := SpielregelBefehl("keep_inventory", "true"); err != nil || b != "gamerule keep_inventory true" {
		t.Errorf("= %q, %v", b, err)
	}
	if _, err := SpielregelBefehl("minecraft:random_tick_speed", "-3"); err != nil {
		t.Errorf("Namensraum/negative Zahl: %v", err)
	}
	for _, c := range [][2]string{
		{"keep_inventory", "true run stop"}, {"keep inventory", "true"}, {"keep_inventory;stop", "1"},
		{"Keep_Inventory", "true"}, {"", "true"}, {"x", ""}, {"x", "1.5"}, {"x", "12345678901"}, {"x", "TRUE"},
		{"x\n", "1"}, {"x", "1\n"},
	} {
		if err := PruefeSpielregel(c[0], c[1]); err == nil {
			t.Errorf("PruefeSpielregel(%q, %q): erwartet Ablehnung", c[0], c[1])
		}
	}
}

func TestSpielregelLesen(t *testing.T) {
	hilfe := "/gamerule ender_pearls_vanish_on_death [<value>]/gamerule minecraft:ender_pearls_vanish_on_death [<value>]" +
		"/gamerule keep_inventory [<value>]/gamerule minecraft:keep_inventory [<value>]/gamerule mod:x_y [<value>]"
	got := SpielregelNamen(hilfe)
	if strings.Join(got, ",") != "ender_pearls_vanish_on_death,keep_inventory,mod:x_y" {
		t.Errorf("Namen = %v", got)
	}
	if v, ok := SpielregelWert("Gamerule keep_inventory is currently set to: false"); !ok || v != "false" {
		t.Errorf("Wert = %q, %v", v, ok)
	}
	if _, ok := SpielregelWert("Incorrect argument for command"); ok {
		t.Errorf("Fehlertext als Wert gelesen")
	}
	if !SpielregelGesetzt("Gamerule keep_inventory is now set to: true") || SpielregelGesetzt("Expected integer") {
		t.Errorf("SpielregelGesetzt falsch")
	}
}

// Gesperrte Befehle nennen den Grund (der Admin soll wissen, warum), Mods und
// Unbekanntes heißen „kein erlaubter Minecraft-Befehl“.
func TestPruefeGruende(t *testing.T) {
	for in, want := range map[string]string{
		"stop":                      "gesperrt",
		"execute run save-all":      "gesperrt",
		"whitelist off":             "Liste",
		"difficulty hard":           "Reiter „Welt“",
		"polymer generate-pack":     "kein erlaubter Minecraft-Befehl",
		"say a" + string(rune(0x7)): "Steuer",
	} {
		_, err := Pruefe(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Pruefe(%q) = %v; erwartet %q im Grund", in, err, want)
		}
	}
}

// Eine Antwort über MaxAntwort wird schon ab dem ersten Byte zu viel gekürzt.
func TestAntwortGrenze(t *testing.T) {
	got := Antwort(strings.Repeat("x", MaxAntwort+1))
	if got != strings.Repeat("x", MaxAntwort)+" …" {
		t.Errorf("%d Bytes: %d zurück", MaxAntwort+1, len(got))
	}
}
