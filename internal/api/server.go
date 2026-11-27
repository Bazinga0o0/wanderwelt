package api

import "time"

// Reiter „Server“ (nur Admins): Whitelist, Ops, Banns, Spielregeln und
// Befehle an den laufenden Server. Maßgeblich ist der Koordinator; der Host
// übernimmt Listen beim Start und über MembersVersion, Spielregeln und
// Befehle als Aufträge (HostJob), die er per Long-Poll abholt.

// Werte von ServerListChange.List und .Action.
const (
	ListWhitelist = "whitelist"
	ListOps       = "ops"
	ListBans      = "bans"

	ListAdd    = "add"
	ListRemove = "remove"
)

// ListPlayer ist ein Eintrag der Whitelist oder der Ops. Member: ein
// Mitglied der Gruppe (Whitelist nur über die Mitgliederliste änderbar);
// sonst ein Gast.
type ListPlayer struct {
	Name   string `json:"name"`
	UUID   string `json:"uuid"`
	Member bool   `json:"member,omitempty"`
}

// ListBan ist ein gebannter Spieler. By: Minecraft-Name (sonst E-Mail) des
// Admins; Member: der Gebannte ist Mitglied (bleibt es, kommt aber nicht
// herein).
type ListBan struct {
	Name   string    `json:"name"`
	UUID   string    `json:"uuid"`
	By     string    `json:"by,omitempty"`
	Since  time.Time `json:"since,omitzero"`
	Member bool      `json:"member,omitempty"`
}

// ServerLists ist die Antwort auf GET /v1/server/lists. Running: Die Welt
// läuft gerade (bereiter Host), Änderungen wirken dann sofort.
type ServerLists struct {
	Whitelist []ListPlayer `json:"whitelist"`
	Ops       []ListPlayer `json:"ops"`
	Bans      []ListBan    `json:"bans"`
	Running   bool         `json:"running,omitempty"`
}

// ServerListChange ändert eine Liste (POST /v1/server/lists).
type ServerListChange struct {
	List   string `json:"list"`   // ListWhitelist | ListOps | ListBans
	Action string `json:"action"` // ListAdd | ListRemove
	Name   string `json:"name"`
}

// ServerListResult ist die Antwort darauf: ein deutscher Text fürs Fenster
// und die Listen danach.
type ServerListResult struct {
	Text  string      `json:"text"`
	Lists ServerLists `json:"lists"`
}

// ServerCommand ist ein Befehl an den laufenden Server
// (POST /v1/server/command).
type ServerCommand struct {
	Command string `json:"command"`
}

// ServerCommandResult ist das Ergebnis eines Befehls oder einer
// Spielregel-Änderung: OK, wenn der Server ihn ausgeführt hat (bei Befehlen
// heißt das nur: angekommen, die Antwort sagt mehr); Output ist die Antwort
// des Servers (höchstens 4 KiB, ohne Steuerzeichen) oder der Grund.
type ServerCommandResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

// Gamerule ist eine Spielregel der laufenden Welt.
type Gamerule struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Gamerules ist die Antwort auf GET /v1/server/gamerules.
type Gamerules struct {
	Rules []Gamerule `json:"rules"`
}

// GameruleSet setzt eine Spielregel (POST /v1/server/gamerules).
type GameruleSet struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Arten von HostJob.Kind.
const (
	JobCommand   = "befehl"      // Command ausführen
	JobGamerules = "spielregeln" // Spielregeln lesen
	JobGamerule  = "spielregel"  // Name auf Value setzen
)

// HostJobsRequest: Der Host holt Aufträge (POST /v1/host/jobs); der
// Koordinator antwortet, sobald einer da ist, spätestens nach etwa 20 s
// (dann ohne Aufträge).
type HostJobsRequest struct {
	LeaseID   int64  `json:"lease_id"`
	MachineID string `json:"machine_id"`
}

// HostJob ist ein Auftrag an den Host.
type HostJob struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Command string `json:"command,omitempty"`
	Name    string `json:"name,omitempty"`
	Value   string `json:"value,omitempty"`
}

// HostJobs ist die Antwort auf HostJobsRequest.
type HostJobs struct {
	Jobs []HostJob `json:"jobs"`
}

// HostJobResult meldet das Ergebnis eines Auftrags
// (POST /v1/host/jobs/result).
type HostJobResult struct {
	LeaseID   int64      `json:"lease_id"`
	MachineID string     `json:"machine_id"`
	ID        int64      `json:"id"`
	OK        bool       `json:"ok"`
	Output    string     `json:"output,omitempty"`
	Rules     []Gamerule `json:"rules,omitempty"`
}
