package benachrichtigung

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/ereignis"
)

// logPuffer ist ein bytes.Buffer, den mehrere Goroutinen benutzen dürfen.
type logPuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logPuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *logPuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

const geheimesToken = "GEHEIMES-TOKEN-abcdefghijklmnopqrstuvwxyz0123456789"

// ---- Säubern ----

func TestSaeubernNeutralisiertErwaehnungenUndMarkdown(t *testing.T) {
	tests := []struct {
		name, in string
		// verboten: Zeichenfolgen, die in der Ausgabe nie vorkommen dürfen.
		verboten []string
	}{
		{"everyone", "@everyone", []string{"@everyone"}},
		{"here", "Hallo @here!", []string{"@here"}},
		{"Mitglied", "<@123456789012345678>", []string{"<@", "@1"}},
		{"Rolle", "<@&123456789012345678>", []string{"<@", "@&"}},
		{"Kanal", "<#123456789012345678>", []string{"<#"}},
		{"Emoji", "<:smile:123456789012345678>", []string{"<:"}},
		{"Zeitstempel", "<t:1700000000:R>", []string{"<t:"}},
		{"Link", "https://boese.example/x", []string{"://"}},
		{"maskierter Link", "[klick](https://boese.example)", []string{"://"}},
		{"fett und kursiv", "**fett** __unter__ ~~weg~~ `code` ||spoiler||", []string{"**", "__", "~~", "`code`", "||"}},
		{"Zitat und Überschrift", "> zitat # titel", nil},
		{"Rückstrich", `\@everyone`, []string{`\@everyone`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Saeubern(tt.in, 200)
			for _, v := range tt.verboten {
				if strings.Contains(got, v) {
					t.Errorf("Saeubern(%q) = %q enthält noch %q", tt.in, got, v)
				}
			}
			// Jedes Markdown-Zeichen steht hinter einem Rückstrich, jedes @ und < trägt das unsichtbare Leerzeichen.
			rs, rest := []rune(got), []rune{}
			for i := 0; i < len(rs); i++ {
				if rs[i] == '\\' {
					i++ // das geschützte Zeichen
					continue
				}
				rest = append(rest, rs[i])
				if (rs[i] == '@' || rs[i] == '<') && (i+1 >= len(rs) || rs[i+1] != '​') {
					t.Errorf("Saeubern(%q) = %q: %q ohne unsichtbares Leerzeichen", tt.in, got, rs[i])
				}
			}
			if strings.ContainsAny(string(rest), "*_~`|>#[]") {
				t.Errorf("Saeubern(%q) = %q: ungeschütztes Markdown-Zeichen", tt.in, got)
			}
		})
	}
}

func TestSaeubernTextBleibtLesbar(t *testing.T) {
	// Minecraft-Namen und gewöhnlicher Text bleiben (bis auf den Rückstrich
	// vor dem Unterstrich) erhalten.
	if got, want := Saeubern("Swaggolas_Claus", 40), `Swaggolas\_Claus`; got != want {
		t.Errorf("Name: %q, erwartet %q", got, want)
	}
	if got := Saeubern("Unsere Wandergruppe", 80); got != "Unsere Wandergruppe" {
		t.Errorf("Titel: %q", got)
	}
}

func TestSaeubernLaengeUndSteuerzeichen(t *testing.T) {
	if got := Saeubern("a\nb\r\nc\x00d\u202eevil\u200b", 100); strings.ContainsAny(got, "\n\r\x00\u202e") || strings.Contains(got, "\u200b\u200b") {
		t.Errorf("Steuer- oder Formatzeichen übrig: %q", got)
	}
	got := Saeubern(strings.Repeat("x", 500), 40)
	if n := len([]rune(got)); n > 40 {
		t.Errorf("Länge %d, erlaubt 40", n)
	}
	// Ein eingeschmuggeltes unsichtbares Zeichen bricht die Entschärfung nicht.
	if got := Saeubern("@\u200beveryone", 40); strings.Contains(got, "@everyone") {
		t.Errorf("eingeschmuggeltes U+200B: %q", got)
	}
	if got := Saeubern("- Punkt", 40); !strings.HasPrefix(got, `\-`) {
		t.Errorf("Aufzählungszeichen am Anfang: %q", got)
	}
}

// ---- Webhook ----

func TestWebhookPruefe(t *testing.T) {
	gut := []string{
		"https://discord.com/api/webhooks/123456789012345678/" + geheimesToken,
		"https://discordapp.com/api/webhooks/123456789012345678/" + geheimesToken,
		"https://ptb.discord.com/api/webhooks/123456789012345678/" + geheimesToken,
		"https://canary.discord.com/api/v10/webhooks/123456789012345678/" + geheimesToken,
		"  https://discord.com/api/webhooks/123456789012345678/" + geheimesToken + "\n",
	}
	for _, u := range gut {
		if _, err := Pruefe(u); err != nil {
			t.Errorf("%q: %v", u, err)
		}
	}
	schlecht := []string{
		"",
		"http://discord.com/api/webhooks/123456789012345678/" + geheimesToken,
		"https://boese.example/api/webhooks/123456789012345678/" + geheimesToken,
		"https://discord.com.boese.example/api/webhooks/123456789012345678/" + geheimesToken,
		"https://discord.com:8443/api/webhooks/123456789012345678/" + geheimesToken,
		"https://user:pw@discord.com/api/webhooks/123456789012345678/" + geheimesToken,
		"https://discord.com/api/webhooks/123456789012345678/" + geheimesToken + "?wait=true",
		"https://discord.com/api/webhooks/123456789012345678/" + geheimesToken + "#x",
		"https://discord.com/api/webhooks/abc/" + geheimesToken,
		"https://discord.com/api/webhooks/123456789012345678/kurz",
		"https://discord.com/anderer/pfad/" + geheimesToken,
		"nur Text",
	}
	for _, u := range schlecht {
		_, err := Pruefe(u)
		if err == nil {
			t.Errorf("%q wurde akzeptiert", u)
			continue
		}
		if strings.Contains(err.Error(), geheimesToken) || strings.Contains(err.Error(), "boese") {
			t.Errorf("Fehler nennt die Adresse: %v", err)
		}
	}
}

func TestWebhookZeigtSichNie(t *testing.T) {
	w := Webhook("https://discord.com/api/webhooks/123456789012345678/" + geheimesToken)
	var logbuf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l.Info("Konfiguration", "webhook", w)
	js, _ := json.Marshal(struct{ W Webhook }{w})
	ausgaben := []string{
		fmt.Sprint(w), fmt.Sprintf("%v %+v %#v %s %q", w, w, w, w, w), string(js), logbuf.String(),
		fmt.Sprintf("%+v", struct{ W Webhook }{w}),
	}
	for _, a := range ausgaben {
		if strings.Contains(a, geheimesToken) || strings.Contains(a, "123456789012345678") {
			t.Errorf("Adresse sichtbar: %s", a)
		}
	}
	if w.Adresse() == "" || !w.Gesetzt() {
		t.Error("Adresse() muss die echte Adresse liefern")
	}
}

// ---- Sender ----

// uhr ist eine falsche Uhr: Schlafen schiebt sie nur vor.
type uhr struct {
	mu      sync.Mutex
	t       time.Time
	schlief []time.Duration
}

func (u *uhr) jetzt() time.Time { u.mu.Lock(); defer u.mu.Unlock(); return u.t }
func (u *uhr) schlafen(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if d > 0 {
		u.t = u.t.Add(d)
		u.schlief = append(u.schlief, d)
	}
	return nil
}

type aufruf struct {
	at   time.Time
	body nachricht
	raw  string
}

// webhookServer antwortet nach der Reihe mit den Antworten (die letzte gilt
// dann dauerhaft) und merkt Zeit und Körper jedes Aufrufs.
type webhookServer struct {
	*httptest.Server
	mu       sync.Mutex
	aufrufe  []aufruf
	antworte func(n int, w http.ResponseWriter)
	got      chan struct{}
}

func neuerWebhookServer(t *testing.T, u *uhr, antworte func(n int, w http.ResponseWriter)) *webhookServer {
	t.Helper()
	ws := &webhookServer{antworte: antworte, got: make(chan struct{}, 100)}
	ws.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var n nachricht
		json.Unmarshal(raw, &n)
		ws.mu.Lock()
		ws.aufrufe = append(ws.aufrufe, aufruf{at: u.jetzt(), body: n, raw: string(raw)})
		k := len(ws.aufrufe)
		ws.mu.Unlock()
		ws.antworte(k, w)
		ws.got <- struct{}{}
	}))
	t.Cleanup(ws.Close)
	return ws
}

func (ws *webhookServer) liste() []aufruf {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return append([]aufruf(nil), ws.aufrufe...)
}

func ok(_ int, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// laufen startet den Sender und wartet, bis n Aufrufe da sind und die
// Warteschlange leer ist.
func laufen(t *testing.T, s *Sender, ws *webhookServer, n int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Lauf(ctx); close(done) }()
	for i := 0; i < n; i++ {
		select {
		case <-ws.got:
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatalf("nur %d von %d Aufrufen", i, n)
		}
	}
	// Dem Sender Zeit geben, die Antwort zu verarbeiten.
	deadline := time.Now().Add(5 * time.Second)
	for s.Wartend() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func neuerTestSender(ws *webhookServer, u *uhr, log *slog.Logger) *Sender {
	return NeuerSender(SenderOptionen{
		Webhook: Webhook(ws.URL + "/api/webhooks/123456789012345678/" + geheimesToken),
		Client:  ws.Client(), Log: log, Schlafen: u.schlafen, Jetzt: u.jetzt,
	})
}

func TestSenderBegrenztDieRate(t *testing.T) {
	u := &uhr{t: time.Unix(1_000_000, 0)}
	ws := neuerWebhookServer(t, u, ok)
	s := neuerTestSender(ws, u, nil)
	for i := 0; i < 4; i++ {
		s.Senden(fmt.Sprintf("Nachricht %d", i))
	}
	laufen(t, s, ws, 4)
	as := ws.liste()
	if len(as) != 4 {
		t.Fatalf("%d Aufrufe", len(as))
	}
	for i, a := range as {
		if want := fmt.Sprintf("Nachricht %d", i); a.body.Content != want {
			t.Errorf("Aufruf %d: %q, erwartet %q (Reihenfolge)", i, a.body.Content, want)
		}
		if i > 0 && a.at.Sub(as[i-1].at) < MinAbstand {
			t.Errorf("Abstand %d: %v, erlaubt mindestens %v", i, a.at.Sub(as[i-1].at), MinAbstand)
		}
	}
}

func TestSenderVerbietetErwaehnungen(t *testing.T) {
	u := &uhr{t: time.Unix(1_000_000, 0)}
	ws := neuerWebhookServer(t, u, ok)
	s := neuerTestSender(ws, u, nil)
	s.Senden("Hallo")
	laufen(t, s, ws, 1)
	a := ws.liste()[0]
	if !strings.Contains(a.raw, `"allowed_mentions":{"parse":[]}`) {
		t.Errorf("allowed_mentions fehlt oder parse ist nicht leer: %s", a.raw)
	}
}

func TestSenderWartetAufRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name string
		resp func(n int, w http.ResponseWriter)
		min  time.Duration
	}{
		{"429 mit Kopf", func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}, 30 * time.Second},
		{"429 mit Körper", func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, `{"message":"You are being rate limited.","retry_after":12.5,"global":false}`)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}, 12500 * time.Millisecond},
		{"5xx mit Kopf", func(n int, w http.ResponseWriter) {
			if n == 1 {
				w.Header().Set("Retry-After", "20")
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}, 20 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := &uhr{t: time.Unix(1_000_000, 0)}
			ws := neuerWebhookServer(t, u, tt.resp)
			s := neuerTestSender(ws, u, nil)
			s.Senden("Hallo")
			laufen(t, s, ws, 2)
			as := ws.liste()
			if len(as) != 2 {
				t.Fatalf("%d Aufrufe, erwartet 2 (ein Versuch, eine Wiederholung)", len(as))
			}
			if d := as[1].at.Sub(as[0].at); d < tt.min {
				t.Errorf("Wiederholung nach %v, Retry-After verlangte %v", d, tt.min)
			}
		})
	}
}

func TestSenderGibtNachDreiVersuchenAufUndBleibtDanachBereit(t *testing.T) {
	u := &uhr{t: time.Unix(1_000_000, 0)}
	ws := neuerWebhookServer(t, u, func(n int, w http.ResponseWriter) {
		if n <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s := neuerTestSender(ws, u, nil)
	s.Senden("verloren")
	s.Senden("kommt an")
	laufen(t, s, ws, 4)
	as := ws.liste()
	if len(as) != 4 || as[3].body.Content != "kommt an" {
		t.Fatalf("Aufrufe: %d, letzter %q", len(as), as[len(as)-1].body.Content)
	}
	for i := 0; i < 3; i++ {
		if as[i].body.Content != "verloren" {
			t.Errorf("Aufruf %d: %q", i, as[i].body.Content)
		}
	}
}

func TestSenderVerwirftAbgelehnteNachrichtenOhneWiederholung(t *testing.T) {
	u := &uhr{t: time.Unix(1_000_000, 0)}
	ws := neuerWebhookServer(t, u, func(n int, w http.ResponseWriter) { w.WriteHeader(http.StatusNotFound) })
	s := neuerTestSender(ws, u, nil)
	s.Senden("a")
	s.Senden("b")
	laufen(t, s, ws, 2)
	if as := ws.liste(); len(as) != 2 {
		t.Errorf("%d Aufrufe, erwartet 2 (404 wird nicht wiederholt)", len(as))
	}
}

func TestSenderBlockiertNieUndBegrenztDieWarteschlange(t *testing.T) {
	u := &uhr{t: time.Unix(1_000_000, 0)}
	ws := neuerWebhookServer(t, u, ok)
	s := neuerTestSender(ws, u, nil) // Lauf läuft nicht: nichts wird abgearbeitet
	fertig := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Senden(fmt.Sprintf("n%d", i))
		}
		close(fertig)
	}()
	select {
	case <-fertig:
	case <-time.After(5 * time.Second):
		t.Fatal("Senden blockiert")
	}
	if n := s.Wartend(); n != MaxWarteschlange {
		t.Errorf("%d wartend, erlaubt %d", n, MaxWarteschlange)
	}
	// Die ältesten fielen weg, die neuesten sind noch da.
	if first, _ := s.holen(); first != fmt.Sprintf("n%d", 1000-MaxWarteschlange) {
		t.Errorf("älteste Nachricht in der Schlange: %q", first)
	}
}

// Die Adresse steht in keinem Log, auch nicht, wenn die Verbindung scheitert
// (net/http schreibt sie sonst in den Fehlertext) oder Discord antwortet mit
// Fehlern.
func TestSenderLogEnthaeltNieDieAdresse(t *testing.T) {
	logbuf := &logPuffer{}
	log := slog.New(slog.NewTextHandler(logbuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// 1. Fehlerantworten.
	u := &uhr{t: time.Unix(1_000_000, 0)}
	status := []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusBadRequest}
	ws := neuerWebhookServer(t, u, func(n int, w http.ResponseWriter) { w.WriteHeader(status[(n-1)%len(status)]) })
	s := neuerTestSender(ws, u, log)
	s.Senden("x")
	s.Senden("y")
	laufen(t, s, ws, 5)

	// 2. Verbindung scheitert: der Server ist weg.
	ws2 := neuerWebhookServer(t, u, ok)
	s2 := neuerTestSender(ws2, u, log)
	ws2.Close()
	s2.Senden("z")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s2.Lauf(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logbuf.String(), "Verbindung fehlgeschlagen") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	for s2.Wartend() > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	out := logbuf.String()
	if !strings.Contains(out, "Discord-Webhook") {
		t.Fatalf("Debug-Log ist leer: %q", out)
	}
	for _, v := range []string{geheimesToken, "123456789012345678", "/api/webhooks", "http://"} {
		if strings.Contains(out, v) {
			t.Errorf("Log enthält %q:\n%s", v, out)
		}
	}
}

func TestRetryAfterLesen(t *testing.T) {
	tests := []struct {
		kopf string
		body string
		want time.Duration
	}{
		{"3", "", 3 * time.Second},
		{"1.5", "", 1500 * time.Millisecond},
		{"", `{"retry_after": 2.25}`, 2250 * time.Millisecond},
		{"", `kaputt`, 0},
		{"-4", "", 0},
		{"abc", `{"retry_after": 7}`, 7 * time.Second},
		{"99999999", "", time.Hour},
	}
	for _, tt := range tests {
		if got := retryAfter(tt.kopf, []byte(tt.body)); got != tt.want {
			t.Errorf("retryAfter(%q, %q) = %v, erwartet %v", tt.kopf, tt.body, got, tt.want)
		}
	}
}

// ---- Dienst ----

type sammler struct {
	mu   sync.Mutex
	text []string
}

func (s *sammler) senden(t string) { s.mu.Lock(); s.text = append(s.text, t); s.mu.Unlock() }
func (s *sammler) alle() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.text...)
}

var t0 = time.Unix(2_000_000, 0)

func host(name string) *api.HostInfo {
	return &api.HostInfo{Email: strings.ToLower(name) + "@geheim.example", MinecraftName: name, MachineID: "m"}
}

func bereit(h string, spieler ...string) api.Status {
	return api.Status{Host: host(h), HostReady: true, Players: spieler}
}

func neuerDienst(s *sammler, mod func(*Optionen)) *Dienst {
	o := Optionen{Gruppe: "Unsere Gruppe", Senden: s.senden}
	if mod != nil {
		mod(&o)
	}
	return Neu(o)
}

// Nach dem Start (Anlaufzeit) gibt es nie Nachrichten über den Zustand, der
// schon da war – auch nicht, wenn Spielerliste und „bereit“ erst mit dem
// nächsten Lebenszeichen des Hosts zurückkommen.
func TestDienstSendetNachNeustartKeineFlut(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, nil)
	// Der Koordinator startet: Host aus lease.json, aber noch nicht bereit, keine Spieler.
	d.Beobachte(api.Status{Host: host("Anna")}, t0)
	// Nach 10 s das erste Lebenszeichen: bereit, drei Spieler.
	d.Beobachte(bereit("Anna", "Anna", "Ben", "Clara"), t0.Add(10*time.Second))
	d.Beobachte(bereit("Anna", "Anna", "Ben", "Clara"), t0.Add(20*time.Second))
	if got := s.alle(); len(got) != 0 {
		t.Fatalf("Nachrichten nach dem Start: %q", got)
	}
	// Nach der Anlaufzeit wirken Änderungen.
	d.Beobachte(bereit("Anna", "Anna", "Ben", "Clara", "Dora"), t0.Add(Anlauf+5*time.Second))
	d.Beobachte(bereit("Anna", "Anna", "Ben", "Clara", "Dora"), t0.Add(Anlauf+20*time.Second))
	if got := s.alle(); len(got) != 1 || got[0] != "Unsere Gruppe: Dora ist beigetreten." {
		t.Fatalf("Nachrichten: %q", got)
	}
}

func TestDienstFasstBeitritteZusammen(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, nil)
	t1 := t0.Add(time.Minute) // nach der Anlaufzeit
	d.Beobachte(bereit("Anna", "Anna"), t0)
	d.Beobachte(bereit("Anna", "Anna"), t1)

	d.Beobachte(bereit("Anna", "Anna", "Ben"), t1.Add(5*time.Second))
	d.Beobachte(bereit("Anna", "Anna", "Ben", "Clara"), t1.Add(8*time.Second))
	d.Beobachte(bereit("Anna", "Anna", "Clara"), t1.Add(10*time.Second)) // Ben geht wieder: fällt weg
	d.Beobachte(bereit("Anna", "Anna", "Clara", "Dora"), t1.Add(12*time.Second))
	d.Beobachte(bereit("Anna", "Anna", "Clara", "Dora"), t1.Add(14*time.Second))
	if got := s.alle(); len(got) != 0 {
		t.Fatalf("zu früh gesendet: %q", got)
	}
	// 10 s nach dem ersten Ereignis (t1+5s) ist es Zeit.
	d.Beobachte(bereit("Anna", "Anna", "Clara", "Dora"), t1.Add(15*time.Second))
	got := s.alle()
	if len(got) != 1 || got[0] != "Unsere Gruppe: Clara und Dora sind beigetreten." {
		t.Fatalf("Nachrichten: %q", got)
	}
	d.Beobachte(bereit("Anna", "Anna", "Clara", "Dora"), t1.Add(60*time.Second))
	if got := s.alle(); len(got) != 1 {
		t.Errorf("nichts Neues, aber: %q", got)
	}
}

func TestDienstSchicktGesammeltesVorDemNaechstenEreignis(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, nil)
	t1 := t0.Add(time.Minute)
	d.Beobachte(bereit("Anna", "Anna", "Ben"), t0)
	d.Beobachte(bereit("Anna", "Anna", "Ben"), t1)
	d.Beobachte(bereit("Anna", "Anna"), t1.Add(5*time.Second)) // Ben geht, wird gesammelt
	if got := s.alle(); len(got) != 0 {
		t.Fatalf("zu früh gesendet: %q", got)
	}
	// Ein anderes Ereignis schickt das Gesammelte zuerst ab.
	wartung := bereit("Anna", "Anna")
	wartung.Maintenance = &api.Maintenance{Reason: "Update"}
	d.Beobachte(wartung, t1.Add(10*time.Second))
	got := s.alle()
	if len(got) != 2 || got[0] != "Unsere Gruppe: Ben hat die Welt verlassen." || !strings.Contains(got[1], "Wartung am Server angekündigt") {
		t.Errorf("Nachrichten: %q", got)
	}
}

func TestDienstStandardschalterUndUmstellung(t *testing.T) {
	ablauf := func(d *Dienst, s *sammler) []string {
		t1 := t0.Add(time.Minute)
		d.Beobachte(api.Status{}, t0)
		d.Beobachte(api.Status{}, t1)
		d.Beobachte(api.Status{Host: host("Anna")}, t1.Add(5*time.Second))                               // hostet jetzt
		d.Beobachte(bereit("Anna"), t1.Add(10*time.Second))                                              // online
		d.Beobachte(bereit("Anna", "Ben"), t1.Add(15*time.Second))                                       // Ben
		d.Beobachte(bereit("Anna", "Ben"), t1.Add(30*time.Second))                                       // (Sammelzeit um)
		d.Beobachte(api.Status{}, t1.Add(35*time.Second))                                                // niemand
		d.Beobachte(api.Status{Maintenance: &api.Maintenance{Reason: "Update"}}, t1.Add(40*time.Second)) // Wartung
		d.Beobachte(api.Status{}, t1.Add(45*time.Second))                                                // vorbei
		return s.alle()
	}
	s := &sammler{}
	got := ablauf(neuerDienst(s, nil), s)
	want := []string{
		"Unsere Gruppe: Anna hostet jetzt – die Welt ist gleich bereit.",
		"Unsere Gruppe: Die Welt ist online (Host Anna).",
		"Unsere Gruppe: Ben ist beigetreten.",
		"Unsere Gruppe: Wartung am Server angekündigt (Update). Der Server stoppt gleich sauber, danach ist Spielen wieder möglich.",
		"Unsere Gruppe: Wartung vorbei – ihr könnt wieder spielen.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Standard:\n%s\nerwartet:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Umgestellt: Spieler aus, „niemand“ an; Beitrittsanfrage gibt es auf Discord nie.
	s2 := &sammler{}
	got = ablauf(neuerDienst(s2, func(o *Optionen) {
		o.Arten = map[ereignis.Art]bool{ereignis.Spieler: false, ereignis.NiemandHostet: true, ereignis.Beitrittsanfrage: true}
	}), s2)
	for _, g := range got {
		if strings.Contains(g, "beigetreten") || strings.Contains(g, "Beitrittsanfrage") {
			t.Errorf("abgeschaltet, aber gesendet: %s", g)
		}
	}
	if !slicesContain(got, "Unsere Gruppe: Niemand hostet mehr – die Welt ist offline.") {
		t.Errorf("„Niemand hostet mehr“ fehlt: %q", got)
	}
}

func slicesContain(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestDienstWackelndesOnline(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, nil)
	t1 := t0.Add(time.Minute)
	d.Beobachte(bereit("Anna"), t0)
	d.Beobachte(bereit("Anna"), t1)
	d.Beobachte(api.Status{Host: host("Anna")}, t1.Add(5*time.Second))
	d.Beobachte(bereit("Anna"), t1.Add(10*time.Second))
	d.Beobachte(api.Status{Host: host("Anna")}, t1.Add(15*time.Second))
	d.Beobachte(bereit("Anna"), t1.Add(20*time.Second))
	if got := s.alle(); len(got) != 1 {
		t.Errorf("%d Nachrichten bei wackelndem „bereit“: %q", len(got), got)
	}
}

// Namen und Titel sind fremder Text: nichts davon darf erwähnen, verlinken
// oder formatieren, und keine E-Mail-Adresse taucht auf.
func TestDienstSaeubertNamenUndTitel(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, func(o *Optionen) { o.Gruppe = "@everyone **Boss** https://boese.example" })
	t1 := t0.Add(time.Minute)
	d.Beobachte(api.Status{}, t0)
	d.Beobachte(api.Status{}, t1)
	d.Beobachte(api.Status{Host: &api.HostInfo{Email: "geheim@geheim.example", MinecraftName: "<@123456789012345678>"}}, t1.Add(5*time.Second))
	got := strings.Join(s.alle(), "\n")
	for _, v := range []string{"@everyone", "<@", "**Boss**", "://", "geheim"} {
		if strings.Contains(got, v) {
			t.Errorf("Nachricht enthält %q: %s", v, got)
		}
	}
	if got == "" {
		t.Fatal("keine Nachricht")
	}
}

func TestDienstNenntWeltBeiMehrerenWelten(t *testing.T) {
	s := &sammler{}
	d := neuerDienst(s, nil)
	t1 := t0.Add(time.Minute)
	mit := func(h *api.HostInfo) api.Status {
		return api.Status{Host: h, Worlds: []api.GroupWorld{{ID: "w1", Name: "Survival"}, {ID: "w2", Name: "Kreativ"}}, RunningWorld: "w2"}
	}
	d.Beobachte(api.Status{}, t0)
	d.Beobachte(api.Status{}, t1)
	d.Beobachte(mit(host("Anna")), t1.Add(5*time.Second))
	if got := s.alle(); len(got) != 1 || !strings.HasPrefix(got[0], "Unsere Gruppe · Kreativ: Anna hostet") {
		t.Errorf("Nachrichten: %q", got)
	}
}

func TestDienstBeobachteBlockiertNie(t *testing.T) {
	// Ohne laufenden Sender und mit Flut an Ereignissen kehrt Beobachte immer zurück.
	d := Neu(Optionen{Webhook: "http://127.0.0.1:1/x", Anlauf: time.Nanosecond})
	done := make(chan struct{})
	go func() {
		now := t0
		for i := 0; i < 500; i++ {
			now = now.Add(5 * time.Second)
			st := api.Status{}
			if i%2 == 0 {
				st = bereit("Anna")
			}
			d.Beobachte(st, now)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Beobachte blockiert")
	}
}
