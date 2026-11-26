package group

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Beitritts-Geheimnis für Discord (Rich Presence): Die öffentlichen
// Gruppen-Adressen in höchstens 128 Bytes, nie ein Token. Ein Geheimnis von
// außen ist unbekannte Eingabe: Es wird streng geprüft und nur nach
// ausdrücklicher Bestätigung mit sichtbaren Adressen benutzt.
//
// Form: "w1." + Base64 (URL-Alphabet, ohne Auffüllung) von vier Zeilen:
//
//	coordinator   Host[:Port]  (immer https)
//	tunnel_server Host:Port
//	web_url       Host[:Port]  (https); leer: https://<Host von tunnel_server>
//	token_url     Pfad auf web_url (beginnt mit "/"); leer: keiner
const (
	joinPrefix = "w1."
	// MaxJoinSecret ist die Grenze von Discord für Geheimnisse (Bytes).
	MaxJoinSecret = 128
)

var (
	hostRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	tokenPathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,63}$`)
)

// checkAuthority prüft "host" oder "host:port" (Kleinbuchstaben, mindestens
// ein Punkt, keine Zugangsdaten); needPort verlangt den Port.
func checkAuthority(s string, needPort bool) (host string, err error) {
	host, port, hasPort := strings.Cut(s, ":")
	if len(s) > 260 || !hostRe.MatchString(host) {
		return "", fmt.Errorf("ungültiger Rechnername %q", s)
	}
	if !hasPort {
		if needPort {
			return "", fmt.Errorf("%q: Port fehlt", s)
		}
		return host, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", fmt.Errorf("%q: ungültiger Port", s)
	}
	return host, nil
}

// httpsAuthority: u muss eine reine https-Adresse sein (keine
// Zugangsdaten, kein Pfad außer "/", keine Anfrage); liefert Host[:Port].
func httpsAuthority(raw string, allowPath bool) (auth, path string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", "", fmt.Errorf("%q: erwartet https://Rechner[:Port]", raw)
	}
	path = u.Path
	if path == "/" {
		path = ""
	}
	if path != "" && !allowPath {
		return "", "", fmt.Errorf("%q: kein Pfad erlaubt", raw)
	}
	if _, err := checkAuthority(u.Host, false); err != nil {
		return "", "", err
	}
	return u.Host, path, nil
}

// EncodeJoin kodiert die Adressen von inv als Beitritts-Geheimnis. Fehler,
// wenn eine Adresse nicht streng genug ist (kein https, Pfad, Zugangsdaten,
// Rechnername ungewöhnlich) oder das Ergebnis über 128 Bytes liegt.
func EncodeJoin(inv *Invite) (string, error) {
	coord, _, err := httpsAuthority(inv.Coordinator, false)
	if err != nil {
		return "", fmt.Errorf("coordinator: %w", err)
	}
	tunnelHost, err := checkAuthority(inv.TunnelServer, true)
	if err != nil {
		return "", fmt.Errorf("tunnel_server: %w", err)
	}
	web := ""
	if inv.WebURL != "" {
		w, _, err := httpsAuthority(inv.WebURL, false)
		if err != nil {
			return "", fmt.Errorf("web_url: %w", err)
		}
		if w != tunnelHost { // gleich dem Standard: weglassen
			web = w
		}
	}
	tokenPath := ""
	if inv.TokenURL != "" {
		tw, p, err := httpsAuthority(inv.TokenURL, true)
		if err != nil {
			return "", fmt.Errorf("token_url: %w", err)
		}
		wantHost := web
		if wantHost == "" {
			wantHost = tunnelHost
		}
		if tw != wantHost || (p != "" && !tokenPathRe.MatchString(p)) {
			return "", errors.New("token_url: nicht auf der Webseite oder Pfad ungewöhnlich")
		}
		tokenPath = p
	}
	raw := strings.Join([]string{coord, inv.TunnelServer, web, tokenPath}, "\n")
	s := joinPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
	if len(s) > MaxJoinSecret {
		return "", fmt.Errorf("Beitritts-Geheimnis zu lang (%d Bytes)", len(s))
	}
	return s, nil
}

// DecodeJoin liest ein Beitritts-Geheimnis und prüft es streng. Das
// Ergebnis ist eine geprüfte Einladung (ohne Token), die der Aufrufer dem
// Mitglied zur Bestätigung zeigen muss.
func DecodeJoin(secret string) (*Invite, error) {
	if len(secret) > MaxJoinSecret || !strings.HasPrefix(secret, joinPrefix) {
		return nil, errors.New("kein wanderwelt-Beitritts-Geheimnis")
	}
	raw, err := base64.RawURLEncoding.DecodeString(secret[len(joinPrefix):])
	if err != nil || len(raw) > 200 {
		return nil, errors.New("Beitritts-Geheimnis unlesbar")
	}
	f := strings.Split(string(raw), "\n")
	if len(f) != 4 {
		return nil, errors.New("Beitritts-Geheimnis unvollständig")
	}
	if _, err := checkAuthority(f[0], false); err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}
	tunnelHost, err := checkAuthority(f[1], true)
	if err != nil {
		return nil, fmt.Errorf("tunnel_server: %w", err)
	}
	web := f[2]
	if web == "" {
		web = tunnelHost
	} else if _, err := checkAuthority(web, false); err != nil {
		return nil, fmt.Errorf("web_url: %w", err)
	}
	inv := &Invite{Coordinator: "https://" + f[0], TunnelServer: f[1], WebURL: "https://" + web}
	if f[3] != "" {
		if !tokenPathRe.MatchString(f[3]) || strings.Contains(f[3], "..") {
			return nil, errors.New("token_url: ungültiger Pfad")
		}
		inv.TokenURL = inv.WebURL + f[3]
	}
	// Dieselben Prüfungen wie bei Gruppen-Build-Adressen.
	if err := checkCoordinator(inv.Coordinator); err != nil {
		return nil, err
	}
	if err := checkWebURL(inv.WebURL); err != nil {
		return nil, err
	}
	if inv.TokenURL != "" {
		if err := checkWebURL(inv.TokenURL); err != nil {
			return nil, err
		}
	}
	return inv, nil
}
