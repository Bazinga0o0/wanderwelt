package server

// Datenschutz: E-Mail-Adressen von Mitgliedern sieht nur der Serverbetreiber
// (Befehlszeile „mitglied …“, Dateien und Logs auf dem Server). Über die API
// bekommt jede Person nur ihre eigene Adresse; andere erscheinen mit
// Minecraft-Namen und pseudonymer Kennung (internal/koord/kennung).
//
// Alle Antworten, die ein Mitglied beschreiben, laufen durch die Helfer dieser
// Datei; „Anfragender == Eigentümer“ ist das einzige, was die Adresse
// freigibt (eigenerKonto).

import (
	"strings"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
)

// loadIDKey lädt den Schlüssel der Mitglieds-Kennungen aus data_dir (legt ihn
// beim ersten Start an). Scheitert das, gilt ein nur im Speicher gehaltener
// Schlüssel (die Kennungen ändern sich dann beim Neustart) und es wird laut;
// eine kaputte Datei bleibt unangetastet. Nie den Schlüssel loggen.
func (s *Server) loadIDKey() {
	k, neu, err := kennung.Laden(s.cfg.DataDir)
	if err != nil {
		s.log.Error("KENNUNGS-SCHLÜSSEL (idkey) NICHT LESBAR – Mitglieds-Kennungen gelten nur bis zum Neustart; Datei in data_dir prüfen",
			"err", err)
		k = kennung.Fluechtig()
	} else if neu && s.cfg.DataDir != "" {
		s.log.Info("Kennungs-Schlüssel angelegt", "datei", kennung.Datei)
	}
	s.ids = k
}

// id ist die pseudonyme Kennung der Adresse email.
func (s *Server) id(email string) string { return s.ids.ID(email) }

// eigenerKonto: Die Adresse gehört dem Anfragenden (Groß/Klein egal; leer
// gehört niemandem).
func eigenerKonto(owner, viewer string) bool {
	return owner != "" && viewer != "" && strings.EqualFold(owner, viewer)
}

// hostFor beschreibt h für den Anfragenden viewer (E-Mail; "" für niemanden,
// etwa das interne Beobachten): immer mit Kennung, die Adresse nur, wenn sie
// dem Anfragenden gehört. machine: auch die MachineID (nur für den Host
// selbst).
func (s *Server) hostFor(h api.HostInfo, viewer string, machine bool) *api.HostInfo {
	out := &api.HostInfo{ID: s.id(h.Email), MinecraftName: h.MinecraftName}
	if eigenerKonto(h.Email, viewer) {
		out.Email = h.Email
	}
	if machine {
		out.MachineID = h.MachineID
	}
	return out
}

// generationFor ist g (eine Kopie) für den Anfragenden: Die Adresse des Hosts
// bleibt nur beim Host selbst, dazu die Kennung.
func (s *Server) generationFor(g *api.Generation, viewer string) *api.Generation {
	if g == nil {
		return nil
	}
	c := *g
	c.HostID = s.id(g.HostEmail)
	if !eigenerKonto(g.HostEmail, viewer) {
		c.HostEmail = ""
	}
	return &c
}

// who nennt das Mitglied m für Meldungen an andere: Minecraft-Name, sonst die
// Kennung – nie die Adresse.
func (s *Server) who(m config.Member) string {
	if m.Minecraft != "" {
		return m.Minecraft
	}
	return "Mitglied " + s.id(m.Email)
}

// memberKey löst den Schlüssel aus einem Pfad ({email}) auf: Eine Kennung
// (16 Hex-Zeichen, nie ein „@“) gehört zum Mitglied unter members, sonst gilt
// der Wert als E-Mail-Adresse (Betreiber-Befehle, ältere Starter). Gibt es
// keinen Treffer, kommt key unverändert zurück.
func (s *Server) memberKey(members []config.Member, key string) string {
	if !kennung.IstID(key) {
		return key
	}
	for _, m := range members {
		if s.id(m.Email) == key {
			return m.Email
		}
	}
	return key
}

// indexKey ist indexMember für Adresse oder Kennung.
func (s *Server) indexKey(members []config.Member, key string) int {
	return indexMember(members, s.memberKey(members, key))
}

// pfadSchluessel prüft den Schlüssel {email} aus einem Pfad einer Admin-
// Aktion: Erlaubt sind die Kennung und die eigene Adresse (ältere Starter
// schicken sie so). Eine fremde Adresse würde die API zum Ratespiel machen
// („gehört diese Adresse zu einem Mitglied?“); sie gilt darum wie ein
// unbekanntes Mitglied (404). Der Serverbetreiber nutzt dafür die
// Befehlszeile.
func pfadSchluessel(key, viewer string) bool {
	return kennung.IstID(key) || eigenerKonto(key, viewer)
}
