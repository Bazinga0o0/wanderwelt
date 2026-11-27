package app

// Lesbare Dialoge der Einrichtung unter Windows.
//
// Die Bibliothek zenity (v0.10.15) baut unter Windows für List, Entry und
// Progress FESTE Fenster: 281×281, 281×141 bzw. 281×133 Punkte (dpi-skaliert)
// mit einer einzigen Textzeile (Label 241×16 Punkte, kein Umbruch; was nicht
// passt, wird mit „…“ abgeschnitten). Das Listenfeld hat 241 Punkte Breite
// und keinen waagerechten Bildlauf. zenity.Width und zenity.Height gelten nur
// unter Unix. Nur die Meldungsfenster (Info, Question, Error, Warning →
// MessageBox) passen sich dem Text an.
//
// Darum gilt hier: List-, Entry- und Progress-Fenster bekommen genau EINE
// kurze Zeile (maxZeile Zeichen), Listeneinträge sind höchstens maxEintrag
// Zeichen lang, und jede längere Erklärung kommt vorher als Meldungsfenster
// (showInfo). Außerhalb dieser Datei darf niemand zenity.List, zenity.Entry,
// zenity.Progress oder ProgressDialog.Text aufrufen; der Wächter-Test
// (dialoge_test.go) liest die Quelldateien und lehnt es ab.
//
// Gemessen (Windows 11, 125 % Skalierung): etwa 40 normale Zeichen passen in
// die Zeile, bei breiten Buchstaben (W, M) weniger; das Listenfeld zeigt
// etwa 34 Zeichen neben der Bildlaufleiste.

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ncruces/zenity"
)

const (
	// maxZeile: Höchstzahl der Zeichen (Runen) in der Textzeile von Eingabe-,
	// Listen- und Fortschrittsfenster.
	maxZeile = 40
	// maxEintrag: Höchstzahl der Zeichen eines Listeneintrags.
	maxEintrag = 34

	ellipse = "…"

	titelEinrichten = "wanderwelt einrichten"
	titelWillkommen = "Willkommen bei wanderwelt!"

	// Kurze Beschriftungen der Fenster (alle höchstens maxZeile lang; der Test
	// prüft das).
	labelWeiter       = "Wie geht es weiter?"
	labelGruppeWahl   = "Wie kommst du in eine Gruppe?"
	labelGruppeBeitr  = "Welcher Gruppe beitreten?"
	labelToken        = "Token einfügen (Strg+V):"
	labelGruppencode  = "Gruppencode einfügen:"
	labelServer       = "Server-Adresse (z. B. name.de):"
	labelMinecraftNam = "Dein Minecraft-Java-Name:"
	labelWelt         = "Welche Welt übernehmen?"

	// Texte der Fortschrittsfenster.
	textWartenAufFreigabe = "Warte auf Freigabe durch einen Admin …"
	textAbfrageKlemmt     = "Abfrage klappt gerade nicht …"
	textPruefeDeps        = "Prüfe Java, Server und Mods …"
)

// Die echten Fenster; im Test austauschbar, damit er sieht, was wirklich an
// zenity geht.
var (
	rawListe       = zenity.List
	rawEingabe     = zenity.Entry
	rawFortschritt = zenity.Progress
)

// eineZeile macht aus text eine einzelne Zeile von höchstens max Zeichen
// (Runen): die erste nicht leere Zeile, außen ohne Leerraum; ist sie noch zu
// lang, wird sie mit „…“ gekürzt. Das Kürzen ist nur die Notbremse – die
// Aufrufer wählen kurze Texte.
func eineZeile(text string, max int) string {
	text = strings.ReplaceAll(text, "\r", "")
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return kuerze(l, max)
		}
	}
	return ""
}

// kuerze kürzt s auf höchstens max Runen; das letzte Zeichen ist dann „…“.
func kuerze(s string, max int) string {
	if max < 1 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + ellipse
}

// zeileMitName setzt vor+name+nach zusammen und kürzt, wenn nötig, den Namen
// (nicht den festen Text), damit die Zeile in max Zeichen passt, etwa
// „Lade fabric-api-0.100….jar (2,1 MB) …“. Bleibt für den Namen zu wenig
// Platz, kürzt eineZeile die ganze Zeile.
func zeileMitName(vor, name, nach string, max int) string {
	frei := max - utf8.RuneCountInString(vor) - utf8.RuneCountInString(nach)
	if frei < 4 {
		return eineZeile(vor+name+nach, max)
	}
	return eineZeile(vor+kuerze(name, frei)+nach, max)
}

// kurzeEintraege kürzt die Listeneinträge auf maxEintrag Zeichen und macht
// sie eindeutig („[2]“ hinter dem Doppelten): Die Auswahl geht über den Text
// des Eintrags zurück auf den Index.
func kurzeEintraege(items []string) []string {
	out := make([]string, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		s := eineZeile(it, maxEintrag)
		for n := 2; seen[s]; n++ {
			suffix := fmt.Sprintf(" [%d]", n)
			s = kuerze(eineZeile(it, maxEintrag), maxEintrag-utf8.RuneCountInString(suffix)) + suffix
		}
		seen[s] = true
		out[i] = s
	}
	return out
}

// zeigeListe öffnet das Listenfenster mit einer kurzen Zeile label und
// gekürzten Einträgen und liefert den Index des gewählten Eintrags. Fehler:
// die von zenity (zenity.ErrCanceled bei Abbruch).
func zeigeListe(label string, items []string, opts ...zenity.Option) (int, error) {
	kurz := kurzeEintraege(items)
	s, err := rawListe(eineZeile(label, maxZeile), kurz, opts...)
	if err != nil {
		return -1, err
	}
	i := slices.Index(kurz, s)
	if i < 0 {
		return -1, zenity.ErrCanceled
	}
	return i, nil
}

// listeMitMeldung zeigt erst die Erklärung intro (wenn nicht leer) im
// Meldungsfenster, dann die Liste mit der kurzen Zeile label.
func listeMitMeldung(intro, label string, items []string, opts ...zenity.Option) (int, error) {
	if intro != "" {
		showInfo(intro)
	}
	return zeigeListe(label, items, opts...)
}

// zeigeEingabe öffnet das Eingabefenster mit einer kurzen Zeile label.
func zeigeEingabe(label string, opts ...zenity.Option) (string, error) {
	return rawEingabe(eineZeile(label, maxZeile), opts...)
}

// eingabeMitMeldung zeigt erst hinweis (wenn nicht leer: Erklärung, Fehler
// oder Hinweis) im Meldungsfenster, dann das Eingabefenster mit der kurzen
// Zeile label.
func eingabeMitMeldung(hinweis, label string, opts ...zenity.Option) (string, error) {
	if hinweis != "" {
		showInfo(hinweis)
	}
	return zeigeEingabe(label, opts...)
}

// oeffneFortschritt öffnet ein Fortschrittsfenster. Den Text setzt man nur
// mit setzeFortschrittText.
func oeffneFortschritt(opts ...zenity.Option) (zenity.ProgressDialog, error) {
	return rawFortschritt(opts...)
}

// setzeFortschrittText setzt die Zeile des Fortschrittsfensters (höchstens
// maxZeile Zeichen, einzeilig); dlg nil: nichts.
func setzeFortschrittText(dlg zenity.ProgressDialog, text string) {
	if dlg == nil {
		return
	}
	_ = dlg.Text(eineZeile(text, maxZeile))
}
