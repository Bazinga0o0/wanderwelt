// Package server ist die HTTP-Schnittstelle des Koordinators.
//
// Jede Anfrage braucht eine Identität aus den Headern von tunneld
// (X-Tunnel-Account-Email, X-Tunnel-Account-Id) und muss von einem Mitglied
// der Gruppe kommen; sonst 401 bzw. 403. Fehler gehen immer als
// {"error":"<Text>"} hinaus.
//
// Mit jedem neuen Zuschlag setzt der Server den Halter des Minecraft-Ports
// auf das Konto des Hosts; bei Rückgabe und Ablauf räumt er ihn.
package server

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/benachrichtigung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/karte"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/lease"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/portholder"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/stats"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/store"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/wartung"
)

// MaxPartSize ist die größte Teilgröße beim Blob-Upload (32 MiB).
const MaxPartSize int64 = 32 << 20

const (
	// maxJSONBody begrenzt kleine JSON-Anfragen.
	maxJSONBody = 1 << 20
	// maxListBody begrenzt Hash-Listen und Commits (Manifeste großer Welten).
	maxListBody = 64 << 20
	// speedTestMax ist die größte Datenmenge der Geschwindigkeitsmessung.
	speedTestMax = 8 << 20
	// uuidTimeout begrenzt das Auflösen aller Minecraft-Namen für die
	// Host-Einstellungen zusammen.
	uuidTimeout = 5 * time.Second
)

// Identitäts-Header von tunneld.
const (
	headerEmail     = "X-Tunnel-Account-Email"
	headerAccountID = "X-Tunnel-Account-Id"
)

// UUIDResolver löst einen Minecraft-Namen in die UUID (mit Bindestrichen) auf.
type UUIDResolver interface {
	UUID(ctx context.Context, name string) (string, error)
}

// SystemClock ist die echte Uhr für lease.Manager.
type SystemClock struct{}

// Now liefert die aktuelle Zeit.
func (SystemClock) Now() time.Time { return time.Now() }

// After feuert nach d.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Server ist der HTTP-Handler des Koordinators.
type Server struct {
	cfg   *config.Config
	lm    *lease.Manager
	st    *store.Store
	ph    portholder.Client
	uuids UUIDResolver
	log   *slog.Logger
	now   func() time.Time
	mux   *http.ServeMux

	// phMu ordnet Port-Halter-Aufrufe und Zuschläge: Set für einen neuen
	// Zuschlag und Clear laufen nur unter phMu, und Clear nur, wenn unter
	// phMu kein gültiger Host besteht. So kann ein spätes Clear nie den
	// Halter eines frischen Zuschlags räumen.
	phMu sync.Mutex
	// needClear: Der Halter muss noch geräumt werden (Ablauf, Rückgabe oder
	// fehlgeschlagenes Set, dessen Wirkung unbekannt ist). Unter phMu. Der
	// Merker steht nicht auf der Platte; nach einem Neustart ist unbekannt,
	// ob ein Räumen ausstand, darum beginnt er mit true.
	needClear bool

	// uuidTimeout: Frist für alle UUID-Auflösungen einer Anfrage zusammen.
	uuidTimeout time.Duration

	// members ist die wirksame Mitgliederliste: die aus der Konfiguration
	// (cfgMembers) mit den Überlagerungen names und roles aus data_dir
	// (members_api.go). Sie kann sich zur Laufzeit ändern (ReloadMembers
	// nach SIGHUP, Rollen und Namen über die API) und wird dann ganz
	// ersetzt, nie an Ort und Stelle verändert; alles andere aus cfg bleibt
	// fest.
	membersMu sync.RWMutex
	members   []config.Member

	// overlayMu ordnet Änderungen an den Überlagerungen in data_dir und
	// das Neuladen der Mitglieder. Darunter: cfgMembers, roles, names,
	// corrupt.
	overlayMu   sync.Mutex
	confMembers []config.Member // nur die aus der Konfiguration
	cfgMembers  []config.Member // dazu die angenommenen (joined); Grundlage für roles und names
	joined      config.JoinedMembers
	joins       config.JoinEntries
	roles       config.Roles
	names       config.Names
	// lists: Gäste und Banns des Reiters „Server“ (serverlisten.go).
	lists config.ServerLists
	// corrupt: Überlagerungen (Pfad), die beim letzten Lesen kaputt waren.
	// Sie gelten nicht, bleiben aber liegen, bis die nächste Änderung sie
	// beiseitelegt (Kopie .kaputt-<Zeit>).
	corrupt map[string]bool

	// manifest ist das ausgelieferte Versions-Manifest (manifest.go): beim
	// Start oder letzten Neuladen gelesen und geprüft; nil, solange keins
	// gelesen ist. Wird ganz ersetzt, nie verändert. Unter manifestMu, das
	// nur für das Lesen und Setzen des Zeigers gehalten wird. reloadMu reiht
	// Lesen und Prüfen (Hashen) neuer Stände; Anfragen warten nie darauf,
	// außer es gibt noch gar kein Manifest.
	manifestMu sync.RWMutex
	manifest   *api.Manifest
	reloadMu   sync.Mutex
	// manifestCheckHook läuft zu Beginn jeder Prüfung (nur Tests).
	manifestCheckHook func()

	// membersVersion ändert sich mit jeder Änderung an Mitgliedern, Rollen
	// oder Minecraft-Namen (api.HeartbeatResponse.MembersVersion); beim
	// Start die Startzeit in Unix-Nanosekunden.
	membersVersion atomic.Int64
	// openJoins: Zahl der offenen Beitrittsanfragen (join.go; für Admins in
	// api.Me.PendingJoins).
	openJoins atomic.Int64
	// budget ist das wirksame Speicherbudget (settings.json oder
	// storage_budget).
	budget atomic.Int64
	// diskFree meldet die freien Bytes auf dem Dateisystem von data_dir
	// (Tests setzen eine eigene).
	diskFree func(dir string) (int64, bool)
	// bg: Hintergrundarbeit, etwa das Aufräumen nach einem neuen Budget.
	bg sync.WaitGroup

	profMu sync.Mutex // profiles.json

	// pending ist das ausstehende Zurückholen (restore.go). Gesetzt und
	// gelöscht wird es nur unter phMu, zusammen mit den Entscheidungen über
	// den Zuschlag; Lebenszeichen und Status lesen es ohne Sperre.
	pending atomic.Pointer[pendingRestore]

	// wartung ist der Wartungszustand (wartung.go): nil ohne Wartung. Er
	// wird ganz ersetzt, nur unter phMu (oder im Aufbau); Lebenszeichen und
	// Status lesen ihn ohne Sperre. wartungErr: der zuletzt geloggte Fehler
	// beim Lesen (unter phMu).
	wartung    atomic.Pointer[wartung.Zustand]
	wartungErr string
	// resume ist das Fortsetzen-Fenster nach einer Wartung (wartung.go): nil
	// ohne. Ersetzt nur unter phMu (oder im Aufbau); Status und Spielen
	// lesen es ohne Sperre.
	resume atomic.Pointer[wartung.Fortsetzen]

	// Host-Übergabe (handover.go). ho, repl, hoDirty und hoSeq ändern sich
	// nur unter phMu; ho und repl lesen Lebenszeichen und Status ohne
	// Sperre. commitMu ordnet Commit und die Meldung ready der Übergabe
	// (Reihenfolge: phMu vor commitMu). presMu schützt presence.
	ho       atomic.Pointer[handover]
	repl     atomic.Pointer[replPort]
	hoDirty  map[int]bool
	hoSeq    int64
	commitMu sync.Mutex
	presMu   sync.Mutex
	presence map[string]presenceEntry

	// ids bildet die pseudonymen Mitglieds-Kennungen (datenschutz.go).
	ids *kennung.Schluessel

	// stats zählt Spiel- und Hostzeit je Mitglied (stats.go).
	stats *stats.Tracker
	// health: Laufzeit-Merker der Gesundheit (health.go).
	health healthState
	// benach: Benachrichtigungen an Discord (nil: kein Webhook eingetragen).
	// Gesetzt nur in New (und in Tests).
	benach *benachrichtigung.Dienst
	// karte: Karten-Dienst (karte.go); nil: nicht eingerichtet. Gesetzt nur
	// über UseKarte (vor dem Betrieb).
	karte *karte.Dienst

	// jobs: Aufträge an den Host aus dem Reiter „Server“ (auftraege.go);
	// jobPoll: so lange hält ein Abruf des Hosts (Tests verkürzen ihn).
	jobs    *jobQueue
	jobPoll time.Duration
}

// New baut den Server. Im Dev-Modus warnt er, weil dann jeder, der den
// Koordinator erreicht, jede Identität behaupten kann.
func New(cfg *config.Config, lm *lease.Manager, st *store.Store, ph portholder.Client, uuids UUIDResolver) *Server {
	s := &Server{
		cfg:   cfg,
		lm:    lm,
		st:    st,
		ph:    ph,
		uuids: uuids,
		log:   slog.Default(),
		now:   time.Now,
		mux:   http.NewServeMux(),

		needClear:   true,
		uuidTimeout: uuidTimeout,
		confMembers: slices.Clone(cfg.Members),
		cfgMembers:  slices.Clone(cfg.Members),
		joined:      config.JoinedMembers{},
		joins:       config.JoinEntries{},
		corrupt:     map[string]bool{},
		diskFree:    diskFree,
		hoDirty:     map[int]bool{},
		presence:    map[string]presenceEntry{},
		hoSeq:       time.Now().UnixNano(),
		jobs:        newJobQueue(),
		jobPoll:     jobPoll,
	}
	// Nach dem Start ist unbekannt, was auf den Ersatz-Ports liegt: der
	// erste Takt räumt sie.
	for _, p := range cfg.PortHolder.HandoverPorts {
		s.hoDirty[p] = true
	}
	if cfg.Auth.Mode == "dev" {
		s.log.Warn("auth.mode dev: Identität kommt ungeprüft aus X-Tunnel-Account-Email – nur lokal benutzen")
	}
	s.membersVersion.Store(time.Now().UnixNano())
	s.loadIDKey()
	s.loadOverlays()
	s.loadHo()
	s.openStats()
	s.initHealth()
	if cfg.DiscordWebhook.Gesetzt() {
		s.benach = benachrichtigung.Neu(benachrichtigung.Optionen{
			Webhook: cfg.DiscordWebhook, Gruppe: cfg.Gruppe.Titel, Arten: cfg.DiscordArten, Log: s.log,
		})
		s.log.Info("Discord-Benachrichtigungen sind an") // nie die Adresse
	}

	s.mux.HandleFunc("GET /v1/me", s.handleMe)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /v1/manifest", s.handleManifest)
	s.mux.HandleFunc("GET /v1/artifacts/{name}", s.handleArtifact)
	s.mux.HandleFunc("POST /v1/play", s.handlePlay)
	s.mux.HandleFunc("POST /v1/lease/heartbeat", s.handleHeartbeat)
	s.mux.HandleFunc("POST /v1/lease/release", s.handleRelease)
	s.mux.HandleFunc("POST /v1/presence", s.handlePresence)
	s.mux.HandleFunc("POST /v1/handover/start", s.handleHandoverStart)
	s.mux.HandleFunc("POST /v1/handover/ready", s.handleHandoverReady)
	s.mux.HandleFunc("POST /v1/handover/abort", s.handleHandoverAbort)
	s.mux.HandleFunc("GET /v1/host/settings", s.handleHostSettings)
	s.mux.HandleFunc("POST /v1/blobs/missing", s.handleMissing)
	s.mux.HandleFunc("POST /v1/blobs/fetch", s.handleFetchBlobs)
	s.mux.HandleFunc("POST /v1/blobs/batch", s.handleBatchBlobs)
	s.mux.HandleFunc("PUT /v1/blobs/{sha}", s.handlePutBlob)
	s.mux.HandleFunc("GET /v1/blobs/{sha}", s.handleGetBlob)
	s.mux.HandleFunc("GET /v1/generations", s.handleGenerations)
	s.mux.HandleFunc("POST /v1/restore", s.handleRestore)
	s.mux.HandleFunc("GET /v1/generations/head", s.handleHead)
	s.mux.HandleFunc("GET /v1/generations/{world}/{n}", s.handleGeneration)
	s.mux.HandleFunc("POST /v1/generations", s.handleCommit)
	s.mux.HandleFunc("PUT /v1/profile/{machine}", s.handleProfile)
	s.mux.HandleFunc("GET /v1/stats", s.handleStats)
	s.mux.HandleFunc("GET /v1/members", s.handleMembers)
	s.mux.HandleFunc("PUT /v1/members/{email}", s.handleSetRole)
	s.mux.HandleFunc("PUT /v1/members/{email}/minecraft", s.handleSetMemberMinecraft)
	s.mux.HandleFunc("POST /v1/me/minecraft", s.handleSetMyMinecraft)
	s.mux.HandleFunc("PUT /v1/storage/budget", s.handleSetBudget)
	s.mux.HandleFunc("POST /v1/speedtest", s.handleSpeedTest)
	s.mux.HandleFunc("GET /v1/ping", s.handlePing)
	s.mux.HandleFunc("POST /v1/join", s.handleJoin)
	s.mux.HandleFunc("GET /v1/join", s.handleJoinStatus)
	s.mux.HandleFunc("GET /v1/info", s.handleInfo)
	s.mux.HandleFunc("GET /v1/join-requests", s.handleJoinRequests)
	s.mux.HandleFunc("POST /v1/join-requests/{email}/accept", s.handleJoinAccept)
	s.mux.HandleFunc("POST /v1/join-requests/{email}/decline", s.handleJoinDecline)
	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/karte/{welt}/manifest.json", s.handleKarteManifest)
	s.mux.HandleFunc("GET /v1/karte/{welt}/t/{z}/{x}/{y}", s.handleKarteKachel)
	s.mux.HandleFunc("GET /v1/worlds", s.handleWorlds)
	s.mux.HandleFunc("POST /v1/worlds", s.handleWorldCreate)
	s.mux.HandleFunc("PATCH /v1/worlds/{id}", s.handleWorldPatch)
	s.mux.HandleFunc("DELETE /v1/worlds/{id}", s.handleWorldDelete)
	s.mux.HandleFunc("GET /v1/server/lists", s.handleServerLists)
	s.mux.HandleFunc("POST /v1/server/lists", s.handleServerListChange)
	s.mux.HandleFunc("GET /v1/server/gamerules", s.handleGamerules)
	s.mux.HandleFunc("POST /v1/server/gamerules", s.handleGameruleSet)
	s.mux.HandleFunc("POST /v1/server/command", s.handleServerCommand)
	s.mux.HandleFunc("POST /v1/host/jobs", s.handleHostJobs)
	s.mux.HandleFunc("POST /v1/host/jobs/result", s.handleHostJobResult)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unbekannter Endpunkt "+r.Method+" "+r.URL.Path)
	})
	// Eine laufende Wartung gilt gleich nach dem Start (sie steht in data_dir).
	s.readWartungLocked()
	s.loadResumeLocked()
	return s
}

// ServeHTTP prüft Identität und Mitgliedschaft und reicht dann weiter.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, code, msg := s.identify(r)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if id.guest && !guestAllowed(r) {
		writeError(w, http.StatusForbidden, notMemberMsg)
		return
	}
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
}

// ExpireTick räumt einen abgelaufenen Zuschlag ab und den Port-Halter,
// sofern danach kein gültiger Host besteht. Ein fehlgeschlagenes Räumen
// wird beim nächsten Takt wiederholt. Wartet ein Zurückholen auf den
// abgelaufenen Zuschlag, wird es danach angewandt.
func (s *Server) ExpireTick(ctx context.Context) {
	s.stats.Tick(s.now())
	s.expireTick(ctx)
	// Nach dem Takt (ohne phMu): Veränderungen an Discord melden.
	if s.benach != nil {
		s.benach.Beobachte(s.statusNow(), s.now())
	}
}

// RunBenachrichtigung stellt Discord-Nachrichten zu, bis ctx endet (blockiert;
// in eigener Goroutine aufrufen). Ohne Webhook kehrt es sofort zurück.
func (s *Server) RunBenachrichtigung(ctx context.Context) {
	if s.benach != nil {
		s.benach.Lauf(ctx)
	}
}

func (s *Server) expireTick(ctx context.Context) {
	s.phMu.Lock()
	defer s.phMu.Unlock()
	s.wartungTickLocked(ctx)
	if expired, account := s.lm.Expire(); expired {
		s.log.Info("Zuschlag abgelaufen", "konto", account)
		s.needClear = true
	}
	s.handoverStepLocked(ctx)
	if s.needClear {
		s.clearLocked(ctx)
	}
	s.settleRestoreLocked(ctx)
	// Aufträge eines Zuschlags, der nicht mehr (bereit) hostet, verfallen.
	s.jobs.dropExcept(s.currentLease())
}

// PruneTick wendet Aufbewahrung und Aufräumlauf des Speichers an.
func (s *Server) PruneTick(context.Context) {
	start := s.now()
	if s.wartung.Load() != nil {
		// Nach einem Update nur als Probe, bis „wartung ende“ (wartung.go).
		rep, err := s.st.PruneProbe(start)
		s.notePrune(pruneResult(start, true, rep, err))
		switch {
		case err != nil:
			s.log.Error("AUFRÄUM-PROBE FEHLGESCHLAGEN", "err", err)
		case rep.Empty():
			s.log.Info("Aufräum-Probe während der Wartung: nichts zu löschen")
		default:
			s.log.Warn("AUFRÄUM-PROBE WÄHREND DER WARTUNG – gelöscht wird erst nach „wartung ende“",
				"stände", rep.Generations, "blobs", rep.Blobs, "bytes", rep.Bytes, "temp", rep.Junk)
		}
		return
	}
	err := s.st.Prune(start)
	s.notePrune(pruneResult(start, false, store.PruneReport{}, err))
	if err != nil {
		s.log.Error("AUFRÄUMEN DES SPEICHERS FEHLGESCHLAGEN", "err", err)
		return
	}
	s.log.Info("Speicher aufgeräumt", "dauer", s.now().Sub(start).Round(time.Millisecond))
}

// --- Identität ---

type identity struct {
	member    config.Member // bei einem Gast nur die E-Mail
	accountID int64
	// guest: authentifiziertes Konto, das nicht zur Gruppe gehört. Es darf
	// nur die Beitritts-Endpunkte (guestAllowed); alles andere bleibt 403.
	guest bool
}

const notMemberMsg = "dieses Konto gehört nicht zur Gruppe"

// guestAllowed: Endpunkte, die auch Nicht-Mitglieder benutzen dürfen
// (nur diese, genau dieser Pfad).
func guestAllowed(r *http.Request) bool {
	if r.URL.Path == "/v1/info" {
		return r.Method == http.MethodGet
	}
	return r.URL.Path == "/v1/join" && (r.Method == http.MethodGet || r.Method == http.MethodPost)
}

type identityKey struct{}

func identityOf(r *http.Request) identity {
	id, _ := r.Context().Value(identityKey{}).(identity)
	return id
}

// identify liefert die Identität der Anfrage oder einen Fehlerstatus.
// Außer im Dev-Modus zählen die Header nur, wenn die Anfrage über Loopback
// kommt (von tunneld), und die Konto-ID ist Pflicht (für den Port-Halter).
func (s *Server) identify(r *http.Request) (identity, int, string) {
	dev := s.cfg.Auth.Mode == "dev"
	if !dev && !fromLoopback(r) {
		return identity{}, http.StatusUnauthorized, "Anfrage kommt nicht über tunneld"
	}
	emails := r.Header.Values(headerEmail)
	if len(emails) != 1 || strings.TrimSpace(emails[0]) == "" {
		return identity{}, http.StatusUnauthorized, "keine eindeutige Identität (" + headerEmail + ")"
	}
	var accountID int64
	switch ids := r.Header.Values(headerAccountID); {
	case len(ids) > 1:
		return identity{}, http.StatusUnauthorized, "mehrere " + headerAccountID
	case len(ids) == 1:
		n, ok := parseDigits(strings.TrimSpace(ids[0]))
		if !ok || n <= 0 {
			return identity{}, http.StatusUnauthorized, "ungültige " + headerAccountID
		}
		accountID = n
	case !dev:
		return identity{}, http.StatusUnauthorized, headerAccountID + " fehlt"
	}
	m, ok := s.member(strings.TrimSpace(emails[0]))
	if !ok {
		return identity{member: config.Member{Email: strings.TrimSpace(emails[0])}, accountID: accountID, guest: true}, 0, ""
	}
	return identity{member: m, accountID: accountID}, 0, ""
}

func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// --- Mitgliederliste ---

// memberList liefert die aktuelle Mitgliederliste (nur lesen).
func (s *Server) memberList() []config.Member {
	s.membersMu.RLock()
	defer s.membersMu.RUnlock()
	return s.members
}

// member sucht ein Mitglied über die E-Mail; Groß/Klein spielt keine Rolle
// (wie config.Config.Member).
func (s *Server) member(email string) (config.Member, bool) {
	want := strings.ToLower(email)
	for _, m := range s.memberList() {
		if strings.ToLower(m.Email) == want {
			return m, true
		}
	}
	return config.Member{}, false
}

// ReloadMembers liest die Konfigurationsdatei path neu, prüft sie wie beim
// Start und übernimmt daraus nur die Mitgliederliste; alle anderen
// Einstellungen gelten erst nach einem Neustart. Ist die Datei ungültig,
// bleibt die bisherige Liste (Fehler im Log und als Ergebnis). Die
// Überlagerungen für Rollen und Namen liest es ebenfalls neu (ein von Hand
// berichtigtes roles.json gilt so ohne Neustart), und sie gelten weiter;
// Einträge für Mitglieder, die nicht mehr in der Liste stehen, fallen weg –
// wer später wieder aufgenommen wird, beginnt mit den Werten aus der
// Konfiguration. Danach löst es die Minecraft-Namen auf wie beim Start;
// Whitelist und Ops entstehen aus der neuen Liste (MembersVersion).
func (s *Server) ReloadMembers(ctx context.Context, path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		s.log.Error("MITGLIEDER NICHT NEU GELADEN – die bisherige Liste gilt weiter", "datei", path, "err", err)
		return err
	}
	s.overlayMu.Lock()
	old := s.memberList()
	s.confMembers = slices.Clone(cfg.Members)
	s.readMemberOverlaysLocked()
	s.dropStaleOverlaysLocked()
	s.keepAdminLocked()
	now := s.applyOverlaysLocked()
	s.overlayMu.Unlock()
	s.membersVersion.Add(1)

	s.log.Info("Mitglieder neu geladen (übrige Einstellungen erst nach einem Neustart)",
		"anzahl", len(now), "neu", memberDiff(now, old), "entfernt", memberDiff(old, now))
	s.WarmUUIDs(ctx)
	return nil
}

// memberDiff liefert die E-Mails aus a, die in b fehlen.
func memberDiff(a, b []config.Member) []string {
	var out []string
	for _, m := range a {
		if !slices.ContainsFunc(b, func(o config.Member) bool { return strings.EqualFold(o.Email, m.Email) }) {
			out = append(out, m.Email)
		}
	}
	return out
}

// WarmUUIDs löst alle Mitglieder einmal auf: füllt den Zwischenspeicher und
// zeigt Tippfehler in Minecraft-Namen gleich im Log (beim Start und nach
// dem Neuladen).
func (s *Server) WarmUUIDs(ctx context.Context) {
	for _, m := range s.memberList() {
		if m.Minecraft == "" {
			continue // meldet seinen Namen noch selbst
		}
		if _, err := s.uuids.UUID(ctx, m.Minecraft); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.log.Error("UUID nicht auflösbar, Mitglied fehlt in der Whitelist", "minecraft", m.Minecraft, "email", m.Email, "err", err)
		}
	}
}

// --- Allgemeines ---

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meOf(identityOf(r).member))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.statusFor(identityOf(r).member.Email))
}

// statusNow ist der Status für die Benachrichtigungen (Server intern, ohne
// Anfragenden: keine E-Mail-Adresse).
func (s *Server) statusNow() api.Status { return s.statusFor("") }

// statusFor ist der Status für Starter und Fenster; die Host-Adresse steht
// nur drin, wenn viewer (E-Mail des Anfragenden) selbst der Host ist.
func (s *Server) statusFor(viewer string) api.Status {
	running := s.runningWorld()
	worldID, head, created := s.st.HeadInfoOf(cmp.Or(running, s.normWorld("")))
	st := api.Status{
		Players:           s.lm.Players(),
		HeadGeneration:    head,
		WorldID:           worldID,
		MinecraftAddress:  s.cfg.MinecraftAddress,
		HeadCreated:       created,
		SnapshotIntervalS: int(s.cfg.SnapshotInterval / time.Second),
		RestorePending:    s.restoreNotice(),
		Handover:          s.handoverState(s.ho.Load(), false, viewer),
		Maintenance:       s.wartungNotice(),
		Resume:            s.resumeNotice(),
	}
	st.Members = len(s.memberList())
	st.NoDiscordJoin = s.cfg.NoDiscordJoin
	st.Worlds, st.RunningWorld = s.worldList(running), running
	if info, ok := s.lm.Info(); ok {
		st.Host = s.hostFor(info.Host, viewer, false)
		// Ein Zuschlag nur zum Hochladen hat keinen Server: nie bereit.
		st.HostReady = info.Ready && !info.UploadOnly
		st.HostUploading = info.UploadOnly
	}
	st.DiscordChannel = s.benach != nil
	return st
}

// handleManifest liefert das beim Start oder letzten Neuladen gelesene und
// geprüfte Manifest (manifest.go).
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	m, herr := s.currentManifest()
	if herr != nil {
		writeError(w, herr.code, herr.msg)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// ArtifactDir ist der Ordner, aus dem der Koordinator Artefakte selbst
// ausliefert (Manifest-URL wanderwelt:<name>): DataDir/artifacts.
func ArtifactDir(dataDir string) string { return filepath.Join(dataDir, "artifacts") }

// ArtifactScheme ist das URL-Schema im Manifest für Artefakte vom
// Koordinator: "wanderwelt:<name>".
const ArtifactScheme = "wanderwelt"

// ValidArtifactName: 1–128 Zeichen aus Buchstaben, Ziffern, ".", "_" und
// "-", nicht "." oder ".." – ein einfacher Dateiname, kein Pfad.
func ValidArtifactName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 128 {
		return false
	}
	for i := 0; i < len(n); i++ {
		c := n[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// handleArtifact liefert eine Datei aus DataDir/artifacts, die der Admin
// dort abgelegt hat (Manifest-URL wanderwelt:<name>). Nur einfache Namen,
// nur reguläre Dateien, kein Verzeichnislisting; mit Content-Length und
// Range. Den Hash prüft der Starter gegen das Manifest.
func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !ValidArtifactName(name) {
		writeError(w, http.StatusBadRequest, "ungültiger Artefakt-Name")
		return
	}
	f, err := os.Open(filepath.Join(ArtifactDir(s.cfg.DataDir), name))
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, http.StatusNotFound, "Artefakt "+name+" liegt nicht auf dem Koordinator")
		return
	}
	if err != nil {
		s.internal(w, "Artefakt", err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.internal(w, "Artefakt", err)
		return
	}
	if !fi.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "Artefakt "+name+" liegt nicht auf dem Koordinator")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// CheckArtifacts prüft die Manifest-Einträge mit URL wanderwelt:<name>: Die
// Datei muss in ArtifactDir(dataDir) liegen und den Hash aus dem Manifest
// haben. Ergebnis sind die Befunde für das Log (leer: alles in Ordnung).
func CheckArtifacts(dataDir string, m *api.Manifest) []string {
	var out []string
	all := append([]api.Artifact{m.Java, m.FabricServer, m.Tunnel}, m.Mods...)
	for _, c := range m.Configs {
		all = append(all, api.Artifact{Name: c.Path, URL: c.URL, Hash: c.Hash})
	}
	for _, a := range all {
		scheme, name, ok := strings.Cut(a.URL, ":")
		if !ok || !strings.EqualFold(scheme, ArtifactScheme) {
			continue
		}
		if !ValidArtifactName(name) {
			out = append(out, fmt.Sprintf("%s: ungültiger Artefakt-Name in %q", a.Name, a.URL))
			continue
		}
		path := filepath.Join(ArtifactDir(dataDir), name)
		got, err := fileHash(path, a.Hash)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("%s: %v", a.Name, err))
		case !strings.EqualFold(got, strings.TrimSpace(a.Hash)):
			out = append(out, fmt.Sprintf("%s: %s hat %s, im Manifest steht %q", a.Name, path, got, a.Hash))
		}
	}
	return out
}

// fileHash bildet den Hash von path im Verfahren aus want ("sha256:…" oder
// "sha512:…"), in derselben Form.
func fileHash(path, want string) (string, error) {
	algo, _, _ := strings.Cut(strings.TrimSpace(want), ":")
	var h hash.Hash
	switch strings.ToLower(algo) {
	case "sha256":
		h = sha256.New()
	case "sha512":
		h = sha512.New()
	default:
		return "", fmt.Errorf("Hash %q: erwartet \"sha256:<hex>\" oder \"sha512:<hex>\"", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToLower(algo) + ":" + hex.EncodeToString(h.Sum(nil)), nil
}

// --- Zuschlag ---

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.PlayRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	if !validMachineID(req.MachineID) {
		writeError(w, http.StatusBadRequest, "ungültige machine_id")
		return
	}
	world, reason := s.playWorld(req.GroupWorld)
	cand := lease.Candidate{
		Email:         id.member.Email,
		MinecraftName: id.member.Minecraft,
		MachineID:     req.MachineID,
		AccountID:     id.accountID,
		Score:         req.Score,
		World:         world,
	}
	// Auch vor einem Zuschlag nur zum Hochladen: Ein schnell neu gestarteter
	// Starter reicht den Stand seiner abgestürzten Sitzung nach und wartete
	// sonst bis zum Ablauf seines eigenen toten Zuschlags (90 s).
	s.releaseStale(r.Context(), cand)
	// Ist der Haupt-Zuschlag einer Übergabe zu Ende, wird der wartende
	// befördert, bevor jemand einen neuen Zuschlag will.
	s.handoverSettle(context.WithoutCancel(r.Context()))
	// Während der Wartung startet niemand und spielt niemand mit; der
	// Grund steht im Hinweis des Starters (wartung.go).
	if reason := s.wartungReason(); reason != "" {
		s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied}, reason, world)
		return
	}
	// Das automatische Fortsetzen nach der Wartung gibt es nur im offenen
	// Fenster (wartung.go); ein gewöhnliches „Spielen“ ist nie betroffen.
	if req.Resume && !s.resumeOpen() {
		s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied}, api.ReasonResumeClosed, world)
		return
	}
	// Während ein Zurückholen aussteht, startet niemand und spielt niemand
	// mit: Der Host stoppt gleich, danach gibt es einen neuen Head.
	if reason := s.restoreBlocksPlay(context.WithoutCancel(r.Context())); reason != "" {
		s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied}, reason, world)
		return
	}
	// Die gewünschte Welt gibt es nicht (mehr).
	if reason != "" {
		s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied}, reason, "")
		return
	}
	if req.UploadOnly {
		s.playUploadOnly(w, r, req, cand)
		return
	}
	// Eine leere Welt darf nur ein Admin zum ersten Mal starten (nur er
	// darf den ersten Stand einreichen): Mitspielen bei laufendem Host
	// geht, einen Host für eine leere Welt wählen lassen nicht.
	if e, ok := s.st.GroupWorld(world); ok && e.Empty() && !id.member.Admin {
		if _, _, hosting := s.lm.Current(); !hosting {
			s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied},
				"Die Welt „"+e.Name+"“ ist noch leer – ein Admin muss sie zuerst starten (oder eine Welt übernehmen).", world)
			return
		}
	}

	g, err := s.lm.Request(r.Context(), cand)
	if err != nil {
		if r.Context().Err() != nil {
			return // Anfrage abgebrochen; niemand liest die Antwort
		}
		s.internal(w, "Zuschlag", err)
		return
	}
	if g.Role == api.RoleHost {
		s.phMu.Lock()
		err := s.setHolderLocked(r.Context(), g, req.MachineID)
		s.phMu.Unlock()
		if err != nil {
			writeHolderError(w, err)
			return
		}
	}
	s.writePlay(w, cand.Email, g, g.Reason, world)
}

// releaseStale gibt den Zuschlag frei, den derselbe PC (Konto und
// MachineID) noch hält, wenn er wieder spielen oder einen Zuschlag nur zum
// Hochladen (Nachreichen, Welt übernehmen) will: Der Starter dort wurde neu
// gestartet, die alte Sitzung (oder ein Nachreichen) lebt nicht mehr – eine
// laufende Sitzung fragt nie erneut, und solange sie läuft, belegt sie die
// Welt, sodass derselbe Starter weder nachreicht noch übernimmt. Ohne das
// bekäme er bis zum Ablauf „join“ bzw. „denied“ zu seinem eigenen toten
// Zuschlag. Zuschläge anderer PCs bleiben unberührt. Der Port-Halter wird
// wie bei jeder Rückgabe geräumt; ein neuer Zuschlag setzt ihn neu.
func (s *Server) releaseStale(ctx context.Context, c lease.Candidate) {
	s.phMu.Lock()
	defer s.phMu.Unlock()
	// Ein wartender Zuschlag dieses PCs gehört ebenfalls einer toten Sitzung:
	// Die Übergabe scheitert sofort statt nach dem Ablauf.
	if in, ok := s.lm.Incoming(); ok && in.Host.MachineID == c.MachineID && strings.EqualFold(in.Host.Email, c.Email) {
		if err := s.lm.Release(in.LeaseID, c.MachineID); err == nil {
			s.log.Info("wartender Zuschlag desselben PCs freigegeben (Starter neu gestartet)", "lease", in.LeaseID)
			s.handoverStepLocked(context.WithoutCancel(ctx))
		}
	}
	info, ok := s.lm.Info()
	if !ok || info.Host.MachineID != c.MachineID || !strings.EqualFold(info.Host.Email, c.Email) {
		return
	}
	if err := s.lm.Release(info.LeaseID, c.MachineID); err != nil {
		if !errors.Is(err, lease.ErrLeaseLost) {
			s.log.Error("alten Zuschlag desselben PCs nicht freigegeben", "lease", info.LeaseID, "err", err)
		}
		return
	}
	s.log.Info("alter Zuschlag desselben PCs freigegeben (Starter neu gestartet)",
		"lease", info.LeaseID, "host", info.Host.Email, "machine", c.MachineID, "nur_hochladen", info.UploadOnly)
	s.needClear = true
	s.clearLocked(context.WithoutCancel(ctx))
}

// playUploadOnly vergibt einen Zuschlag nur zum Nachreichen oder für den
// Welt-Import: ohne Sammelfenster, nur ohne Host und nur, wenn die Basis
// der Head ist (ohne Welt: beides leer bzw. 0).
func (s *Server) playUploadOnly(w http.ResponseWriter, r *http.Request, req api.PlayRequest, cand lease.Candidate) {
	g, reason, err := func() (lease.Grant, string, error) {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		if host, _, ok := s.lm.Current(); ok {
			return lease.Grant{}, host.MinecraftName + " hostet gerade", nil
		}
		if reason, err := s.baseMismatch(req, cand.World); reason != "" || err != nil {
			return lease.Grant{}, reason, err
		}
		g, err := s.lm.Acquire(cand)
		if errors.Is(err, lease.ErrBusy) {
			return lease.Grant{}, "gerade wird ein Host gewählt oder jemand hostet", nil
		}
		if err != nil {
			return lease.Grant{}, "", err
		}
		// Mit dem Zuschlag kann sonst niemand einreichen; jetzt steht der
		// Head fest. Hat sich zwischen Prüfung und Zuschlag etwas getan,
		// gilt die Basis nicht mehr.
		if reason, err := s.baseMismatch(req, cand.World); reason != "" || err != nil {
			s.releaseGrant(g, req.MachineID)
			return lease.Grant{}, reason, err
		}
		if err := s.setHolderLocked(r.Context(), g, req.MachineID); err != nil {
			return lease.Grant{}, "", err
		}
		return g, "", nil
	}()
	switch {
	case isHolderError(err):
		writeHolderError(w, err)
	case err != nil:
		s.internal(w, "Zuschlag zum Nachreichen", err)
	case reason != "":
		s.writePlay(w, cand.Email, lease.Grant{Role: api.RoleDenied}, reason, cand.World)
	default:
		s.writePlay(w, cand.Email, g, "", cand.World)
	}
}

// holderError: Der Port-Halter ließ sich für einen frischen Zuschlag nicht
// setzen; code ist der Status für den Starter.
type holderError struct {
	code int
	err  error
}

func (e *holderError) Error() string { return e.err.Error() }
func (e *holderError) Unwrap() error { return e.err }

func isHolderError(err error) bool {
	_, ok := errors.AsType[*holderError](err)
	return ok
}

func writeHolderError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	if he, ok := errors.AsType[*holderError](err); ok {
		code = he.code
	}
	writeError(w, code, err.Error())
}

// baseMismatch meldet, warum die Basis von req nicht der Head ist ("" =
// passt).
func (s *Server) baseMismatch(req api.PlayRequest, world string) (string, error) {
	worldID, head := s.st.HeadRefOf(world)
	if req.BaseWorldID != worldID || req.BaseGeneration != head {
		return fmt.Sprintf("seitdem hat sich der Stand geändert (Basis %q/%d, aktuell %q/%d)",
			req.BaseWorldID, req.BaseGeneration, worldID, head), nil
	}
	return "", nil
}

// setHolderLocked setzt den Port-Halter für den frischen Zuschlag g. Geht
// das nicht, wird der Zuschlag sofort zurückgenommen; der Fehler ist dann
// ein *holderError. Aufruf unter phMu.
func (s *Server) setHolderLocked(ctx context.Context, g lease.Grant, machineID string) error {
	if _, cur, ok := s.lm.Current(); !ok || cur != g.LeaseID {
		return &holderError{http.StatusServiceUnavailable, errors.New("Zuschlag ist schon wieder verfallen, bitte gleich noch einmal versuchen")}
	}
	if err := s.ph.Set(ctx, s.cfg.PortHolder.Port, g.HostAccountID); err != nil {
		s.log.Error("Port-Halter nicht gesetzt, Zuschlag zurückgenommen",
			"lease", g.LeaseID, "konto", g.HostAccountID, "err", err)
		s.releaseGrant(g, machineID)
		// Ob tunneld den Halter trotzdem umgesetzt hat, ist offen.
		s.needClear = true
		if errors.Is(err, portholder.ErrUnknownAccount) {
			return &holderError{http.StatusForbidden, fmt.Errorf(
				"tunneld kennt dein tunnel-Konto nicht oder hat es noch nicht freigeschaltet – bitte den Admin, es freizuschalten (%w)", err)}
		}
		return &holderError{http.StatusServiceUnavailable, fmt.Errorf("Port-Halter nicht erreichbar, bitte gleich noch einmal versuchen: %w", err)}
	}
	s.needClear = false
	s.log.Info("Zuschlag erteilt", "lease", g.LeaseID, "host", g.Host.Email, "machine", g.Host.MachineID)
	return nil
}

func (s *Server) releaseGrant(g lease.Grant, machineID string) {
	if err := s.lm.Release(g.LeaseID, machineID); err != nil && !errors.Is(err, lease.ErrLeaseLost) {
		s.log.Error("Zuschlag nicht zurückgenommen", "lease", g.LeaseID, "err", err)
	}
}

// clearLocked räumt den Port-Halter, wenn es keinen gültigen Host gibt.
// Gibt es einen, hat (oder wird) dessen Set den Halter ersetzt. Aufruf
// unter phMu.
func (s *Server) clearLocked(ctx context.Context) {
	if _, _, ok := s.lm.Current(); ok {
		s.needClear = false
		return
	}
	if err := s.ph.Clear(ctx, s.cfg.PortHolder.Port); err != nil {
		s.needClear = true
		s.log.Error("Port-Halter nicht geräumt, neuer Versuch beim nächsten Takt", "err", err)
		return
	}
	s.needClear = false
}

// writePlay antwortet auf eine Spielanfrage. world ist die gewünschte Welt
// (aufgelöste Kennung, "" ohne Weltliste); Head und Welt der Antwort sind
// die des Zuschlags, bei „denied“ wegen einer anderen laufenden Welt die der
// laufenden.
func (s *Server) writePlay(w http.ResponseWriter, viewer string, g lease.Grant, reason, world string) {
	if g.Reason == lease.ReasonOtherWorld {
		running := s.normWorld(g.World)
		name := "eine andere Welt"
		if e, ok := s.st.GroupWorld(running); ok {
			name = "„" + e.Name + "“"
		}
		reason = "Gerade läuft " + name + " – es läuft immer nur eine Welt. Wähle diese Welt oder warte, bis das Hosten beendet ist."
		world = running
	} else if g.Role == api.RoleHost || g.Role == api.RoleJoin {
		world = cmp.Or(g.World, world)
	}
	worldID, head := s.st.HeadRefOf(world)
	resp := api.PlayResponse{
		Role:             g.Role,
		HeadGeneration:   head,
		WorldID:          worldID,
		MinecraftAddress: s.cfg.MinecraftAddress,
		Reason:           reason,
	}
	if e, ok := s.st.GroupWorld(world); ok {
		resp.GroupWorld, resp.GroupWorldName, resp.Empty = e.ID, e.Name, e.Empty()
	}
	switch g.Role {
	case api.RoleHost:
		resp.LeaseID = g.LeaseID
		resp.Host = s.hostFor(g.Host, viewer, true)
	case api.RoleJoin:
		resp.Host = s.hostFor(g.Host, viewer, false)
	default:
		if info, ok := s.lm.Info(); ok {
			resp.Host = s.hostFor(info.Host, viewer, false)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// isHost meldet, ob leaseID der gültige Zuschlag ist und dem Konto von id
// gehört. Ein Mitspieler darf nie für den Host handeln, auch wenn er
// LeaseID und MachineID kennt.
func (s *Server) isHost(id identity, leaseID int64) bool {
	host, cur, ok := s.lm.Current()
	return ok && cur == leaseID && strings.EqualFold(host.Email, id.member.Email)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req api.HeartbeatRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	id := identityOf(r)
	s.noteStarter(id.member.Email, req.MachineID, req.StarterVersion)
	if s.isIncoming(id, req.LeaseID) {
		// Der wartende Zuschlag der Übergabe: lebt, solange er Lebenszeichen
		// schickt; ready macht die Übergabe „bereit“.
		if err := s.lm.Heartbeat(req.LeaseID, req.MachineID); err != nil {
			if errors.Is(err, lease.ErrLeaseLost) {
				writeError(w, http.StatusConflict, "lease lost")
				return
			}
			s.internal(w, "Lebenszeichen", err)
			return
		}
		s.lm.Report(req.LeaseID, req.Players, req.Ready)
		s.handoverTouch(context.WithoutCancel(r.Context()), req.LeaseID, req.Ready)
		writeJSON(w, http.StatusOK, api.HeartbeatResponse{MembersVersion: s.membersVersion.Load()})
		return
	}
	if !s.isHost(id, req.LeaseID) {
		writeError(w, http.StatusConflict, "lease lost")
		return
	}
	if err := s.lm.Heartbeat(req.LeaseID, req.MachineID); err != nil {
		if errors.Is(err, lease.ErrLeaseLost) {
			writeError(w, http.StatusConflict, "lease lost")
			return
		}
		s.internal(w, "Lebenszeichen", err)
		return
	}
	s.lm.Report(req.LeaseID, req.Players, req.Ready)
	s.statsSample(req.LeaseID, id.member.Email, req.Players)
	// Ältere Starter lesen den Körper nicht; sie prüfen nur auf 2xx.
	resp := api.HeartbeatResponse{
		Restore:        s.restoreNotice(),
		Maintenance:    s.wartungNotice(),
		MembersVersion: s.membersVersion.Load(),
	}
	if h := s.ho.Load(); h != nil && h.oldLease == req.LeaseID {
		resp.Handover = s.handoverState(h, true, id.member.Email)
	}
	// Der beförderte Host öffnet jetzt auch den Haupt-Port.
	if rp := s.repl.Load(); rp != nil && rp.leaseID == req.LeaseID {
		resp.MainPort = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	var req api.ReleaseRequest
	if !s.decode(w, r, maxJSONBody, &req) {
		return
	}
	err := func() error {
		s.phMu.Lock()
		defer s.phMu.Unlock()
		ctx := context.WithoutCancel(r.Context())
		if s.isIncoming(identityOf(r), req.LeaseID) {
			// Der wartende Zuschlag gibt zurück: Die Übergabe scheitert.
			if err := s.lm.Release(req.LeaseID, req.MachineID); err != nil {
				return err
			}
			s.log.Info("wartender Zuschlag zurückgegeben", "lease", req.LeaseID)
			s.handoverStepLocked(ctx)
			return nil
		}
		if !s.isHost(identityOf(r), req.LeaseID) {
			return lease.ErrLeaseLost
		}
		if err := s.lm.Release(req.LeaseID, req.MachineID); err != nil {
			return err
		}
		s.log.Info("Zuschlag zurückgegeben", "lease", req.LeaseID)
		// Mit der Rückgabe wird ein wartender Zuschlag befördert; dann
		// räumt clearLocked nichts mehr.
		s.handoverStepLocked(ctx)
		// Das Räumen soll nicht daran scheitern, dass der Starter nach der
		// Rückgabe die Verbindung schließt.
		s.clearLocked(context.WithoutCancel(r.Context()))
		// Ein Zurückholen, das auf diesen Zuschlag wartet, gilt, bevor der
		// Starter neu fragt.
		s.settleRestoreLocked(context.WithoutCancel(r.Context()))
		return nil
	}()
	switch {
	case errors.Is(err, lease.ErrLeaseLost):
		writeError(w, http.StatusConflict, "lease lost")
	case err != nil:
		s.internal(w, "Rückgabe", err)
	default:
		if s.karte != nil {
			s.karte.Schluss() // Schluss-Stand des Hosts: einmal die Karte nachführen
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleHostSettings liefert Welt-Einstellungen, Whitelist, Ops, den
// Abstand der Zwischenstände und die Leerlauf-Frist, nur an das Konto des
// gültigen Hosts. Mitglieder, deren UUID sich nicht (oder
// nicht rechtzeitig) auflösen lässt, fehlen in der Whitelist (laut
// geloggt), statt das Hosten für alle zu verhindern; Mitglieder ohne
// Minecraft-Namen fehlen still.
func (s *Server) handleHostSettings(w http.ResponseWriter, r *http.Request) {
	// Der Haupt-Zuschlag oder – bei einer Host-Übergabe – der wartende des
	// Kontos; die Welt kommt vom Zuschlag.
	var info lease.Info
	if main, ok := s.lm.Info(); ok && strings.EqualFold(main.Host.Email, identityOf(r).member.Email) {
		info = main
	} else if in, ok := s.lm.Incoming(); ok && strings.EqualFold(in.Host.Email, identityOf(r).member.Email) {
		info = in
	} else {
		writeError(w, http.StatusConflict, "kein gültiger Zuschlag für dieses Konto")
		return
	}
	hs := api.HostSettings{
		Properties: maps.Clone(s.cfg.WorldSettings),
		Whitelist:  []api.Player{},
		Ops:        []api.Player{},
	}
	if hs.Properties == nil {
		hs.Properties = map[string]string{}
	}
	// Eigenschaften der Welt (Spielmodus, Schwierigkeit) gehen vor
	// world_settings; ungesetzte lassen den Gruppenstandard stehen.
	// Der Chat-Verlauf der Welt kommt immer mit (ohne Eintrag: an, Standardgröße).
	hs.Chat = &api.ChatSettings{MiB: api.ChatMiBStandard}
	if e, ok := s.st.GroupWorld(s.normWorld(info.World)); ok {
		if e.Gamemode != "" {
			hs.Properties["gamemode"] = e.Gamemode
		}
		if e.Difficulty != "" {
			hs.Properties["difficulty"] = e.Difficulty
		}
		hs.Chat = &api.ChatSettings{Off: e.ChatOff, MiB: api.ChatMiBOrDefault(e.ChatMiB)}
	}
	// Abstand der Zwischenstände (snapshot_interval). 0 (Konfiguration nicht
	// über Load gelesen) fehlt in der Antwort; der Host nimmt dann 5 min.
	hs.SnapshotIntervalS = int(s.cfg.SnapshotInterval / time.Second)
	// Leerlauf-Frist (empty_timeout), ebenso; der Host nimmt dann 10 min.
	hs.EmptyTimeoutS = int(s.cfg.EmptyTimeout / time.Second)
	members := slices.DeleteFunc(slices.Clone(s.memberList()), func(m config.Member) bool { return m.Minecraft == "" })
	uuids := s.resolveMembers(r.Context(), members)
	for i, m := range members {
		if uuids[i] == "" {
			continue
		}
		p := api.Player{UUID: uuids[i], Name: m.Minecraft}
		hs.Whitelist = append(hs.Whitelist, p)
		if m.Op {
			hs.Ops = append(hs.Ops, p)
		}
	}
	// Gäste und Banns des Reiters „Server“.
	s.addListsToSettings(&hs)
	writeJSON(w, http.StatusOK, hs)
}

// resolveMembers löst die Minecraft-Namen von members gleichzeitig auf,
// zusammen höchstens uuidTimeout lang. Ergebnis in Mitglieder-Reihenfolge;
// "" für jeden, der nicht (rechtzeitig) aufgelöst wurde – das wird geloggt
// und übersprungen. Ein Resolver, der hängt, hält die Antwort nicht auf.
func (s *Server) resolveMembers(ctx context.Context, members []config.Member) []string {
	ctx, cancel := context.WithTimeout(ctx, s.uuidTimeout)
	defer cancel()
	type result struct {
		i    int
		uuid string
		err  error
	}
	ch := make(chan result, len(members)) // gepuffert: Nachzügler blockieren nicht
	for i, m := range members {
		go func() {
			uuid, err := s.uuids.UUID(ctx, m.Minecraft)
			ch <- result{i, uuid, err}
		}()
	}
	out := make([]string, len(members))
	answered := make([]bool, len(members))
collect:
	for range members {
		select {
		case r := <-ch:
			answered[r.i] = true
			if r.err != nil {
				s.log.Error("UUID nicht auflösbar, Mitglied fehlt in der Whitelist",
					"minecraft", members[r.i].Minecraft, "email", members[r.i].Email, "err", r.err)
				continue
			}
			out[r.i] = r.uuid
		case <-ctx.Done():
			break collect
		}
	}
	for i, m := range members {
		if !answered[i] {
			s.log.Error("UUID nicht rechtzeitig aufgelöst, Mitglied fehlt in der Whitelist",
				"minecraft", m.Minecraft, "email", m.Email, "frist", s.uuidTimeout)
		}
	}
	return out
}

// --- Blobs und Generationen ---

func (s *Server) handleMissing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hashes []string `json:"hashes"`
	}
	if !s.decode(w, r, maxListBody, &in) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Missing []string `json:"missing"`
	}{s.st.Missing(in.Hashes)})
}

// handleFetchBlobs liefert mehrere Blobs in einer Antwort: {"hashes":[…]}
// mit höchstens api.FetchMaxBlobs Hashes und zusammen höchstens
// api.TransferMaxBytes Bytes (sonst 413). Erst wird jeder Blob geprüft;
// fehlen welche, antwortet der Koordinator 404 mit {"missing":[…]}. Sonst
// folgen die Blobs in Anfrage-Reihenfolge direkt hintereinander, mit genauer
// Content-Length. Verschwindet ein Blob währenddessen, bricht die
// Verbindung ab; der Starter erkennt den zu kurzen Körper.
func (s *Server) handleFetchBlobs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hashes []string `json:"hashes"`
	}
	if !s.decode(w, r, maxJSONBody, &in) {
		return
	}
	if len(in.Hashes) > api.FetchMaxBlobs {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("höchstens %d Blobs je Anfrage", api.FetchMaxBlobs))
		return
	}
	sizes, missing, err := s.st.BlobSizes(in.Hashes)
	if err != nil {
		s.storeError(w, "Blobs", err)
		return
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusNotFound, struct {
			Error   string   `json:"error"`
			Missing []string `json:"missing"`
		}{fmt.Sprintf("%d Blobs fehlen", len(missing)), missing})
		return
	}
	var total int64
	for _, n := range sizes {
		if n > api.TransferMaxBytes-total {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("höchstens %d Bytes je Anfrage", api.TransferMaxBytes))
			return
		}
		total += n
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
	w.WriteHeader(http.StatusOK)
	for i, h := range in.Hashes {
		f, n, err := s.st.OpenBlob(h)
		if err == nil && n == sizes[i] {
			_, err = io.CopyN(w, f, n)
		} else if err == nil {
			err = fmt.Errorf("Blob %s hat jetzt %d statt %d Bytes", h, n, sizes[i])
		}
		if f != nil {
			f.Close()
		}
		if err != nil {
			s.log.Warn("Blob-Abruf abgebrochen", "blob", h, "err", err)
			panic(http.ErrAbortHandler) // Körper zu kurz: der Starter verwirft ihn
		}
	}
}

// handleBatchBlobs nimmt mehrere Blobs in einer Anfrage an: Rahmen aus 32
// Byte SHA-256 (roh), uint64 big-endian Größe und Daten, höchstens
// api.BatchMaxFrames Rahmen und zusammen höchstens api.TransferMaxBytes
// (sonst 413). Jeder Rahmen wird geprüft und abgelegt;
// ein falscher Rahmen ergibt 400, die Rahmen davor bleiben.
func (s *Server) handleBatchBlobs(w http.ResponseWriter, r *http.Request) {
	tooLarge := func() {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("höchstens %d Bytes je Anfrage", api.TransferMaxBytes))
	}
	if r.ContentLength > api.TransferMaxBytes {
		tooLarge()
		return
	}
	n, err := s.st.PutBlobs(http.MaxBytesReader(w, r.Body, api.TransferMaxBytes), api.BatchMaxFrames)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			tooLarge()
			return
		}
		if errors.Is(err, store.ErrTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("höchstens %d Blobs je Anfrage", api.BatchMaxFrames))
			return
		}
		s.storeError(w, "Blob-Paket", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Stored int `json:"stored"`
	}{n})
}

// handlePutBlob nimmt einen Teil eines Blobs an: "Content-Range: bytes
// a-b/total" (b inklusive) oder "bytes */0" mit leerem Körper für den leeren
// Blob. 201 = Blob vollständig, 202 = Teil angenommen.
func (s *Server) handlePutBlob(w http.ResponseWriter, r *http.Request) {
	start, end, total, err := parseContentRange(r.Header.Get("Content-Range"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	n := end - start
	if n > MaxPartSize {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Teil größer als %d Bytes", MaxPartSize))
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != n {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("Content-Length %d passt nicht zu Content-Range (%d Bytes)", r.ContentLength, n))
		return
	}
	body := &exactReader{r: r.Body, n: n}
	// Solange PutBlobPart liest, ist der Blob belegt (409 für jeden zweiten
	// Upload). Bricht der Starter ab, endet das Lesen sofort mit einem
	// Fehler; verschwindet seine Verbindung ohne Abbau, nach blobStall.
	sr := &stallReader{r: body, rc: http.NewResponseController(w), stall: blobStall}
	complete, err := s.st.PutBlobPart(r.PathValue("sha"), start, total, sr)
	if err != nil {
		switch {
		case body.short:
			writeError(w, http.StatusBadRequest, "Teil kürzer als in Content-Range angegeben")
		case sr.err != nil:
			s.log.Warn("Blob-Teil nicht vollständig angekommen", "blob", r.PathValue("sha"), "err", sr.err)
			writeError(w, http.StatusBadRequest, "Teil nicht vollständig angekommen: "+sr.err.Error())
		default:
			s.storeError(w, "Blob-Teil", err)
		}
		return
	}
	if complete {
		w.WriteHeader(http.StatusCreated)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleGetBlob(w http.ResponseWriter, r *http.Request) {
	f, _, err := s.st.OpenBlob(r.PathValue("sha"))
	if err != nil {
		s.storeError(w, "Blob", err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *Server) handleHead(w http.ResponseWriter, r *http.Request) {
	// Ohne ?world= der Head der ersten Welt (so liest ein älterer Starter).
	world := r.URL.Query().Get("world")
	if _, ok := s.st.GroupWorld(world); !ok && world != "" {
		writeError(w, http.StatusNotFound, "diese Welt gibt es nicht (mehr)")
		return
	}
	_, gen, err := s.st.HeadGenOf(world)
	if err != nil {
		s.internal(w, "Head lesen", err)
		return
	}
	if gen == nil {
		writeError(w, http.StatusNotFound, "es gibt noch keine Welt")
		return
	}
	writeJSON(w, http.StatusOK, s.generationFor(gen, identityOf(r).member.Email))
}

func (s *Server) handleGeneration(w http.ResponseWriter, r *http.Request) {
	n, ok := parseDigits(r.PathValue("n"))
	if !ok {
		writeError(w, http.StatusBadRequest, "ungültige Generationsnummer")
		return
	}
	g, err := s.st.Generation(r.PathValue("world"), n)
	if err != nil {
		s.storeError(w, "Generation", err)
		return
	}
	writeJSON(w, http.StatusOK, s.generationFor(g, identityOf(r).member.Email))
}

// handleCommit nimmt einen Stand an. Er muss vom Konto und PC des gültigen
// Zuschlags kommen und auf dem Head aufbauen; eine neue Welt anlegen dürfen
// nur Admins.
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	id := identityOf(r)
	var req api.CommitRequest
	if !s.decode(w, r, maxListBody, &req) {
		return
	}

	// commitMu: Nach der Meldung ready der Übergabe kommt kein Stand des
	// alten Zuschlags mehr durch (handover.go).
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	host, cur, ok := s.lm.Current()
	if !ok || cur != req.LeaseID || host.MachineID != req.MachineID || !strings.EqualFold(host.Email, id.member.Email) {
		writeError(w, http.StatusConflict, "lease lost")
		return
	}
	if h := s.ho.Load(); h != nil && h.state == api.HandoverStarting && h.oldLease == cur {
		writeError(w, http.StatusConflict, "Host-Übergabe läuft: dieser Zuschlag reicht nichts mehr ein")
		return
	}
	// Der Stand geht in die Welt des Zuschlags. Eine leere Welt füllen oder
	// ihre Weltkennung ersetzen dürfen nur Admins.
	var leaseWorld string
	if info, ok := s.lm.Info(); ok && info.LeaseID == cur {
		leaseWorld = s.normWorld(info.World)
	}
	if worldID, _ := s.st.HeadRefOf(leaseWorld); (req.NewWorld || worldID == "") && !id.member.Admin {
		writeError(w, http.StatusForbidden, "eine Welt anlegen dürfen nur Admins")
		return
	}
	g, err := s.st.CommitTo(leaseWorld, req, *host, s.now())
	if err != nil {
		s.storeError(w, "Commit", err)
		return
	}
	s.log.Info("Stand angenommen", "welt", g.WorldID, "generation", g.Number,
		"dateien", len(g.Files), "host", g.HostEmail, "lease", g.LeaseID)
	if s.karte != nil {
		s.karte.Melde() // nur ein Wecker, blockiert nie
	}
	writeJSON(w, http.StatusOK, s.generationFor(g, id.member.Email))
}

// --- Profile und Messung ---

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	machine := r.PathValue("machine")
	if !validMachineID(machine) {
		writeError(w, http.StatusBadRequest, "ungültige PC-Kennung")
		return
	}
	var p api.Profile
	if !s.decode(w, r, maxJSONBody, &p) {
		return
	}
	if p.XmxMB < 0 || p.ViewDistance < 0 || p.SimulationDistance < 0 || p.C2METhreads < 0 {
		writeError(w, http.StatusBadRequest, "Profil mit negativen Werten")
		return
	}
	if err := s.saveProfile(identityOf(r).member.Email, machine, p); err != nil {
		s.internal(w, "Profil speichern", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// saveProfile legt das Profil pro Konto (E-Mail klein) und PC in
// DataDir/profiles.json ab.
// loadProfiles liest alle Profile (leer, wenn es keine gibt oder die Datei
// nicht lesbar ist): je E-Mail (klein) und PC.
func (s *Server) loadProfiles() map[string]map[string]api.Profile {
	s.profMu.Lock()
	defer s.profMu.Unlock()
	all := map[string]map[string]api.Profile{}
	data, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "profiles.json"))
	if err != nil {
		return all
	}
	if err := json.Unmarshal(data, &all); err != nil {
		s.log.Error("profiles.json nicht lesbar", "err", err)
		return map[string]map[string]api.Profile{}
	}
	return all
}

func (s *Server) saveProfile(email, machine string, p api.Profile) error {
	s.profMu.Lock()
	defer s.profMu.Unlock()
	path := filepath.Join(s.cfg.DataDir, "profiles.json")
	all := map[string]map[string]api.Profile{}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &all); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	key := strings.ToLower(email)
	if all[key] == nil {
		all[key] = map[string]api.Profile{}
	}
	all[key][machine] = p
	out, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, out)
}

func (s *Server) handleSpeedTest(w http.ResponseWriter, r *http.Request) {
	if _, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, speedTestMax)); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("höchstens %d Bytes", speedTestMax))
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePing beantwortet GET /v1/ping leer (204): Zeitmessung des Starters
// für die Host-Wahl (Score.PingMs). Keine Daten, keine Sperren. Dass der
// Endpunkt da ist, zeigt dem Starter zugleich, dass der Koordinator
// ping_ms kennt.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// --- Hilfen ---

// decode liest genau ein JSON-Objekt nach v; unbekannte Felder sind ein
// Fehler (ein älterer Koordinator soll nichts still verwerfen).
func (s *Server) decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		if _, terr := dec.Token(); terr != io.EOF {
			err = errors.New("Daten nach dem JSON-Objekt")
		}
	}
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("Anfrage größer als %d Bytes", limit))
			return false
		}
		writeError(w, http.StatusBadRequest, "Anfrage nicht lesbar: "+err.Error())
		return false
	}
	return true
}

// storeError bildet die Fehler des Speichers auf Statuscodes ab.
func (s *Server) storeError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, store.ErrMissingBlob): // vor ErrInvalid, das es mit einschließt
		writeError(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		s.internal(w, what, err)
	}
}

// internal loggt err und antwortet 500 ohne Einzelheiten (Pfade bleiben
// auf dem Server).
func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what+" fehlgeschlagen", "err", err)
	writeError(w, http.StatusInternalServerError, what+": interner Fehler des Koordinators")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, struct {
		Error string `json:"error"`
	}{msg})
}

// parseContentRange liest "bytes a-b/total" (b inklusive) und "bytes */0".
// end ist exklusiv.
func parseContentRange(h string) (start, end, total int64, err error) {
	bad := fmt.Errorf("Content-Range %q: erwartet \"bytes a-b/total\" oder \"bytes */0\"", h)
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, 0, 0, bad
	}
	rng, tot, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, 0, 0, bad
	}
	total, ok = parseDigits(tot)
	if !ok {
		return 0, 0, 0, bad
	}
	if rng == "*" {
		if total != 0 {
			return 0, 0, 0, bad
		}
		return 0, 0, 0, nil
	}
	a, b, ok := strings.Cut(rng, "-")
	if !ok {
		return 0, 0, 0, bad
	}
	start, ok1 := parseDigits(a)
	last, ok2 := parseDigits(b)
	if !ok1 || !ok2 || last < start || last >= total {
		return 0, 0, 0, bad
	}
	return start, last + 1, total, nil
}

// parseDigits liest eine nicht negative Dezimalzahl ohne Vorzeichen.
func parseDigits(s string) (int64, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// validMachineID: 1–128 Zeichen aus Buchstaben, Ziffern, ".", "-" und "_"
// (der Starter erzeugt eine UUID).
func validMachineID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// exactReader liefert höchstens n Bytes aus r; endet r vorher, meldet er
// io.ErrUnexpectedEOF und merkt sich das.
type exactReader struct {
	r     io.Reader
	n     int64
	short bool
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > e.n {
		p = p[:e.n]
	}
	k, err := e.r.Read(p)
	e.n -= int64(k)
	if (err == io.EOF || err == io.ErrUnexpectedEOF) && e.n > 0 {
		e.short = true
		err = io.ErrUnexpectedEOF
	}
	if err == io.EOF {
		err = nil // genau n Bytes gelesen; das nächste Read meldet EOF
	}
	return k, err
}

// blobStall: So lange darf ein Blob-Teil ohne neue Daten bleiben; dann
// endet das Lesen mit einem Fehler und der Blob ist wieder frei. Ohne Frist
// hielte ein Starter, dessen Verbindung ohne Abbau verschwunden ist (Schlaf,
// Netz weg), den Blob bis zum TCP-Keepalive (Minuten) belegt. Im Test
// austauschbar (export_test.go).
//
// Nicht länger als statusInterval des Starters (internal/starter/app):
// Nach einem Verbindungsverlust stößt dort der nächste gelingende Status
// das Nachreichen an; bis dahin muss der Blob des abgerissenen Uploads
// wieder frei sein.
var blobStall = 30 * time.Second

// stallReader setzt vor jedem Lesen die Lese-Frist der Verbindung auf
// stall ab jetzt. Am Ende des Körpers (EOF) fällt die Frist weg: Danach
// liest der Server im Hintergrund weiter, um ein Schließen der Verbindung zu
// bemerken, und das soll nicht an der Frist scheitern. Nach einem Lesefehler
// bleibt sie: Der Server verwirft dann den Rest des Körpers und schließt die
// Verbindung, statt ohne Frist auf einen verschwundenen Starter zu warten
// (vor der nächsten Anfrage setzt net/http die Fristen ohnehin neu). err ist
// der erste Lesefehler außer EOF.
type stallReader struct {
	r     io.Reader
	rc    *http.ResponseController
	stall time.Duration
	err   error
}

func (s *stallReader) Read(p []byte) (int, error) {
	// Ohne Unterstützung für Fristen (ErrNotSupported) geht es ohne weiter.
	s.rc.SetReadDeadline(time.Now().Add(s.stall))
	n, err := s.r.Read(p)
	switch {
	case err == io.EOF:
		s.rc.SetReadDeadline(time.Time{})
	case err != nil && s.err == nil:
		s.err = err
	}
	return n, err
}

// writeFileAtomic schreibt data über eine Temp-Datei im selben Ordner.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// --- Mojang-UUIDs ---

// mojangAPI liefert zu einem Namen {"id":"<32 Hex>","name":"…"}; 204 oder
// 404, wenn es den Namen nicht gibt.
const mojangAPI = "https://api.mojang.com/users/profiles/minecraft/"

// negativeTTL: So lange gilt ein Fehlschlag beim Auflösen eines Namens
// (unbekannt, Mojang gestört), bevor wieder gefragt wird.
const negativeTTL = 10 * time.Minute

// MojangResolver löst Minecraft-Namen über die Mojang-API auf und merkt sich
// die Ergebnisse in einer JSON-Datei (Name klein → UUID). Fehlschläge merkt
// er sich nur im Speicher für negativeTTL (ein unbekannter Name oder eine
// gestörte API wird nicht bei jedem Hosten neu gefragt); endet der Kontext
// des Aufrufers vorher, zählt das nicht.
type MojangResolver struct {
	path string
	base string
	http *http.Client
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]string
	neg   map[string]negative
}

type negative struct {
	until time.Time
	err   error
}

// NewMojangResolver lädt den Zwischenspeicher aus cachePath (etwa
// DataDir/uuids.json). Eine unlesbare Datei zählt als leer.
func NewMojangResolver(cachePath string) *MojangResolver {
	m := &MojangResolver{
		path:  cachePath,
		base:  mojangAPI,
		http:  &http.Client{Timeout: 10 * time.Second},
		now:   time.Now,
		cache: map[string]string{},
		neg:   map[string]negative{},
	}
	data, err := os.ReadFile(cachePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		slog.Warn("UUID-Zwischenspeicher nicht lesbar, fange leer an", "err", err)
	default:
		if err := json.Unmarshal(data, &m.cache); err != nil {
			slog.Warn("UUID-Zwischenspeicher kaputt, fange leer an", "path", cachePath, "err", err)
			m.cache = map[string]string{}
		}
	}
	return m
}

// UUID liefert die UUID (mit Bindestrichen) zum Minecraft-Namen name.
func (m *MojangResolver) UUID(ctx context.Context, name string) (string, error) {
	if !validMinecraftName(name) {
		return "", fmt.Errorf("ungültiger Minecraft-Name %q", name)
	}
	key := strings.ToLower(name)
	m.mu.Lock()
	uuid, ok := m.cache[key]
	neg, negOK := m.neg[key]
	m.mu.Unlock()
	if ok {
		return uuid, nil
	}
	if negOK && m.now().Before(neg.until) {
		return "", neg.err
	}

	uuid, err := m.fetch(ctx, name)
	if err != nil {
		if ctx.Err() == nil {
			m.mu.Lock()
			m.neg[key] = negative{until: m.now().Add(negativeTTL), err: err}
			m.mu.Unlock()
		}
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.neg, key)
	m.cache[key] = uuid
	data, err := json.MarshalIndent(m.cache, "", "  ")
	if err == nil {
		err = writeFileAtomic(m.path, data)
	}
	if err != nil {
		slog.Warn("UUID-Zwischenspeicher nicht geschrieben", "err", err)
	}
	return uuid, nil
}

func (m *MojangResolver) fetch(ctx context.Context, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Mojang-API: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent, http.StatusNotFound:
		return "", unknownNameError(name)
	default:
		return "", fmt.Errorf("Mojang-API: %s für %q", resp.Status, name)
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("Mojang-API: Antwort für %q nicht lesbar: %w", name, err)
	}
	uuid, ok := dashedUUID(body.ID)
	if !ok {
		return "", fmt.Errorf("Mojang-API: ungültige UUID %q für %q", body.ID, name)
	}
	return uuid, nil
}

// unknownNameError: Mojang kennt den Namen nicht (errors.Is ErrUnknownName).
type unknownNameError string

func (e unknownNameError) Error() string {
	return fmt.Sprintf("Mojang kennt den Minecraft-Namen %q nicht", string(e))
}

func (e unknownNameError) Is(target error) bool { return target == ErrUnknownName }

// dashedUUID macht aus 32 Hex-Zeichen (mit oder ohne Bindestriche) die Form
// 8-4-4-4-12 in Kleinbuchstaben.
func dashedUUID(id string) (string, bool) {
	h := strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(h) != 32 {
		return "", false
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return "", false
		}
	}
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], true
}

// validMinecraftName: wie config.ValidMinecraftName.
func validMinecraftName(s string) bool { return config.ValidMinecraftName(s) }
