// Package lease vergibt den Hosting-Zuschlag: höchstens ein Host zur selben
// Zeit, Zuschläge verfallen ohne Lebenszeichen, und der Zustand überlebt einen
// Neustart des Koordinators.
//
// Gibt es keinen Host, öffnet die erste Anfrage ein Sammelfenster. Alle
// Anfragen im Fenster warten gemeinsam; am Ende bekommt der beste Kandidat
// (Best) den Zuschlag, alle anderen werden Mitspieler. Die Zeit kommt nur
// aus der injizierten Uhr.
package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

const (
	// HeartbeatInterval ist der Takt der Lebenszeichen des Hosts.
	HeartbeatInterval = 10 * time.Second
	// LeaseTTL ist die Zeit ohne Lebenszeichen, nach der ein Zuschlag verfällt.
	LeaseTTL = 90 * time.Second
	// ReadyMaxAge: So lange nach dem letzten Lebenszeichen gilt ein bereit
	// gemeldeter Host noch als bereit – zwei verpasste Lebenszeichen plus
	// Spielraum. Nach einem Absturz gilt der Zuschlag noch bis LeaseTTL,
	// aber Mitspieler sollen sich nicht mehr zu ihm verbinden.
	ReadyMaxAge = 2*HeartbeatInterval + 5*time.Second
	// Window ist die Länge des Sammelfensters.
	Window = 5 * time.Second
)

var (
	// ErrLeaseLost: Zuschlag unbekannt, abgelaufen oder von einem anderen PC.
	ErrLeaseLost = errors.New("lease lost")
	// ErrBusy: Acquire geht nicht, weil es einen Host oder ein offenes
	// Sammelfenster gibt.
	ErrBusy = errors.New("busy")
)

// Clock ist die Zeitquelle des Managers; Tests setzen eine falsche Uhr ein.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Candidate ist ein PC, der hosten könnte. Der Zuschlag gilt pro PC, also
// für das Paar aus Konto und MachineID.
type Candidate struct {
	Email, MinecraftName, MachineID string
	AccountID                       int64
	Score                           api.Score
}

// Grant ist die Antwort auf eine Anfrage. Bei Role "join" verweisen LeaseID,
// Host und HostAccountID auf den Zuschlag des Hosts. Bei Role "denied"
// nennt Reason den Grund.
type Grant struct {
	Role          string
	LeaseID       int64
	Host          api.HostInfo
	HostAccountID int64
	Reason        string
}

// ReasonUploading: Den Zuschlag hält gerade jemand nur zum Hochladen
// (Nachreichen, Welt übernehmen); einen Server zum Mitspielen gibt es nicht.
const ReasonUploading = "Welt wird gerade hochgeladen, gleich nochmal"

// ReasonHandover: Der Haupt-Zuschlag ist zu Ende, der wartende der
// Host-Übergabe wird gleich befördert.
const ReasonHandover = "Host-Übergabe läuft, gleich nochmal"

// Info beschreibt den gültigen Zuschlag.
type Info struct {
	Host      api.HostInfo
	LeaseID   int64
	AccountID int64
	// UploadOnly: Zuschlag aus Acquire (nur zum Hochladen, ohne Server).
	UploadOnly bool
	// Ready: Das letzte Lebenszeichen meldete einen laufenden, erreichbaren
	// Server und ist jünger als ReadyMaxAge. Nur im Speicher.
	Ready bool
}

// Manager hält den Zuschlag. Alle Methoden sind nebenläufig nutzbar.
type Manager struct {
	path string
	clk  Clock

	mu      sync.Mutex
	st      state
	win     *window   // offenes Sammelfenster oder nil
	players []string  // nur im Speicher; kommt mit jedem Lebenszeichen neu
	ready   bool      // nur im Speicher; kommt mit jedem Lebenszeichen neu
	readyAt time.Time // nur im Speicher; Zeit des Lebenszeichens zu ready

	// Dasselbe für den wartenden Zuschlag (Incoming); mit Promote gehen die
	// Werte auf den Haupt-Zuschlag über.
	inPlayers []string
	inReady   bool
	inReadyAt time.Time
}

// state ist der Inhalt der Zustandsdatei.
type state struct {
	LastLeaseID int64      `json:"last_lease_id"`
	Host        *hostState `json:"host,omitempty"`
	// Incoming: wartender zweiter Zuschlag der Host-Übergabe (GrantIncoming).
	Incoming *hostState `json:"incoming,omitempty"`
}

type hostState struct {
	LeaseID       int64     `json:"lease_id"`
	AccountID     int64     `json:"account_id"`
	Email         string    `json:"email"`
	MinecraftName string    `json:"minecraft_name"`
	MachineID     string    `json:"machine_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	// UploadOnly: Zuschlag aus Acquire, nur zum Hochladen.
	UploadOnly bool `json:"upload_only,omitempty"`
}

type window struct {
	entries []*entry
	done    chan struct{} // geschlossen, sobald alle entries beantwortet sind
}

type entry struct {
	c         Candidate
	withdrawn bool // Anfrage vor der Entscheidung abgebrochen
	grant     Grant
	err       error
}

// New lädt den Zustand aus statePath. Fehlt die Datei, gibt es noch keinen
// Zuschlag. Ein beim Laden schon abgelaufener Zuschlag zählt wie jeder
// abgelaufene als kein Host; Expire meldet ihn noch einmal, damit der
// Port-Halter auch nach einem Neustart geräumt wird. Eine unlesbare Datei ist
// ein Fehler: neu anfangen würde LeaseIDs wiederverwenden.
func New(statePath string, clk Clock) (*Manager, error) {
	m := &Manager{path: statePath, clk: clk}
	data, err := os.ReadFile(statePath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("lease: %w", err)
	default:
		if err := json.Unmarshal(data, &m.st); err != nil {
			return nil, fmt.Errorf("lease: %s: %w", statePath, err)
		}
	}
	if h := m.st.Host; h != nil {
		m.st.LastLeaseID = max(m.st.LastLeaseID, h.LeaseID)
	}
	if h := m.st.Incoming; h != nil {
		m.st.LastLeaseID = max(m.st.LastLeaseID, h.LeaseID)
	}
	return m, nil
}

// Request will hosten. Gibt es einen gültigen Host, kommt sofort "join"
// zurück – oder "denied" (ReasonUploading), wenn der Zuschlag nur zum
// Hochladen dient. Sonst wartet Request bis zum Ende des Sammelfensters
// (höchstens Window) und bekommt "host" oder "join". Bricht ctx vorher ab,
// nimmt die Anfrage nicht mehr an der Wahl teil.
func (m *Manager) Request(ctx context.Context, c Candidate) (Grant, error) {
	m.mu.Lock()
	if h := m.validLocked(); h != nil {
		m.mu.Unlock()
		if h.UploadOnly {
			g := joinGrant(h)
			g.Role, g.Reason = api.RoleDenied, ReasonUploading
			return g, nil
		}
		return joinGrant(h), nil
	}
	if m.validIncomingLocked() != nil {
		// Der Haupt-Zuschlag ist weg, der wartende noch nicht befördert:
		// niemand bekommt dazwischen einen eigenen Zuschlag.
		m.mu.Unlock()
		return Grant{Role: api.RoleDenied, Reason: ReasonHandover}, nil
	}
	if m.win == nil {
		m.win = &window{done: make(chan struct{})}
		go m.closeWindow(m.win, m.clk.After(Window))
	}
	w := m.win
	e := &entry{c: c}
	w.entries = append(w.entries, e)
	m.mu.Unlock()

	select {
	case <-w.done:
		return e.grant, e.err
	case <-ctx.Done():
		m.mu.Lock()
		defer m.mu.Unlock()
		select {
		case <-w.done: // Entscheidung kam zuerst; sie gilt.
			return e.grant, e.err
		default:
		}
		e.withdrawn = true
		return Grant{}, ctx.Err()
	}
}

// closeWindow wartet auf das Ende des Fensters, wählt und beantwortet alle
// Anfragen des Fensters.
func (m *Manager) closeWindow(w *window, fire <-chan time.Time) {
	<-fire
	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(w.done)
	m.win = nil

	var active []*entry
	var cands []Candidate
	for _, e := range w.entries {
		if !e.withdrawn {
			active = append(active, e)
			cands = append(cands, e.c)
		}
	}
	if len(active) == 0 {
		return
	}
	winner := active[Best(cands)]
	h, err := m.grantLocked(winner.c, false)
	for _, e := range active {
		switch {
		case err != nil:
			e.err = err
		case e == winner:
			e.grant = hostGrant(h)
		default:
			e.grant = joinGrant(h)
		}
	}
}

// Acquire gibt sofort einen Zuschlag, aber nur, wenn es weder einen
// gültigen Host noch ein offenes Sammelfenster gibt; sonst ErrBusy. Für
// Anfragen, die nie an einer Wahl teilnehmen (Nachreichen, Welt übernehmen):
// Der Zuschlag ist als UploadOnly markiert; wer währenddessen spielen will,
// bekommt "denied" statt "join".
func (m *Manager) Acquire(c Candidate) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.win != nil || m.validIncomingLocked() != nil {
		return Grant{}, ErrBusy
	}
	h, err := m.grantLocked(c, true)
	if err != nil {
		return Grant{}, err
	}
	return hostGrant(h), nil
}

// grantLocked vergibt einen neuen Zuschlag an c. Er gilt erst, wenn er auf
// der Platte steht.
func (m *Manager) grantLocked(c Candidate, uploadOnly bool) (*hostState, error) {
	if m.validLocked() != nil {
		return nil, ErrBusy
	}
	h := &hostState{
		LeaseID:       m.st.LastLeaseID + 1,
		AccountID:     c.AccountID,
		Email:         c.Email,
		MinecraftName: c.MinecraftName,
		MachineID:     c.MachineID,
		ExpiresAt:     m.clk.Now().Add(LeaseTTL),
		UploadOnly:    uploadOnly,
	}
	if m.validIncomingLocked() != nil {
		return nil, ErrBusy
	}
	next := state{LastLeaseID: h.LeaseID, Host: h}
	if err := m.save(next); err != nil {
		return nil, err
	}
	m.st = next
	m.players, m.ready = nil, false
	return h, nil
}

// GrantIncoming vergibt den wartenden zweiten Zuschlag der Host-Übergabe.
// Er geht nur, solange ein gültiger Haupt-Zuschlag (kein Zuschlag nur zum
// Hochladen) besteht und noch kein gültiger wartender; sonst ErrLeaseLost
// bzw. ErrBusy. Der wartende Zuschlag hat eine eigene LeaseID; Heartbeat,
// Valid, Report und Release gelten auch für ihn. Er wird erst mit Promote
// zum Haupt-Zuschlag.
func (m *Manager) GrantIncoming(c Candidate) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h := m.validLocked(); h == nil || h.UploadOnly {
		return Grant{}, ErrLeaseLost
	}
	if m.validIncomingLocked() != nil {
		return Grant{}, ErrBusy
	}
	in := &hostState{
		LeaseID:       m.st.LastLeaseID + 1,
		AccountID:     c.AccountID,
		Email:         c.Email,
		MinecraftName: c.MinecraftName,
		MachineID:     c.MachineID,
		ExpiresAt:     m.clk.Now().Add(LeaseTTL),
	}
	next := state{LastLeaseID: in.LeaseID, Host: m.st.Host, Incoming: in}
	if err := m.save(next); err != nil {
		return Grant{}, err
	}
	m.st = next
	m.inPlayers, m.inReady = nil, false
	return hostGrant(in), nil
}

// Incoming liefert den gültigen wartenden Zuschlag, falls es einen gibt.
func (m *Manager) Incoming() (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.validIncomingLocked()
	if h == nil {
		return Info{}, false
	}
	ready := m.inReady && m.clk.Now().Sub(m.inReadyAt) < ReadyMaxAge
	return Info{Host: h.info(), LeaseID: h.LeaseID, AccountID: h.AccountID, Ready: ready}, true
}

// DropIncoming verwirft den wartenden Zuschlag (auch einen abgelaufenen).
func (m *Manager) DropIncoming() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st.Incoming == nil {
		return nil
	}
	next := state{LastLeaseID: m.st.LastLeaseID, Host: m.st.Host}
	if err := m.save(next); err != nil {
		return err
	}
	m.st = next
	m.inPlayers, m.inReady = nil, false
	return nil
}

// Promote macht den wartenden Zuschlag zum Haupt-Zuschlag. Es geht nur,
// wenn der Haupt-Zuschlag zurückgegeben oder abgelaufen ist und der
// wartende noch gilt; sonst false. Der Zuschlag behält seine LeaseID und
// läuft mit seinem bisherigen Ablauf weiter.
func (m *Manager) Promote() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.validIncomingLocked()
	if m.validLocked() != nil || in == nil {
		return false, nil
	}
	next := state{LastLeaseID: m.st.LastLeaseID, Host: in}
	if err := m.save(next); err != nil {
		return false, err
	}
	m.st = next
	m.players, m.ready, m.readyAt = m.inPlayers, m.inReady, m.inReadyAt
	m.inPlayers, m.inReady = nil, false
	return true, nil
}

// Heartbeat verlängert den Zuschlag um LeaseTTL. ErrLeaseLost, wenn
// leaseID und machineID nicht zum gültigen Zuschlag passen.
func (m *Manager) Heartbeat(leaseID int64, machineID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.st
	switch {
	case m.matchLocked(leaseID, machineID) != nil:
		renewed := *m.st.Host
		renewed.ExpiresAt = m.clk.Now().Add(LeaseTTL)
		next.Host = &renewed
	case m.matchIncomingLocked(leaseID, machineID) != nil:
		renewed := *m.st.Incoming
		renewed.ExpiresAt = m.clk.Now().Add(LeaseTTL)
		next.Incoming = &renewed
	default:
		return ErrLeaseLost
	}
	// Erst auf der Platte, dann gültig: sonst könnte ein Neustart einen
	// früheren Ablauf sehen und einen zweiten Host zulassen.
	if err := m.save(next); err != nil {
		return err
	}
	m.st = next
	return nil
}

// Release gibt den Zuschlag zurück. ErrLeaseLost, wenn leaseID und
// machineID nicht zum gültigen Zuschlag passen.
func (m *Manager) Release(leaseID int64, machineID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Der Haupt-Zuschlag geht, ein gültiger wartender bleibt bestehen; gibt
	// der wartende zurück, bleibt der Haupt-Zuschlag.
	next := state{LastLeaseID: m.st.LastLeaseID, Host: m.st.Host, Incoming: m.validIncomingLocked()}
	main := m.matchLocked(leaseID, machineID) != nil
	switch {
	case main:
		next.Host = nil
	case m.matchIncomingLocked(leaseID, machineID) != nil:
		next.Incoming = nil
	default:
		return ErrLeaseLost
	}
	if err := m.save(next); err != nil {
		return err
	}
	m.st = next
	if main {
		m.players, m.ready = nil, false
	} else {
		m.inPlayers, m.inReady = nil, false
	}
	return nil
}

// Valid meldet, ob leaseID der gültige Zuschlag des PCs machineID ist (der
// Haupt-Zuschlag oder der wartende).
func (m *Manager) Valid(leaseID int64, machineID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.matchLocked(leaseID, machineID) != nil || m.matchIncomingLocked(leaseID, machineID) != nil
}

// Current liefert den gültigen Host, falls es einen gibt.
func (m *Manager) Current() (host *api.HostInfo, leaseID int64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.validLocked()
	if h == nil {
		return nil, 0, false
	}
	info := h.info()
	return &info, h.LeaseID, true
}

// Info liefert den gültigen Zuschlag samt Merkmalen, falls es einen gibt.
func (m *Manager) Info() (Info, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.validLocked()
	if h == nil {
		return Info{}, false
	}
	ready := m.ready && m.clk.Now().Sub(m.readyAt) < ReadyMaxAge
	return Info{Host: h.info(), LeaseID: h.LeaseID, AccountID: h.AccountID, UploadOnly: h.UploadOnly, Ready: ready}, true
}

// Expire räumt einen abgelaufenen Zuschlag ab und meldet ihn genau einmal
// mit dem Konto des alten Hosts (für den Port-Halter). Wurde inzwischen ein
// neuer Zuschlag vergeben, ist der alte schon ersetzt und wird nicht
// gemeldet.
func (m *Manager) Expire() (expired bool, accountID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.st.Host
	hostGone := h != nil && !m.clk.Now().Before(h.ExpiresAt)
	// Ein abgelaufener wartender Zuschlag verfällt wie jeder andere, ohne
	// Meldung; ein gültiger bleibt, auch wenn der Haupt-Zuschlag abläuft.
	inGone := m.st.Incoming != nil && m.validIncomingLocked() == nil
	if !hostGone && !inGone {
		return false, 0
	}
	next := state{LastLeaseID: m.st.LastLeaseID, Host: m.st.Host, Incoming: m.st.Incoming}
	if hostGone {
		next.Host = nil
		m.players, m.ready = nil, false
	}
	if inGone {
		next.Incoming = nil
		m.inPlayers, m.inReady = nil, false
	}
	m.st = next
	// Schlägt das Schreiben fehl, steht der abgelaufene Zuschlag noch auf
	// der Platte. Das ist unschädlich: nach einem Neustart gilt er als kein
	// Host und wird höchstens noch einmal gemeldet (Port-Halter räumen ist
	// wiederholbar).
	_ = m.save(m.st)
	if !hostGone {
		return false, 0
	}
	return true, h.AccountID
}

// Report merkt sich Spielerliste und Bereitschaft aus dem Lebenszeichen des
// gültigen Zuschlags leaseID (Haupt-Zuschlag oder wartender); für jeden
// anderen Zuschlag tut es nichts. Die Bereitschaft gilt ReadyMaxAge lang
// (siehe Info).
func (m *Manager) Report(leaseID int64, players []string, ready bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h := m.validLocked(); h != nil && h.LeaseID == leaseID {
		m.players = append([]string(nil), players...)
		m.ready, m.readyAt = ready, m.clk.Now()
	} else if h := m.validIncomingLocked(); h != nil && h.LeaseID == leaseID {
		m.inPlayers = append([]string(nil), players...)
		m.inReady, m.inReadyAt = ready, m.clk.Now()
	}
}

// Players liefert die zuletzt gemeldeten Spieler des gültigen Hosts (leer,
// wenn es keinen gibt).
func (m *Manager) Players() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.validLocked() == nil {
		return []string{}
	}
	return append([]string{}, m.players...)
}

// validLocked liefert den Host, solange sein Zuschlag nicht abgelaufen ist.
func (m *Manager) validLocked() *hostState {
	h := m.st.Host
	if h == nil || !m.clk.Now().Before(h.ExpiresAt) {
		return nil
	}
	return h
}

// validIncomingLocked liefert den wartenden Zuschlag, solange er nicht
// abgelaufen ist.
func (m *Manager) validIncomingLocked() *hostState {
	h := m.st.Incoming
	if h == nil || !m.clk.Now().Before(h.ExpiresAt) {
		return nil
	}
	return h
}

func (m *Manager) matchIncomingLocked(leaseID int64, machineID string) *hostState {
	h := m.validIncomingLocked()
	if h == nil || h.LeaseID != leaseID || h.MachineID != machineID {
		return nil
	}
	return h
}

func (m *Manager) matchLocked(leaseID int64, machineID string) *hostState {
	h := m.validLocked()
	if h == nil || h.LeaseID != leaseID || h.MachineID != machineID {
		return nil
	}
	return h
}

// save schreibt st atomar: Temp-Datei im selben Ordner, Sync, Rename.
func (m *Manager) save(st state) (err error) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	f, err := os.CreateTemp(dir, filepath.Base(m.path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("lease: Zustand schreiben: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
			err = fmt.Errorf("lease: Zustand schreiben: %w", err)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, m.path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir macht das Rename dauerhaft (Linux). Unter Windows lässt sich ein
// Ordner nicht syncen; dort genügt das Rename.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (h *hostState) info() api.HostInfo {
	return api.HostInfo{Email: h.Email, MinecraftName: h.MinecraftName, MachineID: h.MachineID}
}

func hostGrant(h *hostState) Grant {
	return Grant{Role: api.RoleHost, LeaseID: h.LeaseID, Host: h.info(), HostAccountID: h.AccountID}
}

func joinGrant(h *hostState) Grant {
	return Grant{Role: api.RoleJoin, LeaseID: h.LeaseID, Host: h.info(), HostAccountID: h.AccountID}
}

// Best liefert den Index des besten Kandidaten, -1 bei leerer Liste.
//
// Es zählen nur Kandidaten mit freiem RAM ≥ Xmx + 1024 MB und Upload
// ≥ 5 Mbit/s; erfüllt das niemand, zählen alle. Jede Größe wird durch ihr
// Maximum unter den zählenden Kandidaten geteilt; Score = 0,5·Upload +
// 0,35·CPU + 0,15·freier RAM. Bei Gleichstand gewinnt der frühere Index.
func Best(cands []Candidate) int {
	var pool []int
	for i, c := range cands {
		if c.Score.RAMFreeMB >= c.Score.XmxMB+1024 && c.Score.UploadMbps >= 5 {
			pool = append(pool, i)
		}
	}
	if len(pool) == 0 {
		for i := range cands {
			pool = append(pool, i)
		}
	}
	var maxUp, maxCPU, maxRAM float64
	for _, i := range pool {
		s := cands[i].Score
		maxUp = max(maxUp, s.UploadMbps)
		maxCPU = max(maxCPU, s.CPUScore)
		maxRAM = max(maxRAM, float64(s.RAMFreeMB))
	}
	best, bestScore := -1, 0.0
	for _, i := range pool {
		s := cands[i].Score
		score := 0.5*norm(s.UploadMbps, maxUp) + 0.35*norm(s.CPUScore, maxCPU) + 0.15*norm(float64(s.RAMFreeMB), maxRAM)
		if best < 0 || score > bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

func norm(v, maxV float64) float64 {
	if maxV <= 0 {
		return 0
	}
	return v / maxV
}
