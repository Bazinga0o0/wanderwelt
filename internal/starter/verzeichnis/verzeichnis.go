// Package verzeichnis fragt das Verzeichnis der gelisteten Gruppen eines
// tunnel-Servers ab (GET <web_url>/api/v1/directory mit dem persönlichen
// API-Token) und prüft die Antwort streng: Größen- und Längengrenzen,
// erlaubte Zeichen, Adressen nur unterhalb der Domain des tunnel-Servers.
// Titel und Beschreibung sind fremder Text; die Oberfläche zeigt sie nur als
// Text, nie als HTML.
package verzeichnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	// Pfad am Web-Host des tunnel-Servers.
	Pfad = "/api/v1/directory"

	MaxBody         = 128 << 10 // Antwort höchstens
	MaxDienste      = 100       // Einträge höchstens
	MaxTitel        = 100       // Zeichen
	MaxBeschreibung = 500       // Zeichen
	MaxURL          = 200
	frist           = 12 * time.Second
)

// Dienst ist ein gelisteter Dienst (eine Gruppe).
type Dienst struct {
	Name        string
	Title       string
	Description string
	// URL ist die Koordinator-Adresse der Gruppe (https://<subdomain>.<domain>).
	URL string
}

var (
	// ErrToken: Der tunnel-Server kennt den Token nicht (401/403).
	ErrToken = errors.New("der tunnel-Server lehnt den Token ab")
	// ErrKeine: Der Server hat keine gelisteten Gruppen oder kein Verzeichnis (404).
	ErrKeine = errors.New("der tunnel-Server hat keine gelisteten Gruppen")
)

// BusyError: Der Server ist gerade überlastet oder begrenzt (429, 503); später
// noch einmal versuchen.
type BusyError struct {
	Status     int
	RetryAfter time.Duration // 0: unbekannt
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("das Verzeichnis ist gerade nicht verfügbar (%d)", e.Status)
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Abfragen holt das Verzeichnis von webURL (Basis-Adresse der tunnel-
// Webseite) mit token. Der Token geht nur an diese Adresse; Weiterleitungen
// werden nie verfolgt. hc darf nil sein.
func Abfragen(ctx context.Context, hc *http.Client, webURL, token string) ([]Dienst, error) {
	base, err := parseBase(webURL)
	if err != nil {
		return nil, err
	}
	var c http.Client
	if hc != nil {
		c = *hc
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(ctx, frist)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base.String(), "/")+Pfad, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("Verzeichnis nicht erreichbar: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrToken
	case http.StatusNotFound:
		return nil, ErrKeine
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return nil, &BusyError{Status: resp.StatusCode, RetryAfter: retryAfter(resp)}
	default:
		return nil, fmt.Errorf("Verzeichnis: unerwartete Antwort %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("Verzeichnis: Antwort nicht lesbar: %w", err)
	}
	return Parse(body, base.Hostname())
}

func retryAfter(resp *http.Response) time.Duration {
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 && n <= 3600 {
		return time.Duration(n) * time.Second
	}
	return 0
}

func parseBase(webURL string) (*url.URL, error) {
	s := strings.TrimSpace(webURL)
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("ungültige Webseiten-Adresse des tunnel-Servers")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname())) {
		return nil, errors.New("die Webseite des tunnel-Servers muss https sein")
	}
	return u, nil
}

func loopback(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Parse prüft die Antwort und liefert die gültigen Einträge. Ein kaputtes
// Dokument, zu große oder zu viele Antworten sind Fehler; einzelne ungültige
// Einträge (zu lang, Steuerzeichen, Adresse außerhalb der Domain) werden
// übersprungen. webHost ist der Host der tunnel-Webseite (Apex-Domain oder
// app.<domain>): Adressen müssen darunter liegen.
func Parse(body []byte, webHost string) ([]Dienst, error) {
	if len(body) > MaxBody {
		return nil, errors.New("Verzeichnis: Antwort zu groß")
	}
	var doc struct {
		Services []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			URL         string `json:"url"`
		} `json:"services"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("Verzeichnis: Antwort ist kein gültiges JSON")
	}
	if len(doc.Services) > MaxDienste {
		return nil, errors.New("Verzeichnis: zu viele Einträge")
	}
	seen := map[string]bool{}
	var out []Dienst
	for _, s := range doc.Services {
		d := Dienst{Name: s.Name, Title: strings.TrimSpace(s.Title), Description: strings.TrimSpace(s.Description),
			URL: strings.TrimRight(strings.TrimSpace(s.URL), "/")}
		if !gueltig(d, webHost) || seen[d.URL] {
			continue
		}
		seen[d.URL] = true
		out = append(out, d)
	}
	return out, nil
}

func gueltig(d Dienst, webHost string) bool {
	if !nameRE.MatchString(d.Name) || len(d.URL) > MaxURL {
		return false
	}
	if d.Title == "" || utf8.RuneCountInString(d.Title) > MaxTitel || !Sauber(d.Title, false) {
		return false
	}
	if utf8.RuneCountInString(d.Description) > MaxBeschreibung || !Sauber(d.Description, true) {
		return false
	}
	return urlPasst(d.URL, webHost)
}

// Sauber: gültiges UTF-8, keine Steuer- und Formatzeichen (Zeilenumbruch nur
// in Beschreibungen).
func Sauber(s string, umbruch bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\n' && umbruch:
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r), r == utf8.RuneError:
			return false
		}
	}
	return true
}

// urlPasst: gültige Koordinator-Adresse (https, kein Pfad), deren Host unter
// der Domain des tunnel-Servers liegt: webHost ohne führendes "app." (bei
// Loopback/IP derselbe Host). Nur dorthin geht später der Token.
func urlPasst(raw, webHost string) bool {
	if err := group.CheckEndpoint(raw); err != nil {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	h, w := strings.ToLower(u.Hostname()), strings.ToLower(webHost)
	if net.ParseIP(w) != nil || loopback(w) {
		return h == w
	}
	domain := strings.TrimPrefix(w, "app.")
	return strings.HasSuffix(h, "."+domain)
}
