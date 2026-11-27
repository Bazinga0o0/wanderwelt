package fehlerbericht

import (
	"strings"
	"testing"
)

const (
	testHome  = `C:\Users\Max Mustermann`
	testUser  = "Max Mustermann"
	testToken = "tok-GEHEIM-1234abcd"
)

func testSaeuberer() *Saeuberer {
	return NeuerSaeuberer(testHome, testUser, testToken, "rconPASS99")
}

// Jedes Muster: Das Geheimnis ist weg, der Rest der Zeile bleibt.
func TestSaeubernGeheimnisseWeg(t *testing.T) {
	s := testSaeuberer()
	hex40 := "0123456789abcdef0123456789abcdef01234567"
	b64 := "dG9rZW46IGdlaGVpbS1zdXBlci1sYW5nZXItYmxvY2steG1sS2V5MTIzNDU2Nzg5MEFCQ0RF"
	cases := []struct {
		name, in string
		weg      []string // darf nicht mehr vorkommen
		bleibt   []string // muss noch vorkommen
	}{
		{"Bearer-Header", "Authorization: Bearer abc123.DEF-456_ghi", []string{"abc123.DEF-456_ghi"}, []string{"Authorization", "Bearer"}},
		{"Bearer klein im Text", "Header bearer 9f8e7d6c5b4a3210 gesendet", []string{"9f8e7d6c5b4a3210"}, []string{"gesendet"}},
		{"Authorization Basic", `authorization: Basic dXNlcjpwYXNz`, []string{"dXNlcjpwYXNz"}, []string{"Basic"}},
		{"Authorization in JSON", `{"Authorization":"Bearer sehrgeheim12345"}`, []string{"sehrgeheim12345"}, []string{"Authorization"}},
		{"token=", "GET /v1/status?token=abcdef123456&x=1", []string{"abcdef123456"}, []string{"/v1/status?", "&x=1"}},
		{"secret=", "client secret=SuperS3cret! ende", []string{"SuperS3cret!"}, []string{"ende"}},
		{"password=", "password=hunter2", []string{"hunter2"}, []string{"password="}},
		{"Passwort Doppelpunkt", "RCON-Passwort: Geh3im", []string{"Geh3im"}, nil},
		{"rcon.password", "rcon.password=abcXYZ789", []string{"abcXYZ789"}, []string{"rcon.password="}},
		{"Java-Property", "-Drcon.password=abc123XYZ -Xmx4G", []string{"abc123XYZ"}, []string{"-Xmx4G"}},
		{"JSON password", `{"user":"max","password":"p@ss w0rd","x":1}`, []string{"p@ss w0rd"}, []string{`"user":"max"`, `"x":1`}},
		{"YAML token", "token: tun_abcdef0123456789", []string{"tun_abcdef0123456789"}, []string{"token:"}},
		{"YAML in Anführungszeichen", `token: 'a b c'`, []string{"a b c"}, nil},
		{"access_token", "access_token=ya29.a0AfH6SMB", []string{"ya29.a0AfH6SMB"}, nil},
		{"api-key", "x-api-key: 1234567890abcdef", []string{"1234567890abcdef"}, nil},
		{"Kommandozeile", "start --accessToken eyxyz98765 --uuid 1", []string{"eyxyz98765"}, []string{"--uuid"}},
		{"Kommandozeile mit =", "run --password=geheim123 --fast", []string{"geheim123"}, []string{"--fast"}},
		{"Cookie", "Cookie: sid=abc123; theme=dark", []string{"abc123", "dark"}, []string{"Cookie"}},
		{"JWT", "id eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.SflKxwRJSMeKKF2QT4fw ok", []string{"eyJhbGci", "SflKxwRJSMeKKF2QT4fw"}, []string{"ok"}},
		{"GitHub-Token", "git ghp_abcdefghijklmnopqrstuvwxyz0123456789 push", []string{"ghp_abcdefghij"}, []string{"push"}},
		{"Discord-Webhook", "post https://discord.com/api/webhooks/123456789012345678/AbCdEf-ghIJKlmnOPq_rstUVwxyz0123 done", []string{"AbCdEf", "123456789012345678"}, []string{"post", "done"}},
		{"Discord-Webhook discordapp v10", "https://discordapp.com/api/v10/webhooks/1/abc_DEF-ghi", []string{"abc_DEF-ghi"}, nil},
		{"Slack-Webhook", "https://hooks.slack.com/services/T000/B000/XXXXXXXX", []string{"XXXXXXXX"}, nil},
		{"URL-Zugangsdaten", "clone https://max:s3cr3t@git.example.org/repo.git", []string{"max:s3cr3t", "s3cr3t"}, []string{"git.example.org/repo.git"}},
		{"URL nur Nutzer", "ftp://nutzer@ftp.example.org/x", []string{"nutzer@"}, []string{"ftp.example.org/x"}},
		{"URL Passwort mit @", "https://max:p@ss@host.example.org/", []string{"p@ss"}, []string{"host.example.org"}},
		{"Gruppencode", "Gruppencode w1.aHR0cHM6Ly9leGFtcGxlLm9yZw abgelehnt", []string{"aHR0cHM6Ly9leGFtcGxlLm9yZw"}, []string{"abgelehnt"}},
		{"Beitritts-Link", "wanderwelt://beitreten?s=w1.YWJjZGVmZ2hpamtsbW5v", []string{"YWJjZGVmZ2hpamtsbW5v"}, []string{"wanderwelt://beitreten"}},
		{"Base64-Block", "gruppeEingebettet=" + b64 + " Ende", []string{b64}, []string{"Ende"}},
		{"Base64 mit Auffüllung", "blob " + b64 + "==", []string{b64}, nil},
		{"Dateiname mit Token", `Downloads\wanderwelt-freunde\finja-token-AbCd1234Efgh.yaml`, []string{"AbCd1234Efgh"}, []string{"finja-", ".yaml"}},
		{"bekanntes Token wörtlich", "Antwort 401 für " + testToken + " vom Koordinator", []string{testToken}, []string{"vom Koordinator"}},
		{"bekanntes RCON-Passwort", "rcon: rconPASS99 abgelehnt", []string{"rconPASS99"}, []string{"abgelehnt"}},
		{"Spieler-IP", "Alice[/203.0.113.77:54321] logged in with entity id 5", []string{"203.0.113.77"}, []string{"Alice", "logged in with entity id 5"}},
		{"Spieler-IP ohne Port", "Bob (/198.51.100.4) joined", []string{"198.51.100.4"}, []string{"Bob", "joined"}},
		{"Hex-Hash neben Geheimnis", "sha512:" + hex40 + " token=abc12345xyz", []string{"abc12345xyz"}, []string{hex40}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.Zeile(c.in)
			for _, w := range c.weg {
				if strings.Contains(got, w) {
					t.Errorf("%q noch in %q", w, got)
				}
			}
			for _, b := range c.bleibt {
				if !strings.Contains(got, b) {
					t.Errorf("%q fehlt in %q", b, got)
				}
			}
			// Zweimal säubern ändert nichts mehr.
			if again := s.Zeile(got); again != got {
				t.Errorf("nicht idempotent:\n 1: %q\n 2: %q", got, again)
			}
		})
	}
}

// Pfade: Der Benutzer verschwindet, der Rest des Pfads bleibt.
func TestSaeubernPfade(t *testing.T) {
	s := testSaeuberer()
	cases := []struct{ in, want string }{
		{`Home C:\Users\Max Mustermann\AppData\Local\wanderwelt`, `Home %USERPROFILE%\AppData\Local\wanderwelt`},
		{`"C:\\Users\\Max Mustermann\\AppData\\x"`, `"%USERPROFILE%\\AppData\\x"`},
		{`C:/Users/Max Mustermann/Downloads/a.zip`, `%USERPROFILE%/Downloads/a.zip`},
		{`c:\users\MAX MUSTERMANN\Desktop`, `%USERPROFILE%\Desktop`},
		{`D:\Users\Anna\Documents\x`, `%USERPROFILE%\Documents\x`},               // anderer Benutzer, anderes Laufwerk
		{`\\?\C:\Users\Anna\x`, `\\?\%USERPROFILE%\x`},                           // Präfix für lange Pfade
		{`Ordner C:\Users\Public\Documents`, `Ordner C:\Users\Public\Documents`}, // kein Benutzer
		{`C:\Users\Default\NTUSER.DAT`, `C:\Users\Default\NTUSER.DAT`},
		{`Benutzer Max Mustermann meldet sich an`, `Benutzer %USERNAME% meldet sich an`},
		{`C:\Users\Anna Lena`, `%USERPROFILE%`}, // Name ohne Trennzeichen dahinter: bis zum Leerzeichen
	}
	for _, c := range cases {
		got := s.Zeile(c.in)
		if c.in == `C:\Users\Anna Lena` {
			// Bis zum Leerzeichen genügt: Der Rest ist ein Wort.
			if !strings.HasPrefix(got, "%USERPROFILE%") || strings.Contains(got, "Anna") {
				t.Errorf("%q -> %q", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%q\n  ist  %q\n  soll %q", c.in, got, c.want)
		}
	}
}

// Der Benutzername als Teil anderer Wörter bleibt, wenn er zu allgemein oder
// Teil eines längeren Wortes ist.
func TestSaeubernBenutzername(t *testing.T) {
	s := NeuerSaeuberer(`C:\Users\Leon`, "Leon")
	if got := s.Zeile("Leonardo und Leon sind da, Leon."); got != "Leonardo und %USERNAME% sind da, %USERNAME%." {
		t.Errorf("Wortgrenzen: %q", got)
	}
	// Zu kurz oder zu allgemein: kein Ersetzen im Text.
	for _, u := range []string{"ab", "admin", "Public", ""} {
		s := NeuerSaeuberer("", u)
		in := "admin und ab melden sich an"
		if got := s.Zeile(in); got != in {
			t.Errorf("Benutzer %q: %q", u, got)
		}
	}
	// Ein Geheimnis unter 6 Zeichen wird nicht wörtlich entfernt.
	s = NeuerSaeuberer("", "", "abc12")
	if got := s.Zeile("abc12 bleibt"); got != "abc12 bleibt" {
		t.Errorf("kurzes Geheimnis: %q", got)
	}
}

// Das längere Geheimnis wird zuerst entfernt, sonst bliebe ein Rest.
func TestSaeubernLaengeresGeheimnisZuerst(t *testing.T) {
	s := NeuerSaeuberer("", "", "geheim-abc", "geheim-abc-und-mehr")
	got := s.Zeile("x geheim-abc-und-mehr y")
	if strings.Contains(got, "mehr") || strings.Contains(got, "geheim") {
		t.Errorf("Rest übrig: %q", got)
	}
}

// Negativfälle: Harmlose Zeilen bleiben Byte für Byte gleich.
func TestSaeubernHarmlosBleibt(t *testing.T) {
	s := testSaeuberer()
	hex128 := strings.Repeat("0123456789abcdef", 8)
	harmlos := []string{
		"2026/11/27 12:34:56.123456 host: Stand w1/12 eingereicht",
		"host: Stand 7: speichern 1,2 s, bereitstellen 3,4 s, hashen 0,5 s, hochgeladen 12,5 MB in 2,1 s, gesamt 7,3 s",
		"mcserver: Server gestartet (PID 4242) in C:\\ProgramData\\wanderwelt\\server",
		"mcserver: Server bereit (127.0.0.1:25565)",
		"Verbindung zu localhost/127.0.0.1:25565 verloren",
		"Version 26.2.0.1 und Fabric 0.20.1",
		"sha512:" + hex128,
		"sha256:" + strings.Repeat("ab12", 16),
		"https://wanderwelt.example.org/v1/head",
		"https://example.org:7443/pfad?seite=2",
		"Kontakt: user@example.org",
		"Bearer of bad news",
		"Der Token ist abgelaufen",
		"Token abgelaufen, bitte neu anmelden",
		"Passwort-Änderung wird nicht unterstützt",
		"Secrets Management ist ein Thema",
		"Alice joined the game",
		"<Alice> hallo zusammen, wie geht's?",
		"world/region/r.0.0.mca geändert",
		"mods/wanderwelt-chatverlauf-26.2-1.0.0.jar kopiert",
		"C:\\ProgramData\\wanderwelt\\mods\\fabric-api-0.161.0+26.2.jar",
		"wanderwelt-logs/2026-11-27-a1b2c3.log",
		"Dauerbetrieb an (Netz)",
		"key bindings geladen",
		"Datei w1.json gelesen",
		"w1/5 ist die Welt",
		"",
		"   ",
	}
	for _, l := range harmlos {
		if got := s.Zeile(l); got != l {
			t.Errorf("verändert:\n  vorher %q\n  nachher %q", l, got)
		}
	}
	// Mehrzeilig, mit CRLF: Zeilenenden bleiben.
	in := "a: 1\r\nb: zwei\r\n\r\nc\n"
	if got := s.Text(in); got != in {
		t.Errorf("Zeilenenden: %q", got)
	}
}

// Mehrzeiliger Text: jede Zeile einzeln, Zeilenenden bleiben.
func TestSaeubernTextZeilenweise(t *testing.T) {
	s := testSaeuberer()
	in := "ok\r\npassword=aaa111\r\nnoch ok\ntoken: bbb222\n"
	got := s.Text(in)
	if strings.Contains(got, "aaa111") || strings.Contains(got, "bbb222") {
		t.Errorf("Geheimnis übrig: %q", got)
	}
	if !strings.HasPrefix(got, "ok\r\n") || !strings.Contains(got, "\r\nnoch ok\n") || !strings.HasSuffix(got, "\n") {
		t.Errorf("Zeilenenden verändert: %q", got)
	}
}

// Der Standard-Säuberer (Saeubern) arbeitet ohne Vorbereitung.
func TestSaeubernStandard(t *testing.T) {
	if got := Saeubern("Authorization: Bearer abc123def456"); strings.Contains(got, "abc123def456") {
		t.Errorf("Standard: %q", got)
	}
	if got := Saeubern(""); got != "" {
		t.Errorf("leer: %q", got)
	}
}

// Sehr lange Zeilen bleiben schnell und stürzen nicht ab.
func TestSaeubernLangeZeile(t *testing.T) {
	s := testSaeuberer()
	l := strings.Repeat("a b ", 100000) + "token=abc123xyz"
	got := s.Zeile(l)
	if strings.Contains(got, "abc123xyz") {
		t.Error("Token in langer Zeile nicht entfernt")
	}
}

// Ein Benutzerordner, der nicht unter C:\Users liegt (umgeleitetes Profil,
// anderes Laufwerk), wird über den Pfad des PCs erkannt, in jeder
// Schreibweise.
func TestSaeubernProfilAusserhalbUsers(t *testing.T) {
	s := NeuerSaeuberer(`D:\Daten\Profile\Anna Lena`, "Anna Lena")
	cases := []struct{ in, want string }{
		{`Home D:\Daten\Profile\Anna Lena\AppData\x`, `Home %USERPROFILE%\AppData\x`},
		{`"D:\Daten\Profile\Anna Lena\x"`, `"%USERPROFILE%\x"`},
		{`d:/daten/profile/anna lena/Downloads`, `%USERPROFILE%/Downloads`},
		{`Ordner D:\Daten\Profile\Annabelle\x`, `Ordner D:\Daten\Profile\Annabelle\x`}, // anderer Name: bleibt
	}
	for _, c := range cases {
		if got := s.Zeile(c.in); got != c.want {
			t.Errorf("%q\n  ist  %q\n  soll %q", c.in, got, c.want)
		}
	}
}

// Ein Authorization-Wert ohne bekanntes Schema (nur der Kopf verrät ihn).
func TestSaeubernAuthorizationOhneSchema(t *testing.T) {
	s := NeuerSaeuberer("", "")
	for _, in := range []string{"Authorization: meingeheimerwert", `"authorization": "meingeheimerwert"`, "proxy-authorization=meingeheimerwert"} {
		if got := s.Zeile(in); strings.Contains(got, "meingeheimerwert") {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
