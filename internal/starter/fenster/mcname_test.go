package fenster

import "testing"

func TestPruefeMcName(t *testing.T) {
	for s, ok := range map[string]bool{"Bob_42": true, "abc": true, "ab": false, "": false, "Zu_langer_Name_123": false, "a b c": false, "Ünï": false} {
		if err := pruefeMcName(s); (err == nil) != ok {
			t.Errorf("pruefeMcName(%q) = %v", s, err)
		}
	}
}
