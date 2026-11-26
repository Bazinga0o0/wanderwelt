package verzeichnis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func doc(entries ...string) []byte {
	return []byte(`{"services":[` + strings.Join(entries, ",") + `]}`)
}

func eintrag(name, titel, beschr, url string) string {
	b, _ := json.Marshal(map[string]string{"name": name, "title": titel, "description": beschr, "url": url})
	return string(b)
}

func TestParseGueltig(t *testing.T) {
	got, err := Parse(doc(
		eintrag("wanderwelt", "Wandergruppe", "Survival mit Freunden\nzweite Zeile", "https://wanderwelt.example.org"),
		eintrag("zwei", "<b>Fett</b> & \"Anführung\"", "", "https://zwei.example.org/"),
	), "app.example.org")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Description != "Survival mit Freunden\nzweite Zeile" || got[1].URL != "https://zwei.example.org" {
		t.Errorf("%+v", got)
	}
	// HTML bleibt Text (die Oberfläche fügt nie HTML ein).
	if got[1].Title != "<b>Fett</b> & \"Anführung\"" {
		t.Errorf("Titel verändert: %q", got[1].Title)
	}
	if got, err := Parse([]byte(`{"services":[]}`), "example.org"); err != nil || len(got) != 0 {
		t.Errorf("leer: %v %v", got, err)
	}
}

func TestParseMuell(t *testing.T) {
	for _, body := range []string{"", "müll", "[]", `{"services":"x"}`, `{"services":[1,2]}`, `null`, `{"services":[{"name":5}]}`, "\x00\x01"} {
		got, err := Parse([]byte(body), "example.org")
		if err == nil && len(got) != 0 {
			t.Errorf("%q: Einträge aus Müll: %v", body, got)
		}
	}
}

func TestParseUeberlang(t *testing.T) {
	if _, err := Parse(make([]byte, MaxBody+1), "example.org"); err == nil {
		t.Error("zu große Antwort")
	}
	var es []string
	for i := range MaxDienste + 1 {
		es = append(es, eintrag(fmt.Sprintf("n%d", i), "t", "", fmt.Sprintf("https://g%d.example.org", i)))
	}
	if _, err := Parse(doc(es...), "example.org"); err == nil {
		t.Error("zu viele Einträge")
	}
	lang := strings.Repeat("ä", MaxTitel+1)
	bad := []string{
		eintrag("a", lang, "", "https://a.example.org"),
		eintrag("a", "t", strings.Repeat("x", MaxBeschreibung+1), "https://a.example.org"),
		eintrag(strings.Repeat("n", 65), "t", "", "https://a.example.org"),
		eintrag("a", "t", "", "https://"+strings.Repeat("a", 200)+".example.org"),
		eintrag("a", "", "", "https://a.example.org"),
	}
	for _, e := range bad {
		if got, err := Parse(doc(e), "example.org"); err != nil || len(got) != 0 {
			t.Errorf("%.60s: %v %v", e, got, err)
		}
	}
	// Genau an der Grenze ist erlaubt.
	ok := eintrag("a", strings.Repeat("ä", MaxTitel), strings.Repeat("ö", MaxBeschreibung), "https://a.example.org")
	if got, _ := Parse(doc(ok), "example.org"); len(got) != 1 {
		t.Error("Grenzwerte abgelehnt")
	}
}

func TestParseZeichenUndAdressen(t *testing.T) {
	bad := []string{
		eintrag("a", "Titel\x00x", "", "https://a.example.org"),
		eintrag("a", "Titel\nzwei", "", "https://a.example.org"), // Umbruch nur in der Beschreibung
		eintrag("a", "t", "x\x1b[31mrot", "https://a.example.org"),
		eintrag("a", "t\u202eumgekehrt", "", "https://a.example.org"), // Format-Steuerzeichen (Bidi)
		eintrag("a", "t", "", "http://a.example.org"),                 // kein https
		eintrag("a", "t", "", "https://evil.test"),                    // fremde Domain
		eintrag("a", "t", "", "https://example.org.evil.test"),        // Suffix-Trick
		eintrag("a", "t", "", "https://notexample.org"),               // kein Unterstrich der Domain
		eintrag("a", "t", "", "https://example.org"),                   // die Domain selbst ist keine Gruppe
		eintrag("a", "t", "", "https://u:p@a.example.org"),
		eintrag("a", "t", "", "https://a.example.org/pfad"),
		eintrag("a", "t", "", "https://a.example.org?x=1"),
		eintrag("a", "t", "", "javascript:alert(1)"),
		eintrag("a", "t", "", "file:///c:/x"),
		eintrag("a b", "t", "", "https://a.example.org"),
		eintrag("../x", "t", "", "https://a.example.org"),
	}
	for _, e := range bad {
		if got, err := Parse(doc(e), "app.example.org"); err != nil || len(got) != 0 {
			t.Errorf("%.70s: angenommen: %v %v", e, got, err)
		}
	}
	// Ungültiges UTF-8 im Titel.
	if got, _ := Parse([]byte(`{"services":[{"name":"a","title":"x`+"\xff"+`y","url":"https://a.example.org"}]}`), "example.org"); len(got) != 0 {
		t.Error("ungültiges UTF-8 angenommen")
	}
	// Ein guter Eintrag neben schlechten bleibt erhalten; Doppelte fallen weg.
	got, _ := Parse(doc(bad[0], eintrag("g", "Gut", "", "https://g.example.org"), eintrag("g2", "Gut", "", "https://g.example.org")), "example.org")
	if len(got) != 1 || got[0].Name != "g" {
		t.Errorf("%v", got)
	}
	// Loopback (lokale Tests): nur derselbe Host.
	if got, _ := Parse(doc(eintrag("a", "t", "", "http://127.0.0.1:9")), "127.0.0.1"); len(got) != 1 {
		t.Error("Loopback")
	}
	if got, _ := Parse(doc(eintrag("a", "t", "", "http://localhost:9")), "127.0.0.1"); len(got) != 0 {
		t.Error("anderer Loopback-Host")
	}
}

func server(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestAbfragen(t *testing.T) {
	var auth, path string
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		w.Write(doc(eintrag("g", "Gruppe", "d", "http://127.0.0.1:1")))
	})
	got, err := Abfragen(context.Background(), nil, url+"/", "tok-123")
	if err != nil || len(got) != 1 || auth != "Bearer tok-123" || path != "/api/v1/directory" {
		t.Fatalf("%v %v %q %q", got, err, auth, path)
	}
}

func TestAbfragenStatus(t *testing.T) {
	cases := []struct {
		code  int
		retry string
		check func(error) bool
	}{
		{401, "", func(e error) bool { return errors.Is(e, ErrToken) }},
		{404, "", func(e error) bool { return errors.Is(e, ErrKeine) }},
		{429, "7", func(e error) bool {
			var b *BusyError
			return errors.As(e, &b) && b.Status == 429 && b.RetryAfter == 7*time.Second
		}},
		{503, "", func(e error) bool {
			var b *BusyError
			return errors.As(e, &b) && b.Status == 503 && b.RetryAfter == 0
		}},
		{500, "", func(e error) bool { return e != nil && !errors.Is(e, ErrToken) }},
	}
	for _, c := range cases {
		url := server(t, func(w http.ResponseWriter, r *http.Request) {
			if c.retry != "" {
				w.Header().Set("Retry-After", c.retry)
			}
			http.Error(w, "geheimer Fehlertext", c.code)
		})
		_, err := Abfragen(context.Background(), nil, url, "tok")
		if !c.check(err) {
			t.Errorf("%d: %v", c.code, err)
		}
		if err != nil && strings.Contains(err.Error(), "geheimer") {
			t.Errorf("%d: Serverantwort im Fehlertext: %v", c.code, err)
		}
	}
}

func TestAbfragenFolgtKeinerWeiterleitung(t *testing.T) {
	var gesehen bool
	ziel := server(t, func(w http.ResponseWriter, r *http.Request) { gesehen = true })
	url := server(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, ziel+"/x", http.StatusFound) })
	_, err := Abfragen(context.Background(), nil, url, "tok")
	if err == nil || gesehen {
		t.Errorf("Weiterleitung gefolgt (Token wäre abgeflossen): %v gesehen=%v", err, gesehen)
	}
}

func TestAbfragenGrossUndMuell(t *testing.T) {
	url := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"services":[`))
		w.Write([]byte(strings.Repeat(" ", MaxBody)))
		w.Write([]byte(`]}`))
	})
	if _, err := Abfragen(context.Background(), nil, url, "tok"); err == nil {
		t.Error("zu große Antwort angenommen")
	}
	url = server(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>Login</html>")) })
	if _, err := Abfragen(context.Background(), nil, url, "tok"); err == nil {
		t.Error("HTML angenommen")
	}
}

func TestAbfragenVerweigertUnsichereBasis(t *testing.T) {
	for _, b := range []string{"http://example.org", "ftp://example.org", "", "https://", "https://u:p@example.org", "javascript:1"} {
		if _, err := Abfragen(context.Background(), nil, b, "tok"); err == nil {
			t.Errorf("%q angenommen", b)
		}
	}
}

func TestAbfragenKontext(t *testing.T) {
	url := server(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := Abfragen(ctx, nil, url, "tok"); err == nil || time.Since(began) > 3*time.Second {
		t.Errorf("Kontextgrenze: %v nach %v", err, time.Since(began))
	}
}
