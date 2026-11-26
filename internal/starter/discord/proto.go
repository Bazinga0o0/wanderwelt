package discord

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Operationen des Discord-IPC.
const (
	opHandshake uint32 = 0
	opFrame     uint32 = 1
	opClose     uint32 = 2
	opPing      uint32 = 3
	opPong      uint32 = 4

	maxFrame = 64 << 10
)

// writeFrame schreibt einen Rahmen: Operation und Länge (je uint32, little
// endian), dann JSON – in einem Write.
func writeFrame(w io.Writer, op uint32, payload []byte) error {
	buf := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(buf[0:], op)
	binary.LittleEndian.PutUint32(buf[4:], uint32(len(payload)))
	copy(buf[8:], payload)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (op uint32, payload []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	op = binary.LittleEndian.Uint32(hdr[0:])
	n := binary.LittleEndian.Uint32(hdr[4:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("Rahmen zu groß (%d Bytes)", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return op, payload, err
}

type timestamps struct {
	Start int64 `json:"start"`
}
type assets struct {
	LargeImage string `json:"large_image"`
	LargeText  string `json:"large_text,omitempty"`
}
type button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type party struct {
	ID   string `json:"id"`
	Size [2]int `json:"size"`
}
type secrets struct {
	Join string `json:"join"`
}
type activityJSON struct {
	Details    string      `json:"details"`
	State      string      `json:"state"`
	Timestamps *timestamps `json:"timestamps,omitempty"`
	Assets     assets      `json:"assets"`
	Buttons    []button    `json:"buttons"`
	Party      *party      `json:"party,omitempty"`
	Secrets    *secrets    `json:"secrets,omitempty"`
}

// activityPayload ist das JSON für SET_ACTIVITY; a nil löscht die Aktivität.
func activityPayload(a *Activity, pid int, nonce string) ([]byte, error) {
	args := map[string]any{"pid": pid, "activity": nil}
	if a != nil {
		aj := activityJSON{
			Details: Details,
			State:   a.State,
			Assets:  assets{LargeImage: LargeImage, LargeText: Details},
			Buttons: []button{{Label: ButtonText, URL: ButtonURL}},
		}
		if JoinEnabled && a.JoinSecret != "" && a.PartyMax > 0 {
			aj.Party = &party{ID: a.PartyID, Size: [2]int{a.PartySize, a.PartyMax}}
			aj.Secrets = &secrets{Join: a.JoinSecret}
		}
		if !a.Start.IsZero() {
			aj.Timestamps = &timestamps{Start: a.Start.Unix()}
		}
		args["activity"] = aj
	}
	return json.Marshal(map[string]any{"cmd": "SET_ACTIVITY", "args": args, "nonce": nonce})
}

func subscribePayload(evt, nonce string) []byte {
	b, _ := json.Marshal(map[string]any{"cmd": "SUBSCRIBE", "evt": evt, "args": map[string]any{}, "nonce": nonce})
	return b
}

func handshakePayload(clientID string) []byte {
	b, _ := json.Marshal(map[string]any{"v": 1, "client_id": clientID})
	return b
}

var errClosed = errors.New("Discord hat die Verbindung geschlossen")

// response ist die Antwort auf einen Befehl (nur was uns interessiert).
type response struct {
	Cmd   string `json:"cmd"`
	Evt   string `json:"evt"`
	Nonce string `json:"nonce"`
	Data  struct {
		Message string `json:"message"`
		Secret  string `json:"secret"`
	} `json:"data"`
}

// evtJoin ist das Ereignis, das Discord schickt, wenn jemand „Beitreten“
// klickt.
const evtJoin = "ACTIVITY_JOIN"

// readResponse liest Rahmen bis zur Antwort auf einen Befehl. Ereignisse
// (cmd DISPATCH) gehen an onEvent; ist wantEvt nicht leer, beendet nur
// dieses Ereignis (etwa READY) das Lesen, sonst die erste Antwort, die kein
// Ereignis ist. PING wird beantwortet, CLOSE ist ein Fehler.
func readResponse(rw io.ReadWriter, wantEvt string, onEvent func(response)) (response, error) {
	for {
		r, ok, err := readOne(rw)
		if err != nil {
			return response{}, err
		}
		if !ok {
			continue
		}
		if r.Cmd == "DISPATCH" {
			if wantEvt != "" && r.Evt == wantEvt {
				return r, nil
			}
			if onEvent != nil {
				onEvent(r)
			}
			continue
		}
		if wantEvt == "" {
			return r, nil
		}
	}
}

// readOne liest einen Rahmen; ok false bei PING und fremden Operationen.
func readOne(rw io.ReadWriter) (r response, ok bool, err error) {
	op, p, err := readFrame(rw)
	if err != nil {
		return r, false, err
	}
	switch op {
	case opPing:
		return r, false, writeFrame(rw, opPong, p)
	case opClose:
		return r, false, errClosed
	case opFrame:
		if err := json.Unmarshal(p, &r); err != nil {
			return r, false, err
		}
		return r, true, nil
	}
	return r, false, nil
}
