package group

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestJoinRundlauf(t *testing.T) {
	cases := []Invite{
		{Coordinator: "https://wanderwelt.example.org", TunnelServer: "tunnel.example.org:7443", WebURL: "https://tunnel.example.org"},
		{Coordinator: "https://koord.example.org:8443", TunnelServer: "tunnel.example.org:7443", WebURL: "https://web.example.org", TokenURL: "https://web.example.org/token"},
		{Coordinator: "https://wanderwelt.example.org/", TunnelServer: "t.example.org:1", TokenURL: "https://t.example.org/konto/token"},
	}
	for _, in := range cases {
		s, err := EncodeJoin(&in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		if len(s) > MaxJoinSecret {
			t.Errorf("%d Bytes", len(s))
		}
		out, err := DecodeJoin(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		wantWeb := in.WebURL
		if wantWeb == "" {
			wantWeb = "https://t.example.org"
		}
		if out.Coordinator != strings.TrimRight(in.Coordinator, "/") || out.TunnelServer != in.TunnelServer || out.WebURL != wantWeb || out.TokenURL != in.TokenURL {
			t.Errorf("Rundlauf: %+v -> %+v", in, out)
		}
	}
}

func TestJoinEnthaeltKeinenToken(t *testing.T) {
	s, err := EncodeJoin(&Invite{Coordinator: "https://a.example.org", TunnelServer: "t.example.org:1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(s[len(joinPrefix):])
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Errorf("Geheimnis nennt token: %q", raw)
	}
}

func enc(lines ...string) string {
	return joinPrefix + base64.RawURLEncoding.EncodeToString([]byte(strings.Join(lines, "\n")))
}

func TestDecodeJoinMuell(t *testing.T) {
	long := enc("a.example.org", "t.example.org:1", "", strings.Repeat("/x", 40))
	bad := []string{
		"", "w1.", "x1.abc", "w1.!!!", "foo",
		enc("a.example.org"), // zu wenige Felder
		enc("a.example.org", "t.example.org:1", "", "", ""),
		enc("a.example.org", "t.example.org", "", ""),   // Port fehlt
		enc("a.example.org", "t.example.org:0", "", ""), // Port 0
		enc("a.example.org", "t.example.org:99999", "", ""),
		enc("a.example.org", "t.example.org:01", "", ""),        // führende Null
		enc("user@evil.example.org", "t.example.org:1", "", ""), // Zugangsdaten
		enc("evil.example.org/pfad", "t.example.org:1", "", ""),
		enc("evil.example.org?x=1", "t.example.org:1", "", ""),
		enc("A.example.org", "t.example.org:1", "", ""), // Großbuchstaben
		enc("localhost", "t.example.org:1", "", ""),     // kein Punkt
		enc("a.example.org", "t.example.org:1", "x y.example.org", ""),
		enc("a.example.org", "t.example.org:1", "", "token"), // Pfad ohne /
		enc("a.example.org", "t.example.org:1", "", "/../x"),
		enc("a.example.org", "t.example.org:1", "", "/a?b=1"),
		enc("a.example.org", "t.example.org:1", "", "/a b"),
		enc("a.example.org\x00", "t.example.org:1", "", ""),
		enc("a.example.org", "[::1]:1", "", ""),
		long,
		joinPrefix + strings.Repeat("A", 200),
	}
	// Ein gültiges Geheimnis, mit Zeichen versetzt, die der Base64-Decoder
	// stillschweigend übergeht oder die nicht ins Alphabet gehören.
	good := enc("a.example.org", "t.example.org:1", "", "")
	for _, junk := range []string{"\n", "\r\n", " ", "%0A", "%41", "=", "+", "/", "\x00", "\u00e4"} {
		bad = append(bad, good+junk, good[:6]+junk+good[6:], " "+good, "\n"+good)
	}
	for _, s := range bad {
		if inv, err := DecodeJoin(s); err == nil {
			t.Errorf("%q angenommen: %+v", s, inv)
		}
	}
}

func TestEncodeJoinLehntBoeseAdressenAb(t *testing.T) {
	bad := []Invite{
		{Coordinator: "http://a.example.org", TunnelServer: "t.example.org:1"},
		{Coordinator: "http://localhost:8080", TunnelServer: "t.example.org:1"},
		{Coordinator: "https://u:p@a.example.org", TunnelServer: "t.example.org:1"},
		{Coordinator: "https://a.example.org/pfad", TunnelServer: "t.example.org:1"},
		{Coordinator: "https://a.example.org", TunnelServer: "t.example.org"},
		{Coordinator: "https://a.example.org", TunnelServer: ""},
		{Coordinator: "https://a.example.org", TunnelServer: "t.example.org:1", WebURL: "javascript:alert(1)"},
		{Coordinator: "https://a.example.org", TunnelServer: "t.example.org:1", WebURL: "https://w.example.org/x"},
		{Coordinator: "https://a.example.org", TunnelServer: "t.example.org:1", TokenURL: "https://fremd.example.org/token"},
		{Coordinator: "https://" + strings.Repeat("abcdefghij.", 12) + "org", TunnelServer: "t.example.org:1"}, // zu lang
	}
	for _, in := range bad {
		if s, err := EncodeJoin(&in); err == nil {
			t.Errorf("%+v kodiert: %q", in, s)
		}
	}
}
