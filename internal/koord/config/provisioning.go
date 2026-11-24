package config

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Standardwerte des Abschnitts provisioning.
const (
	DefaultTunneldBin    = "/opt/tunneld/tunneld"
	DefaultRunAs         = "tunneld"
	DefaultReloadCommand = "systemctl reload wanderwelt-koordinator"
)

// Provisioning beschreibt, wie „wanderwelt-koordinator mitglied …“ die
// tunneld-Admin-CLI aufruft und was in die Gruppendatei kommt. Für serve
// spielt der Abschnitt keine Rolle und darf fehlen.
type Provisioning struct {
	// TunneldBin ist das tunneld-Programm (Standard DefaultTunneldBin).
	TunneldBin string `yaml:"tunneld_bin"`
	// TunneldConfig ist die Konfiguration von tunneld (Pflicht für mitglied).
	TunneldConfig string `yaml:"tunneld_config"`
	// RunAs ist der Benutzer, unter dem die CLI läuft (runuser -u; Standard
	// DefaultRunAs).
	RunAs string `yaml:"run_as"`
	// CoordinatorURL und TunnelServer landen in der Gruppendatei (Pflicht
	// für mitglied neu).
	CoordinatorURL string `yaml:"coordinator_url"`
	TunnelServer   string `yaml:"tunnel_server"`
	// WebURL ist die Web-Anmeldung von tunneld für die Zugangsdatei eines
	// neuen Kontos (Standard: https:// und der Host aus tunnel_server).
	WebURL string `yaml:"web_url"`
	// ReloadCommand lässt den laufenden Koordinator die Mitglieder neu lesen;
	// läuft ohne Shell, an Leerzeichen getrennt (Standard
	// DefaultReloadCommand).
	ReloadCommand string `yaml:"reload_command"`
}

// WithDefaults liefert p mit den Standardwerten für leere Felder.
func (p Provisioning) WithDefaults() Provisioning {
	p.TunneldBin = cmp.Or(strings.TrimSpace(p.TunneldBin), DefaultTunneldBin)
	p.RunAs = cmp.Or(strings.TrimSpace(p.RunAs), DefaultRunAs)
	p.ReloadCommand = cmp.Or(strings.TrimSpace(p.ReloadCommand), DefaultReloadCommand)
	p.TunneldConfig = strings.TrimSpace(p.TunneldConfig)
	p.CoordinatorURL = strings.TrimRight(strings.TrimSpace(p.CoordinatorURL), "/")
	p.TunnelServer = strings.TrimSpace(p.TunnelServer)
	p.WebURL = strings.TrimRight(strings.TrimSpace(p.WebURL), "/")
	if p.WebURL == "" && p.TunnelServer != "" {
		host := p.TunnelServer
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]" // IPv6
		}
		p.WebURL = "https://" + host
	}
	return p
}

// Check prüft, ob p für die tunneld-CLI reicht; mit forGroup auch die
// Felder der Gruppendatei. p hat schon die Standardwerte (WithDefaults).
func (p Provisioning) Check(forGroup bool) error {
	if p.TunneldConfig == "" {
		return errors.New("provisioning.tunneld_config fehlt (Konfiguration von tunneld, etwa /etc/tunneld/config.yaml)")
	}
	if !forGroup {
		return nil
	}
	if p.CoordinatorURL == "" {
		return errors.New("provisioning.coordinator_url fehlt (Adresse des Koordinators für die Gruppendatei)")
	}
	u, err := url.Parse(p.CoordinatorURL)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("provisioning.coordinator_url %q ist keine gültige Adresse (erwartet z. B. https://wanderwelt.example.org)", p.CoordinatorURL)
	}
	// Wie der Starter: http nur für Loopback, sonst ginge der Token im
	// Klartext über das Netz.
	if u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())) {
		return fmt.Errorf("provisioning.coordinator_url %q: Schema muss https sein", p.CoordinatorURL)
	}
	if p.TunnelServer == "" {
		return errors.New("provisioning.tunnel_server fehlt (tunneld-Server für die Gruppendatei, etwa tunnel.example.org:7443)")
	}
	if u, err := url.Parse(p.WebURL); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("provisioning.web_url %q ist keine gültige Adresse (erwartet z. B. https://tunnel.example.org)", p.WebURL)
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
