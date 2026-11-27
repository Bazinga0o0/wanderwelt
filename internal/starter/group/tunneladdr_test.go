package group

import "testing"

func TestTunnelAddr(t *testing.T) {
	cases := map[string]string{
		"twebl.de":              "twebl.de:7443",
		"twebl.de:7443":         "twebl.de:7443",
		"tunnel.example.org":    "tunnel.example.org:7443",
		"tunnel.example.org:99": "tunnel.example.org:99",
		"  twebl.de  ":          "twebl.de:7443",
		"":                      "",
	}
	for in, want := range cases {
		if got := TunnelAddr(in); got != want {
			t.Errorf("TunnelAddr(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
