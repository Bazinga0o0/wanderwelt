// Package kennung bildet die pseudonyme Mitglieds-Kennung (ID) aus der
// E-Mail-Adresse.
//
// Die ID ist Hex von HMAC-SHA256(Schlüssel, kleingeschriebene E-Mail), auf 16
// Hex-Zeichen gekürzt: stabil (gleiche Adresse, gleicher Schlüssel, gleiche
// ID, egal wie sie geschrieben ist) und nicht umkehrbar. Ein ungeschlüsselter
// Hash ließe sich durch Raten bekannter Adressen umkehren; darum liegt ein
// zufälliger, geheimer Schlüssel in data_dir (Datei idkey, 0600, beim ersten
// Start angelegt). Der Schlüssel steht nie in der API und nie im Log.
//
// Wer die E-Mail zu einer ID wissen will, ist der Serverbetreiber: Er hat den
// Schlüssel und die Mitgliederdateien (Befehl „mitglied liste“ zeigt beides).
package kennung

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// Datei ist der Name der Schlüsseldatei in data_dir.
	Datei = "idkey"
	// Laenge: Zahl der Hex-Zeichen einer ID.
	Laenge = 16
	// schluesselBytes: Länge des Schlüssels (die Datei hat sie hex-kodiert).
	schluesselBytes = 32
)

// Schluessel erzeugt die IDs. Der Nullwert taugt nicht; New, Laden oder
// Fluechtig benutzen.
type Schluessel struct {
	key []byte
}

// New baut einen Schlüssel aus key (mindestens 16 Bytes).
func New(key []byte) (*Schluessel, error) {
	if len(key) < 16 {
		return nil, errors.New("Schlüssel zu kurz (mindestens 16 Bytes)")
	}
	return &Schluessel{key: append([]byte(nil), key...)}, nil
}

// Fluechtig liefert einen frischen, nur im Speicher gehaltenen Schlüssel
// (ohne data_dir und als Notlösung bei kaputter Datei: die IDs gelten dann
// nur bis zum Neustart).
func Fluechtig() *Schluessel {
	k := make([]byte, schluesselBytes)
	if _, err := rand.Read(k); err != nil {
		panic("kein Zufall: " + err.Error())
	}
	return &Schluessel{key: k}
}

// ID liefert die Kennung der E-Mail-Adresse (ohne Rücksicht auf Groß/Klein
// und Randleerzeichen); "" für eine leere Adresse.
func (k *Schluessel) ID(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	m := hmac.New(sha256.New, k.key)
	m.Write([]byte(email))
	return hex.EncodeToString(m.Sum(nil))[:Laenge]
}

// IstID meldet, ob s wie eine ID aussieht (16 Hex-Zeichen, klein): Eine
// E-Mail-Adresse enthält immer ein „@“, ist also nie eine ID.
func IstID(s string) bool {
	if len(s) != Laenge {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Laden liest den Schlüssel aus dataDir/idkey und legt ihn zufällig an, wenn
// die Datei fehlt (0600). Ein leeres dataDir ergibt einen flüchtigen Schlüssel
// (neu true). Ist die Datei kaputt, gibt es einen Fehler und die Datei bleibt
// unangetastet (ein neuer Schlüssel würde alle IDs ändern).
func Laden(dataDir string) (k *Schluessel, neu bool, err error) {
	if dataDir == "" {
		return Fluechtig(), true, nil
	}
	path := filepath.Join(dataDir, Datei)
	if k, err := lesen(path); err == nil {
		return k, false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	raw := make([]byte, schluesselBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, false, fmt.Errorf("kein Zufall: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, false, err
	}
	// Erst ganz in eine Hilfsdatei schreiben, dann per Link veröffentlichen:
	// Ein zweiter Prozess (Befehlszeile) sieht nie eine halbe Datei und
	// überschreibt nie einen fremden Schlüssel.
	tmp, err := os.CreateTemp(dataDir, ".idkey-*")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return nil, false, err
	}
	if _, err := tmp.WriteString(hex.EncodeToString(raw) + "\n"); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			k, err := lesen(path) // ein anderer Prozess war schneller
			return k, false, err
		}
		return nil, false, err
	}
	k, err = New(raw)
	return k, true, err
}

// Lesen liest den vorhandenen Schlüssel, ohne etwas anzulegen (Befehlszeile);
// ok false, wenn die Datei fehlt.
func Lesen(dataDir string) (k *Schluessel, ok bool, err error) {
	k, err = lesen(filepath.Join(dataDir, Datei))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return k, err == nil, err
}

func lesen(path string) (*Schluessel, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(raw) < 16 {
		// Nie den Inhalt in die Meldung: Das wäre der Schlüssel.
		return nil, fmt.Errorf("%s ist kein gültiger Schlüssel (erwartet: %d Bytes als Hex)", path, schluesselBytes)
	}
	return New(raw)
}
