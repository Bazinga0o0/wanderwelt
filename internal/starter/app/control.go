package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/starter/fensterapi"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// Steuerung der laufenden Instanz: Sie lauscht auf 127.0.0.1 auf einem
// zufälligen Port und legt Port und ein Zufallstoken in Home()/control.json
// ab. Ein zweiter Start (etwa die Desktop-Verknüpfung „wanderwelt.exe
// play“) liest die Datei und schickt seinen Wunsch dorthin; das Fenster
// („wanderwelt.exe fenster“) holt dort Zustand und Daten und löst Aktionen
// aus (fenster_api.go, Schnittstelle in internal/starter/fensterapi).

const (
	// Aktionen eines zweiten Starts (Kommandozeile). „play“ benutzt auch das
	// Fenster.
	actionPlay = "play"
	actionShow = "show"
	// actionJoin: wanderwelt://beitreten wurde geöffnet; ohne Daten, öffnet
	// nur das Fenster (beitritt_code.go).
	actionJoin = "beitreten"
	// actionImportCLI: „wanderwelt.exe import -ja <ordner>“ übergibt die Welt
	// (Körper: importRequest), Ordner beliebig. „import“ ist die Aktion des
	// Fensters: nur Speicherstände aus dem saves-Ordner.
	actionImportCLI = "import-kommandozeile"
	// actionImportOld: So hieß die Übergabe bis zur Fenster-Version; eine
	// ältere laufende Instanz kennt nur sie.
	actionImportOld = "import"
	// actionUpdate: Eine neue Version wartet auf die Sperre; die laufende
	// Instanz beendet das Hosten sauber und endet.
	actionUpdate = "update"

	maxControlBody = 8 << 10

	// sendWait: So lange versucht ein zweiter Start die laufende Instanz zu
	// erreichen (sie kann gerade erst starten).
	sendWait = 20 * time.Second
)

func controlPath() string { return filepath.Join(group.Home(), fensterapi.ControlFile) }

// controlServer ist die Steuerung der laufenden Instanz.
type controlServer struct {
	srv   *http.Server
	ln    net.Listener
	token string
	host  string
}

// fensterSteuerung bedient die Endpunkte des Fensters (App, fenster_api.go).
// Beide Methoden schreiben die Antwort selbst, auch 404 für Unbekanntes.
type fensterSteuerung interface {
	// ansicht beantwortet GET /v1/<name>.
	ansicht(w http.ResponseWriter, r *http.Request, name string)
	// aktion führt POST /v1/<name> aus (body: gelesener Körper).
	aktion(w http.ResponseWriter, r *http.Request, name string, body []byte)
}

// startControl lauscht auf 127.0.0.1:<zufällig> und schreibt control.json.
// handle bekommt jede Aktion eines zweiten Starts samt Körper; es muss
// sofort zurückkehren. fenster (nil: keins) bedient die Endpunkte des
// Fensters. Jeder Endpunkt verlangt Token und Host-Kopf (guard).
func startControl(handle func(action string, body []byte), fenster fensterSteuerung) (*controlServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("Steuerung: %w", err)
	}
	var raw [32]byte
	rand.Read(raw[:])
	cs := &controlServer{ln: ln, token: hex.EncodeToString(raw[:]), host: ln.Addr().String()}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/{action}", cs.guard(func(w http.ResponseWriter, r *http.Request) {
		action := r.PathValue("action")
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxControlBody))
		if err != nil {
			http.Error(w, "Körper zu groß", http.StatusRequestEntityTooLarge)
			return
		}
		switch {
		case action == actionPlay || action == actionShow || action == actionJoin || action == actionImportCLI || action == actionUpdate:
			log.Printf("Steuerung: %s", action)
			handle(action, body)
			w.WriteHeader(http.StatusAccepted)
		case fenster != nil:
			fenster.aktion(w, r, action, body)
		default:
			http.Error(w, "unbekannte Aktion", http.StatusNotFound)
		}
	}))
	if fenster != nil {
		mux.HandleFunc("GET /v1/{name}", cs.guard(func(w http.ResponseWriter, r *http.Request) {
			fenster.ansicht(w, r, r.PathValue("name"))
		}))
	}
	cs.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Durchgereichte Aufrufe beim Koordinator dauern höchstens koordWait.
		WriteTimeout:   koordWait + 5*time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	go func() {
		if err := cs.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("Steuerung: %v", err)
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	data, _ := json.MarshalIndent(fensterapi.ControlInfo{Port: port, Token: cs.token, PID: os.Getpid()}, "", "  ")
	if err := writeFileAtomic(controlPath(), append(data, '\n'), 0o600); err != nil {
		cs.srv.Close()
		return nil, fmt.Errorf("Steuerung: %w", err)
	}
	return cs, nil
}

// guard lässt nur Anfragen an genau diese Adresse (gegen DNS-Rebinding aus
// dem Browser) und mit dem Token aus control.json durch; sonst 403.
func (cs *controlServer) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != cs.host || !cs.authorized(r.Header.Get("Authorization")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (cs *controlServer) authorized(h string) bool {
	tok, ok := strings.CutPrefix(h, "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(tok), []byte(cs.token)) == 1
}

// Close beendet die Steuerung und entfernt control.json, wenn sie noch zu
// dieser Instanz gehört.
func (cs *controlServer) Close() {
	cs.srv.Close()
	if data, err := os.ReadFile(controlPath()); err == nil {
		var ci fensterapi.ControlInfo
		if json.Unmarshal(data, &ci) == nil && ci.Token == cs.token {
			os.Remove(controlPath())
		}
	}
}

// sendToRunning schickt action an die laufende Instanz. Sie kann gerade erst
// starten (control.json fehlt noch oder zeigt auf den vorigen Lauf); es wird
// bis sendWait wiederholt.
func sendToRunning(action string) error { return sendToRunningBody(action, nil) }

// errUnknownAction: Die laufende Instanz kennt die Aktion nicht (eine
// ältere Version).
var errUnknownAction = errors.New("die laufende wanderwelt-Version kennt diese Aktion nicht")

// sendToRunningBody ist sendToRunning mit Körper.
func sendToRunningBody(action string, body []byte) error {
	deadline := time.Now().Add(sendWait)
	hc := &http.Client{Timeout: 5 * time.Second}
	var last error
	for {
		last = sendOnce(hc, action, body)
		if last == nil || errors.Is(last, errUnknownAction) {
			return last
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wanderwelt läuft schon, reagiert aber nicht: %w", last)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func sendOnce(hc *http.Client, action string, body []byte) error {
	data, err := os.ReadFile(controlPath())
	if err != nil {
		return err
	}
	var ci fensterapi.ControlInfo
	if err := json.Unmarshal(data, &ci); err != nil {
		return fmt.Errorf("%s: %w", controlPath(), err)
	}
	if ci.Port <= 0 || ci.Port > 65535 || ci.Token == "" {
		return fmt.Errorf("%s: unvollständig", controlPath())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/%s", ci.Port, action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ci.Token)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("%w (%s)", errUnknownAction, action)
	}
	return fmt.Errorf("Steuerung antwortet %d", resp.StatusCode)
}

// writeFileAtomic schreibt data über eine Temp-Datei neben path.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	os.Chmod(tmp, perm)
	for i := range 10 {
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
	}
	return err
}
