package fenster

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Mehrere Welten pro Gruppe im Fenster.
//
// Reiter „Start“: Karte „WELT“ (ab zwei Welten) mit der Auswahl – gespielt wird
// die Welt, die gerade läuft („läuft gerade: <Name>“), sonst die gewählte;
// gewechselt wird nur, solange niemand hostet. Reiter „Welt“, nur für Admins:
// Karte „WELTEN VERWALTEN“ (anlegen, umbenennen, Eigenschaften ändern,
// löschen mit Rückfrage – nicht, solange die Welt läuft). Die Liste kommt
// mit dem Status der Instanz (Status.Worlds); Namen sind Text des
// Koordinators und werden nur als Text gezeichnet.

// Aktionen der Steuerung (fensterapi), Grenzen und Namen der Spielmodi und
// Schwierigkeiten auf Deutsch.
const (
	weltAuftragAnlegen  = "weltneu"
	weltAuftragAendern  = "weltaendern"
	weltAuftragLoeschen = "weltloeschen"
	weltAuftragWaehlen  = "welt"

	maxWeltNameFenster     = 40 // wie der Koordinator
	weltNameKuerzenAuswahl = 24
	standardBezeichnung    = "Gruppenstandard"

	// Chat-Verlauf je Welt (Server-Mod chatverlauf): Werte des Schalters.
	chatAn      = "an"
	chatAusWert = "aus"
	chatHinweis = "Datenschutz: Bei „an“ liegt der Chat dieser Welt dauerhaft (auf die Größengrenze gekürzt) in der Welt " +
		"und damit bis zu 30 Tage in den Ständen auf dem Koordinator. Alle Mitglieder sollten das wissen. " +
		"Bei „aus“ wird nichts mehr gespeichert oder gezeigt; schon gespeicherter Chat wird nicht gelöscht. " +
		"Gilt ab dem nächsten Start der Welt."
)

var chatWerte = []string{chatAn, chatAusWert}

func chatText(w string) string {
	if w == chatAusWert {
		return "Aus"
	}
	return "An"
}

var (
	spielmodiNamen       = map[string]string{"survival": "Überleben", "creative": "Kreativ", "adventure": "Abenteuer"}
	schwierigkeitenNamen = map[string]string{"peaceful": "Friedlich", "easy": "Leicht", "normal": "Normal", "hard": "Schwer"}
	spielmodiReihenfolge = []string{"", "survival", "creative", "adventure"}
	schwierigkeitenFolge = []string{"", "peaceful", "easy", "normal", "hard"}
)

// modusText und schwierigkeitText: deutscher Text ("" = Gruppenstandard).
func modusText(m string) string {
	if t, ok := spielmodiNamen[m]; ok {
		return t
	}
	return standardBezeichnung
}

func schwierigkeitText(s string) string {
	if t, ok := schwierigkeitenNamen[s]; ok {
		return t
	}
	return standardBezeichnung
}

// weltEigenschaften beschreibt Spielmodus, Schwierigkeit und – wenn vom
// Standard abweichend – den Chat-Verlauf einer Welt in einem Satzstück (""
// wenn alles Standard ist).
func weltEigenschaften(w api.GroupWorld) string {
	var teile []string
	if w.Gamemode != "" {
		teile = append(teile, "Spielmodus "+modusText(w.Gamemode))
	}
	if w.Difficulty != "" {
		teile = append(teile, "Schwierigkeit "+schwierigkeitText(w.Difficulty))
	}
	switch {
	case w.ChatOff:
		teile = append(teile, "Chat-Verlauf aus")
	case api.ChatMiBOrDefault(w.ChatMiB) != api.ChatMiBStandard:
		teile = append(teile, fmt.Sprintf("Chat-Verlauf bis %d MiB", w.ChatMiB))
	}
	return strings.Join(teile, " · ")
}

// chatMiBLesen liest die Größengrenze aus dem Eingabefeld (ganze Zahl von
// api.ChatMiBMin bis api.ChatMiBMax).
func chatMiBLesen(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < api.ChatMiBMin || n > api.ChatMiBMax {
		return 0, false
	}
	return n, true
}

// weltenListe liefert die Welten der Gruppe aus dem Zustand (leer ohne Liste).
func weltenListe(s schnappschuss) []api.GroupWorld {
	if s.z == nil || s.z.Status == nil {
		return nil
	}
	return s.z.Status.Worlds
}

// weltPerID sucht die Welt id in der Liste.
func weltPerID(list []api.GroupWorld, id string) (api.GroupWorld, bool) {
	i := slices.IndexFunc(list, func(w api.GroupWorld) bool { return w.ID == id })
	if i < 0 {
		return api.GroupWorld{}, false
	}
	return list[i], true
}

// ---- Auswahl auf dem Reiter „Start“ ----

type weltAuswahl struct {
	knoepfe map[string]*widget.Clickable
	wartet  bool
	fehler  string
}

// layout zeichnet die Karte „WELT“ (nichts bei weniger als zwei Welten).
func (v *weltAuswahl) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	list := weltenListe(s)
	if len(list) < 2 {
		return layout.Dimensions{}
	}
	t := u.th
	z := s.z
	laufend := z.Welt.Laufend
	sperre := z.Welt.Sperre
	if v.knoepfe == nil {
		v.knoepfe = map[string]*widget.Clickable{}
	}
	for _, w := range list {
		k := v.knoepfe[w.ID]
		if k == nil {
			k = new(widget.Clickable)
			v.knoepfe[w.ID] = k
		}
		if !k.Clicked(gtx) || w.ID == z.Welt.Aktiv || v.wartet {
			continue
		}
		if sperre != "" {
			u.zeige("fehler", sperre, gtx.Now)
			continue
		}
		w := w
		v.wartet, v.fehler = true, ""
		u.d.aktion(weltAuftragWaehlen, map[string]string{"id": w.ID}, func(err error) {
			v.wartet = false
			if err != nil {
				v.fehler = err.Error()
				return
			}
			u.zeige("info", "Welt gewählt: "+w.Name, time.Now())
		})
	}
	aktiv, _ := weltPerID(list, z.Welt.Aktiv)
	kopf := "Gespielt wird: " + aktiv.Name
	if laufend != "" {
		kopf = "Läuft gerade: " + aktiv.Name
	}
	eig := weltEigenschaften(aktiv)
	if aktiv.Empty {
		eig = strings.TrimSuffix("noch leer · "+eig, " · ")
	}
	chips := make([]layout.Widget, 0, len(list))
	for _, w := range list {
		k := v.knoepfe[w.ID]
		art := knopfNeben
		if w.ID == z.Welt.Aktiv {
			art = knopfHaupt
		}
		frei := w.ID != z.Welt.Aktiv && sperre == "" && !v.wartet
		chips = append(chips, func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, k, kuerze(w.Name, weltNameKuerzenAuswahl), art, frei)
		})
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("WELT")),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.text(gtx, schrift{groesse: 16, fett: true, farbe: t.f.text, zeilen: 2}, kopf)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if eig == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Top: 4}.Layout(gtx, t.leise(eig))
			}),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return fluss(gtx, 8, chips) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				switch {
				case v.fehler != "":
					return layout.Inset{Top: 10}.Layout(gtx, t.farbig(v.fehler, t.f.fehler))
				case sperre != "":
					return layout.Inset{Top: 10}.Layout(gtx, t.leise(sperre))
				}
				return layout.Dimensions{}
			}),
		)
	})
}

// ---- Verwalten auf dem Reiter „Welt“ (nur Admins) ----

type weltenVerwaltung struct {
	offen         bool
	auf, zu       widget.Clickable
	neu           widget.Clickable
	aendern       map[string]*widget.Clickable
	loeschen      map[string]*widget.Clickable
	wartet        bool
	fehler        string
	formular      bool   // das Formular ist offen
	formularID    string // "": neue Welt, sonst die Welt, die geändert wird
	nameEd        widget.Editor
	modus, schwer string
	modusKn       map[string]*widget.Clickable
	schwerKn      map[string]*widget.Clickable
	chatAus       bool // Chat-Verlauf der Welt aus
	chatKn        map[string]*widget.Clickable
	chatMiBEd     widget.Editor // Größengrenze in MiB
	speichern     widget.Clickable
	abbrechen     widget.Clickable
}

func (v *weltenVerwaltung) formularOeffnen(w *api.GroupWorld) {
	v.formular, v.fehler = true, ""
	v.nameEd.SingleLine = true
	v.nameEd.MaxLen = maxWeltNameFenster
	v.chatMiBEd.SingleLine = true
	v.chatMiBEd.MaxLen = 2
	v.chatMiBEd.Filter = "0123456789"
	if w == nil {
		v.formularID, v.modus, v.schwer, v.chatAus = "", "", "", false
		v.nameEd.SetText("")
		v.chatMiBEd.SetText(strconv.Itoa(api.ChatMiBStandard))
		return
	}
	v.formularID, v.modus, v.schwer, v.chatAus = w.ID, w.Gamemode, w.Difficulty, w.ChatOff
	v.nameEd.SetText(w.Name)
	v.chatMiBEd.SetText(strconv.Itoa(api.ChatMiBOrDefault(w.ChatMiB)))
}

// layout zeichnet die Karte „WELTEN VERWALTEN“.
func (v *weltenVerwaltung) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	list := weltenListe(s)
	if v.auf.Clicked(gtx) {
		v.offen = true
	}
	if v.zu.Clicked(gtx) {
		v.offen, v.formular = false, false
	}
	if v.neu.Clicked(gtx) && !v.wartet {
		v.formularOeffnen(nil)
	}
	laufend := ""
	if s.z != nil {
		laufend = s.z.Welt.Laufend
	}
	for _, w := range list {
		w := w
		if k := klick(&v.aendern, w.ID); k.Clicked(gtx) && !v.wartet {
			v.formularOeffnen(&w)
		}
		if k := klick(&v.loeschen, w.ID); k.Clicked(gtx) && !v.wartet {
			v.frageLoeschen(u, w, len(list), laufend == w.ID)
		}
	}
	if v.formular {
		v.formularAuswerten(gtx, u, list)
	}

	teile := []layout.FlexChild{
		layout.Rigid(t.klein("WELTEN VERWALTEN")),
		layout.Rigid(layout.Spacer{Height: 10}.Layout),
	}
	if !v.offen {
		teile = append(teile,
			layout.Rigid(t.leise("Mehrere Welten für die Gruppe: anlegen, umbenennen, Spielmodus, Schwierigkeit und Chat-Verlauf ändern, löschen. Es läuft immer nur eine Welt.")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, &v.auf, "Welten verwalten", knopfNeben, true)
			}),
		)
		return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, teile...)
		})
	}
	if len(list) == 0 {
		teile = append(teile, layout.Rigid(t.leise("Dieser Koordinator kennt noch keine mehreren Welten (oder die Liste ist noch nicht geladen).")))
	}
	for i, w := range list {
		erste, letzte := i == 0, i == len(list)-1
		lauf := laufend == w.ID
		teile = append(teile, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return kartenZeile(gtx, t, erste, letzte, func(gtx layout.Context) layout.Dimensions {
				return v.zeile(gtx, u, w, lauf)
			})
		}))
	}
	teile = append(teile,
		layout.Rigid(layout.Spacer{Height: 12}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if v.formular {
				return v.formularZeichnen(gtx, u)
			}
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.neu, "Neue Welt", knopfHaupt, !v.wartet)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.zu, "Schließen", knopfNeben, true)
				}),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if v.fehler == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: 8}.Layout(gtx, t.farbig(v.fehler, t.f.fehler))
		}),
	)
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, teile...)
	})
}

// zeile: eine Welt mit Name, Eigenschaften und den Knöpfen „Ändern“ und
// „Löschen“ (gesperrt, solange die Welt läuft).
func (v *weltenVerwaltung) zeile(gtx layout.Context, u *oberflaeche, w api.GroupWorld, laeuft bool) layout.Dimensions {
	t := u.th
	eig := weltEigenschaften(w)
	switch {
	case w.Empty && eig != "":
		eig = "noch leer · " + eig
	case w.Empty:
		eig = "noch leer"
	case eig == "":
		eig = fmt.Sprintf("Stand %d", w.HeadGeneration)
	}
	if laeuft {
		eig = "läuft gerade · " + eig
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.text(gtx, schrift{groesse: 14.5, fett: true, farbe: t.f.text, zeilen: 1}, w.Name)
				}),
				layout.Rigid(t.leise(eig)),
			)
		}),
		layout.Rigid(layout.Spacer{Width: 10}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, klick(&v.aendern, w.ID), "Ändern", knopfNeben, !v.wartet)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return t.knopf(gtx, klick(&v.loeschen, w.ID), "Löschen", knopfNeben, !v.wartet && !laeuft)
		}),
	)
}

// frageLoeschen: Rückfrage vor dem Löschen. Läuft die Welt oder ist sie die
// letzte, gibt es nur den Hinweis.
func (v *weltenVerwaltung) frageLoeschen(u *oberflaeche, w api.GroupWorld, anzahl int, laeuft bool) {
	switch {
	case laeuft:
		v.fehler = "Die Welt „" + w.Name + "“ läuft gerade – gelöscht wird erst nach dem Ende des Hostens."
		return
	case anzahl < 2:
		v.fehler = "Die letzte Welt der Gruppe lässt sich nicht löschen."
		return
	}
	v.fehler = ""
	text := "Die Welt „" + w.Name + "“ wird aus der Liste der Gruppe genommen und kann nicht mehr gespielt werden."
	if !w.Empty {
		text += "\n\nIhre Stände (Karte, Inventare, Spieler-IDs) bleiben noch auf dem Koordinator liegen, bis der Speicher aufgeräumt wird; wiederherstellen lassen sie sich über das Fenster nicht."
	}
	u.frage(rueckfrage{titel: "Welt „" + kuerze(w.Name, 30) + "“ löschen?", text: text + "\n\nWirklich löschen?",
		ja: "Löschen", nein: "Behalten", gefahr: true, beiJa: func() {
			v.wartet, v.fehler = true, ""
			u.d.aktion(weltAuftragLoeschen, map[string]string{"id": w.ID, "name": w.Name}, func(err error) {
				v.wartet = false
				if err != nil {
					v.fehler = err.Error()
					return
				}
				u.zeige("info", "Welt gelöscht: "+w.Name, time.Now())
			})
		}})
}

// formularAuswerten reagiert auf die Knöpfe des Formulars.
func (v *weltenVerwaltung) formularAuswerten(gtx layout.Context, u *oberflaeche, list []api.GroupWorld) {
	for _, m := range spielmodiReihenfolge {
		if klick(&v.modusKn, m).Clicked(gtx) {
			v.modus = m
		}
	}
	for _, d := range schwierigkeitenFolge {
		if klick(&v.schwerKn, d).Clicked(gtx) {
			v.schwer = d
		}
	}
	for _, k := range []string{chatAn, chatAusWert} {
		if klick(&v.chatKn, k).Clicked(gtx) {
			v.chatAus = k == chatAusWert
		}
	}
	if v.abbrechen.Clicked(gtx) {
		v.formular, v.fehler = false, ""
		return
	}
	if !v.speichern.Clicked(gtx) || v.wartet {
		return
	}
	mib, ok := chatMiBLesen(v.chatMiBEd.Text())
	if !ok {
		v.fehler = fmt.Sprintf("Die Größengrenze des Chat-Verlaufs muss eine ganze Zahl von %d bis %d MiB sein.", api.ChatMiBMin, api.ChatMiBMax)
		return
	}
	name := strings.TrimSpace(v.nameEd.Text())
	if name == "" {
		v.fehler = "Bitte einen Namen für die Welt eingeben."
		return
	}
	for _, w := range list {
		if w.ID != v.formularID && strings.EqualFold(w.Name, name) {
			v.fehler = "Es gibt schon eine Welt „" + w.Name + "“."
			return
		}
	}
	v.wartet, v.fehler = true, ""
	fertig := func(was string) func(error) {
		return func(err error) {
			v.wartet = false
			if err != nil {
				v.fehler = err.Error()
				return
			}
			v.formular = false
			u.zeige("info", was+": "+name, time.Now())
		}
	}
	if v.formularID == "" {
		req := api.WorldCreate{Name: name, Gamemode: v.modus, Difficulty: v.schwer, ChatOff: v.chatAus}
		if mib != api.ChatMiBStandard {
			req.ChatMiB = mib
		}
		u.d.aktion(weltAuftragAnlegen, req, fertig("Welt angelegt"))
		return
	}
	modus, schwer := v.modus, v.schwer
	patch := api.WorldPatch{Name: &name, Gamemode: &modus, Difficulty: &schwer}
	// Die Chat-Felder nur senden, wenn sie sich ändern: Ein älterer
	// Koordinator kennt sie nicht und lehnt unbekannte Felder ab.
	if alt, ok := weltPerID(list, v.formularID); ok {
		if alt.ChatOff != v.chatAus {
			chatAus := v.chatAus
			patch.ChatOff = &chatAus
		}
		if api.ChatMiBOrDefault(alt.ChatMiB) != mib {
			patch.ChatMiB = &mib
		}
	}
	u.d.aktion(weltAuftragAendern, map[string]any{"id": v.formularID, "patch": patch}, fertig("Welt geändert"))
}

// formularZeichnen: Name, Spielmodus, Schwierigkeit, Speichern/Abbrechen.
func (v *weltenVerwaltung) formularZeichnen(gtx layout.Context, u *oberflaeche) layout.Dimensions {
	t := u.th
	auswahl := func(titel string, werte []string, text func(string) string, aktuell string, kn *map[string]*widget.Clickable) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			var chips []layout.Widget
			for _, w := range werte {
				art := knopfNeben
				if w == aktuell {
					art = knopfHaupt
				}
				chips = append(chips, func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, klick(kn, w), text(w), art, !v.wartet)
				})
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(t.leise(titel)),
				layout.Rigid(layout.Spacer{Height: 6}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return fluss(gtx, 8, chips) }),
			)
		}
	}
	aktuellChat := chatAn
	if v.chatAus {
		aktuellChat = chatAusWert
	}
	titel := "Neue Welt"
	if v.formularID != "" {
		titel = "Welt ändern"
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(t.titel(titel)),
		layout.Rigid(layout.Spacer{Height: 8}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(t.leise("Name")),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return eingabe(gtx, t, &v.nameEd, "z. B. Survival", 260) }),
			)
		}),
		layout.Rigid(layout.Spacer{Height: 12}.Layout),
		layout.Rigid(auswahl("Spielmodus (für neue Spieler)", spielmodiReihenfolge, modusText, v.modus, &v.modusKn)),
		layout.Rigid(layout.Spacer{Height: 12}.Layout),
		layout.Rigid(auswahl("Schwierigkeit", schwierigkeitenFolge, schwierigkeitText, v.schwer, &v.schwerKn)),
		layout.Rigid(layout.Spacer{Height: 12}.Layout),
		layout.Rigid(auswahl("Chat-Verlauf (wird neuen Spielern beim Beitritt gezeigt)", chatWerte, chatText, aktuellChat, &v.chatKn)),
		layout.Rigid(layout.Spacer{Height: 8}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(t.leise("Größengrenze")),
				layout.Rigid(layout.Spacer{Width: 10}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return eingabe(gtx, t, &v.chatMiBEd, "5", 70) }),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(t.leise(fmt.Sprintf("MiB (%d bis %d, Standard %d)", api.ChatMiBMin, api.ChatMiBMax, api.ChatMiBStandard))),
			)
		}),
		layout.Rigid(layout.Spacer{Height: 6}.Layout),
		layout.Rigid(t.leise(chatHinweis)),
		layout.Rigid(layout.Spacer{Height: 14}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.speichern, "Speichern", knopfHaupt, !v.wartet)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return t.knopf(gtx, &v.abbrechen, "Abbrechen", knopfNeben, !v.wartet)
				}),
			)
		}),
	)
}
