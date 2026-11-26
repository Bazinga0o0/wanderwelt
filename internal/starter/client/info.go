package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode"
	"unicode/utf8"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const (
	// maxInfoBody: So viel liest Info höchstens (die Antwort ist winzig).
	maxInfoBody    = 16 << 10
	maxInfoTitel   = 100 // Zeichen
	maxInfoBeschr  = 500 // Zeichen
	maxInfoMitglie = 100000
)

// Info fragt die öffentliche Beschreibung einer Gruppe (GET /v1/info): Titel,
// Beschreibung, Mitgliederzahl und den eigenen Zustand. Geht für jedes
// gültige tunnel-Konto, auch für Nicht-Mitglieder. Die Antwort kommt von einem
// fremden Server und wird streng geprüft (Größe, Längen, Zeichen, Werte);
// ein Verstoß ist ein Fehler.
func (c *Client) Info(ctx context.Context) (*api.GroupInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.send(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInfoBody+1))
	if err != nil {
		return nil, fmt.Errorf("Koordinator: GET /v1/info: Antwort nicht lesbar: %w", err)
	}
	if len(body) > maxInfoBody {
		return nil, errors.New("Koordinator: GET /v1/info: Antwort zu groß")
	}
	var gi api.GroupInfo
	if err := json.Unmarshal(body, &gi); err != nil {
		return nil, errors.New("Koordinator: GET /v1/info: Antwort nicht lesbar")
	}
	if err := checkInfo(&gi); err != nil {
		return nil, fmt.Errorf("Koordinator: GET /v1/info: %w", err)
	}
	return &gi, nil
}

func checkInfo(gi *api.GroupInfo) error {
	if n := utf8.RuneCountInString(gi.Title); n > maxInfoTitel || !infoText(gi.Title, false) {
		return errors.New("Titel ungültig")
	}
	if n := utf8.RuneCountInString(gi.Description); n > maxInfoBeschr || !infoText(gi.Description, true) {
		return errors.New("Beschreibung ungültig")
	}
	if gi.Members < 0 || gi.Members > maxInfoMitglie {
		return errors.New("Mitgliederzahl ungültig")
	}
	switch gi.You {
	case api.YouMember, api.YouRequested, api.YouNone:
	default:
		return errors.New("Zustand ungültig")
	}
	return nil
}

// infoText: gültiges UTF-8 ohne Steuer- und Formatzeichen (Zeilenumbruch nur
// in der Beschreibung).
func infoText(s string, umbruch bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\n' && umbruch:
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r):
			return false
		}
	}
	return true
}
