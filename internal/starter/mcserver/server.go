// Package mcserver ist der Server-Baustein des Hosts: Er richtet einen
// Minecraft-Server-Ordner ein, startet und stoppt den Server und steuert ihn
// (Spielerliste, Speichern für Zwischenstände).
//
// Stufe 1 und 2 bringen genau eine Umsetzung mit, den Fabric-Server über
// RCON (NewFabric). Weitere Umsetzungen (etwa Pumpkin) kommen später dazu,
// ohne dass sich der Rest des Starters ändert.
package mcserver

import (
	"context"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

// Server ist ein lokaler Minecraft-Server auf 127.0.0.1:25565.
//
// Ablauf: Prepare → Start → (Players, SaveBarrier …) → Stop. Players und
// SaveBarrier dürfen aus verschiedenen Goroutinen kommen; die Befehle laufen
// nacheinander.
type Server interface {
	// Prepare richtet in.Dir für den nächsten Start ein: server.properties,
	// whitelist.json, ops.json, eula.txt und den Mods-Ordner. Nicht während
	// der Server läuft.
	Prepare(ctx context.Context, in PrepareInput) error

	// Start startet den Server und kehrt zurück, wenn er bereit ist (RCON
	// antwortet), spätestens nach 5 Minuten. Stirbt der Prozess vorher, ist
	// er nach 5 Minuten nicht bereit oder endet ctx, ist das ein Fehler und
	// kein Prozess läuft mehr: Start stoppt ihn dann selbst über die Konsole
	// und wartet dabei wie Stop höchstens 2 Minuten, bevor er hart beendet
	// wird (die Welt kann gerade geladen werden).
	Start(ctx context.Context) error

	// Stop schickt "stop" und wartet, bis der Prozess endet: höchstens
	// 2 Minuten oder bis ctx endet, danach wird er hart beendet und Stop
	// liefert einen Fehler. ctx darf deshalb beim Aufruf nicht schon beendet
	// sein (beim Herunterfahren context.WithoutCancel plus eigenes
	// Zeitlimit). nil heißt: sauber gestoppt, die Welt ist gespeichert.
	Stop(ctx context.Context) error

	// SaveBarrier schaltet das automatische Speichern ab und speichert alles
	// (save-off, save-all flush). Kehrt erst zurück, wenn der Server das
	// Speichern bestätigt hat; bis release läuft, ändert der Server die
	// Dateien der Welt nicht. release (save-on) muss danach aufgerufen werden.
	SaveBarrier(ctx context.Context) (release func(context.Context) error, err error)

	// Players liefert die Namen der Spieler, die gerade online sind (leere
	// Liste, wenn niemand da ist).
	Players(ctx context.Context) ([]string, error)

	// Command schickt einen Befehl an den laufenden Server (ohne "/", etwa
	// "whitelist add Alex") und liefert seine Antwort. Ein Fehler heißt: Der
	// Befehl kam nicht an oder blieb ohne Antwort; was der Server antwortet
	// (auch „No player was found“), ist kein Fehler.
	Command(ctx context.Context, cmd string) (string, error)

	// Say sagt text allen Spielern im Chat an ("say <text>" über Command).
	Say(ctx context.Context, text string) error

	// Exited meldet das Ende des zuletzt gestarteten Prozesses: genau ein
	// Wert, danach ist der Kanal geschlossen. nil heißt sauber beendet nach
	// Stop, sonst ein Fehler (Absturz, Ende ohne Stop, hart beendet). Vor
	// dem ersten Start ist der Kanal nil (blockiert also). Nach Start
	// abfragen.
	Exited() <-chan error
}

// PrepareInput beschreibt, wie der Server-Ordner aussehen soll.
type PrepareInput struct {
	// Dir ist der Server-Ordner (Arbeitsverzeichnis des Servers).
	Dir string
	// WorldDirName ist der Weltordner in Dir und wird zu level-name;
	// leer heißt "world".
	WorldDirName string
	// Paths kommt aus deps.Ensure (Java, Fabric-Server-Launcher, Mods).
	Paths *deps.Paths
	// Settings sind Welt-Einstellungen, Whitelist und Ops vom Koordinator.
	Settings api.HostSettings
	// Profile ist das Leistungsprofil dieses PCs (Xmx, Sichtweiten).
	Profile api.Profile
}
