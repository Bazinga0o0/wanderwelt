package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// Abwärtskompatibilität: Ohne Banns fehlt das neue Feld ganz (ein älterer
// Koordinator schickt es nie, ein neuer nur mit Banns); ein älterer Starter
// liest die neuen Host-Einstellungen mit dem üblichen Decoder (unbekannte
// Felder werden übergangen), ein neuer Starter liest die eines älteren
// Koordinators (Bans bleibt leer).
func TestHostSettingsBansKompatibel(t *testing.T) {
	ohne, err := json.Marshal(HostSettings{Whitelist: []Player{}, Ops: []Player{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ohne), "bans") {
		t.Fatalf("ohne Banns steht das Feld trotzdem drin: %s", ohne)
	}
	mit, _ := json.Marshal(HostSettings{Bans: []Player{{UUID: "77777777-7777-4777-8777-777777777777", Name: "Troll"}}})
	if !strings.Contains(string(mit), `"bans":[{"uuid":"77777777-7777-4777-8777-777777777777","name":"Troll"}]`) {
		t.Fatalf("Banns: %s", mit)
	}

	// Ein älterer Starter kennt nur diese Felder.
	var alt struct {
		Properties map[string]string `json:"properties"`
		Whitelist  []Player          `json:"whitelist"`
		Ops        []Player          `json:"ops"`
	}
	if err := json.Unmarshal(mit, &alt); err != nil {
		t.Fatalf("älterer Starter: %v", err)
	}
	// Ein älterer Koordinator schickt kein bans.
	var neu HostSettings
	if err := json.Unmarshal([]byte(`{"properties":{},"whitelist":[],"ops":[],"snapshot_interval_s":300}`), &neu); err != nil || neu.Bans != nil {
		t.Fatalf("älterer Koordinator: %+v, %v", neu, err)
	}
}

// Die neuen Typen haben feste JSON-Namen (Koordinator und Starter verschiedener
// Stände sprechen miteinander).
func TestServerTypenJSON(t *testing.T) {
	for v, want := range map[any]string{
		&ServerListChange{List: ListBans, Action: ListAdd, Name: "Troll"}: `{"list":"bans","action":"add","name":"Troll"}`,
		&ServerCommand{Command: "say hi"}:                                 `{"command":"say hi"}`,
		&ServerCommandResult{OK: true, Output: "x"}:                       `{"ok":true,"output":"x"}`,
		&GameruleSet{Name: "keep_inventory", Value: "true"}:               `{"name":"keep_inventory","value":"true"}`,
		&HostJob{ID: 3, Kind: JobCommand, Command: "list"}:                `{"id":3,"kind":"befehl","command":"list"}`,
		&HostJobsRequest{LeaseID: 1, MachineID: "pc"}:                     `{"lease_id":1,"machine_id":"pc"}`,
		&ListPlayer{Name: "A", UUID: "u"}:                                 `{"name":"A","uuid":"u"}`,
	} {
		b, err := json.Marshal(v)
		if err != nil || string(b) != want {
			t.Errorf("%T: %s, erwartet %s (%v)", v, b, want, err)
		}
	}
}
