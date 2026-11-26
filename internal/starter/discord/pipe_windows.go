//go:build windows

package discord

import (
	"context"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procPeekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

// pipeConn ist die Named Pipe zu Discord. Das Handle ist synchron: Ein
// gleichzeitiges Lesen würde das Schreiben blockieren, darum liest der Client
// nur, wenn Available etwas meldet oder eine Antwort aussteht.
type pipeConn struct{ f *os.File }

func (p pipeConn) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p pipeConn) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p pipeConn) Close() error                { return p.f.Close() }

// Available: Bytes, die ohne Warten lesbar sind (PeekNamedPipe).
func (p pipeConn) Available() (int, error) {
	var n uint32
	r, _, err := procPeekNamedPipe.Call(p.f.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	if r == 0 {
		return 0, err
	}
	return int(n), nil
}

// dialPipe probiert \\.\pipe\discord-ipc-0 … 9.
func dialPipe(ctx context.Context) (Conn, error) {
	var last error
	for i := 0; i < 10; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i), os.O_RDWR, 0)
		if err == nil {
			return pipeConn{f}, nil
		}
		last = err
	}
	return nil, fmt.Errorf("Discord nicht erreichbar: %w", last)
}
