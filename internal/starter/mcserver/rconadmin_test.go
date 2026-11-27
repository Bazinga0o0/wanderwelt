package mcserver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

// Prepare schreibt banned-players.json bei jedem Start ganz aus den
// Host-Einstellungen: mit Banns die Liste (Format von 26.2), ohne Banns leer –
// ein alter Bann im Server-Ordner bleibt nie übrig.
func TestPrepareSchreibtBanns(t *testing.T) {
	root, java, jar := prepareDateien(t)
	srv := filepath.Join(root, "srv")
	prep := func(hs api.HostSettings) []map[string]string {
		t.Helper()
		if err := (&fabric{}).Prepare(t.Context(), PrepareInput{
			Dir: srv, Paths: &deps.Paths{Java: java, FabricServerJar: jar},
			Profile: api.Profile{XmxMB: minXmxMB}, Settings: hs,
		}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(srv, "banned-players.json"))
		if err != nil {
			t.Fatal(err)
		}
		var list []map[string]string
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		return list
	}
	gast := api.Player{UUID: "99999999-9999-4999-8999-999999999999", Name: "Gast1"}
	troll := api.Player{UUID: "77777777-7777-4777-8777-777777777777", Name: "Troll"}
	list := prep(api.HostSettings{Whitelist: []api.Player{gast}, Bans: []api.Player{troll}})
	if len(list) != 1 || list[0]["uuid"] != troll.UUID || list[0]["name"] != "Troll" || list[0]["expires"] != "forever" ||
		list[0]["reason"] != BanReason || list[0]["created"] == "" {
		t.Fatalf("Banns: %v", list)
	}
	if wl, _ := os.ReadFile(filepath.Join(srv, "whitelist.json")); !strings.Contains(string(wl), "Gast1") {
		t.Fatalf("Gast fehlt in whitelist.json: %s", wl)
	}
	if list := prep(api.HostSettings{}); len(list) != 0 {
		t.Fatalf("ohne Banns: %v", list)
	}
	// Eine kaputte UUID verhindert den Start (wie bei Whitelist und Ops).
	err := (&fabric{}).Prepare(t.Context(), PrepareInput{
		Dir: srv, Paths: &deps.Paths{Java: java, FabricServerJar: jar}, Profile: api.Profile{XmxMB: minXmxMB},
		Settings: api.HostSettings{Bans: []api.Player{{UUID: "x", Name: "Troll"}}},
	})
	if err == nil {
		t.Fatal("kaputte UUID angenommen")
	}
}

// fakeRCON benimmt sich wie RCON von 26.2: Es liest jedes Paket mit einem
// eigenen Lesevorgang und schließt die Verbindung, wenn darin mehr als ein
// Paket steckt; lange Antworten zerlegt es in Pakete zu 4096 Bytes; auf
// unbekannte Typen antwortet es „Unknown request“.
type fakeRCON struct {
	ln       net.Listener
	password string
	answer   func(cmd string) string

	mu    sync.Mutex
	conns int
	cmds  []string
}

func newFakeRCON(t *testing.T, answer func(string) string) *fakeRCON {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRCON{ln: ln, password: "geheim", answer: answer}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeRCON) serve(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 1460)
	authed := false
	send := func(id, typ int32, body string) {
		b := make([]byte, 14+len(body))
		binary.LittleEndian.PutUint32(b[0:], uint32(10+len(body)))
		binary.LittleEndian.PutUint32(b[4:], uint32(id))
		binary.LittleEndian.PutUint32(b[8:], uint32(typ))
		copy(b[12:], body)
		c.Write(b)
	}
	for {
		n, err := c.Read(buf)
		if err != nil || n < 14 {
			return
		}
		if int(binary.LittleEndian.Uint32(buf)) != n-4 {
			return // zwei Pakete in einem Lesevorgang: wie der echte Server
		}
		id := int32(binary.LittleEndian.Uint32(buf[4:]))
		typ := int32(binary.LittleEndian.Uint32(buf[8:]))
		body := string(buf[12 : n-2])
		switch {
		case typ == 3:
			if body == f.password {
				authed = true
				send(id, 2, "")
			} else {
				send(-1, 2, "")
			}
		case typ == 2 && authed:
			f.mu.Lock()
			f.cmds = append(f.cmds, body)
			f.mu.Unlock()
			out := f.answer(body)
			for {
				part := out[:min(len(out), 4096)]
				send(id, 0, part)
				out = out[len(part):]
				if out == "" {
					break
				}
				time.Sleep(time.Millisecond) // eigene Pakete, wie beim Server
			}
		default:
			send(id, 0, "Unknown request "+string(rune('0'+typ)))
		}
	}
}

func (f *fakeRCON) fabric() *fabric {
	return &fabric{proc: runningProcess(), rc: newRCON(f.ln.Addr().String(), f.password)}
}

// Eine lange Antwort (mehrere Pakete) kommt ganz an, und der nächste Befehl
// bekommt nur seine eigene Antwort.
func TestIsolatedLiestMehrteiligeAntworten(t *testing.T) {
	lang := strings.Repeat("abcdefghij", 1000) // 10 000 Bytes: drei Pakete
	srv := newFakeRCON(t, func(cmd string) string {
		if cmd == "help" {
			return lang
		}
		return "Antwort auf " + cmd
	})
	f := srv.fabric()
	var got []string
	err := f.Isolated(t.Context(), func(run func(string) (string, error)) error {
		for _, c := range []string{"help", "list", "seed"} {
			out, err := run(c)
			if err != nil {
				return err
			}
			got = append(got, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != lang || got[1] != "Antwort auf list" || got[2] != "Antwort auf seed" {
		t.Fatalf("Antworten: %d Bytes, %q, %q", len(got[0]), got[1], got[2])
	}
	// Falsches Passwort: Fehler, kein Befehl.
	f.rc.password = "falsch"
	err = f.Isolated(t.Context(), func(run func(string) (string, error)) error {
		_, err := run("say x")
		return err
	})
	if err == nil {
		t.Fatal("falsches Passwort angenommen")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if strings.Join(srv.cmds, ",") != "help,list,seed" {
		t.Fatalf("Befehle beim Server: %q", srv.cmds)
	}
}

// Ohne laufenden Server gibt es keine Verbindung; ein Nullbyte geht nie
// hinaus; ein abgebrochener Kontext beendet die Sitzung.
func TestIsolatedGrenzen(t *testing.T) {
	if err := (&fabric{}).Isolated(t.Context(), func(func(string) (string, error)) error { return nil }); err == nil {
		t.Fatal("ohne Server: kein Fehler")
	}
	block := make(chan struct{})
	srv := newFakeRCON(t, func(cmd string) string {
		if cmd == "warte" {
			<-block
		}
		return "ok"
	})
	defer close(block)
	f := srv.fabric()
	err := f.Isolated(t.Context(), func(run func(string) (string, error)) error {
		_, err := run("say a\x00stop")
		return err
	})
	if err == nil {
		t.Fatal("Nullbyte angenommen")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = f.Isolated(ctx, func(run func(string) (string, error)) error {
		_, err := run("warte")
		return err
	})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("Abbruch: %v nach %v", err, time.Since(start))
	}
}

// runningProcess ist ein „laufender“ Prozess ohne echten Prozess (nur für
// fabric.client).
func runningProcess() *process { return &process{done: make(chan struct{})} }
