package server_test

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/server"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

// startHalfPut schickt über eine eigene Verbindung einen Blob-PUT mit
// "Expect: 100-continue". Das „100 Continue“ kommt erst, wenn der Handler
// den Körper liest – dann hält er den Blob (PutBlobPart belegt ihn vor dem
// ersten Lesen). Danach gehen nur die ersten 10 Bytes des Körpers hinaus.
// Lehnt der Koordinator gleich ab (409: der Blob war noch belegt), wird
// über eine neue Verbindung neu gesendet. Die Verbindung bleibt offen, bis
// der Aufrufer sie schließt.
func startHalfPut(t *testing.T, ts *httptest.Server, sha string, data []byte) net.Conn {
	t.Helper()
	head := fmt.Sprintf("PUT /v1/blobs/%s HTTP/1.1\r\nHost: koordinator\r\nX-Tunnel-Account-Email: %s\r\nX-Tunnel-Account-Id: %d\r\n"+
		"Content-Type: application/octet-stream\r\nContent-Range: bytes 0-%d/%d\r\nContent-Length: %d\r\nExpect: 100-continue\r\n\r\n",
		sha, emailA, idA, len(data)-1, len(data), len(data))
	for try := 1; ; try++ {
		conn, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte(head)); err != nil {
			t.Fatal(err)
		}
		status, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("Antwort auf den halben PUT: %v", err)
		}
		switch {
		case strings.HasPrefix(status, "HTTP/1.1 100 "):
			conn.SetReadDeadline(time.Time{})
			if _, err := conn.Write(data[:10]); err != nil {
				t.Fatal(err)
			}
			return conn
		case strings.HasPrefix(status, "HTTP/1.1 409 ") && try < 50:
			conn.Close()
			time.Sleep(10 * time.Millisecond)
		default:
			t.Fatalf("halber PUT (%d. Versuch): %q", try, status)
		}
	}
}

// Bricht ein Starter einen Blob-Upload ab (Verbindung zu), gibt der
// Koordinator den Blob sofort frei: Der nächste Upload desselben Blobs
// gelingt (PutBlob wartet ein 409 höchstens etwa 5 s ab; die Lese-Frist ist
// hier die echte von 30 s).
func TestBlobFreedWhenClientAborts(t *testing.T) {
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close) // nach dem Schließen der eigenen Verbindung (LIFO)
	cli := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: emailA})
	data := bytes.Repeat([]byte("abgebrochen "), 20)
	sha := shaHex(data)

	conn := startHalfPut(t, ts, sha, data)
	conn.Close()
	if err := cli.PutBlob(t.Context(), sha, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Upload nach dem Abbruch: %v", err)
	}
	if m, err := cli.Missing(t.Context(), []string{sha}); err != nil || len(m) != 0 {
		t.Fatalf("Missing: %q, %v", m, err)
	}
}

// Verschwindet die Verbindung eines Starters ohne Abbau (es kommen einfach
// keine Daten mehr), gibt der Koordinator den Blob nach der Lese-Frist frei
// (hier 1 s; PutBlob wartet ein 409 höchstens etwa 5 s ab).
func TestBlobFreedAfterStall(t *testing.T) {
	t.Cleanup(server.SetBlobStall(time.Second))
	srv, _, _ := newServer(t, "dev")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close) // nach dem Schließen der eigenen Verbindung (LIFO)
	cli := client.New(&group.Group{Coordinator: ts.URL, DevIdentityEmail: emailA})
	data := bytes.Repeat([]byte("hängt "), 30)
	sha := shaHex(data)

	startHalfPut(t, ts, sha, data) // bleibt offen und still
	if err := cli.PutBlob(t.Context(), sha, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Upload nach der Lese-Frist: %v", err)
	}
	if m, err := cli.Missing(t.Context(), []string{sha}); err != nil || len(m) != 0 {
		t.Fatalf("Missing: %q, %v", m, err)
	}
}
