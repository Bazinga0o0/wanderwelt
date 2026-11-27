package ereignis

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

func host(email, name string) *api.HostInfo {
	return &api.HostInfo{Email: email, MinecraftName: name, MachineID: "geheime-maschine"}
}

func arten(es []Ereignis) []Art {
	var out []Art
	for _, e := range es {
		out = append(out, e.Art)
	}
	return out
}

func TestStandardSchalter(t *testing.T) {
	win := map[Art]bool{HostStartet: true, WeltOnline: true, Spieler: false, NiemandHostet: false, Wartung: true, Uebergabe: false, Beitrittsanfrage: true}
	dc := map[Art]bool{HostStartet: true, WeltOnline: true, Spieler: true, NiemandHostet: false, Wartung: true, Uebergabe: true, Beitrittsanfrage: false}
	if len(Katalog) != len(win) {
		t.Fatalf("Katalog hat %d Schalter, Test kennt %d", len(Katalog), len(win))
	}
	for art, want := range win {
		if got := Standard(Windows, art); got != want {
			t.Errorf("Windows %s: %v, erwartet %v", art, got, want)
		}
	}
	for art, want := range dc {
		if got := Standard(Discord, art); got != want {
			t.Errorf("Discord %s: %v, erwartet %v", art, got, want)
		}
	}
	if Standard(Windows, "gibtsnicht") || Standard(Discord, "gibtsnicht") || Bekannt("gibtsnicht") {
		t.Error("unbekannte Art ist aus und unbekannt")
	}
}

func TestOhneVorgaengerKeineEreignisse(t *testing.T) {
	st := &api.Status{Host: host("a@x", "Anna"), HostReady: true, Players: []string{"Anna"}}
	if es := Aus(nil, st); es != nil {
		t.Errorf("ohne alten Stand: %v", es)
	}
	if es := Aus(st, nil); es != nil {
		t.Errorf("ohne neuen Stand: %v", es)
	}
}

func TestHostStartetUndOnline(t *testing.T) {
	leer := &api.Status{}
	startet := &api.Status{Host: host("a@x", "Anna")}
	bereit := &api.Status{Host: host("a@x", "Anna"), HostReady: true}

	if got, want := arten(Aus(leer, startet)), []Art{HostStartet}; !reflect.DeepEqual(got, want) {
		t.Errorf("Host startet: %v", got)
	}
	if got, want := arten(Aus(startet, bereit)), []Art{WeltOnline}; !reflect.DeepEqual(got, want) {
		t.Errorf("Host bereit: %v", got)
	}
	// Direkt bereit (kurzer Takt): beides, in dieser Reihenfolge.
	if got, want := arten(Aus(leer, bereit)), []Art{HostStartet, WeltOnline}; !reflect.DeepEqual(got, want) {
		t.Errorf("direkt bereit: %v", got)
	}
	if es := Aus(bereit, bereit); len(es) != 0 {
		t.Errorf("unverändert: %v", es)
	}
	// Nur Hochladen (Nachreichen) ist kein Hosten.
	hoch := &api.Status{Host: host("a@x", "Anna"), HostUploading: true}
	if es := Aus(leer, hoch); len(es) != 0 {
		t.Errorf("Hochladen: %v", es)
	}
}

func TestNiemandHostet(t *testing.T) {
	bereit := &api.Status{Host: host("a@x", "Anna"), HostReady: true}
	leer := &api.Status{}
	if got, want := arten(Aus(bereit, leer)), []Art{NiemandHostet}; !reflect.DeepEqual(got, want) {
		t.Errorf("Host weg: %v", got)
	}
	// Während einer Wartung und einer Übergabe ist das Ende erwartet.
	mit := &api.Status{Host: bereit.Host, HostReady: true, Maintenance: &api.Maintenance{}}
	ohne := &api.Status{Maintenance: &api.Maintenance{}}
	if got := arten(Aus(mit, ohne)); len(got) != 0 {
		t.Errorf("Host weg in der Wartung: %v", got)
	}
	ueber := &api.Status{Host: bereit.Host, HostReady: true, Handover: &api.HandoverState{State: api.HandoverStarting, To: *host("b@x", "Ben")}}
	if got := arten(Aus(ueber, leer)); len(got) != 0 {
		t.Errorf("Host weg in der Übergabe: %v", got)
	}
}

func TestSpielerNurImStabilenZustand(t *testing.T) {
	st := func(h *api.HostInfo, ready bool, players ...string) *api.Status {
		return &api.Status{Host: h, HostReady: ready, Players: players}
	}
	a := host("a@x", "Anna")
	es := Aus(st(a, true, "Anna", "Ben"), st(a, true, "anna", "Clara", "Dora"))
	if len(es) != 1 || es[0].Art != Spieler {
		t.Fatalf("Ereignisse: %v", es)
	}
	if want := []string{"Clara", "Dora"}; !reflect.DeepEqual(es[0].Neu, want) {
		t.Errorf("Neu: %v", es[0].Neu)
	}
	if want := []string{"Ben"}; !reflect.DeepEqual(es[0].Weg, want) {
		t.Errorf("Weg: %v", es[0].Weg)
	}
	// Nach einem Neustart des Koordinators ist die Liste erst mit dem ersten
	// Lebenszeichen wieder da: kein Vergleich, solange der Host nicht bereit war.
	if es := Aus(st(a, false), st(a, true, "Anna", "Ben")); len(es) != 1 || es[0].Art != WeltOnline {
		t.Errorf("Host wird bereit mit Spielern: %v", es)
	}
	// Anderer Host: nur „hostet jetzt“/„online“, keine Spieler.
	b := host("b@x", "Ben")
	if got, want := arten(Aus(st(a, true, "Anna"), st(b, true, "Ben", "Clara"))), []Art{HostStartet, WeltOnline}; !reflect.DeepEqual(got, want) {
		t.Errorf("Hostwechsel: %v", got)
	}
}

func TestUebergabe(t *testing.T) {
	a, b := host("a@x", "Anna"), host("b@x", "Ben")
	vorher := &api.Status{Host: a, HostReady: true, Players: []string{"Anna", "Clara"}}
	laeuft := &api.Status{Host: a, HostReady: true, Players: []string{"Anna", "Clara"},
		Handover: &api.HandoverState{State: api.HandoverPreparing, To: *b}}

	es := Aus(vorher, laeuft)
	if len(es) != 1 || es[0].Art != Uebergabe || es[0].Ende || es[0].Wer != "Anna" || es[0].Nach != "Ben" {
		t.Fatalf("Übergabe läuft: %+v", es)
	}
	// Der neue Host löst den alten ab: nur „abgeschlossen“, auch wenn zwischendurch niemand hostete.
	zwischen := &api.Status{Handover: &api.HandoverState{State: api.HandoverReady, To: *b}}
	if got := arten(Aus(laeuft, zwischen)); len(got) != 0 {
		t.Errorf("Zwischenstand: %v", got)
	}
	fertig := &api.Status{Host: b, HostReady: true, Players: []string{"Anna", "Clara"}}
	for _, alt := range []*api.Status{laeuft, zwischen} {
		es = Aus(alt, fertig)
		if len(es) != 1 || es[0].Art != Uebergabe || !es[0].Ende || es[0].Nach != "Ben" {
			t.Errorf("Übergabe fertig: %+v", es)
		}
	}
	// Gescheitert: keine Meldung, der alte Host hostet weiter.
	gescheitert := &api.Status{Host: a, HostReady: true, Handover: &api.HandoverState{State: api.HandoverFailed, To: *b}}
	if es := Aus(laeuft, gescheitert); len(es) != 0 {
		t.Errorf("gescheitert: %v", es)
	}
}

func TestWartung(t *testing.T) {
	ohne := &api.Status{}
	mit := &api.Status{Maintenance: &api.Maintenance{Reason: "Update"}}
	es := Aus(ohne, mit)
	if len(es) != 1 || es[0].Art != Wartung || es[0].Ende || es[0].Grund != "Update" {
		t.Errorf("angekündigt: %+v", es)
	}
	es = Aus(mit, ohne)
	if len(es) != 1 || es[0].Art != Wartung || !es[0].Ende {
		t.Errorf("vorbei: %+v", es)
	}
}

func TestWeltNameNurBeiMehrerenWelten(t *testing.T) {
	leer := &api.Status{}
	eine := &api.Status{Host: host("a@x", "Anna"), Worlds: []api.GroupWorld{{ID: "w1", Name: "Survival"}}, RunningWorld: "w1"}
	zwei := &api.Status{Host: host("a@x", "Anna"), Worlds: []api.GroupWorld{{ID: "w1", Name: "Survival"}, {ID: "w2", Name: "Kreativ"}}, RunningWorld: "w2"}
	if es := Aus(leer, eine); es[0].Welt != "" {
		t.Errorf("eine Welt: %q", es[0].Welt)
	}
	if es := Aus(leer, zwei); es[0].Welt != "Kreativ" {
		t.Errorf("zwei Welten: %q", es[0].Welt)
	}
}

func TestTexte(t *testing.T) {
	tests := []struct {
		e    Ereignis
		want string
	}{
		{Ereignis{Art: HostStartet, Wer: "Anna"}, "Anna hostet jetzt – die Welt ist gleich bereit."},
		{Ereignis{Art: HostStartet}, "ein Mitglied hostet jetzt – die Welt ist gleich bereit."},
		{Ereignis{Art: WeltOnline, Wer: "Anna"}, "Die Welt ist online (Host Anna)."},
		{Ereignis{Art: NiemandHostet}, "Niemand hostet mehr – die Welt ist offline."},
		{Ereignis{Art: Spieler, Neu: []string{"A"}}, "A ist beigetreten."},
		{Ereignis{Art: Spieler, Neu: []string{"A", "B", "C"}, Weg: []string{"D"}}, "A, B und C sind beigetreten, D hat die Welt verlassen."},
		{Ereignis{Art: Spieler, Weg: []string{"A", "B"}}, "A und B haben die Welt verlassen."},
		{Ereignis{Art: Spieler, Neu: []string{"A", "B", "C", "D", "E", "F", "G"}}, "A, B, C, D, E und 2 weitere sind beigetreten."},
		{Ereignis{Art: Wartung, Grund: "Update"}, "Wartung am Server angekündigt (Update). Der Server stoppt gleich sauber, danach ist Spielen wieder möglich."},
		{Ereignis{Art: Wartung, Ende: true}, "Wartung vorbei – ihr könnt wieder spielen."},
		{Ereignis{Art: Uebergabe, Wer: "Anna", Nach: "Ben"}, "Host-Übergabe läuft: Anna gibt die Welt an Ben weiter."},
		{Ereignis{Art: Uebergabe, Ende: true, Nach: "Ben"}, "Host-Übergabe abgeschlossen – Ben hostet jetzt."},
		{Ereignis{Art: Beitrittsanfrage, Wer: "Y"}, "Neue Beitrittsanfrage von Y"},
	}
	for _, tt := range tests {
		if got := Text(tt.e, nil); got != tt.want {
			t.Errorf("%v: %q, erwartet %q", tt.e.Art, got, tt.want)
		}
	}
}

// Keine E-Mail-Adresse und keine Maschinenkennung gelangt je in einen Text.
func TestTexteOhneEmail(t *testing.T) {
	a, b := host("anna@geheim.example", "Anna"), host("ben@geheim.example", "")
	alt := &api.Status{Host: a, HostReady: true, Players: []string{"Anna"}}
	neu := &api.Status{Host: b, HostReady: true, Players: []string{"Ben"}, Maintenance: &api.Maintenance{Reason: "x"},
		Handover: &api.HandoverState{State: api.HandoverStarting, To: *b}}
	for _, e := range Aus(alt, neu) {
		txt := Text(e, nil)
		if strings.Contains(txt, "geheim") || strings.Contains(txt, "@") || strings.Contains(txt, "maschine") {
			t.Errorf("%s: %q", e.Art, txt)
		}
	}
}

func TestKurz(t *testing.T) {
	if got := Kurz("  a\nb\t\x00c  ", 20); got != "a b c" {
		t.Errorf("Steuerzeichen: %q", got)
	}
	if got := Kurz(strings.Repeat("ä", 50), 10); got != strings.Repeat("ä", 9)+"…" {
		t.Errorf("Länge: %q", got)
	}
}
