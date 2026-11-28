// Package ereignis leitet aus zwei aufeinanderfolgenden Status des
// Koordinators (api.Status) die Ereignisse ab, über die Mitglieder
// benachrichtigt werden: „X hostet jetzt“, „Die Welt ist online“, Spieler
// kommen und gehen, Wartung, Host-Übergabe. Es enthält außerdem den Katalog
// der Schalter (mit Standardwerten je Kanal) und die deutschen Texte.
//
// Zwei Kanäle benutzen es: die Windows-Hinweise im Starter (Vergleich bei
// jeder Statusabfrage) und der Discord-Webhook im Koordinator (Vergleich im
// 5-s-Takt, internal/koord/benachrichtigung). Beide folgen damit genau
// denselben Regeln. Das Paket kennt nur api.
//
// Datenschutz: Texte enthalten nur Minecraft-Namen (die in der Gruppe ohnehin
// sichtbar sind) und den Wartungsgrund des Admins, nie E-Mail-Adressen.
// Die E-Mail-Schlüssel in Ereignis dienen nur dem Vergleich (etwa „bin ich
// selbst gemeint“) und stehen in keinem Text.
package ereignis

import (
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Art ist der Schlüssel eines Schalters (und Ereignisses).
type Art string

// Die Arten. Wartung und Übergabe haben je zwei Ereignisse (Ereignis.Ende).
const (
	HostStartet      Art = "host_startet"
	WeltOnline       Art = "welt_online"
	Spieler          Art = "spieler"
	NiemandHostet    Art = "niemand_hostet"
	Wartung          Art = "wartung"
	Uebergabe        Art = "uebergabe"
	Beitrittsanfrage Art = "beitrittsanfrage"
)

// Kanal ist ein Weg, auf dem benachrichtigt wird.
type Kanal int

// Die Kanäle.
const (
	Windows Kanal = iota
	Discord
)

// Schalter beschreibt einen Schalter für die Einstellungen.
type Schalter struct {
	Art   Art
	Titel string
	Hilfe string
	// Windows und Discord: Standardwert des Schalters je Kanal.
	Windows, Discord bool
	// NurAdmin: nur Admins bekommen das Ereignis (und sehen den Schalter);
	// NurWindows: es gibt das Ereignis auf Discord nie.
	NurAdmin, NurWindows bool
}

// Katalog sind alle Schalter in der Reihenfolge der Anzeige.
var Katalog = []Schalter{
	{Art: HostStartet, Titel: "Jemand hostet",
		Hilfe: "Ein Mitglied startet den Server – die Welt ist gleich bereit.", Windows: true, Discord: true},
	{Art: WeltOnline, Titel: "Welt ist online",
		Hilfe: "Der Server läuft, ihr könnt euch verbinden.", Windows: true, Discord: true},
	{Art: Spieler, Titel: "Spieler kommen und gehen",
		Hilfe: "Wer der Welt beitritt oder sie verlässt (mehrere zusammen in einer Meldung).", Discord: true},
	{Art: NiemandHostet, Titel: "Niemand hostet mehr",
		Hilfe: "Der Server wurde beendet, die Welt ist offline."},
	{Art: Wartung, Titel: "Wartung am Server",
		Hilfe: "Eine Wartung wird angekündigt, und sie ist vorbei.", Windows: true, Discord: true},
	{Art: Uebergabe, Titel: "Host-Übergabe",
		Hilfe: "Der Host gibt die Welt an einen anderen PC weiter: läuft und abgeschlossen.", Discord: true},
	{Art: Beitrittsanfrage, Titel: "Neue Beitrittsanfragen",
		Hilfe: "Jemand möchte der Gruppe beitreten (nur für Admins).", Windows: true, NurAdmin: true, NurWindows: true},
}

// Bekannt meldet, ob art ein Schalter des Katalogs ist.
func Bekannt(art Art) bool {
	return slices.ContainsFunc(Katalog, func(s Schalter) bool { return s.Art == art })
}

// Standard ist der Standardwert des Schalters art auf dem Kanal k; false
// für unbekannte Arten und für Ereignisse, die es auf dem Kanal nie gibt.
func Standard(k Kanal, art Art) bool {
	for _, s := range Katalog {
		if s.Art != art {
			continue
		}
		if k == Discord {
			return s.Discord && !s.NurWindows
		}
		return s.Windows
	}
	return false
}

// Ereignis ist eine Veränderung in der Gruppe.
//
// Wer ist bei HostStartet, WeltOnline und NiemandHostet der Host, bei
// Uebergabe der alte Host (kann leer sein), Nach bei Uebergabe der neue und
// bei Beitrittsanfrage der Name des Anfragenden. Die Schlüssel
// (kleingeschriebene E-Mail-Adressen) sind nur zum Vergleichen da und
// dürfen in keinem Text und in keiner Nachricht erscheinen.
type Ereignis struct {
	Art Art
	// Ende: Wartung vorbei bzw. Übergabe abgeschlossen.
	Ende bool

	Wer, Nach       string
	WerKey, NachKey string

	// Grund: der Wartungsgrund des Admins (fremder Text).
	Grund string
	// Neu und Weg: die Spieler, die beigetreten sind bzw. gegangen sind.
	Neu, Weg []string
	// Welt: Name der laufenden Welt, nur bei mehreren Welten der Gruppe.
	Welt string
}

// Anonym ist der Name für ein Mitglied ohne Minecraft-Namen (nie die
// E-Mail-Adresse).
const Anonym = "ein Mitglied"

// Aus liefert die Ereignisse zwischen alt und neu. Fehlt einer der beiden
// (erster Stand, Koordinator nicht erreichbar), gibt es keine: So entsteht
// nach einem Start oder Ausfall nie eine Flut alter Ereignisse.
//
// Spieler werden nur verglichen, solange derselbe Host bereit ist und keine
// Übergabe läuft (sonst würde jeder Hostwechsel alle Spieler „beitreten“
// lassen); eine Übergabe, die ihr Ziel erreicht hat, meldet „abgeschlossen“
// statt „hostet jetzt“ und „online“.
func Aus(alt, neu *api.Status) []Ereignis {
	if alt == nil || neu == nil {
		return nil
	}
	var out []Ereignis
	welt := laufendeWelt(neu)

	switch {
	case alt.Maintenance == nil && neu.Maintenance != nil:
		out = append(out, Ereignis{Art: Wartung, Grund: neu.Maintenance.Reason})
	case alt.Maintenance != nil && neu.Maintenance == nil:
		out = append(out, Ereignis{Art: Wartung, Ende: true})
	}

	hostAlt, hostNeu := hostOf(alt), hostOf(neu)
	hAlt, hNeu := uebergabeAktiv(alt.Handover), uebergabeAktiv(neu.Handover)
	fertig := false
	switch {
	case hAlt == nil && hNeu != nil:
		out = append(out, Ereignis{Art: Uebergabe, Wer: name(hostNeu), WerKey: schluessel(hostNeu),
			Nach: name(&hNeu.To), NachKey: schluessel(&hNeu.To)})
	case hAlt != nil && hNeu == nil && hostNeu != nil && schluessel(hostNeu) != "" && schluessel(hostNeu) == schluessel(&hAlt.To):
		fertig = true
		out = append(out, Ereignis{Art: Uebergabe, Ende: true, Wer: name(hostAlt), WerKey: schluessel(hostAlt),
			Nach: name(hostNeu), NachKey: schluessel(hostNeu)})
	}

	startet := func() {
		e := Ereignis{Art: HostStartet, Wer: name(hostNeu), WerKey: schluessel(hostNeu), Welt: welt}
		out = append(out, e)
		if neu.HostReady {
			e.Art = WeltOnline
			out = append(out, e)
		}
	}
	switch {
	case hostAlt == nil && hostNeu == nil:
	case hostAlt == nil:
		if !fertig {
			startet()
		}
	case hostNeu == nil:
		// Während einer Wartung oder Übergabe ist das Ende des Hostens
		// erwartet und hat seine eigene Meldung.
		if hAlt == nil && alt.Maintenance == nil && neu.Maintenance == nil {
			out = append(out, Ereignis{Art: NiemandHostet, Wer: name(hostAlt), WerKey: schluessel(hostAlt), Welt: laufendeWelt(alt)})
		}
	case schluessel(hostAlt) != schluessel(hostNeu):
		if !fertig {
			startet()
		}
	default:
		if !alt.HostReady && neu.HostReady && !fertig {
			out = append(out, Ereignis{Art: WeltOnline, Wer: name(hostNeu), WerKey: schluessel(hostNeu), Welt: welt})
		}
		if alt.HostReady && neu.HostReady && hAlt == nil && hNeu == nil {
			neuP, wegP := spielerDiff(alt.Players, neu.Players)
			if len(neuP) > 0 || len(wegP) > 0 {
				out = append(out, Ereignis{Art: Spieler, Neu: neuP, Weg: wegP, Welt: welt})
			}
		}
	}
	return out
}

// hostOf ist der Host, auf den sich Hinweise beziehen: nil, wenn niemand
// hostet oder der Zuschlag nur zum Hochladen dient (kein Server).
func hostOf(st *api.Status) *api.HostInfo {
	if st.Host == nil || st.HostUploading {
		return nil
	}
	return st.Host
}

func uebergabeAktiv(h *api.HandoverState) *api.HandoverState {
	if h == nil {
		return nil
	}
	switch h.State {
	case api.HandoverPreparing, api.HandoverStarting, api.HandoverReady:
		return h
	}
	return nil
}

func name(h *api.HostInfo) string {
	if h == nil {
		return ""
	}
	return h.MinecraftName
}

// schluessel ist der Vergleichswert eines Hosts: die pseudonyme Kennung, bei
// älteren Koordinatoren die E-Mail-Adresse (klein). Fremde Adressen sendet der
// Koordinator nicht mehr (Datenschutz).
func schluessel(h *api.HostInfo) string { return h.Schluessel() }

// laufendeWelt ist der Name der laufenden Welt, aber nur, wenn die Gruppe
// mehrere hat (sonst ist er nichtssagend).
func laufendeWelt(st *api.Status) string {
	if len(st.Worlds) < 2 || st.RunningWorld == "" {
		return ""
	}
	for _, w := range st.Worlds {
		if w.ID == st.RunningWorld {
			return w.Name
		}
	}
	return ""
}

// spielerDiff liefert, wer in neu dazugekommen und gegangen ist (ohne
// Rücksicht auf Groß-/Kleinschreibung, Reihenfolge wie in der Liste).
func spielerDiff(alt, neu []string) (dazu, weg []string) {
	in := func(liste []string, n string) bool {
		return slices.ContainsFunc(liste, func(x string) bool { return strings.EqualFold(x, n) })
	}
	for _, n := range neu {
		if n != "" && !in(alt, n) && !in(dazu, n) {
			dazu = append(dazu, n)
		}
	}
	for _, n := range alt {
		if n != "" && !in(neu, n) && !in(weg, n) {
			weg = append(weg, n)
		}
	}
	return dazu, weg
}

// maxNamen: So viele Namen stehen in einer Aufzählung, der Rest als „N weitere“.
const maxNamen = 5

// Text ist die kurze deutsche Meldung zu e (reiner Text, ohne Gruppennamen).
// san bereinigt jeden fremden Text (Namen, Wartungsgrund); es darf nil sein.
func Text(e Ereignis, san func(string) string) string {
	if san == nil {
		san = func(s string) string { return s }
	}
	wer := func(s string) string {
		if s = san(s); s == "" {
			return Anonym
		}
		return s
	}
	switch e.Art {
	case HostStartet:
		return wer(e.Wer) + " hostet jetzt – die Welt ist gleich bereit."
	case WeltOnline:
		return "Die Welt ist online (Host " + wer(e.Wer) + ")."
	case NiemandHostet:
		return "Niemand hostet mehr – die Welt ist offline."
	case Spieler:
		var teile []string
		if len(e.Neu) > 0 {
			teile = append(teile, Aufzaehlung(e.Neu, san)+beitritt(len(e.Neu)))
		}
		if len(e.Weg) > 0 {
			teile = append(teile, Aufzaehlung(e.Weg, san)+verlassen(len(e.Weg)))
		}
		return strings.Join(teile, ", ") + "."
	case Wartung:
		if e.Ende {
			return "Wartung vorbei – ihr könnt wieder spielen."
		}
		t := "Wartung am Server angekündigt"
		if g := san(e.Grund); g != "" {
			t += " (" + g + ")"
		}
		return t + ". Der Server stoppt gleich sauber, danach ist Spielen wieder möglich."
	case Uebergabe:
		if e.Ende {
			return "Host-Übergabe abgeschlossen – " + wer(e.Nach) + " hostet jetzt."
		}
		von := "Der Host"
		if e.Wer != "" {
			von = wer(e.Wer)
		}
		return "Host-Übergabe läuft: " + von + " gibt die Welt an " + wer(e.Nach) + " weiter."
	case Beitrittsanfrage:
		return "Neue Beitrittsanfrage von " + wer(e.Wer)
	}
	return ""
}

func beitritt(n int) string {
	if n == 1 {
		return " ist beigetreten"
	}
	return " sind beigetreten"
}

func verlassen(n int) string {
	if n == 1 {
		return " hat die Welt verlassen"
	}
	return " haben die Welt verlassen"
}

// Aufzaehlung: „A“, „A und B“, „A, B und C“, bei mehr als fünf „…und 3 weitere“.
func Aufzaehlung(namen []string, san func(string) string) string {
	if san == nil {
		san = func(s string) string { return s }
	}
	rest := 0
	if len(namen) > maxNamen {
		namen, rest = namen[:maxNamen], len(namen)-maxNamen
	}
	teile := make([]string, 0, len(namen)+1)
	for _, n := range namen {
		if n = san(n); n == "" {
			n = Anonym
		}
		teile = append(teile, n)
	}
	if rest > 0 {
		teile = append(teile, strconv.Itoa(rest)+" weitere")
	}
	switch len(teile) {
	case 0:
		return ""
	case 1:
		return teile[0]
	}
	return strings.Join(teile[:len(teile)-1], ", ") + " und " + teile[len(teile)-1]
}

// Kurz macht aus fremdem Text eine einzelne kurze Zeile: Steuerzeichen und
// Zeilenumbrüche werden zu Leerzeichen, Leerraum wird zusammengezogen, mehr
// als max Zeichen werden mit „…“ gekürzt.
func Kurz(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}

// NurWindows meldet, ob es das Ereignis art nur als Windows-Hinweis gibt.
func NurWindows(art Art) bool {
	i := slices.IndexFunc(Katalog, func(s Schalter) bool { return s.Art == art })
	return i >= 0 && Katalog[i].NurWindows
}
