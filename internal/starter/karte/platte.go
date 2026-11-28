package karte

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// PlatteMax ist die Obergrenze des Zwischenspeichers auf der Platte je Welt.
const PlatteMax = 200 << 20

// Platte ist der Zwischenspeicher auf der Platte: je Gruppe und Welt ein
// Ordner mit Dateien <sha256>.png. Der Name ist der Hash des Inhalts; beim
// Lesen wird der Inhalt gegen ihn geprüft (eine veränderte oder halb
// geschriebene Datei wird gelöscht und gilt als nicht vorhanden). Über der
// Obergrenze fliegen die ältesten Dateien (nach Änderungs- bzw. Lesezeit)
// raus. Für mehrere Goroutinen geeignet.
type Platte struct {
	dir string
	max int64

	mu     sync.Mutex
	index  map[string]dateiInfo // Hash → Größe, Zeit
	gesamt int64
	jetzt  func() time.Time // Tests
}

type dateiInfo struct {
	groesse int64
	zeit    time.Time
}

// SichererName macht aus einer Gruppen- oder Weltkennung einen Ordnernamen,
// der nie aus dem Zwischenspeicher hinausführt: nur Buchstaben, Ziffern,
// Punkt, Binde- und Unterstrich; alles andere wird „_“, kein Punkt am Anfang
// oder Ende, höchstens 80 Zeichen, keine reservierten Windows-Namen (CON,
// NUL …).
func SichererName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	n := b.String()
	if n == "" || n[0] == '.' {
		n = "_" + n
	}
	n = strings.TrimRight(n, ".") // Windows streicht Punkte am Ende
	if n == "" {
		n = "_"
	}
	switch strings.ToUpper(strings.SplitN(n, ".", 2)[0]) {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		n = "_" + n
	}
	return n
}

// OrdnerFuer ist der Ordner des Zwischenspeichers einer Welt:
// <basis>/karte-cache/<gruppe>/<welt>.
func OrdnerFuer(basis, gruppe, welt string) string {
	return filepath.Join(basis, "karte-cache", SichererName(gruppe), SichererName(welt))
}

// NeuePlatte öffnet (und legt an) den Zwischenspeicher in dir mit der
// Obergrenze max Bytes (≤ 0: PlatteMax). Reste halb geschriebener Dateien
// werden entfernt; zu viel Vorhandenes wird sofort aufgeräumt.
func NeuePlatte(dir string, max int64) (*Platte, error) {
	if max <= 0 {
		max = PlatteMax
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("Zwischenspeicher der Karte: %w", err)
	}
	p := &Platte{dir: dir, max: max, index: map[string]dateiInfo{}, jetzt: time.Now}
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Zwischenspeicher der Karte: %w", err)
	}
	for _, e := range es {
		name := e.Name()
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		hash, ok := strings.CutSuffix(name, ".png")
		if !ok || !GueltigerHash(hash) {
			if strings.HasSuffix(name, ".tmp") {
				os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		p.index[hash] = dateiInfo{fi.Size(), fi.ModTime()}
		p.gesamt += fi.Size()
	}
	p.aufraeumenLocked()
	return p, nil
}

func (p *Platte) pfad(hash string) string { return filepath.Join(p.dir, hash+".png") }

// Lesen liefert den Inhalt der Datei zum Hash, wenn sie da ist und wirklich
// diesen Hash hat. Eine Datei mit anderem Inhalt wird gelöscht.
func (p *Platte) Lesen(hash string) ([]byte, bool) {
	if !GueltigerHash(hash) {
		return nil, false
	}
	data, err := os.ReadFile(p.pfad(hash))
	if err != nil {
		return nil, false
	}
	if len(data) > MaxKachelBytes || hashVon(data) != hash {
		p.Verwerfen(hash)
		return nil, false
	}
	p.mu.Lock()
	jetzt := p.jetzt()
	if _, ok := p.index[hash]; !ok {
		p.gesamt += int64(len(data))
	}
	p.index[hash] = dateiInfo{int64(len(data)), jetzt}
	p.mu.Unlock()
	os.Chtimes(p.pfad(hash), jetzt, jetzt) // „zuletzt benutzt“ fürs Aufräumen
	return data, true
}

// Verwerfen löscht die Datei zum Hash.
func (p *Platte) Verwerfen(hash string) {
	if !GueltigerHash(hash) {
		return
	}
	os.Remove(p.pfad(hash))
	p.mu.Lock()
	if fi, ok := p.index[hash]; ok {
		p.gesamt -= fi.groesse
		delete(p.index, hash)
	}
	p.mu.Unlock()
}

// Schreiben legt data unter hash ab – nur, wenn data wirklich diesen Hash hat
// (sonst Fehler) –, atomar über eine Zwischendatei, und räumt danach auf.
func (p *Platte) Schreiben(hash string, data []byte) error {
	if !GueltigerHash(hash) {
		return errors.New("Zwischenspeicher der Karte: ungültiger Hash")
	}
	if len(data) > MaxKachelBytes || hashVon(data) != hash {
		return errors.New("Zwischenspeicher der Karte: Inhalt passt nicht zum Hash")
	}
	var r [6]byte
	rand.Read(r[:])
	tmp := filepath.Join(p.dir, hash+"."+hex.EncodeToString(r[:])+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("Zwischenspeicher der Karte: %w", err)
	}
	if err := os.Rename(tmp, p.pfad(hash)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("Zwischenspeicher der Karte: %w", err)
	}
	p.mu.Lock()
	if alt, ok := p.index[hash]; ok {
		p.gesamt -= alt.groesse
	}
	p.index[hash] = dateiInfo{int64(len(data)), p.jetzt()}
	p.gesamt += int64(len(data))
	p.aufraeumenLocked()
	p.mu.Unlock()
	return nil
}

// Groesse ist die Summe der Dateigrößen im Zwischenspeicher.
func (p *Platte) Groesse() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gesamt
}

// aufraeumenLocked löscht die ältesten Dateien, bis höchstens 90 % der
// Obergrenze belegt sind (nur wenn die Obergrenze überschritten ist).
func (p *Platte) aufraeumenLocked() {
	if p.gesamt <= p.max {
		return
	}
	hashes := make([]string, 0, len(p.index))
	for h := range p.index {
		hashes = append(hashes, h)
	}
	slices.SortFunc(hashes, func(a, b string) int {
		if c := p.index[a].zeit.Compare(p.index[b].zeit); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	ziel := p.max / 10 * 9
	for _, h := range hashes {
		if p.gesamt <= ziel {
			break
		}
		os.Remove(p.pfad(h))
		p.gesamt -= p.index[h].groesse
		delete(p.index, h)
	}
}

func hashVon(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// HashVon ist der sha256 von data als kleine Hexzeichen (der Hash im
// Manifest).
func HashVon(data []byte) string { return hashVon(data) }
