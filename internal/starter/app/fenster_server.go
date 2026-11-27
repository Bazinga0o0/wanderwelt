package app

// Reiter „Server“ (nur Admins): Aktionen der lokalen Steuerung, die an den
// Koordinator gehen (Whitelist, Ops, Banns, Spielregeln, Befehle). Die
// Instanz prüft Admin-Recht und Eingaben vorab für eine schnelle Antwort;
// entscheidend sind die Prüfungen des Koordinators und des Hosts.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/serverbefehl"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
)

// istAdmin: Das eigene Konto ist laut letztem /v1/me Admin.
func (a *App) istAdmin() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.me != nil && a.me.Admin
}

// leseStreng liest body nach v; unbekannte Felder sind Fehler.
func leseStreng(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// serverAktion führt eine Aktion des Reiters „Server“ aus.
func (a *App) serverAktion(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	if !a.istAdmin() {
		antwortFehler(w, http.StatusForbidden, "Den Reiter „Server“ dürfen nur Admins benutzen.")
		return
	}
	switch name {
	case fensterapi.AktionServerliste:
		var req api.ServerListChange
		if err := leseStreng(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Welche Änderung? Erwartet {\"list\": …, \"action\": …, \"name\": …}.")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		serverAufruf(a, w, r, "Serverliste", func(ctx context.Context, c *client.Client) (*api.ServerListResult, error) {
			return c.ChangeServerList(ctx, req)
		})
	case fensterapi.AktionSpielregel:
		var req api.GameruleSet
		if err := leseStreng(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Welche Spielregel? Erwartet {\"name\": …, \"value\": …}.")
			return
		}
		if err := serverbefehl.PruefeSpielregel(req.Name, req.Value); err != nil {
			antwortFehler(w, http.StatusBadRequest, err.Error())
			return
		}
		serverAufruf(a, w, r, "Spielregel", func(ctx context.Context, c *client.Client) (*api.ServerCommandResult, error) {
			return c.SetGamerule(ctx, req)
		})
	case fensterapi.AktionBefehl:
		var req api.ServerCommand
		if err := leseStreng(body, &req); err != nil {
			antwortFehler(w, http.StatusBadRequest, "Welcher Befehl? Erwartet {\"command\": …}.")
			return
		}
		cmd, err := serverbefehl.Pruefe(req.Command)
		if err != nil {
			antwortFehler(w, http.StatusBadRequest, err.Error())
			return
		}
		serverAufruf(a, w, r, "Befehl", func(ctx context.Context, c *client.Client) (*api.ServerCommandResult, error) {
			return c.ServerCommand(ctx, cmd)
		})
	}
}

// serverAufruf ist koordAufruf für den Reiter „Server“: Meldet der
// Koordinator, dass der Host nicht (rechtzeitig) geantwortet hat (502/504
// mit Text), geht genau dieser Text ins Fenster statt einer allgemeinen
// Fehlermeldung.
func serverAufruf[T any](a *App, w http.ResponseWriter, r *http.Request, was string,
	f func(context.Context, *client.Client) (T, error)) {
	c := a.client()
	if c == nil {
		antwortFehler(w, http.StatusConflict, textStartetNoch)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), koordWait)
	defer cancel()
	v, err := retry401(ctx, func() (T, error) { return f(ctx, c) })
	if err != nil {
		log.Printf("Fenster: %s: %s", was, logFehler(err))
		if se, ok := errors.AsType[*client.StatusError](err); ok && hostMeldung(se) {
			antwortFehler(w, se.StatusCode, se.Message)
			return
		}
		code, text := koordFehler(err)
		antwortFehler(w, code, text)
		return
	}
	antwortJSON(w, http.StatusOK, v)
}

// hostMeldung: eine Antwort des Koordinators über den Host (502/504 mit
// eigenem deutschen Text, nicht etwa eine Fehlerseite eines Proxys).
func hostMeldung(se *client.StatusError) bool {
	return (se.StatusCode == http.StatusBadGateway || se.StatusCode == http.StatusGatewayTimeout) &&
		strings.HasPrefix(se.Message, "Der Host ")
}
