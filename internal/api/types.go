// Package api enthält die JSON-Typen zwischen Starter und Koordinator.
//
// Manifest, Artifact und Profile liegen zusätzlich als YAML-Dateien vor
// (Versions-Manifest auf dem Koordinator, Leistungsprofil beim Starter) und
// tragen deshalb auch YAML-Namen.
package api

import "time"

// Werte von PlayResponse.Role.
const (
	RoleHost   = "host"
	RoleJoin   = "join"
	RoleDenied = "denied"
)

// Score sind die Messwerte, die ein Starter für die Wahl des Hosts meldet.
type Score struct {
	UploadMbps float64 `json:"upload_mbps"`
	CPUScore   float64 `json:"cpu_score"`
	RAMFreeMB  int     `json:"ram_free_mb"`
	RAMTotalMB int     `json:"ram_total_mb"`
	XmxMB      int     `json:"xmx_mb"`
}

// PlayRequest: Ein Starter will spielen (oder mit UploadOnly einen sauber
// gestoppten Stand nachreichen, der auf BaseWorldID/BaseGeneration aufbaut).
type PlayRequest struct {
	MachineID      string `json:"machine_id"`
	Score          Score  `json:"score"`
	UploadOnly     bool   `json:"upload_only"`
	BaseWorldID    string `json:"base_world_id"`
	BaseGeneration int64  `json:"base_generation"`
}

// PlayResponse ist die Antwort auf PlayRequest.
type PlayResponse struct {
	Role             string    `json:"role"` // "host" | "join" | "denied"
	LeaseID          int64     `json:"lease_id"`
	HeadGeneration   int64     `json:"head_generation"`
	WorldID          string    `json:"world_id"`
	Host             *HostInfo `json:"host,omitempty"`
	MinecraftAddress string    `json:"minecraft_address"`
	Reason           string    `json:"reason,omitempty"`
}

// HostInfo beschreibt den aktuellen Host.
type HostInfo struct {
	Email         string `json:"email"`
	MinecraftName string `json:"minecraft_name"`
	MachineID     string `json:"machine_id"`
}

// HeartbeatRequest ist das Lebenszeichen des Hosts samt Spielerliste.
// Ready: Der Server läuft und ist (über tunnel) erreichbar; erst dann
// meldet der Koordinator den Host als bereit (Status.HostReady).
type HeartbeatRequest struct {
	LeaseID   int64    `json:"lease_id"`
	MachineID string   `json:"machine_id"`
	Players   []string `json:"players"`
	Ready     bool     `json:"ready"`
}

// ReleaseRequest gibt den Zuschlag zurück.
type ReleaseRequest struct {
	LeaseID   int64  `json:"lease_id"`
	MachineID string `json:"machine_id"`
}

// Status zeigt, wer hostet und wer drin ist.
//
// HostReady: Der Host hat zuletzt gemeldet, dass sein Server läuft und
// erreichbar ist, und dieses Lebenszeichen ist jünger als 25 s (zwei
// verpasste Lebenszeichen); vorher lädt er noch die Welt oder startet den
// Server, danach ist er vielleicht abgestürzt.
// HostUploading: Der Zuschlag dient nur dem Hochladen eines Stands
// (Nachreichen, Welt übernehmen); einen Server gibt es dann nicht.
type Status struct {
	Host             *HostInfo `json:"host,omitempty"`
	HostReady        bool      `json:"host_ready"`
	HostUploading    bool      `json:"host_uploading,omitempty"`
	Players          []string  `json:"players"`
	HeadGeneration   int64     `json:"head_generation"`
	WorldID          string    `json:"world_id"`
	MinecraftAddress string    `json:"minecraft_address"`
	// HeadCreated: Zeit des Heads; SnapshotIntervalS: Abstand der
	// Zwischenstände (für „nächster Stand“ im Fenster); RestorePending: ein
	// ausstehendes Zurückholen.
	HeadCreated       time.Time      `json:"head_created,omitempty"`
	SnapshotIntervalS int            `json:"snapshot_interval_s,omitempty"`
	RestorePending    *RestoreNotice `json:"restore_pending,omitempty"`
	// Handover: laufende Übergabe des Hosts an einen neuen Starter.
	Handover *HandoverState `json:"handover,omitempty"`
	// Maintenance: Wartung am Server (angekündigt oder laufend); nil ohne.
	Maintenance *Maintenance `json:"maintenance,omitempty"`
	// Members: Zahl der Gruppenmitglieder (ohne offene Beitrittsanfragen, ohne
	// Namen); 0: ältere Koordinatoren kennen das Feld nicht.
	Members int `json:"members,omitempty"`
}

// Maintenance beschreibt eine Wartung am Server, die der Admin mit
// „wanderwelt-koordinator wartung start“ ausgelöst hat. Since: seit wann
// Spielen abgelehnt wird, StopAt: wann der Host stoppen muss. RemainingS:
// Sekunden bis StopAt nach der Uhr des Koordinators (die Uhr eines PCs kann
// abweichen; ältere Koordinatoren kennen das Feld nicht: 0 bei vergangenem
// StopAt, sonst aus StopAt abzuleiten).
type Maintenance struct {
	Reason     string    `json:"reason"`
	StopAt     time.Time `json:"stop_at"`
	Since      time.Time `json:"since"`
	RemainingS int64     `json:"remaining_s"`
}

// Me beschreibt das anfragende Mitglied.
type Me struct {
	Email            string `json:"email"`
	MinecraftName    string `json:"minecraft_name"`
	Admin            bool   `json:"admin"`
	Op               bool   `json:"op"`
	MinecraftAddress string `json:"minecraft_address"`
	// PendingJoins: Zahl der offenen Beitrittsanfragen (nur für Admins,
	// sonst nicht gesetzt).
	PendingJoins int `json:"pending_joins,omitempty"`
}

// FileEntry ist eine Datei eines Weltstands. Path ist relativ zum
// Weltordner und `/`-getrennt.
//
// Ohne Rezept ist die Datei der Blob SHA256. Mit Rezept (zerlegte
// Regionsdateien) ist Recipe der Hash eines Rezept-Blobs (siehe
// EncodeRecipe), und die Datei ist die Aneinanderreihung der dort gelisteten
// Segment-Blobs; SHA256 und Size beschreiben dann die ganze Datei, einen
// Blob dafür gibt es nicht, und die Segmentgrößen ergeben zusammen Size.
type FileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Recipe string `json:"recipe,omitempty"`
}

// Segment ist ein Stück einer zerlegten Datei, als eigener Blob abgelegt
// und in ihrem Rezept gelistet.
type Segment struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Generation ist ein angenommener Stand der Welt.
type Generation struct {
	Number           int64       `json:"number"`
	Parent           int64       `json:"parent"`
	WorldID          string      `json:"world_id"`
	Created          time.Time   `json:"created"`
	HostEmail        string      `json:"host_email"`
	MachineID        string      `json:"machine_id"`
	LeaseID          int64       `json:"lease_id"`
	MinecraftVersion string      `json:"minecraft_version"`
	Files            []FileEntry `json:"files"`
	// Players: wer beim Einreichen online war (Minecraft-Namen);
	// RestoredFrom: der Stand, dessen Inhalt ein Zurückholen übernommen hat.
	Players      []string `json:"players,omitempty"`
	RestoredFrom *GenRef  `json:"restored_from,omitempty"`
}

// CommitRequest reicht einen Stand ein. Er baut auf Generation
// BaseGeneration der Welt BaseWorldID auf (beides leer bzw. 0, solange es
// noch keine Welt gibt); Generationsnummern beginnen in jeder Welt bei 1,
// darum zählt die Welt mit. NewWorld macht ihn zur neuen Gruppenwelt (Welt
// übernehmen); die Basis zählt dann nicht.
type CommitRequest struct {
	LeaseID          int64       `json:"lease_id"`
	MachineID        string      `json:"machine_id"`
	BaseWorldID      string      `json:"base_world_id"`
	BaseGeneration   int64       `json:"base_generation"`
	NewWorld         bool        `json:"new_world"`
	MinecraftVersion string      `json:"minecraft_version"`
	Files            []FileEntry `json:"files"`
	// Players: wer beim Einreichen online war (Minecraft-Namen).
	Players []string `json:"players,omitempty"`
}

// HostSettings bekommt der Host-Starter vor dem Serverstart:
// Welt-Einstellungen (server.properties), Whitelist und Ops.
//
// SnapshotIntervalS: Abstand der Zwischenstände in Sekunden
// (snapshot_interval des Koordinators). Fehlt er (älterer Koordinator) oder
// liegt er außerhalb von MinSnapshotIntervalS–MaxSnapshotIntervalS, nimmt
// der Host 5 Minuten.
//
// EmptyTimeoutS: Leerlauf-Frist in Sekunden (empty_timeout des
// Koordinators): So lange ohne Spieler, dann endet die Host-Sitzung (außer
// im Dauerbetrieb). Fehlt sie (älterer Koordinator) oder liegt sie
// außerhalb von MinEmptyTimeoutS–MaxEmptyTimeoutS, nimmt der Host 10
// Minuten.
type HostSettings struct {
	Properties        map[string]string `json:"properties"`
	Whitelist         []Player          `json:"whitelist"`
	Ops               []Player          `json:"ops"`
	SnapshotIntervalS int               `json:"snapshot_interval_s,omitempty"`
	EmptyTimeoutS     int               `json:"empty_timeout_s,omitempty"`
}

// Erlaubter Abstand der Zwischenstände in Sekunden (1–30 Minuten).
const (
	MinSnapshotIntervalS = 60
	MaxSnapshotIntervalS = 1800
)

// Erlaubte Leerlauf-Frist in Sekunden (1–60 Minuten).
const (
	MinEmptyTimeoutS = 60
	MaxEmptyTimeoutS = 3600
)

// Player ist ein Minecraft-Spieler (UUID mit Bindestrichen).
type Player struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Manifest ist das Versions-Manifest: was der Host braucht und in welcher
// Version gehostet wird.
type Manifest struct {
	MinecraftVersion string     `json:"minecraft_version" yaml:"minecraft_version"`
	Java             Artifact   `json:"java" yaml:"java"`
	FabricServer     Artifact   `json:"fabric_server" yaml:"fabric_server"`
	Tunnel           Artifact   `json:"tunnel" yaml:"tunnel"`
	Mods             []Artifact `json:"mods" yaml:"mods"`
	// Configs sind Konfigdateien für den Server-Ordner (optional), siehe
	// ConfigFile.
	Configs []ConfigFile `json:"configs,omitempty" yaml:"configs,omitempty"`
	// Starter ist die aktuelle Version des Starters (Selbst-Update); nil:
	// keine. Der Koordinator liefert den Eintrag nur aus, wenn er beim
	// Start oder Neuladen die Datei gefunden und den Hash geprüft hat.
	Starter *StarterRelease `json:"starter,omitempty" yaml:"starter,omitempty"`
}

// StarterRelease nennt eine Release-Version des Starters (wanderwelt.exe).
// Version: "MAJOR.MINOR.PATCH" (ein "v" davor ist erlaubt). URL: nur
// "wanderwelt:<datei>" – der Starter lädt sich nur über den angemeldeten
// Koordinator-Client. SHA256: Hex (auch als "sha256:<hex>"). Size: Bytes,
// 0 unbekannt.
type StarterRelease struct {
	Version string `json:"version" yaml:"version"`
	URL     string `json:"url" yaml:"url"`
	SHA256  string `json:"sha256" yaml:"sha256"`
	Size    int64  `json:"size,omitempty" yaml:"size,omitempty"`
}

// Artifact ist eine Datei zum Herunterladen. Hash hat die Form
// "sha256:<hex>" oder "sha512:<hex>".
type Artifact struct {
	Name     string `json:"name" yaml:"name"`
	URL      string `json:"url" yaml:"url"`
	Hash     string `json:"hash" yaml:"hash"`
	Optional bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// Profile ist das Leistungsprofil eines PCs.
//
// Dauerbetrieb: Dieser PC hostet von selbst, sobald niemand hostet, und
// seine Sitzung endet nicht mangels Spielern – nur, solange er am Netz
// hängt (ein Notebook auf Akku hostet normal). Standard aus.
type Profile struct {
	XmxMB              int  `json:"xmx_mb" yaml:"xmx_mb"`
	ViewDistance       int  `json:"view_distance" yaml:"view_distance"`
	SimulationDistance int  `json:"simulation_distance" yaml:"simulation_distance"`
	C2ME               bool `json:"c2me" yaml:"c2me"`
	C2METhreads        int  `json:"c2me_threads" yaml:"c2me_threads"`
	Dauerbetrieb       bool `json:"dauerbetrieb,omitempty" yaml:"dauerbetrieb,omitempty"`
	// KeineUebernahme: Dieser PC darf das Hosten nicht übernehmen (Häkchen
	// „Darf das Hosten übernehmen“ aus). Standard aus.
	KeineUebernahme bool `json:"keine_uebernahme,omitempty" yaml:"keine_uebernahme,omitempty"`
}

// --- Stände-Liste, Zurückholen, Mitglieder (Fenster) ---

// GenRef bezeichnet eine Generation einer Welt.
type GenRef struct {
	WorldID string `json:"world_id"`
	Number  int64  `json:"number"`
}

// GenerationInfo beschreibt einen Stand in der Stände-Liste.
type GenerationInfo struct {
	WorldID          string    `json:"world_id"`
	Number           int64     `json:"number"`
	Created          time.Time `json:"created"`
	HostName         string    `json:"host_name"` // Minecraft-Name, "" wenn unbekannt
	Players          []string  `json:"players,omitempty"`
	Size             int64     `json:"size"`
	MinecraftVersion string    `json:"minecraft_version"`
	RestoredFrom     *GenRef   `json:"restored_from,omitempty"`
}

// WorldInfo ist eine Welt mit ihren Ständen.
type WorldInfo struct {
	WorldID     string           `json:"world_id"`
	Active      bool             `json:"active"`
	Generations []GenerationInfo `json:"generations"` // neueste zuerst
}

// GenerationsResponse ist die Antwort auf GET /v1/generations.
type GenerationsResponse struct {
	Worlds  []WorldInfo  `json:"worlds"` // aktive zuerst, dann Archive neueste zuerst
	Storage *StorageInfo `json:"storage,omitempty"`
}

// StorageInfo beschreibt die Belegung des Speichers.
type StorageInfo struct {
	UsedBytes     int64 `json:"used_bytes"`      // Blobs aller behaltenen Stände, jeder einmal gezählt
	BudgetBytes   int64 `json:"budget_bytes"`    // wirksames Budget (Überlagerung oder storage_budget)
	DiskFreeBytes int64 `json:"disk_free_bytes"` // frei auf dem Dateisystem von data_dir
	OverBudget    bool  `json:"over_budget"`     // trotz Aufräumen über dem Budget
}

// BudgetRequest setzt das Speicherbudget.
type BudgetRequest struct {
	BudgetBytes int64 `json:"budget_bytes"`
}

// RestoreRequest: Ein Admin holt einen Stand zurück.
type RestoreRequest struct {
	WorldID string `json:"world_id"`
	Number  int64  `json:"number"`
}

// RestoreNotice beschreibt ein ausstehendes Zurückholen (Ansage beim Host,
// Anzeige im Status).
type RestoreNotice struct {
	By            string    `json:"by"` // Minecraft-Name des Admins
	Target        GenRef    `json:"target"`
	TargetCreated time.Time `json:"target_created"`
	RequestedAt   time.Time `json:"requested_at"`
	DelayS        int       `json:"delay_s"` // 30
}

// RestoreResponse ist die Antwort auf RestoreRequest: sofort angewandt
// (New ist der neue Head) oder ausstehend, bis der Host endet.
type RestoreResponse struct {
	Applied bool    `json:"applied"`
	Pending bool    `json:"pending"`
	New     *GenRef `json:"new,omitempty"`
}

// HeartbeatResponse ist die Antwort auf ein Lebenszeichen; Restore ist ein
// ausstehendes Zurückholen, das der Host ansagen soll.
type HeartbeatResponse struct {
	Restore *RestoreNotice `json:"restore,omitempty"`
	// Handover: laufende Übergabe, für den alten Host.
	Handover *HandoverState `json:"handover,omitempty"`
	// Maintenance: Wartung am Server; der Host sagt sie an und stoppt zu
	// StopAt sauber (Schluss-Stand, Zuschlag zurück), ohne Übergabe.
	Maintenance *Maintenance `json:"maintenance,omitempty"`
	// MainPort: der neue Host soll jetzt auch den Haupt-Port öffnen.
	MainPort bool `json:"main_port,omitempty"`
	// MembersVersion ändert sich, wenn Mitglieder oder Rollen sich ändern
	// (Neuladen, Rollen-Umschalten); der Host holt dann die Host-Einstellungen
	// neu und gleicht Whitelist und Ops des laufenden Servers an. 0: unbekannt.
	MembersVersion int64 `json:"members_version,omitempty"`
}

// MemberInfo ist ein Mitglied in der Mitgliederliste (nur für Admins).
type MemberInfo struct {
	Email         string `json:"email"`
	MinecraftName string `json:"minecraft_name"`
	Admin         bool   `json:"admin"`
	Op            bool   `json:"op"`
	Online        bool   `json:"online"`
}

// RoleRequest setzt Admin- und Op-Recht eines Mitglieds.
// MinecraftNameRequest setzt den Minecraft-Namen eines Mitglieds: das
// Mitglied selbst einmal, solange noch keiner eingetragen ist
// (POST /v1/me/minecraft), ein Admin jederzeit
// (PUT /v1/members/{email}/minecraft).
type MinecraftNameRequest struct {
	MinecraftName string `json:"minecraft_name"`
}

type RoleRequest struct {
	Admin bool `json:"admin"`
	Op    bool `json:"op"`
}

// Bereitschaft eines Starters (alle 5 s, solange er übernehmen darf).
type PresenceRequest struct {
	MachineID string `json:"machine_id"`
	Score     Score  `json:"score"`
	Joined    bool   `json:"joined,omitempty"` // spielt gerade mit (sonst untätiger Dauerbetrieb)
}
type PresenceResponse struct {
	Handover *HandoverOffer `json:"handover,omitempty"`
}

// Angebot an den neuen Host: wie eine Play-Antwort mit Rolle host.
type HandoverOffer struct {
	HandoverID       int64    `json:"handover_id"`
	LeaseID          int64    `json:"lease_id"`
	WorldID          string   `json:"world_id"`
	HeadGeneration   int64    `json:"head_generation"`
	Port             int      `json:"port"` // Ersatz-Port für die übergebenen Spieler
	From             HostInfo `json:"from"`
	MinecraftAddress string   `json:"minecraft_address"`
}

type HandoverStartRequest struct {
	LeaseID   int64  `json:"lease_id"`
	MachineID string `json:"machine_id"`
}

type HandoverStartResponse struct {
	HandoverID int64    `json:"handover_id"`
	To         HostInfo `json:"to"`
}

type HandoverReadyRequest struct {
	HandoverID int64  `json:"handover_id"`
	LeaseID    int64  `json:"lease_id"`
	MachineID  string `json:"machine_id"`
	WorldID    string `json:"world_id"`
	Generation int64  `json:"generation"`
}

// HandoverAbortRequest: Der alte Host bricht seine Übergabe vor dem
// gelungenen Commit ab (POST /v1/handover/abort) und hostet weiter.
type HandoverAbortRequest struct {
	HandoverID int64  `json:"handover_id"`
	LeaseID    int64  `json:"lease_id"`
	MachineID  string `json:"machine_id"`
}

// Werte von HandoverState.State.
const (
	HandoverPreparing = "vorbereitung" // alter Host speichert und lädt hoch
	HandoverStarting  = "start"        // neuer Host startet
	HandoverReady     = "bereit"       // neuer Host bereit: Spieler schicken
	HandoverFailed    = "gescheitert"
)

// HandoverState ist der Stand einer Host-Übergabe.
type HandoverState struct {
	HandoverID int64    `json:"handover_id"`
	State      string   `json:"state"`
	Address    string   `json:"address,omitempty"` // host:port, bei "bereit"
	Reason     string   `json:"reason,omitempty"`
	To         HostInfo `json:"to"`
}

// Beitrittsanfragen (internal/koord/server/join.go). Ein Konto, das tunneld
// kennt, aber nicht in der Mitgliederliste steht, darf nur POST und GET
// /v1/join; alle anderen Endpunkte antworten ihm mit 403.

// Zustände einer Beitrittsanfrage (JoinStatus.State).
const (
	JoinNone     = "keine"      // noch keine Anfrage gestellt
	JoinOpen     = "offen"      // wartet auf einen Admin
	JoinAccepted = "angenommen" // das Konto ist Mitglied
	JoinDeclined = "abgelehnt"  // frühestens nach JoinRetryAfter wieder anfragen
)

// JoinRetryAfter ist die Wartezeit nach einer Ablehnung, bevor das Konto
// erneut anfragen darf.
const JoinRetryAfter = 24 * time.Hour

// MaxOpenJoins ist die Höchstzahl offener Beitrittsanfragen insgesamt.
const MaxOpenJoins = 50

// JoinRequest ist die Beitrittsanfrage eines Nicht-Mitglieds
// (POST /v1/join): sein Minecraft-Name. Antwort ist JoinStatus (201).
// Fehler: 400 ungültiger oder bei Mojang unbekannter Name, 409 der Name
// gehört schon einem Mitglied oder ist angefragt oder das Konto ist schon
// Mitglied, 429 schon eine offene Anfrage, 50 offene insgesamt oder weniger
// als 24 h seit einer Ablehnung, 503 Mojang
// nicht erreichbar.
type JoinRequest struct {
	MinecraftName string `json:"minecraft_name"`
}

// JoinStatus ist der Stand der eigenen Anfrage (GET /v1/join, auch für
// Mitglieder: dann "angenommen"). Since ist der Zeitpunkt der Anfrage
// (offen), der Ablehnung (abgelehnt) bzw. der Annahme (angenommen, falls
// bekannt); bei "keine" nicht gesetzt.
type JoinStatus struct {
	State string    `json:"state"`
	Since time.Time `json:"since,omitzero"`
}

// JoinRequestInfo ist eine offene Beitrittsanfrage (GET /v1/join-requests,
// nur Admins, älteste zuerst). Annehmen und Ablehnen:
// POST /v1/join-requests/{email}/accept bzw. /decline (Antwort 204; 404 keine
// offene Anfrage dieses Kontos, 409 der Name ist inzwischen vergeben).
type JoinRequestInfo struct {
	Email         string    `json:"email"`
	MinecraftName string    `json:"minecraft_name"`
	Requested     time.Time `json:"requested"`
}
