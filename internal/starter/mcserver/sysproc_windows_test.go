//go:build windows

package mcserver

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

const helperEnv = "WANDERWELT_MCSERVER_TEST_HELPER"

// TestMain: Im Helfermodus ist das Testprogramm ein Stand-in für Java: Es
// meldet, an wie vielen Prozessen seine Konsole hängt (0: keine Konsole),
// und beantwortet danach jede stdin-Zeile mit "echo <Zeile>" auf stdout.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "konsole" {
		var ids [8]uint32
		n, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList").
			Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
		fmt.Printf("konsole=%d\n", n)
		go func() {
			time.Sleep(time.Minute)
			os.Exit(2)
		}()
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			fmt.Printf("echo %s\n", sc.Text())
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Der Server bekommt keine Konsole: weder CREATE_NO_WINDOW (versteckte
// Konsole, die Windows beim Herunterfahren sofort abbaut und mit ihr alle
// Prozesse daran) noch eine geerbte.
func TestSysProcAttrOhneKonsole(t *testing.T) {
	a := sysProcAttr()
	if a.CreationFlags&detachedProcess == 0 {
		t.Errorf("DETACHED_PROCESS fehlt: %#x", a.CreationFlags)
	}
	if a.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CREATE_NEW_PROCESS_GROUP fehlt: %#x", a.CreationFlags)
	}
	if a.CreationFlags&0x08000000 != 0 { // CREATE_NO_WINDOW
		t.Errorf("CREATE_NO_WINDOW darf nicht gesetzt sein (schließt DETACHED_PROCESS aus): %#x", a.CreationFlags)
	}
}

// Ein echter Prozess mit sysProcAttr() hat keine Konsole, und Pipes für
// stdin, stdout und stderr gehen trotzdem (Server-Log, "stop" als Rückfall).
func TestProzessOhneKonsoleHatPipes(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), helperEnv+"=konsole")
	cmd.SysProcAttr = sysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	rd := bufio.NewReader(stdout)
	line, err := rd.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(line) != "konsole=0" {
		t.Fatalf("Helfer meldet %q, erwartet konsole=0 (keine Konsole)", line)
	}
	if _, err := io.WriteString(stdin, "stop\n"); err != nil {
		t.Fatal(err)
	}
	line, err = rd.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "echo stop" {
		t.Fatalf("stdin-Pipe: %q, %v", line, err)
	}
}
