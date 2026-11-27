package fenster

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// Reiter „Server“ (nur Admins): Whitelist, Ops und Banns der Gruppe,
// Spielregeln der laufenden Welt und „Befehl senden“. Jede Zeile wirkt
// sofort (Hinzufügen, Entfernen, Setzen); das Ergebnis kommt als Hinweis.
// Alles sind Textknoten. Maßgeblich ist der Koordinator; der laufende Host
// übernimmt Änderungen nach wenigen Sekunden, sonst gelten sie beim nächsten
// Start.

// serverTakt: So oft holt das Fenster die Listen bei sichtbarem Reiter.
const serverTakt = 15 * time.Second

// serverDaten sind Listen und Spielregeln im Schnappschuss.
type serverDaten struct {
	l      *api.ServerLists
	err    string
	ab     time.Time // wann geholt (Null: noch nie)
	laeuft bool

	regeln      *api.Gamerules
	regelnErr   string
	regelnLaedt bool
}

// serverQuelle ist die Fähigkeit der Quelle für den Reiter „Server“
// (*fensterapi.Client kann es; Test-Quellen müssen es nicht).
type serverQuelle interface {
	ServerListen(ctx context.Context) (*api.ServerLists, error)
	Spielregeln(ctx context.Context) (*api.Gamerules, error)
	AktionAntwort(ctx context.Context, name string, body, out any) error
}

// serverRunde holt die Listen, wenn der Reiter „Server“ sichtbar ist, der
// Nutzer Admin ist und der letzte Abruf länger als serverTakt zurückliegt.
func (d *daten) serverRunde(ctx context.Context, c quelle, r reiter, admin bool) {
	sq, ok := c.(serverQuelle)
	d.mu.Lock()
	hole := ok && admin && r == reiterServer && !d.s.server.laeuft && faellig(d.s.server.ab, d.jetzt(), serverTakt)
	if hole {
		d.s.server.laeuft = true
	}
	d.mu.Unlock()
	if !hole {
		return
	}
	actx, cancel := context.WithTimeout(ctx, anfrageFrist)
	l, err := sq.ServerListen(actx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	g := &d.s.server
	g.laeuft, g.ab = false, d.jetzt()
	if err != nil {
		g.err = err.Error()
	} else {
		g.l, g.err = l, ""
	}
	d.mu.Unlock()
}

// serverNeuLaden: „Aktualisieren“ – die nächste Runde holt sofort.
func (d *daten) serverNeuLaden() {
	d.mu.Lock()
	d.s.server.ab = time.Time{}
	d.mu.Unlock()
	d.wecken()
}

// serverListenSetzen übernimmt die Listen aus der Antwort einer Änderung.
func (d *daten) serverListenSetzen(l api.ServerLists) {
	d.mu.Lock()
	d.s.server.l, d.s.server.err, d.s.server.ab = &l, "", d.jetzt()
	d.mu.Unlock()
	d.malen(true)
}

// spielregelnHolen fragt die Spielregeln der laufenden Welt (über den Host;
// das dauert etwas, darum in einer eigenen Goroutine).
func (d *daten) spielregelnHolen() {
	d.mu.Lock()
	sq, ok := d.c.(serverQuelle)
	if !ok || d.s.server.regelnLaedt {
		d.mu.Unlock()
		return
	}
	d.s.server.regelnLaedt = true
	d.mu.Unlock()
	d.malen(true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), aktionsFrist)
		g, err := sq.Spielregeln(ctx)
		cancel()
		d.mu.Lock()
		d.s.server.regelnLaedt = false
		if err != nil {
			d.s.server.regelnErr = err.Error()
		} else {
			d.s.server.regeln, d.s.server.regelnErr = g, ""
		}
		d.mu.Unlock()
		d.malen(true)
	}()
}

// serverAktion schickt eine Aktion des Reiters und liest die Antwort nach
// out; fertig läuft danach in der Oberfläche.
func (d *daten) serverAktion(name string, body, out any, fertig func(error)) {
	d.mu.Lock()
	sq, ok := d.c.(serverQuelle)
	d.mu.Unlock()
	if !ok {
		d.auftrag(func() { fertig(fensterapi.ErrLaeuftNicht) })
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), aktionsFrist)
		err := sq.AktionAntwort(ctx, name, body, out)
		cancel()
		d.auftrag(func() { fertig(err) })
	}()
}

// ---- Modell (rein, getestet) ----

// serverZeile ist eine Zeile einer Liste: Name, Zusatz (Mitglied/Gast, wer
// gebannt hat) und ob sie hier entfernt werden kann.
type serverZeile struct {
	name, zusatz string
	entfernbar   bool
}

// whitelistZeilen: Mitglieder zuerst (nicht hier entfernbar), dann Gäste;
// je Gruppe nach Namen.
func whitelistZeilen(l *api.ServerLists) []serverZeile {
	return spielerZeilen(l.Whitelist, false)
}

// opsZeilen: wie whitelistZeilen; Op entziehen geht bei allen.
func opsZeilen(l *api.ServerLists) []serverZeile {
	return spielerZeilen(l.Ops, true)
}

func spielerZeilen(ps []api.ListPlayer, alleEntfernbar bool) []serverZeile {
	ps = slices.Clone(ps)
	slices.SortFunc(ps, func(a, b api.ListPlayer) int {
		if a.Member != b.Member {
			if a.Member {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	out := make([]serverZeile, 0, len(ps))
	for _, p := range ps {
		z := serverZeile{name: p.Name, zusatz: "Gast", entfernbar: true}
		if p.Member {
			z.zusatz, z.entfernbar = "Mitglied", alleEntfernbar
		}
		out = append(out, z)
	}
	return out
}

// bannZeilen: nach Namen; Zusatz „von X, seit …“ und ein Hinweis bei
// Mitgliedern.
func bannZeilen(l *api.ServerLists, jetzt time.Time) []serverZeile {
	bs := slices.Clone(l.Bans)
	slices.SortFunc(bs, func(a, b api.ListBan) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	out := make([]serverZeile, 0, len(bs))
	for _, b := range bs {
		var teile []string
		if b.By != "" {
			teile = append(teile, "von "+b.By)
		}
		if !b.Since.IsZero() {
			teile = append(teile, seit(b.Since, jetzt))
		}
		if b.Member {
			teile = append(teile, "Mitglied – kommt trotzdem nicht herein")
		}
		out = append(out, serverZeile{name: b.Name, zusatz: strings.Join(teile, ", "), entfernbar: true})
	}
	return out
}

// istSchalterRegel: Wahrheitswerte zeigt der Reiter als Schalter.
func istSchalterRegel(v string) bool { return v == "true" || v == "false" }

// befehlsAntwort: Text unter der Eingabe nach einem Befehl.
func befehlsAntwort(r api.ServerCommandResult) string {
	if strings.TrimSpace(r.Output) == "" {
		if r.OK {
			return "Ausgeführt (der Server hat nichts geantwortet)."
		}
		return "Nicht ausgeführt."
	}
	return r.Output
}

// ---- Ansicht ----

// nameFilter: erlaubte Zeichen eines Minecraft-Namens.
const nameFilter = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"

type serverAnsicht struct {
	liste       widget.List
	aktualisier widget.Clickable

	wlEd, opEd, banEd    widget.Editor
	wlAdd, opAdd, banAdd widget.Clickable
	entf                 map[string]*widget.Clickable // Liste + "/" + Name (klein)
	wartet               bool                         // eine Listenänderung läuft
	fehler               string

	regelnLaden widget.Clickable
	regelnStand *api.Gamerules // die Liste, auf die Schalter und Felder gestellt sind
	regelBool   map[string]*widget.Bool
	regelEd     map[string]*widget.Editor
	regelSetzen map[string]*widget.Clickable
	regelWartet bool

	befEd      widget.Editor
	befSenden  widget.Clickable
	befWartet  bool
	befAntwort string
	befFehler  bool
}

func (v *serverAnsicht) init() {
	if v.entf != nil {
		return
	}
	v.liste.Axis = layout.Vertical
	v.entf = map[string]*widget.Clickable{}
	for _, ed := range []*widget.Editor{&v.wlEd, &v.opEd, &v.banEd} {
		ed.SingleLine, ed.Submit, ed.MaxLen, ed.Filter = true, true, 16, nameFilter
	}
	v.befEd.SingleLine, v.befEd.Submit, v.befEd.MaxLen = true, true, serverbefehl.MaxBefehl
}

func (v *serverAnsicht) knopfFuer(liste, name string) *widget.Clickable {
	k := liste + "/" + strings.ToLower(name)
	c := v.entf[k]
	if c == nil {
		c = new(widget.Clickable)
		v.entf[k] = c
	}
	return c
}

// eingereicht: Klick auf k oder Enter in ed.
func eingereicht(gtx layout.Context, ed *widget.Editor, k *widget.Clickable) bool {
	ja := k.Clicked(gtx)
	for {
		e, ok := ed.Update(gtx)
		if !ok {
			break
		}
		if _, ok := e.(widget.SubmitEvent); ok {
			ja = true
		}
	}
	return ja
}

// aendern schickt eine Listenänderung.
func (v *serverAnsicht) aendern(u *oberflaeche, liste, aktion, name string, danach func()) {
	if v.wartet {
		return
	}
	v.wartet, v.fehler = true, ""
	var res api.ServerListResult
	u.d.serverAktion(fensterapi.AktionServerliste, api.ServerListChange{List: liste, Action: aktion, Name: name}, &res, func(err error) {
		v.wartet = false
		if err != nil {
			v.fehler = err.Error()
			u.zeige("fehler", err.Error(), time.Now())
			return
		}
		u.zeige("info", res.Text, time.Now())
		u.d.serverListenSetzen(res.Lists)
		if danach != nil {
			danach()
		}
	})
}

// klicks wertet Eingaben und Knöpfe der Listen aus.
func (v *serverAnsicht) klicks(gtx layout.Context, u *oberflaeche, l *api.ServerLists) {
	hinzu := func(ed *widget.Editor, k *widget.Clickable, liste string, frage bool) {
		if !eingereicht(gtx, ed, k) || v.wartet {
			return
		}
		name := strings.TrimSpace(ed.Text())
		if !serverbefehl.GueltigerName(name) {
			u.zeige("fehler", "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.", gtx.Now)
			return
		}
		senden := func() { v.aendern(u, liste, api.ListAdd, name, func() { ed.SetText("") }) }
		if frage {
			u.frage(rueckfrage{titel: name + " bannen?",
				text: name + " kommt danach nicht mehr auf den Server, auch nicht als Mitglied. Als Gast fällt er von Whitelist und Ops.",
				ja:   "Bannen", gefahr: true, beiJa: senden})
			return
		}
		senden()
	}
	hinzu(&v.wlEd, &v.wlAdd, api.ListWhitelist, false)
	hinzu(&v.opEd, &v.opAdd, api.ListOps, false)
	hinzu(&v.banEd, &v.banAdd, api.ListBans, true)
	if l == nil {
		return
	}
	for liste, zeilen := range map[string][]serverZeile{
		api.ListWhitelist: whitelistZeilen(l), api.ListOps: opsZeilen(l), api.ListBans: bannZeilen(l, gtx.Now),
	} {
		for _, z := range zeilen {
			if z.entfernbar && v.knopfFuer(liste, z.name).Clicked(gtx) {
				v.aendern(u, liste, api.ListRemove, z.name, nil)
			}
		}
	}
}

// regelKlicks wertet Schalter und „Setzen“ der Spielregeln aus.
func (v *serverAnsicht) regelKlicks(gtx layout.Context, u *oberflaeche, g *api.Gamerules) {
	if g == nil {
		return
	}
	if v.regelBool == nil {
		v.regelBool, v.regelEd, v.regelSetzen = map[string]*widget.Bool{}, map[string]*widget.Editor{}, map[string]*widget.Clickable{}
	}
	neu := v.regelnStand != g
	v.regelnStand = g
	for _, r := range g.Rules {
		if istSchalterRegel(r.Value) {
			b := v.regelBool[r.Name]
			if b == nil {
				b = new(widget.Bool)
				v.regelBool[r.Name] = b
				neu = true
			}
			if neu || v.regelWartet {
				b.Value = r.Value == "true"
			}
			if b.Update(gtx) && !v.regelWartet {
				v.regelSetzenSenden(u, r.Name, map[bool]string{true: "true", false: "false"}[b.Value])
			}
			continue
		}
		ed := v.regelEd[r.Name]
		if ed == nil {
			ed = &widget.Editor{SingleLine: true, Submit: true, MaxLen: 11, Filter: "-0123456789"}
			v.regelEd[r.Name] = ed
			v.regelSetzen[r.Name] = new(widget.Clickable)
			ed.SetText(r.Value)
		} else if neu {
			ed.SetText(r.Value)
		}
		if eingereicht(gtx, ed, v.regelSetzen[r.Name]) && !v.regelWartet {
			wert := strings.TrimSpace(ed.Text())
			if err := serverbefehl.PruefeSpielregel(r.Name, wert); err != nil {
				u.zeige("fehler", "Der Wert ist eine ganze Zahl.", gtx.Now)
				continue
			}
			v.regelSetzenSenden(u, r.Name, wert)
		}
	}
}

func (v *serverAnsicht) regelSetzenSenden(u *oberflaeche, name, wert string) {
	v.regelWartet = true
	var res api.ServerCommandResult
	u.d.serverAktion(fensterapi.AktionSpielregel, api.GameruleSet{Name: name, Value: wert}, &res, func(err error) {
		v.regelWartet = false
		switch {
		case err != nil:
			u.zeige("fehler", err.Error(), time.Now())
		case !res.OK:
			u.zeige("fehler", "Spielregel nicht gesetzt: "+befehlsAntwort(res), time.Now())
		default:
			u.zeige("info", "Spielregel "+name+" ist jetzt "+wert+".", time.Now())
		}
		u.d.spielregelnHolen() // Schalter und Felder zeigen wieder, was gilt
	})
}

// befehlKlicks schickt den Befehl aus der Eingabe.
func (v *serverAnsicht) befehlKlicks(gtx layout.Context, u *oberflaeche) {
	if !eingereicht(gtx, &v.befEd, &v.befSenden) || v.befWartet {
		return
	}
	cmd, err := serverbefehl.Pruefe(v.befEd.Text())
	if err != nil {
		v.befAntwort, v.befFehler = err.Error(), true
		u.zeige("fehler", err.Error(), gtx.Now)
		return
	}
	v.befWartet, v.befAntwort, v.befFehler = true, "", false
	var res api.ServerCommandResult
	u.d.serverAktion(fensterapi.AktionBefehl, api.ServerCommand{Command: cmd}, &res, func(err error) {
		v.befWartet = false
		switch {
		case err != nil:
			v.befAntwort, v.befFehler = err.Error(), true
			u.zeige("fehler", err.Error(), time.Now())
		case !res.OK:
			v.befAntwort, v.befFehler = befehlsAntwort(res), true
			u.zeige("fehler", "Befehl nicht ausgeführt.", time.Now())
		default:
			v.befAntwort, v.befFehler = befehlsAntwort(res), false
			u.zeige("info", "Befehl ausgeführt: "+cmd, time.Now())
			v.befEd.SetText("")
		}
	})
	gtx.Execute(key.FocusCmd{Tag: &v.befEd})
}

func (v *serverAnsicht) layout(gtx layout.Context, u *oberflaeche, s schnappschuss) layout.Dimensions {
	t := u.th
	v.init()
	g := s.server
	if v.aktualisier.Clicked(gtx) {
		u.d.serverNeuLaden()
	}
	laeuft := g.l != nil && g.l.Running
	if v.regelnLaden.Clicked(gtx) && laeuft {
		u.d.spielregelnHolen()
	}
	v.klicks(gtx, u, g.l)
	v.regelKlicks(gtx, u, g.regeln)
	v.befehlKlicks(gtx, u)

	var teile []layout.Widget
	teile = append(teile, func(gtx layout.Context) layout.Dimensions { return v.kopf(gtx, u, g) })
	switch {
	case g.l == nil && g.err != "":
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.meldung(gtx, meldungWarn, "Listen nicht abrufbar", g.err)
		})
	case g.l == nil:
		teile = append(teile, func(gtx layout.Context) layout.Dimensions {
			return t.karte(gtx, t.leise("Die Listen werden geladen …"))
		})
	default:
		if g.err != "" {
			teile = append(teile, func(gtx layout.Context) layout.Dimensions {
				return t.meldung(gtx, meldungWarn, "Aktualisieren nicht gelungen – die Listen sind vom letzten Abruf", g.err)
			})
		}
		teile = append(teile,
			func(gtx layout.Context) layout.Dimensions {
				return v.listenKarte(gtx, u, "Whitelist",
					"Mitglieder stehen immer darauf (ändern im Reiter „Mitglieder“). Gäste kommen ohne Mitgliedschaft herein.",
					api.ListWhitelist, whitelistZeilen(g.l), "Entfernen", &v.wlEd, &v.wlAdd, "Als Gast eintragen")
			},
			func(gtx layout.Context) layout.Dimensions {
				return v.listenKarte(gtx, u, "Ops",
					"Op wird nur, wer auf der Whitelist steht. Bei Mitgliedern ist es dasselbe Op-Recht wie im Reiter „Mitglieder“.",
					api.ListOps, opsZeilen(g.l), "Op entziehen", &v.opEd, &v.opAdd, "Op geben")
			},
			func(gtx layout.Context) layout.Dimensions {
				return v.listenKarte(gtx, u, "Banns",
					"Gebannte kommen nie herein, auch Mitglieder nicht.",
					api.ListBans, bannZeilen(g.l, gtx.Now), "Bann aufheben", &v.banEd, &v.banAdd, "Bannen")
			},
		)
	}
	teile = append(teile,
		func(gtx layout.Context) layout.Dimensions { return v.regelnKarte(gtx, u, g, laeuft) },
		func(gtx layout.Context) layout.Dimensions { return v.befehlKarte(gtx, u, laeuft) },
	)
	return seite(gtx, &v.liste, teile)
}

// kopf: Zustand der Welt und „Aktualisieren“.
func (v *serverAnsicht) kopf(gtx layout.Context, u *oberflaeche, g serverDaten) layout.Dimensions {
	t := u.th
	titel, unter, farbe := "Server", "Wird geladen …", t.f.aus
	if g.l != nil {
		if g.l.Running {
			titel, unter, farbe = "Die Welt läuft", "Änderungen an den Listen übernimmt der laufende Server in wenigen Sekunden.", t.f.akzent
		} else {
			titel, unter = "Die Welt läuft gerade nicht",
				"Listen werden gespeichert und beim nächsten Start übernommen. Spielregeln und Befehle gehen nur bei laufender Welt."
		}
	}
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return punkt(gtx, farbe, 22) }),
			layout.Rigid(layout.Spacer{Width: 14}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.text(gtx, schrift{groesse: 20, fett: true, farbe: t.f.text, zeilen: 1}, titel)
					}),
					layout.Rigid(t.leise(unter)),
				)
			}),
			layout.Rigid(layout.Spacer{Width: 14}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return t.knopf(gtx, &v.aktualisier, "Aktualisieren", knopfNeben, !g.laeuft)
			}),
		)
	})
}

// listenKarte: Titel, Erklärung, Zeilen mit Knopf, Eingabe zum Hinzufügen.
func (v *serverAnsicht) listenKarte(gtx layout.Context, u *oberflaeche, titel, hinweis, liste string, zeilen []serverZeile,
	entfText string, ed *widget.Editor, add *widget.Clickable, addText string) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		kinder := []layout.FlexChild{
			layout.Rigid(t.klein(strings.ToUpper(titel))),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(t.leise(hinweis)),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
		}
		if len(zeilen) == 0 {
			kinder = append(kinder, layout.Rigid(t.leise("– leer –")))
		}
		for _, z := range zeilen {
			kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.text(gtx, schrift{groesse: 14, fett: true, farbe: t.f.text, zeilen: 1}, z.name)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									return t.text(gtx, schrift{groesse: 12.5, farbe: t.f.leise, zeilen: 1}, z.zusatz)
								}),
							)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if !z.entfernbar {
								return layout.Dimensions{}
							}
							return t.knopf(gtx, v.knopfFuer(liste, z.name), entfText, knopfLeise, !v.wartet)
						}),
					)
				})
			}))
		}
		kinder = append(kinder,
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return eingabe(gtx, t, ed, "Minecraft-Name", 200) }),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						art := knopfNeben
						if liste == api.ListBans {
							art = knopfGefahr
						}
						return t.knopf(gtx, add, addText, art, !v.wartet)
					}),
				)
			}),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
	})
}

// regelnKarte: Spielregeln der laufenden Welt (auf Knopfdruck geladen).
func (v *serverAnsicht) regelnKarte(gtx layout.Context, u *oberflaeche, g serverDaten, laeuft bool) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		kinder := []layout.FlexChild{
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, t.klein("SPIELREGELN")),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						txt := "Spielregeln laden"
						if g.regeln != nil {
							txt = "Neu laden"
						}
						return t.knopf(gtx, &v.regelnLaden, txt, knopfNeben, laeuft && !g.regelnLaedt)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(t.leise("Sie gehören zur Welt und wandern mit dem Stand. Ändern geht nur, solange die Welt läuft.")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
		}
		switch {
		case !laeuft:
			kinder = append(kinder, layout.Rigid(t.leise("Welt läuft nicht.")))
		case g.regelnLaedt:
			kinder = append(kinder, layout.Rigid(t.leise("Die Spielregeln werden vom Host gelesen …")))
		case g.regelnErr != "" && g.regeln == nil:
			kinder = append(kinder, layout.Rigid(t.farbig(g.regelnErr, t.f.fehler)))
		case g.regeln == nil:
			kinder = append(kinder, layout.Rigid(t.leise("Noch nicht geladen.")))
		default:
			for _, r := range g.regeln.Rules {
				kinder = append(kinder, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: 3, Bottom: 3}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return t.text(gtx, schrift{groesse: 13.5, mono: true, farbe: t.f.text, zeilen: 1}, r.Name)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								if b := v.regelBool[r.Name]; b != nil && istSchalterRegel(r.Value) {
									return t.schalter(gtx, b, !v.regelWartet)
								}
								ed, k := v.regelEd[r.Name], v.regelSetzen[r.Name]
								if ed == nil || k == nil {
									return t.text(gtx, schrift{groesse: 13.5, farbe: t.f.leise}, r.Value)
								}
								return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions { return eingabe(gtx, t, ed, "Zahl", 90) }),
									layout.Rigid(layout.Spacer{Width: 8}.Layout),
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return t.knopf(gtx, k, "Setzen", knopfLeise, !v.regelWartet)
									}),
								)
							}),
						)
					})
				}))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kinder...)
	})
}

// befehlKarte: „Befehl senden“ und die letzte Antwort.
func (v *serverAnsicht) befehlKarte(gtx layout.Context, u *oberflaeche, laeuft bool) layout.Dimensions {
	t := u.th
	return t.karte(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.klein("BEFEHL SENDEN")),
			layout.Rigid(layout.Spacer{Height: 4}.Layout),
			layout.Rigid(t.leise("Ein Minecraft-Befehl an den laufenden Server, etwa „time set day“ oder „say Hallo“. "+
				"Gesperrt sind Befehle, die Speichern, Stände oder die Übergabe stören, Dateien schreiben oder die Listen oben umgehen. "+
				"Jeder Befehl steht mit deinem Namen im Log des Koordinators.")),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return eingabe(gtx, t, &v.befEd, "Befehl ohne /", int(gtx.Metric.PxToDp(gtx.Constraints.Max.X)))
					}),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return t.knopf(gtx, &v.befSenden, "Senden", knopfHaupt, laeuft && !v.befWartet)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				switch {
				case !laeuft:
					return layout.Inset{Top: 8}.Layout(gtx, t.leise("Welt läuft nicht."))
				case v.befWartet:
					return layout.Inset{Top: 8}.Layout(gtx, t.leise("Wird ausgeführt …"))
				case v.befAntwort == "":
					return layout.Dimensions{}
				}
				farbe := t.f.text
				if v.befFehler {
					farbe = t.f.fehler
				}
				return layout.Inset{Top: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return hinterlegt(gtx, t.f.flaeche2, t.f.rand, 6, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							return t.text(gtx, schrift{groesse: 13, mono: true, farbe: farbe}, v.befAntwort)
						})
					})
				})
			}),
		)
	})
}
