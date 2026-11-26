// Package discord zeigt in Discord (Rich Presence) „Spielt wanderwelt“ an.
// Rein lokal: Discord-IPC über die Named Pipe \\.\pipe\discord-ipc-0…9, kein
// Server, kein CGO. Läuft Discord nicht, passiert nichts; Fehler stören den
// Starter nie und stehen nur im Debug-Log.
package discord

import (
	"fmt"
	"sync"
	"time"
)

// ClientID ist die Anwendungs-ID aus dem Discord Developer Portal
// (docs/discord-einrichtung.md). Kein Geheimnis. Ist sie "" oder "0", tut
// das Paket nichts (kein Verbindungsversuch).
const ClientID = "1575453522638544927"

// Feste Teile der Anzeige.
const (
	Details    = "Minecraft über Wanderwelt"
	LargeImage = "wanderwelt-discord-1024"                       // Schlüssel des Art Assets im Developer Portal
	LargeText  = "wanderwelt – github.com/Bazinga0o0/wanderwelt" // Hinweistext am Bild
	ButtonText = "wanderwelt herunterladen"
	ButtonURL  = "https://github.com/Bazinga0o0/wanderwelt/releases/latest"

	// JoinText und JoinURL: Der Knopf „Beitreten“ führt auf den Link
	// wanderwelt://beitreten, den der Starter selbst öffnet (ohne
	// Gruppendaten in der Adresse). Discord erlaubt Knöpfe und Geheimnisse
	// (secrets.join) nicht zugleich, darum gibt es keine Geheimnisse.
	// JoinPage ist der Rückfall, falls Discord das Schema nicht annimmt: die
	// statische Seite docs/beitreten (GitHub Pages); sie wird nicht mehr
	// gesendet.
	JoinText = "Beitreten"
	JoinURL  = "wanderwelt://beitreten"
	JoinPage = "https://bazinga0o0.github.io/wanderwelt/beitreten/"
)

// Activity ist die variable Anzeige: zweite Zeile und Spielzeit-Beginn,
// dazu Party („N von M“, nur Anzeige; PartyMax 0: keine Party).
type Activity struct {
	State     string
	Start     time.Time
	PartyID   string
	PartySize int
	PartyMax  int
	// NoJoin: Knopf „Beitreten“ weglassen (Admin hat den Beitritt über
	// Discord ausgeschaltet); der Download-Knopf bleibt immer.
	NoJoin bool
}

// Presence ist die Schnittstelle des Starters zu Discord. Alle Methoden
// kehren sofort zurück (Close wartet höchstens kurz).
type Presence interface {
	// Set setzt die Aktivität (nur bei Änderung wird etwas gesendet).
	Set(Activity)
	// Clear löscht die Aktivität.
	Clear()
	// Close löscht die Aktivität (soweit möglich) und beendet die Arbeit.
	Close()
}

// FriendsText ist die zweite Zeile nach der Zahl der anderen Spieler.
func FriendsText(others int) string {
	switch {
	case others <= 0:
		return "ohne Freunde :("
	case others == 1:
		return "mit 1 Freund"
	}
	return fmt.Sprintf("mit %d Freunden", others)
}

// Noop tut nichts (Discord aus).
type Noop struct{}

func (Noop) Set(Activity) {}
func (Noop) Clear()       {}
func (Noop) Close()       {}

// Fake merkt sich die Aufrufe (für Tests).
type Fake struct {
	mu     sync.Mutex
	sets   []Activity
	clears int
	closed bool
}

func (f *Fake) Set(a Activity) { f.mu.Lock(); f.sets = append(f.sets, a); f.mu.Unlock() }
func (f *Fake) Clear()         { f.mu.Lock(); f.clears++; f.mu.Unlock() }
func (f *Fake) Close()         { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

// Snapshot liest den Stand sicher.
func (f *Fake) Snapshot() (sets []Activity, clears int, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Activity(nil), f.sets...), f.clears, f.closed
}
