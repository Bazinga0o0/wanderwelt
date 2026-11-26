package app

// Reiter „Entdecken“ (Instanz-Seite): fragt das Verzeichnis der tunnel-Server
// ab, bei denen dieser PC Gruppen hat (Token aus der jeweiligen
// Gruppendatei), holt je Eintrag GET /v1/info und stellt auf Wunsch eine
// Beitrittsanfrage (derselbe Weg wie beim Selbst-Beitritt der Einrichtung:
// POST /v1/join). Wird die Anfrage angenommen, entsteht eine neue
// Gruppendatei unter gruppen\<id> mit denselben Tunnel-Zugangsdaten.
//
// Sicherheit: Der Token geht nur an den Web-Host des tunnel-Servers und an
// Koordinator-Adressen, die das Verzeichnis unterhalb von dessen Domain
// nennt (verzeichnis.Parse prüft das). „Beitritt anfragen“ gilt nur für
// Adressen, die die letzte Liste enthielt. Alle Antworten sind streng
// geprüft; Titel und Beschreibung gehen nur als Text ans Fenster. Der Token
// steht nie im Log.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/verzeichnis"
)

const (
	aktionEntdeckenBeitritt = "entdeckenbeitritt"

	// entdeckenFrist: So lange gilt ein geholter Stand, auch wenn das Fenster
	// öfter fragt (schont die Server).
	entdeckenFrist = 20 * time.Second
	// entdeckenGesamt: Obergrenze für eine ganze Abfrage.
	entdeckenGesamt = 40 * time.Second
	// maxEntdeckenServer, maxEntdeckenEintraege, entdeckenParallel: Grenzen.
	maxEntdeckenServer    = 5
	maxEntdeckenEintraege = 50
	entdeckenParallel     = 4
	// entdeckenPause: Wartezeit nach 429/503 ohne Retry-After.
	entdeckenPause = 60 * time.Second
)

// entdeckenZiel: wohin eine Beitrittsanfrage für eine gelistete Adresse geht.
type entdeckenZiel struct {
	quelle *group.Group // Gruppe, deren Token und Tunnel-Server gelten
}

// entdeckenState gehört zur App.
type entdeckenState struct {
	mu     sync.Mutex // serialisiert Abfragen und schützt die Felder
	stand  time.Time
	res    fensterapi.Entdecken
	ziele  map[string]entdeckenZiel // Koordinator-Adresse → Quelle
	pausen map[string]time.Time     // Web-Basis → nicht vor
	// hinweise: laufende Benachrichtigungen (Tests warten darauf).
	hinweise sync.WaitGroup
}

// Im Test austauschbar.
var (
	entdeckenHTTP *http.Client // nil: Standard
	// mcNameVorschlag liefert den Minecraft-Namen aus dem Launcher.
	mcNameVorschlag = func() string {
		if n, _, ok := detectMinecraftName(deps.MinecraftDir()); ok && mcNameRE.MatchString(n) {
			return n
		}
		return ""
	}
)

// entdeckQuelle: ein tunnel-Server, bei dem dieser PC eine Gruppe hat.
type entdeckQuelle struct {
	web string
	g   *group.Group
}

// entdeckQuellen: je Web-Basis der erste Eintrag mit Token (die aktive
// Gruppe zuerst), höchstens maxEntdeckenServer.
func (a *App) entdeckQuellen() []entdeckQuelle {
	list := group.List()
	aktiv := group.ActiveID()
	var inv *group.Invite
	a.mu.Lock()
	inv = a.invite
	a.mu.Unlock()
	var out []entdeckQuelle
	seen := map[string]bool{}
	add := func(g *group.Group) {
		if g.Token == "" || len(out) >= maxEntdeckenServer {
			return
		}
		fb := ""
		if inv != nil && inv.TunnelServer == g.TunnelServer {
			fb = inv.WebURL
		}
		web := g.WebBase(fb)
		if web == "" || seen[web] {
			return
		}
		seen[web] = true
		out = append(out, entdeckQuelle{web: web, g: g})
	}
	for _, e := range list {
		if e.ID == aktiv {
			add(e.Group)
		}
	}
	for _, e := range list {
		add(e.Group)
	}
	return out
}

func hostVon(web string) string {
	if u, err := url.Parse(web); err == nil && u.Host != "" {
		return u.Host
	}
	return "dem Tunnel-Server"
}

// entdecken holt die Liste (neu: auch wenn der gemerkte Stand frisch ist).
func (a *App) entdecken(ctx context.Context, neu bool) fensterapi.Entdecken {
	e := &a.ent
	e.mu.Lock()
	defer e.mu.Unlock()
	if !neu && !e.stand.IsZero() && time.Since(e.stand) < entdeckenFrist {
		return e.res
	}
	ctx, cancel := context.WithTimeout(ctx, entdeckenGesamt)
	defer cancel()
	res := fensterapi.Entdecken{MinecraftName: a.mcNameFuerBeitritt()}
	ziele := map[string]entdeckenZiel{}
	if e.pausen == nil {
		e.pausen = map[string]time.Time{}
	}
	lokal := map[string]bool{}
	for _, l := range group.List() {
		lokal[l.ID] = true
	}
	quellen := a.entdeckQuellen()
	if len(quellen) == 0 {
		res.Hinweise = append(res.Hinweise, "Es ist noch keine Gruppe mit Tunnel-Zugang eingerichtet, über die sich Gruppen finden lassen.")
	}
	seen := map[string]bool{}
	for _, q := range quellen {
		host := hostVon(q.web)
		if bis := e.pausen[q.web]; time.Now().Before(bis) {
			res.Hinweise = append(res.Hinweise, fmt.Sprintf("Das Verzeichnis von %s ist gerade überlastet. wanderwelt versucht es gleich noch einmal.", host))
			continue
		}
		ds, err := verzeichnis.Abfragen(ctx, entdeckenHTTP, q.web, q.g.Token)
		var busy *verzeichnis.BusyError
		switch {
		case err == nil:
		case errors.Is(err, verzeichnis.ErrToken):
			res.Hinweise = append(res.Hinweise, fmt.Sprintf("%s kennt deinen Token nicht (mehr). Erstelle auf der Webseite einen neuen und lade die Gruppendatei neu.", host))
			continue
		case errors.Is(err, verzeichnis.ErrKeine):
			res.Hinweise = append(res.Hinweise, fmt.Sprintf("%s hat keine gelisteten Gruppen.", host))
			continue
		case errors.As(err, &busy):
			pause := busy.RetryAfter
			if pause <= 0 {
				pause = entdeckenPause
			}
			e.pausen[q.web] = time.Now().Add(pause)
			res.Hinweise = append(res.Hinweise, fmt.Sprintf("Das Verzeichnis von %s ist gerade überlastet. wanderwelt versucht es gleich noch einmal.", host))
			continue
		default:
			log.Printf("Entdecken: Verzeichnis von %s: %v", host, err)
			res.Hinweise = append(res.Hinweise, fmt.Sprintf("Das Verzeichnis von %s ist gerade nicht erreichbar.", host))
			continue
		}
		for _, d := range ds {
			if seen[d.URL] || len(res.Gruppen) >= maxEntdeckenEintraege {
				continue
			}
			seen[d.URL] = true
			ziele[d.URL] = entdeckenZiel{quelle: q.g}
			res.Gruppen = append(res.Gruppen, fensterapi.EntdeckenEintrag{URL: d.URL, Titel: d.Title, Beschreibung: d.Description,
				Zustand: fensterapi.EntdeckenUnbekannt})
		}
	}
	// /v1/info je Eintrag, begrenzt parallel.
	var wg sync.WaitGroup
	sem := make(chan struct{}, entdeckenParallel)
	for i := range res.Gruppen {
		wg.Add(1)
		sem <- struct{}{}
		go func(g *fensterapi.EntdeckenEintrag) {
			defer wg.Done()
			defer func() { <-sem }()
			q := ziele[g.URL].quelle
			c := client.New(&group.Group{Coordinator: g.URL, Token: q.Token, DevIdentityEmail: q.DevIdentityEmail})
			gi, err := c.Info(ctx)
			if err != nil {
				g.Fehler = infoFehler(err)
				return
			}
			g.Mitglieder, g.Zustand = gi.Members, gi.You
			// Der Titel des Koordinators gilt, wenn er einen setzt.
			if gi.Title != "" {
				g.Titel = gi.Title
			}
			if gi.Description != "" {
				g.Beschreibung = gi.Description
			}
		}(&res.Gruppen[i])
	}
	wg.Wait()
	// Lokal vorhandene Gruppen als Mitglied markieren (nicht doppelt anlegen),
	// neu angenommene als Gruppe anlegen.
	for i := range res.Gruppen {
		g := &res.Gruppen[i]
		id, err := group.IDFromCoordinator(g.URL)
		if err != nil {
			continue
		}
		if lokal[id] {
			g.Lokal, g.Zustand = true, fensterapi.EntdeckenMitglied
			a.setTitel(id, g.Titel)
			continue
		}
		if g.Zustand == fensterapi.EntdeckenMitglied {
			if a.gruppeAnlegen(ziele[g.URL].quelle, g.URL) {
				g.Lokal = true
				a.setTitel(id, g.Titel)
				e.hinweise.Add(1)
				go func(t string) {
					defer e.hinweise.Done()
					notify("Du bist jetzt Mitglied von „" + t + "“. Wähle die Gruppe im wanderwelt-Fenster aus.")
				}(g.Titel)
			}
		}
	}
	res.Stand = time.Now()
	e.res, e.ziele, e.stand = res, ziele, res.Stand
	return res
}

// infoFehler: Text für den Nutzer, wenn /v1/info einer Gruppe nicht klappt.
func infoFehler(err error) string {
	if se, ok := errors.AsType[*client.StatusError](err); ok {
		switch se.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Der Koordinator dieser Gruppe lehnt deinen Token ab."
		case http.StatusNotFound:
			return "Der Koordinator dieser Gruppe kennt diese Ansicht noch nicht."
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return "Der Koordinator ist gerade überlastet."
		}
	}
	return "Der Koordinator dieser Gruppe ist nicht erreichbar."
}

// mcNameFuerBeitritt: Vorschlag für den Minecraft-Namen (aktive Gruppe, sonst
// Launcher).
func (a *App) mcNameFuerBeitritt() string {
	a.mu.Lock()
	me := a.me
	a.mu.Unlock()
	if me != nil && mcNameRE.MatchString(me.MinecraftName) {
		return me.MinecraftName
	}
	return mcNameVorschlag()
}

// gruppeAnlegen legt die Gruppe unter gruppen\<id> an: Koordinator-Adresse
// url mit den Tunnel-Zugangsdaten von quelle. Die aktive Gruppe bleibt. Eine
// schon vorhandene Gruppe wird nie überschrieben. true: jetzt vorhanden.
func (a *App) gruppeAnlegen(quelle *group.Group, url string) bool {
	id, err := group.IDFromCoordinator(url)
	if err != nil {
		return false
	}
	for _, l := range group.List() {
		if l.ID == id {
			return true
		}
	}
	if group.Legacy() {
		log.Printf("Entdecken: Gruppe %s nicht angelegt (altes Datenlayout)", id)
		return false
	}
	g := &group.Group{Coordinator: url, TunnelServer: quelle.TunnelServer, Token: quelle.Token, WebURL: quelle.WebURL,
		DevIdentityEmail: quelle.DevIdentityEmail, TunnelInsecureSkipVerify: quelle.TunnelInsecureSkipVerify}
	if err := group.AddGroup(g, false); err != nil {
		log.Printf("Entdecken: Gruppe %s nicht angelegt: %v", id, err)
		return false
	}
	log.Printf("Entdecken: Gruppe %s angelegt (Beitritt angenommen)", id)
	return true
}

// entdeckenAnsicht beantwortet GET /v1/entdecken.
func (a *App) entdeckenAnsicht(w http.ResponseWriter, r *http.Request) {
	antwortJSON(w, http.StatusOK, a.entdecken(r.Context(), r.URL.Query().Get("neu") == "1"))
}

// entdeckenBeitritt: POST /v1/entdeckenbeitritt {"url", "name"}.
func (a *App) entdeckenBeitritt(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.URL == "" {
		antwortFehler(w, http.StatusBadRequest, "Welche Gruppe? Erwartet {\"url\": …, \"name\": …}.")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !mcNameRE.MatchString(req.Name) {
		antwortFehler(w, http.StatusBadRequest, "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.")
		return
	}
	a.ent.mu.Lock()
	ziel, ok := a.ent.ziele[req.URL]
	a.ent.mu.Unlock()
	if !ok {
		antwortFehler(w, http.StatusConflict, "Diese Gruppe steht nicht in der Liste. Aktualisiere den Reiter „Entdecken“ und versuche es noch einmal.")
		return
	}
	q := ziel.quelle
	c := client.New(&group.Group{Coordinator: req.URL, Token: q.Token, DevIdentityEmail: q.DevIdentityEmail})
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	st, err := c.Join(ctx, req.Name)
	if err != nil {
		var cur *api.JoinStatus
		if se, ok := errors.AsType[*client.StatusError](err); ok {
			switch se.StatusCode {
			case http.StatusConflict, http.StatusTooManyRequests:
				// Läuft die eigene Anfrage schon (anderes Gerät, doppelter Klick)?
				if s, e2 := c.JoinStatus(ctx); e2 == nil && (s.State == api.JoinOpen || s.State == api.JoinAccepted) {
					cur = s
				}
			}
		}
		if cur == nil {
			code, text := joinFehler(err)
			log.Printf("Entdecken: Beitrittsanfrage: %s", logFehler(err))
			antwortFehler(w, code, text)
			return
		}
		st = cur
	}
	log.Printf("Entdecken: Beitritt angefragt bei %s (Minecraft-Name %s, Stand %s)", hostVon(req.URL), req.Name, st.State)
	if st.State == api.JoinAccepted {
		a.gruppeAnlegen(q, req.URL)
	}
	a.ent.mu.Lock()
	a.ent.stand = time.Time{} // nächster Abruf holt neu
	a.ent.mu.Unlock()
	zustand := fensterapi.EntdeckenAngefragt
	if st.State == api.JoinAccepted {
		zustand = fensterapi.EntdeckenMitglied
	}
	antwortJSON(w, http.StatusOK, struct {
		Zustand string `json:"zustand"`
	}{zustand})
}

// joinFehler macht aus einem Fehler der Beitrittsanfrage Status und Text.
func joinFehler(err error) (int, string) {
	se, ok := errors.AsType[*client.StatusError](err)
	if !ok {
		return http.StatusBadGateway, explain(err)
	}
	switch se.StatusCode {
	case http.StatusBadRequest:
		return http.StatusBadRequest, se.Message
	case http.StatusConflict:
		return http.StatusConflict, "Der Name gehört schon einem Mitglied oder ist schon angefragt. Nimm einen anderen Namen oder frag den Admin."
	case http.StatusTooManyRequests:
		return http.StatusConflict, "Die Beitrittsanfrage ist gerade nicht möglich: " + se.Message
	case http.StatusForbidden, http.StatusNotFound:
		return http.StatusConflict, msgNoJoin
	case http.StatusUnauthorized:
		return http.StatusUnauthorized, explain(err)
	}
	return http.StatusBadGateway, "Die Beitrittsanfrage ging nicht durch: " + explain(err)
}
