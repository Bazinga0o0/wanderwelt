// Package client spricht HTTPS mit dem Koordinator (durch tunneld, das den
// persönlichen Token prüft). Jede Methode entspricht einem Endpunkt des
// Koordinators und benutzt die Typen aus internal/api.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// PartSize ist die größte Teilgröße beim Blob-Upload (32 MiB).
const PartSize int64 = 32 << 20

const (
	// callTimeout begrenzt normale Aufrufe. /v1/play kann wegen des
	// Sammelfensters bis etwa 5 s blockieren.
	callTimeout = 30 * time.Second
	// commitTimeout ist großzügiger: Ein Commit, der beim Koordinator
	// durchgeht, hier aber als Zeitüberschreitung endet, würde den nächsten
	// Zwischenstand mit falscher Basis einreichen.
	commitTimeout = 2 * time.Minute
	// responseHeaderTimeout fängt einen Koordinator ab, der eine Anfrage
	// vollständig annimmt und dann nie antwortet (gilt auch für Blobs).
	responseHeaderTimeout = 2 * time.Minute

	speedTestSize    = 8 << 20
	speedTestTimeout = 60 * time.Second

	maxErrorBody = 64 << 10
	maxErrorText = 300
)

// ErrLeaseLost: Der Koordinator hat Lebenszeichen oder Commit mit 409
// abgelehnt – der Zuschlag gilt nicht mehr (oder die Basis ist falsch).
// Aufrufer prüfen mit errors.Is.
var ErrLeaseLost = errors.New("lease lost")

// StatusError ist eine Antwort des Koordinators (oder von tunneld) außerhalb
// von 2xx. Message ist der Text aus {"error":"…"}, sonst der Anfang des
// Körpers.
type StatusError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	s := fmt.Sprintf("Koordinator: %s %s: %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Client ist der HTTP-Client zum Koordinator. Er ist für gleichzeitige
// Benutzung aus mehreren Goroutinen geeignet.
type Client struct {
	base  string
	token string
	email string
	http  *http.Client
}

// New baut einen Client für die Gruppe g. Jede Anfrage trägt
// "Authorization: Bearer <Token>" und, falls gesetzt,
// "X-Tunnel-Account-Email: <DevIdentityEmail>".
//
// Die TLS-Prüfung bleibt immer beim Standard; TunnelInsecureSkipVerify gilt
// nur für den tunnel-Client.
func New(g *group.Group) *Client {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	}
	tr.ResponseHeaderTimeout = responseHeaderTimeout
	return &Client{
		base:  strings.TrimRight(strings.TrimSpace(g.Coordinator), "/"),
		token: strings.TrimSpace(g.Token),
		email: strings.TrimSpace(g.DevIdentityEmail),
		// Keine Gesamtzeit am http.Client: Blobs dürfen lange laufen. Normale
		// Aufrufe begrenzt call über den Kontext.
		http: &http.Client{Transport: tr},
	}
}

// Me fragt, wer man selbst ist (GET /v1/me).
func (c *Client) Me(ctx context.Context) (*api.Me, error) {
	var out api.Me
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Status fragt, wer hostet und wer drin ist (GET /v1/status).
func (c *Client) Status(ctx context.Context) (*api.Status, error) {
	var out api.Status
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Manifest holt das Versions-Manifest (GET /v1/manifest).
func (c *Client) Manifest(ctx context.Context) (*api.Manifest, error) {
	var out api.Manifest
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/manifest", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Artifact lädt die Datei name, die der Koordinator selbst ausliefert
// (GET /v1/artifacts/{name}; im Manifest als URL wanderwelt:<name>). size
// ist -1, wenn unbekannt. Den Hash prüft der Aufrufer. Die Dauer begrenzt
// nur ctx.
func (c *Client) Artifact(ctx context.Context, name string) (body io.ReadCloser, size int64, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.send(req)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// Play meldet den Wunsch zu spielen (POST /v1/play). Kann bis zum Ende des
// Sammelfensters (etwa 5 s) blockieren.
func (c *Client) Play(ctx context.Context, req api.PlayRequest) (*api.PlayResponse, error) {
	var out api.PlayResponse
	if err := c.call(ctx, callTimeout, http.MethodPost, "/v1/play", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Heartbeat schickt das Lebenszeichen des Hosts (POST /v1/lease/heartbeat).
// 409 → ErrLeaseLost.
//
// Ein neuer Koordinator antwortet 200 mit api.HeartbeatResponse, ein
// älterer 204 ohne Körper: Dann (und bei leerem Körper) ist das Ergebnis
// nil, nil. Lässt sich eine 2xx-Antwort nicht lesen, ist das Lebenszeichen
// trotzdem angekommen – kein Fehler (der führte beim Host zum Fencing),
// sondern nil, nil und eine Zeile im Log.
func (c *Client) Heartbeat(ctx context.Context, req api.HeartbeatRequest) (*api.HeartbeatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := c.newRequest(ctx, http.MethodPost, "/v1/lease/heartbeat", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	resp, err := c.send(hreq)
	if err != nil {
		return nil, leaseErr(err)
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		log.Printf("Koordinator: Antwort auf das Lebenszeichen nicht lesbar: %v", err)
		return nil, nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var out api.HeartbeatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		log.Printf("Koordinator: Antwort auf das Lebenszeichen nicht lesbar: %v", err)
		return nil, nil
	}
	return &out, nil
}

// ErrNoHandover: Der Koordinator lehnt die Übergabe mit 409 ab (kein
// Kandidat, keine Ersatz-Ports konfiguriert, Zuschlag nicht mehr aktuell
// …). Der Grund aus der Antwort steht im Fehlertext. Aufrufer prüfen mit
// errors.Is.
var ErrNoHandover = errors.New("keine Übergabe möglich")

// Presence meldet die Bereitschaft dieses Starters, das Hosten zu übernehmen
// (POST /v1/presence). Die Antwort trägt ein Übergabe-Angebot, wenn der
// Koordinator diesen PC als neuen Host gewählt hat.
func (c *Client) Presence(ctx context.Context, req api.PresenceRequest) (*api.PresenceResponse, error) {
	var out api.PresenceResponse
	if err := c.call(ctx, callTimeout, http.MethodPost, "/v1/presence", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HandoverStart beginnt die Übergabe des Zuschlags leaseID an einen
// Kandidaten (POST /v1/handover/start). 409 → ErrNoHandover mit dem Grund
// des Koordinators.
func (c *Client) HandoverStart(ctx context.Context, leaseID int64, machineID string) (*api.HandoverStartResponse, error) {
	var out api.HandoverStartResponse
	err := c.call(ctx, callTimeout, http.MethodPost, "/v1/handover/start",
		api.HandoverStartRequest{LeaseID: leaseID, MachineID: machineID}, &out)
	if err != nil {
		if se, ok := errors.AsType[*StatusError](err); ok && se.StatusCode == http.StatusConflict {
			msg := se.Message
			if msg == "" {
				msg = "ohne Angabe"
			}
			return nil, fmt.Errorf("%w: %s", ErrNoHandover, msg)
		}
		return nil, err
	}
	return &out, nil
}

// HandoverReady meldet, dass der Übergabe-Stand eingereicht ist
// (POST /v1/handover/ready); danach reicht der alte Zuschlag nichts mehr
// ein.
func (c *Client) HandoverReady(ctx context.Context, req api.HandoverReadyRequest) error {
	return c.call(ctx, callTimeout, http.MethodPost, "/v1/handover/ready", req, nil)
}

// Release gibt den Zuschlag zurück (POST /v1/lease/release).
func (c *Client) Release(ctx context.Context, req api.ReleaseRequest) error {
	return c.call(ctx, callTimeout, http.MethodPost, "/v1/lease/release", req, nil)
}

// HostSettings holt Welt-Einstellungen, Whitelist und Ops für den Host
// (GET /v1/host/settings).
func (c *Client) HostSettings(ctx context.Context) (*api.HostSettings, error) {
	var out api.HostSettings
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/host/settings", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MissingChunk: so viele Hashes schickt Missing höchstens je Anfrage.
const MissingChunk = 10_000

// Missing liefert die Hashes aus hashes, die der Koordinator noch nicht hat
// (POST /v1/blobs/missing), in Anfragen zu höchstens MissingChunk Hashes.
// Bei leerer Liste gibt es keine Anfrage.
func (c *Client) Missing(ctx context.Context, hashes []string) ([]string, error) {
	var missing []string
	for chunk := range slices.Chunk(hashes, MissingChunk) {
		in := struct {
			Hashes []string `json:"hashes"`
		}{chunk}
		var out struct {
			Missing []string `json:"missing"`
		}
		if err := c.call(ctx, callTimeout, http.MethodPost, "/v1/blobs/missing", in, &out); err != nil {
			return nil, err
		}
		missing = append(missing, out.Missing...)
	}
	return missing, nil
}

// MissingBlobsError: Der Koordinator hat Blobs einer Fetch-Anfrage nicht
// (404); Hashes sind die fehlenden, jeder einmal.
type MissingBlobsError struct {
	Hashes []string
}

func (e *MissingBlobsError) Error() string {
	return fmt.Sprintf("Koordinator: %d Blobs fehlen (%s …)", len(e.Hashes), e.Hashes[0])
}

// FetchBlobs holt die Blobs hashes in einer Anfrage (POST /v1/blobs/fetch):
// höchstens api.FetchMaxBlobs Hashes und api.TransferMaxBytes Bytes, sonst
// antwortet der Koordinator 413 (*StatusError). Der Körper enthält die
// Blobs in Anfrage-Reihenfolge direkt hintereinander; der Aufrufer kennt die
// Größen, zerlegt ihn, prüft jeden Hash und schließt ihn. Fehlen Blobs,
// ist der Fehler *MissingBlobsError. Die Dauer begrenzt nur ctx.
func (c *Client) FetchBlobs(ctx context.Context, hashes []string) (io.ReadCloser, error) {
	if len(hashes) == 0 || len(hashes) > api.FetchMaxBlobs {
		return nil, fmt.Errorf("FetchBlobs: %d Hashes (erlaubt 1–%d)", len(hashes), api.FetchMaxBlobs)
	}
	b, err := json.Marshal(struct {
		Hashes []string `json:"hashes"`
	}{hashes})
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/blobs/fetch", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Koordinator: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		defer drainClose(resp)
		var out struct {
			Missing []string `json:"missing"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) == nil && len(out.Missing) > 0 {
			return nil, &MissingBlobsError{Hashes: out.Missing}
		}
		return nil, &StatusError{Method: req.Method, Path: req.URL.Path, StatusCode: resp.StatusCode, Message: "Blobs fehlen"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer drainClose(resp)
		return nil, statusError(req, resp)
	}
	return resp.Body, nil
}

// BlobFrame ist ein Blob für PutBlobs: Data liefert genau Size Bytes.
type BlobFrame struct {
	SHA256 string
	Size   int64
	Data   io.Reader
}

// PutBlobs lädt frames in einer Anfrage hoch (POST /v1/blobs/batch): je
// Blob 32 Byte SHA-256 (roh), uint64 big-endian Größe, Daten. Höchstens
// api.BatchMaxFrames Rahmen und zusammen höchstens api.TransferMaxBytes
// Bytes samt Rahmenköpfen. Die Daten werden
// beim Senden gelesen, nicht vorher gesammelt. Der Koordinator prüft jeden
// Rahmen gegen seinen Hash. Die Dauer begrenzt nur ctx.
func (c *Client) PutBlobs(ctx context.Context, frames []BlobFrame) error {
	if len(frames) > api.BatchMaxFrames {
		return fmt.Errorf("PutBlobs: %d Rahmen, höchstens %d je Anfrage", len(frames), api.BatchMaxFrames)
	}
	var total int64
	parts := make([]io.Reader, 0, 2*len(frames))
	for _, f := range frames {
		raw, err := hex.DecodeString(f.SHA256)
		if err != nil || len(raw) != sha256.Size || f.Size < 0 {
			return fmt.Errorf("PutBlobs: ungültiger Rahmen %q (%d Bytes)", f.SHA256, f.Size)
		}
		total += api.BatchFrameHeader + f.Size
		if total > api.TransferMaxBytes {
			return fmt.Errorf("PutBlobs: mehr als %d Bytes in einer Anfrage", api.TransferMaxBytes)
		}
		parts = append(parts, bytes.NewReader(binary.BigEndian.AppendUint64(raw, uint64(f.Size))), io.LimitReader(f.Data, f.Size))
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/blobs/batch", io.MultiReader(parts...))
	if err != nil {
		return err
	}
	req.ContentLength = total
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.send(req)
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// blobBusyRetry sind die Wartezeiten (zusammen 5 s), nach denen PutBlob
// einen Blob neu beginnt, wenn der Koordinator ihn mit 409 als „wird gerade
// schon hochgeladen“ ablehnt: Ein abgebrochener Upload desselben Blobs (etwa
// der vorige Versuch dieses Starters) belegt ihn dort noch, bis sein Handler
// den Abbruch bemerkt hat. Im Test austauschbar.
var blobBusyRetry = []time.Duration{
	200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond,
	1600 * time.Millisecond, 2 * time.Second,
}

// PutBlob lädt den Blob sha (SHA-256, hex) mit size Bytes aus f hoch
// (PUT /v1/blobs/{sha}), der Reihe nach in Teilen von höchstens PartSize mit
// "Content-Range: bytes a-b/total". Ein leerer Blob geht als ein leerer Teil
// mit "Content-Range: bytes */0". Die Dauer begrenzt nur ctx. Lädt gerade
// jemand anderes denselben Blob hoch (409; der Endpunkt antwortet nur dann
// mit 409), beginnt PutBlob nach kurzer Pause von vorn, höchstens
// len(blobBusyRetry)-mal.
func (c *Client) PutBlob(ctx context.Context, sha string, f io.ReaderAt, size int64) error {
	if size < 0 {
		return fmt.Errorf("Blob %s: negative Größe %d", sha, size)
	}
	for try := 0; ; try++ {
		err := c.putBlob(ctx, sha, f, size)
		if err == nil || !blobBusy(err) || try >= len(blobBusyRetry) {
			return err
		}
		t := time.NewTimer(blobBusyRetry[try])
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return err
		}
	}
}

// blobBusy: Der Koordinator lehnt den Blob-Teil ab, weil derselbe Blob
// gerade schon hochgeladen wird.
func blobBusy(err error) bool {
	se, ok := errors.AsType[*StatusError](err)
	return ok && se.Method == http.MethodPut && se.StatusCode == http.StatusConflict
}

// putBlob ist ein Versuch von PutBlob, vom ersten Teil an.
func (c *Client) putBlob(ctx context.Context, sha string, f io.ReaderAt, size int64) error {
	path := "/v1/blobs/" + url.PathEscape(sha)
	off := int64(0)
	for {
		n := min(PartSize, size-off)
		complete, err := c.putPart(ctx, path, f, off, n, size)
		if err != nil {
			return fmt.Errorf("Blob %s: %w", sha, err)
		}
		if complete {
			return nil
		}
		off += n
		if off >= size {
			return fmt.Errorf("Blob %s: Koordinator meldet den Blob nach dem letzten Teil nicht als vollständig", sha)
		}
	}
}

// putPart schickt einen Teil; complete ist true bei 201, false bei 202.
func (c *Client) putPart(ctx context.Context, path string, f io.ReaderAt, off, n, total int64) (complete bool, err error) {
	var body io.Reader = http.NoBody
	if n > 0 {
		body = io.NewSectionReader(f, off, n)
	}
	req, err := c.newRequest(ctx, http.MethodPut, path, body)
	if err != nil {
		return false, err
	}
	req.ContentLength = n
	if n > 0 {
		// Erlaubt dem Transport, den Teil auf einer frischen Verbindung zu
		// wiederholen, wenn eine wiederverwendete Verbindung schon zu war.
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(f, off, n)), nil
		}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if total == 0 {
		req.Header.Set("Content-Range", "bytes */0")
	} else {
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, total))
	}

	resp, err := c.send(req)
	if err != nil {
		return false, err
	}
	defer drainClose(resp)
	switch resp.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusAccepted:
		return false, nil
	default:
		return false, fmt.Errorf("Koordinator: PUT %s: unerwartete Antwort %d", req.URL.Path, resp.StatusCode)
	}
}

// GetBlob lädt den Blob sha (SHA-256, hex) nach w (GET /v1/blobs/{sha}) und
// prüft dabei den Hash. Bei einem Fehler hat w unter Umständen schon Daten
// bekommen; der Aufrufer verwirft sie. Die Dauer begrenzt nur ctx.
func (c *Client) GetBlob(ctx context.Context, sha string, w io.Writer) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/blobs/"+url.PathEscape(sha), nil)
	if err != nil {
		return err
	}
	resp, err := c.send(req)
	if err != nil {
		return fmt.Errorf("Blob %s: %w", sha, err)
	}
	defer drainClose(resp)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return fmt.Errorf("Blob %s: %w", sha, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sha) {
		return fmt.Errorf("Blob %s: Inhalt hat den Hash %s", sha, got)
	}
	return nil
}

// Head holt den aktuellen Stand der Welt (GET /v1/generations/head). Gibt es
// noch keine Welt (404), ist das Ergebnis nil, nil.
func (c *Client) Head(ctx context.Context) (*api.Generation, error) {
	var out api.Generation
	err := c.call(ctx, callTimeout, http.MethodGet, "/v1/generations/head", nil, &out)
	if se, ok := errors.AsType[*StatusError](err); ok && se.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Commit reicht einen Stand ein (POST /v1/generations). 409 (Zuschlag
// ungültig oder Basis falsch) → ErrLeaseLost; 400 → *StatusError.
func (c *Client) Commit(ctx context.Context, req api.CommitRequest) (*api.Generation, error) {
	var out api.Generation
	if err := leaseErr(c.call(ctx, commitTimeout, http.MethodPost, "/v1/generations", req, &out)); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutProfile meldet das Leistungsprofil des PCs machine
// (PUT /v1/profile/{machine}).
func (c *Client) PutProfile(ctx context.Context, machine string, p api.Profile) error {
	return c.call(ctx, callTimeout, http.MethodPut, "/v1/profile/"+url.PathEscape(machine), p, nil)
}

// SpeedTest misst die Upload-Rate zum Koordinator in Mbit/s: 8 MiB
// Zufallsdaten an POST /v1/speedtest. Die Zeit läuft ab der fertigen
// Verbindung (ohne Verbindungsaufbau und TLS). Dauert es länger als 60 s,
// zählt das bis dahin Gesendete.
func (c *Client) SpeedTest(ctx context.Context) (mbps float64, err error) {
	data := make([]byte, speedTestSize)
	rand.Read(data)

	tctx, cancel := context.WithTimeout(ctx, speedTestTimeout)
	defer cancel()
	var start atomic.Int64
	trace := &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { start.Store(time.Now().UnixNano()) },
	}
	body := &countingReader{r: bytes.NewReader(data)}
	req, err := c.newRequest(httptrace.WithClientTrace(tctx, trace), http.MethodPost, "/v1/speedtest", body)
	if err != nil {
		return 0, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")

	begin := time.Now()
	resp, err := c.send(req)
	end := time.Now()
	if s := start.Load(); s != 0 {
		begin = time.Unix(0, s)
	}
	elapsed := end.Sub(begin).Seconds()

	if err != nil {
		// Nur die eigene Zeitgrenze, nicht der Abbruch durch den Aufrufer.
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && body.n.Load() > 0 && elapsed > 0 {
			return float64(body.n.Load()*8) / elapsed / 1e6, nil
		}
		return 0, err
	}
	drainClose(resp)
	if elapsed <= 0 {
		return 0, errors.New("Koordinator: Geschwindigkeitsmessung ohne messbare Dauer")
	}
	return float64(len(data)*8) / elapsed / 1e6, nil
}

type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.n.Add(int64(n))
	return n, err
}

// call schickt in (falls nicht nil) als JSON und liest die Antwort nach out
// (falls nicht nil). timeout begrenzt den ganzen Aufruf samt Antwortkörper.
func (c *Client) call(ctx context.Context, timeout time.Duration, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if out != nil {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := c.send(req)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("Koordinator: %s %s: Antwort nicht lesbar: %w", method, path, err)
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.email != "" {
		req.Header.Set("X-Tunnel-Account-Email", c.email)
	}
	return req, nil
}

// send führt req aus. Antworten außerhalb von 2xx werden zu *StatusError.
func (c *Client) send(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Koordinator: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer drainClose(resp)
		return nil, statusError(req, resp)
	}
	return resp, nil
}

func statusError(req *http.Request, resp *http.Response) *StatusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var e struct {
		Error string `json:"error"`
	}
	msg := ""
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	} else {
		msg = shorten(strings.TrimSpace(strings.ToValidUTF8(string(b), "?")), maxErrorText)
	}
	return &StatusError{Method: req.Method, Path: req.URL.Path, StatusCode: resp.StatusCode, Message: msg}
}

// shorten kürzt s auf höchstens limit Bytes, ohne ein Zeichen zu zerteilen.
func shorten(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …"
}

// leaseErr macht aus 409 ErrLeaseLost (der *StatusError bleibt erreichbar).
func leaseErr(err error) error {
	if se, ok := errors.AsType[*StatusError](err); ok && se.StatusCode == http.StatusConflict {
		return fmt.Errorf("%w: %w", ErrLeaseLost, err)
	}
	return err
}

// drainClose liest einen kleinen Rest des Körpers, damit die Verbindung
// wiederverwendet werden kann, und schließt ihn.
func drainClose(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
}

// ---- Fenster: Stände, Zurückholen, Mitglieder, Speicherbudget ----

// ErrNotSupported: Der Koordinator kennt den Endpunkt nicht – er ist älter
// als dieser Starter. Der *StatusError bleibt erreichbar; Aufrufer prüfen
// mit errors.Is.
var ErrNotSupported = errors.New("der Koordinator kennt diesen Endpunkt nicht")

// Generations holt die Stände aller Welten (GET /v1/generations).
func (c *Client) Generations(ctx context.Context) (*api.GenerationsResponse, error) {
	var out api.GenerationsResponse
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/generations", nil, &out); err != nil {
		return nil, notSupported(err, true)
	}
	return &out, nil
}

// Restore holt einen Stand zurück (POST /v1/restore; nur Admins). 404 mit
// eigener Meldung: unbekannter Stand; 409: es steht schon ein Zurückholen
// aus oder die Minecraft-Version passt nicht.
func (c *Client) Restore(ctx context.Context, req api.RestoreRequest) (*api.RestoreResponse, error) {
	var out api.RestoreResponse
	if err := c.call(ctx, callTimeout, http.MethodPost, "/v1/restore", req, &out); err != nil {
		return nil, notSupported(err, false)
	}
	return &out, nil
}

// Members holt die Mitgliederliste (GET /v1/members; nur Admins).
func (c *Client) Members(ctx context.Context) ([]api.MemberInfo, error) {
	var out []api.MemberInfo
	if err := c.call(ctx, callTimeout, http.MethodGet, "/v1/members", nil, &out); err != nil {
		return nil, notSupported(err, true)
	}
	return out, nil
}

// SetRole setzt Admin- und Op-Recht des Mitglieds email
// (PUT /v1/members/{email}; nur Admins). 404 mit eigener Meldung: kein
// Mitglied; 409: danach gäbe es keinen Admin mehr.
func (c *Client) SetRole(ctx context.Context, email string, r api.RoleRequest) (*api.MemberInfo, error) {
	var out api.MemberInfo
	if err := c.call(ctx, callTimeout, http.MethodPut, "/v1/members/"+url.PathEscape(email), r, &out); err != nil {
		return nil, notSupported(err, false)
	}
	return &out, nil
}

// SetMemberMinecraft setzt den Minecraft-Namen des Mitglieds email
// (PUT /v1/members/{email}/minecraft; nur Admins, jederzeit). 400: der Name
// ist ungültig, vergeben oder Mojang kennt ihn nicht (*StatusError mit dem
// Text des Koordinators); 503: Mojang nicht erreichbar.
func (c *Client) SetMemberMinecraft(ctx context.Context, email, name string) error {
	return notSupported(c.call(ctx, callTimeout, http.MethodPut, "/v1/members/"+url.PathEscape(email)+"/minecraft",
		api.MinecraftNameRequest{MinecraftName: name}, nil), false)
}

// SetStorageBudget setzt das Speicherbudget (PUT /v1/storage/budget; nur
// Admins; 400 bei einem unzulässigen Wert).
func (c *Client) SetStorageBudget(ctx context.Context, req api.BudgetRequest) (*api.StorageInfo, error) {
	var out api.StorageInfo
	if err := c.call(ctx, callTimeout, http.MethodPut, "/v1/storage/budget", req, &out); err != nil {
		return nil, notSupported(err, true)
	}
	return &out, nil
}

// notSupported macht aus der Antwort eines älteren Koordinators, der den
// Endpunkt nicht kennt, ErrNotSupported: 405, oder 404 von seinem
// Auffang-Handler („unbekannter Endpunkt …“) bzw. von net/http. anyNotFound:
// Der Endpunkt selbst antwortet nie 404, jede 404 heißt also „unbekannt“.
func notSupported(err error, anyNotFound bool) error {
	se, ok := errors.AsType[*StatusError](err)
	if !ok {
		return err
	}
	unknown := se.StatusCode == http.StatusMethodNotAllowed ||
		se.StatusCode == http.StatusNotFound &&
			(anyNotFound || strings.HasPrefix(se.Message, "unbekannter Endpunkt") || se.Message == "404 page not found")
	if unknown {
		return fmt.Errorf("%w: %w", ErrNotSupported, err)
	}
	return err
}

// ErrNameAlreadySet: Das Mitglied hat schon einen Minecraft-Namen (409).
var ErrNameAlreadySet = errors.New("der Minecraft-Name ist schon gesetzt")

// SetMyMinecraft meldet den eigenen Minecraft-Namen
// (POST /v1/me/minecraft). 409: ErrNameAlreadySet; 400: der Name ist
// unbekannt oder vergeben (*StatusError mit dem Text des Koordinators);
// 503: Mojang nicht erreichbar.
func (c *Client) SetMyMinecraft(ctx context.Context, name string) error {
	err := c.call(ctx, callTimeout, http.MethodPost, "/v1/me/minecraft", api.MinecraftNameRequest{MinecraftName: name}, nil)
	if se, ok := errors.AsType[*StatusError](err); ok && se.StatusCode == http.StatusConflict {
		return fmt.Errorf("%w: %w", ErrNameAlreadySet, err)
	}
	return err
}
