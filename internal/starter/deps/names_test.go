package deps

import (
	"archive/zip"
	"bytes"
	"slices"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Names nennt genau die Artefakte, die Ensure lädt, in seiner Reihenfolge
// (der Einrichtungs-Balken teilt sich danach auf) – mit und ohne C2ME.
func TestNamesMatchesEnsureOrder(t *testing.T) {
	var jre bytes.Buffer
	zw := zip.NewWriter(&jre)
	w, err := zw.Create("jre/bin/" + exeName("java"))
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("JAVA"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	src := fakeSource{
		"jre.zip":     jre.Bytes(),
		"fabric.jar":  []byte("FABRIC"),
		"tunnel.exe":  []byte("TUNNEL"),
		"lithium.jar": []byte("LITHIUM"),
		"c2me.jar":    []byte("C2ME"),
		"ferrite.jar": []byte("FERRITE"),
	}
	art := func(name, file string) api.Artifact {
		return api.Artifact{Name: name, URL: "wanderwelt:" + file, Hash: "sha256:" + sha256Hex(src[file])}
	}
	m := &api.Manifest{
		Java:         art("java", "jre.zip"),
		FabricServer: art("fabric-server", "fabric.jar"),
		Tunnel:       art("tunnel", "tunnel.exe"),
		Mods:         []api.Artifact{art("lithium", "lithium.jar"), art("c2me-fabric", "c2me.jar"), art("ferritecore", "ferrite.jar")},
	}
	for _, c2me := range []bool{false, true} {
		t.Setenv("WANDERWELT_HOME", t.TempDir()) // alles neu laden
		prof := api.Profile{C2ME: c2me}
		var order []string
		if _, err := Ensure(t.Context(), m, prof, src, func(name string, _, _ int64) {
			if len(order) == 0 || order[len(order)-1] != name {
				order = append(order, name)
			}
		}); err != nil {
			t.Fatalf("C2ME %v: Ensure: %v", c2me, err)
		}
		if names := Names(m, prof); !slices.Equal(names, order) {
			t.Errorf("C2ME %v: Names %q, Ensure lädt %q", c2me, names, order)
		}
	}
}
