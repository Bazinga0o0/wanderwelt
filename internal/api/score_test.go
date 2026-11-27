package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// ping_ms ist omitempty: Ein Score ohne Ping (ältere Starter, Messung
// gescheitert, älterer Koordinator) trägt das Feld nicht – ein älterer
// Koordinator lehnt unbekannte Felder ab. Ältere Starter-Daten ohne das Feld
// lesen sich als 0 (fehlt).
func TestScorePingMsOmitempty(t *testing.T) {
	b, err := json.Marshal(Score{UploadMbps: 50, CPUScore: 900, RAMFreeMB: 8000, RAMTotalMB: 16000, XmxMB: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ping") {
		t.Fatalf("Score ohne Ping trägt ein Ping-Feld: %s", b)
	}
	var old Score
	if err := json.Unmarshal(b, &old); err != nil || old.PingMs != 0 {
		t.Fatalf("Score ohne Feld gelesen: %+v, %v", old, err)
	}

	b, err = json.Marshal(Score{PingMs: 23.5})
	if err != nil || !strings.Contains(string(b), `"ping_ms":23.5`) {
		t.Fatalf("Score mit Ping: %s, %v", b, err)
	}
	var got Score
	if err := json.Unmarshal(b, &got); err != nil || got.PingMs != 23.5 {
		t.Fatalf("Rundlauf: %+v, %v", got, err)
	}
}
