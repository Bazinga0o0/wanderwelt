// Package fensterapi ist die Schnittstelle zwischen dem Fenster
// („wanderwelt.exe fenster“, eigener Prozess) und der laufenden
// Starter-Instanz: die Typen, die über deren lokale Steuerung gehen, und der
// Client dafür.
//
// Die Steuerung lauscht nur auf 127.0.0.1; Port und Token stehen in
// Home()/control.json. Jede Anfrage trägt „Authorization: Bearer <Token>“
// und geht an genau 127.0.0.1:<Port> (die Instanz prüft den Host-Kopf gegen
// DNS-Rebinding).
//
// Endpunkte:
//
//	GET  /v1/zustand          → Zustand (JSON)
//	GET  /v1/staende          → api.GenerationsResponse (JSON)
//	GET  /v1/speicherstaende  → []Speicherstand (JSON)
//	GET  /v1/log?zeilen=N     → die letzten N Zeilen von wanderwelt.log als
//	                            Text (Standard 300, höchstens 2000)
//	GET  /v1/mitglieder       → []api.MemberInfo (JSON)
//	GET  /v1/beitritte        → []api.JoinRequestInfo (JSON; nur Admins)
//	POST /v1/{play|beenden|beendenohne|import|profil|autostart|zurueckholen|speicher|rolle|mcname|beitritt|logordner}
//
// Körper der Aktionen (JSON): import = {"dir": …, "ohne_spielerdatei":
// bool}; profil = api.Profile; autostart = {"an": bool}; zurueckholen =
// api.RestoreRequest; speicher = api.BudgetRequest; rolle = {"email": …,
// "admin": bool, "op": bool}; beitritt = {"email": …, "annehmen": bool}
// (Beitrittsanfrage annehmen oder ablehnen); die übrigen ohne Körper (beendenohne: Hosten
// beenden, ohne die Welt an einen Mitspieler zu übergeben). Antwort: 202 für
// play, beenden, beendenohne und import, 200 mit JSON für die übrigen; Fehler als 4xx mit
// {"fehler": "<Text für den Nutzer>"}.
//
// Zustand, Hinweis und Speicherstand haben keine JSON-Namen: Sie gehen mit
// ihren Go-Feldnamen über die Leitung; beide Seiten benutzen diese Typen.
package fensterapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Zustand ist der Schnappschuss der laufenden Instanz für das Fenster.
type Zustand struct {
	Version      string
	Phase        string // Text wie im Tooltip, z. B. „Bereit“, „Hostet“, „Spielt mit“
	Detail       string // z. B. „lädt die Welt: 45 %“
	Busy         string
	LastErr      string
	Hosting      bool
	HostAddr     string
	Status       *api.Status
	StatusErr    string
	Me           *api.Me
	Profil       api.Profile
	RAMTotalMB   int
	Autostart    bool
	Notfallkopie Notfallkopie
	Hinweise     []Hinweis // die letzten 20, Seq aufsteigend
}

// Notfallkopie ist der Stand der Notfallkopie dieses PCs (Reiter Hosten).
type Notfallkopie struct {
	Behalten         bool      // Haken „Notfallkopie behalten“
	Vorhanden        bool      // der Ordner mit der Kopie liegt da
	NichtHochgeladen bool      // die Kopie enthält einen Stand, der nicht eingereicht werden konnte
	Ordner           string    // Pfad (leer: Dokumente-Ordner unbekannt)
	Zeit             time.Time // wann zuletzt kopiert
	Groesse          int64     // Bytes
	WorldID          string
	Generation       int64
}

// Hinweis ist eine Meldung der Instanz, die das Fenster statt eines
// Dialogs zeigt.
type Hinweis struct {
	Seq  int64
	Zeit time.Time
	Art  string // "info" | "fehler"
	Text string
}

// Speicherstand ist ein lokaler Einzelspieler-Speicherstand für „Welt
// übernehmen“.
type Speicherstand struct {
	Folder, LevelName, Version string
	LastPlayed                 time.Time
	Size                       int64
	PlayerFile                 bool
	Problem                    string // leer = übernehmbar
}

// ErrLaeuftNicht: Es gibt keine laufende Instanz (control.json fehlt oder
// niemand antwortet).
var ErrLaeuftNicht = errors.New("wanderwelt läuft nicht")

const (
	// ControlFile ist die Datei in Home(), in der die laufende Instanz Port
	// und Token ablegt.
	ControlFile = "control.json"
	// dialWait: So lange wartet Verbinden auf die Instanz.
	dialWait = 2 * time.Second
	// requestWait: Obergrenze je Anfrage, auch wenn der Kontext keine setzt.
	requestWait = 30 * time.Second
	// maxResponse: Obergrenze für eine Antwort (Stände-Liste, Log).
	maxResponse = 32 << 20
	// maxErrorBody: So viel einer Fehlerantwort wird gelesen.
	maxErrorBody = 8 << 10
)

// Client spricht mit der laufenden Instanz. Endet sie, liefern die Aufrufe
// ErrLaeuftNicht; dann neu Verbinden (eine neue Instanz hat einen neuen
// Port und ein neues Token).
type Client struct {
	base  string // http://127.0.0.1:<Port>
	token string
	hc    *http.Client
}

// ControlInfo ist der Inhalt von control.json; die Steuerung der Instanz
// schreibt ihn, Fenster und zweiter Start lesen ihn.
type ControlInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

// Verbinden liest Port und Token aus Home()/control.json und prüft, dass
// dort jemand lauscht. Fehlt die Datei oder antwortet niemand, ist der
// Fehler ErrLaeuftNicht.
func Verbinden() (*Client, error) {
	path := filepath.Join(group.Home(), ControlFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrLaeuftNicht
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLaeuftNicht, err)
	}
	var ci ControlInfo
	if err := json.Unmarshal(data, &ci); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrLaeuftNicht, path, err)
	}
	if ci.Port <= 0 || ci.Port > 65535 || ci.Token == "" {
		return nil, fmt.Errorf("%w: %s unvollständig", ErrLaeuftNicht, path)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(ci.Port))
	conn, err := net.DialTimeout("tcp", addr, dialWait)
	if err != nil {
		return nil, ErrLaeuftNicht // control.json eines beendeten Laufs
	}
	conn.Close()
	return &Client{
		base:  "http://" + addr,
		token: ci.Token,
		hc: &http.Client{
			Timeout: requestWait,
			// Nie woandershin folgen: Das Token gilt nur hier.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Zustand holt den Schnappschuss der Instanz.
func (c *Client) Zustand(ctx context.Context) (*Zustand, error) {
	var z Zustand
	if err := c.getJSON(ctx, "/v1/zustand", &z); err != nil {
		return nil, err
	}
	return &z, nil
}

// Staende holt die Stände-Liste (vom Koordinator durchgereicht).
func (c *Client) Staende(ctx context.Context) (*api.GenerationsResponse, error) {
	var r api.GenerationsResponse
	if err := c.getJSON(ctx, "/v1/staende", &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Speicherstaende holt die lokalen Einzelspieler-Speicherstände.
func (c *Client) Speicherstaende(ctx context.Context) ([]Speicherstand, error) {
	var r []Speicherstand
	if err := c.getJSON(ctx, "/v1/speicherstaende", &r); err != nil {
		return nil, err
	}
	return r, nil
}

// Log holt die letzten zeilen Zeilen von wanderwelt.log (zeilen <= 0: so
// viele, wie die Instanz von sich aus liefert).
func (c *Client) Log(ctx context.Context, zeilen int) (string, error) {
	path := "/v1/log"
	if zeilen > 0 {
		path += "?zeilen=" + strconv.Itoa(zeilen)
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", c.transportError(ctx, err)
	}
	// Text; liefert die Instanz doch JSON, dann als Zeichenkette.
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/json" {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return "", fmt.Errorf("Steuerung: Log: %w", err)
		}
		return s, nil
	}
	return string(data), nil
}

// Mitglieder holt die Mitgliederliste (nur für Admins; sonst Fehler mit
// dem Text der Instanz).
func (c *Client) Mitglieder(ctx context.Context) ([]api.MemberInfo, error) {
	var r []api.MemberInfo
	if err := c.getJSON(ctx, "/v1/mitglieder", &r); err != nil {
		return nil, err
	}
	return r, nil
}

// Beitritte holt die offenen Beitrittsanfragen (nur für Admins; sonst Fehler
// mit dem Text der Instanz).
func (c *Client) Beitritte(ctx context.Context) ([]api.JoinRequestInfo, error) {
	var r []api.JoinRequestInfo
	if err := c.getJSON(ctx, "/v1/beitritte", &r); err != nil {
		return nil, err
	}
	return r, nil
}

// Aktion löst die Aktion name aus (POST /v1/<name>); body geht als JSON mit,
// nil ohne Körper. Ein Fehler der Instanz trägt deren Text aus
// {"fehler": …}.
func (c *Client) Aktion(ctx context.Context, name string, body any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return fmt.Errorf("Steuerung: %s: %w", name, err)
		}
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/"+url.PathEscape(name), data)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return c.transportError(ctx, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("Steuerung: %s: %w", path, err)
	}
	return nil
}

// do schickt eine Anfrage mit Token. Eine Antwort außerhalb von 2xx wird
// zum Fehler mit dem Text der Instanz; resp gehört bei nil-Fehler dem
// Aufrufer.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return nil, responseError(resp.StatusCode, data)
}

// transportError: Bricht der Aufrufer ab, bleibt es sein Abbruch; sonst
// antwortet niemand (mehr) – die Instanz ist weg oder hängt.
func (c *Client) transportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &zeitFehler{err: ctx.Err()}
	}
	if ctx.Err() != nil {
		return fmt.Errorf("Steuerung: %w", ctx.Err())
	}
	return fmt.Errorf("%w (%v)", ErrLaeuftNicht, err)
}

// zeitFehler: Die Instanz hat innerhalb der Frist nicht geantwortet (deutscher
// Text statt „context deadline exceeded“; Is(DeadlineExceeded) bleibt wahr).
type zeitFehler struct{ err error }

func (e *zeitFehler) Error() string {
	return "Die laufende wanderwelt antwortet nicht rechtzeitig – bitte gleich noch einmal versuchen."
}
func (e *zeitFehler) Unwrap() error { return e.err }

// responseError macht aus einer Fehlerantwort einen Fehler; trägt sie
// {"fehler": "…"}, ist genau dieser Text die Meldung.
func responseError(code int, body []byte) error {
	var e struct {
		Fehler string `json:"fehler"`
	}
	if json.Unmarshal(body, &e) == nil && e.Fehler != "" {
		return errors.New(e.Fehler)
	}
	switch code {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return errors.New("die laufende wanderwelt-Version kennt das noch nicht – bitte wanderwelt neu starten")
	case http.StatusForbidden:
		return errors.New("die laufende wanderwelt lehnt die Anfrage ab (control.json passt nicht)")
	}
	if text := strings.TrimSpace(string(body)); text != "" && len(text) <= 200 {
		return fmt.Errorf("Steuerung antwortet %d: %s", code, text)
	}
	return fmt.Errorf("Steuerung antwortet %d", code)
}

// BeendenText ist Beschriftung und Erklärung des Knopfs (Tray und Fenster),
// der das eigene Spiel beendet: Als Host stoppt er den Server, wer nur
// mitspielt, sagt wanderwelt damit bloß, nicht mehr nachzuhelfen.
func BeendenText(hosting bool) (label, hinweis string) {
	if hosting {
		return "Spiel für mich beenden", "Als Host: Server stoppen und Stand hochladen"
	}
	return "Nicht mehr mitspielen", "Minecraft schließt du selbst; wanderwelt übernimmt dann nicht mehr automatisch"
}

// WartungTray ist die Zeile im Tooltip des Trays während einer Wartung am
// Server (angekündigt oder laufend).
const WartungTray = "Wartung am Server – Spielen ist gleich wieder möglich"

// WartungText beschreibt eine Wartung für das Fenster: Titel und Text.
// jetzt ist die Zeit für „der Host stoppt um …“ (nur vor dem Stopp).
func WartungText(m *api.Maintenance, jetzt time.Time) (titel, text string) {
	text = "Spielen ist gleich wieder möglich."
	if m == nil {
		return "Wartung am Server", text
	}
	if m.Reason != "" {
		text = "Grund: " + m.Reason + ". " + text
	}
	if !m.StopAt.IsZero() && m.StopAt.After(jetzt) {
		text += " Wer hostet, stoppt um " + m.StopAt.Local().Format("15:04") + " den Server sauber."
	}
	return "Wartung am Server", text
}

// AndereSpieler sind die online Spieler ohne own (den eigenen Minecraft-
// Namen, ohne Rücksicht auf Groß- und Kleinschreibung).
func AndereSpieler(players []string, own string) []string {
	var out []string
	for _, p := range players {
		if own == "" || !strings.EqualFold(p, own) {
			out = append(out, p)
		}
	}
	return out
}

// UebergabeText beschreibt eine laufende Übergabe (Tooltip, Start-Reiter);
// "" ohne Übergabe.
func UebergabeText(h *api.HandoverState) string {
	if h == nil {
		return ""
	}
	to := h.To.MinecraftName // keine E-Mail-Adressen außerhalb des Reiters Mitglieder
	if to == "" {
		to = "einen Mitspieler"
	}
	switch h.State {
	case api.HandoverFailed:
		return "Übergabe an " + to + " gescheitert"
	case api.HandoverReady:
		return "Übergabe an " + to + " läuft: die Spieler wechseln …"
	}
	return "Übergabe an " + to + " läuft …"
}

// UebergabeFrage steht in der Rückfrage „Spiel für mich beenden“ als Host,
// wenn andere Spieler online sind (Tray und Fenster).
const UebergabeFrage = "Die Welt wird an den besten PC der Mitspieler übergeben; ihr spielt nach kurzer Pause weiter. " +
	"Klappt das nicht, geht es auf deinem PC normal weiter."
