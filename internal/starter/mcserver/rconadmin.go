package mcserver

// Eigene RCON-Verbindung für Admin-Befehle (Reiter „Server“).
//
// RCON von 26.2 zerlegt Antworten über 4096 Bytes in mehrere Pakete; gorcon
// liest nur das erste, der Rest käme mit der Antwort des nächsten Befehls –
// auf der gemeinsamen Verbindung des Starters also etwa bei „list“ oder beim
// Speichern. Darum laufen Admin-Befehle über eine eigene, kurzlebige
// Verbindung, die jede Antwort ganz liest: Nach dem ersten Antwortpaket geht
// ein Marker-Paket (unbekannter Typ) hinaus; der Server beantwortet es erst,
// wenn er die ganze Antwort geschrieben hat. Erst nach dem ersten Paket, weil
// der Server die Verbindung schließt, wenn zwei Pakete in einem Lesevorgang
// ankommen.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	rconAuth      = 3
	rconAuthResp  = 2
	rconExec      = 2
	rconMarker    = 0 // vom Server mit „Unknown request 0“ beantwortet
	rconMaxPacket = 1 << 16
	// maxIsolated: so viel einer Antwort wird gesammelt (der Rest gelesen
	// und verworfen, bis zu maxIsolatedRead).
	maxIsolated     = 64 << 10
	maxIsolatedRead = 4 << 20
	// isolatedTimeout: Obergrenze einer Sitzung, wenn ctx keine setzt.
	isolatedTimeout = 2 * time.Minute
)

// Isolated führt fn mit einer eigenen RCON-Verbindung zum laufenden Server
// aus (siehe Server.Isolated).
func (f *fabric) Isolated(ctx context.Context, fn func(run func(cmd string) (string, error)) error) error {
	rc, err := f.client()
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, isolatedTimeout)
		defer cancel()
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", rc.addr)
	if err != nil {
		return fmt.Errorf("mcserver: RCON (eigene Verbindung): %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	s := &rconSession{rw: conn}
	if err := s.auth(rc.password); err != nil {
		return fmt.Errorf("mcserver: RCON (eigene Verbindung): %w", err)
	}
	return fn(func(cmd string) (string, error) {
		out, err := s.exec(cmd)
		if err != nil {
			if ctx.Err() != nil {
				err = context.Cause(ctx)
			}
			return "", fmt.Errorf("mcserver: RCON %q: %w", shortCmd(cmd), err)
		}
		return out, nil
	})
}

// shortCmd kürzt einen Befehl für Fehlermeldungen.
func shortCmd(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// rconSession spricht das RCON-Protokoll über rw.
type rconSession struct {
	rw     io.ReadWriter
	nextID int32
	broken bool
}

func (s *rconSession) id() int32 {
	s.nextID++
	if s.nextID <= 0 {
		s.nextID = 1
	}
	return s.nextID
}

func (s *rconSession) write(id, typ int32, body string) error {
	if strings.IndexByte(body, 0) >= 0 {
		return errors.New("Nullbyte im Befehl")
	}
	if len(body) > 1446 {
		// Mehr nimmt der Server nicht in einem Lesevorgang an.
		return errors.New("Befehl zu lang für RCON")
	}
	b := make([]byte, 14+len(body))
	binary.LittleEndian.PutUint32(b[0:], uint32(10+len(body)))
	binary.LittleEndian.PutUint32(b[4:], uint32(id))
	binary.LittleEndian.PutUint32(b[8:], uint32(typ))
	copy(b[12:], body)
	_, err := s.rw.Write(b)
	return err
}

func (s *rconSession) read() (id, typ int32, body []byte, err error) {
	var h [4]byte
	if _, err := io.ReadFull(s.rw, h[:]); err != nil {
		return 0, 0, nil, err
	}
	n := binary.LittleEndian.Uint32(h[:])
	if n < 10 || n > rconMaxPacket {
		return 0, 0, nil, fmt.Errorf("ungültige Paketlänge %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(s.rw, b); err != nil {
		return 0, 0, nil, err
	}
	return int32(binary.LittleEndian.Uint32(b[0:])), int32(binary.LittleEndian.Uint32(b[4:])),
		b[8 : n-2], nil
}

func (s *rconSession) auth(password string) error {
	id := s.id()
	if err := s.write(id, rconAuth, password); err != nil {
		return err
	}
	for range 2 { // manche Server schicken vorher ein leeres Antwortpaket
		rid, typ, _, err := s.read()
		if err != nil {
			return err
		}
		if typ != rconAuthResp {
			continue
		}
		if rid != id {
			return errors.New("Anmeldung abgelehnt")
		}
		return nil
	}
	return errors.New("keine Antwort auf die Anmeldung")
}

// exec schickt cmd und liest die ganze Antwort (bis maxIsolated Bytes).
func (s *rconSession) exec(cmd string) (string, error) {
	if s.broken {
		return "", errors.New("Verbindung nach einem Fehler nicht mehr nutzbar")
	}
	out, err := s.execOnce(cmd)
	if err != nil {
		s.broken = true
	}
	return out, err
}

func (s *rconSession) execOnce(cmd string) (string, error) {
	id := s.id()
	if err := s.write(id, rconExec, cmd); err != nil {
		return "", err
	}
	var out strings.Builder
	read := 0
	add := func(b []byte) {
		read += len(b)
		if room := maxIsolated - out.Len(); room > 0 {
			out.Write(b[:min(len(b), room)])
		}
	}
	rid, _, body, err := s.read()
	if err != nil {
		return "", err
	}
	if rid != id {
		return "", fmt.Errorf("Antwort mit fremder Kennung %d statt %d", rid, id)
	}
	add(body)
	marker := s.id()
	if err := s.write(marker, rconMarker, ""); err != nil {
		return "", err
	}
	for {
		rid, _, body, err := s.read()
		if err != nil {
			return "", err
		}
		switch rid {
		case marker:
			return out.String(), nil
		case id:
			add(body)
			if read > maxIsolatedRead {
				return "", errors.New("Antwort zu lang")
			}
		default:
			return "", fmt.Errorf("Antwort mit fremder Kennung %d", rid)
		}
	}
}
