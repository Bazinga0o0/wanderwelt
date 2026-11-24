package group

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseInvite(t *testing.T) {
	ok := "coordinator: https://wanderwelt.example.org/\ntunnel_server: tunnel.example.org:7443\nweb_url: https://tunnel.example.org\n"
	inv, err := ParseInvite(b64(ok))
	if err != nil || inv.Coordinator != "https://wanderwelt.example.org" || inv.TunnelServer != "tunnel.example.org:7443" ||
		inv.AccountURL() != "https://tunnel.example.org" || inv.CreateTokenURL() != "https://tunnel.example.org" {
		t.Fatalf("%+v %v", inv, err)
	}
	// Ohne Auffüllung und mit token_url.
	raw := base64.RawStdEncoding.EncodeToString([]byte(ok + "token_url: https://tunnel.example.org/token\n"))
	if inv, err = ParseInvite(raw); err != nil || inv.CreateTokenURL() != "https://tunnel.example.org/token" {
		t.Fatalf("%+v %v", inv, err)
	}
	if g := inv.WithToken(" abc \n"); g.Token != "abc" || g.Coordinator != inv.Coordinator || g.TunnelServer != inv.TunnelServer {
		t.Fatalf("%+v", g)
	}
	// Leer: keine Einladung.
	if inv, err := ParseInvite("  "); inv != nil || err != nil {
		t.Fatalf("%v %v", inv, err)
	}
	for name, in := range map[string]string{
		"kein Base64":       "!!!",
		"leer":              b64("# nur Kommentar\n"),
		"Token eingebettet": b64(ok + "token: geheim\n"),
		"http":              b64("coordinator: http://wanderwelt.example.org\ntunnel_server: t:1\nweb_url: https://x.example.org\n"),
		"kein tunnel":       b64("coordinator: https://w.example.org\nweb_url: https://x.example.org\n"),
		"web_url fehlt":     b64("coordinator: https://w.example.org\ntunnel_server: t:1\n"),
		"web_url file":      b64("coordinator: https://w.example.org\ntunnel_server: t:1\nweb_url: file:///c:/x\n"),
		"token_url js":      b64(ok + "token_url: javascript:alert(1)\n"),
	} {
		if inv, err := ParseInvite(in); err == nil {
			t.Errorf("%s: angenommen: %+v", name, inv)
		}
	}
}

func TestCheckToken(t *testing.T) {
	for _, bad := range []string{"", "  ", "a b", "a\nb", strings.Repeat("x", 513)} {
		if err := CheckToken(bad); err == nil {
			t.Errorf("%q angenommen", bad)
		}
	}
	if err := CheckToken("  gut-123_ABC\r\n"); err != nil {
		t.Error(err)
	}
	// Die Meldung nennt den Token nicht.
	if err := CheckToken("geheim und mehr"); err == nil || strings.Contains(err.Error(), "geheim") {
		t.Errorf("%v", err)
	}
}

func TestSaveGroupPrivat(t *testing.T) {
	t.Setenv("WANDERWELT_HOME", t.TempDir())
	g := &Group{Coordinator: "https://w.example.org", TunnelServer: "t:1", Token: "tok-123"}
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	back, err := LoadGroup(GroupPath())
	if err != nil || *back != *g {
		t.Fatalf("%+v %v", back, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(GroupPath())
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("Rechte %v %v", fi, err)
		}
	}
	// Zweites Speichern ersetzt die Datei.
	g.Token = "tok-456"
	if err := SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	if back, _ := LoadGroup(GroupPath()); back == nil || back.Token != "tok-456" {
		t.Fatalf("%+v", back)
	}
}
