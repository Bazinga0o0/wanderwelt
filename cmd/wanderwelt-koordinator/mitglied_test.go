package main

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/group"
)

const (
	secretToken = "tun_GEHEIM_0123456789abcdefghij"
	// tun ist der Anfang jedes Aufrufs der tunneld-CLI, tcfg sein --config.
	tun    = "runuser -u tunneld -- /opt/tunneld/tunneld admin "
	tcfg   = " --config /etc/tunneld/config.yaml"
	reload = "systemctl reload wanderwelt-koordinator"
)

const testConfig = `# Koordinator für die Tests.
listen: 127.0.0.1:8470
data_dir: /var/lib/wanderwelt
auth:
  mode: tunneld

# Mitglieder
members:
  - email: admin@example.org   # der Admin
    minecraft: AdminName
    admin: true
    op: true
  - email: bob@wanderwelt.invalid
    minecraft: Bob_42
  - email: freund@example.org
    minecraft: Freund

provisioning:
  tunneld_config: /etc/tunneld/config.yaml
  coordinator_url: https://wanderwelt.example.org/
  tunnel_server: tunnel.example.org:7443
`

// fakeRunner spielt tunneld-CLI und Neuladen: zeichnet jeden Aufruf auf und
// antwortet wie tunneld.
type fakeRunner struct {
	calls  []string
	stdin  map[string][]byte // Unterbefehl → stdin
	users  string            // Ausgabe von list-users
	tokens string            // Ausgabe von list-tokens
	fail   string            // dieser Unterbefehl (oder "reload") scheitert
	// Nach create-user (auch einem gescheiterten): usersAfterCreate ist
	// dann die Ausgabe von list-users, mit listFailsAfterCreate scheitert
	// list-users.
	usersAfterCreate     string
	listFailsAfterCreate bool
	created              bool
}

func (f *fakeRunner) Run(stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	sub := "reload"
	if name == "runuser" && len(args) > 5 {
		sub = args[5] // runuser -u tunneld -- <bin> admin <sub> …
	}
	if f.stdin == nil {
		f.stdin = map[string][]byte{}
	}
	f.stdin[sub] = stdin
	if sub == "create-user" {
		defer func() { f.created = true }()
	}
	if sub == f.fail || sub == "list-users" && f.created && f.listFailsAfterCreate {
		return nil, []byte("error: Datenbank weg\n"), errors.New("exit status 1")
	}
	switch sub {
	case "list-users":
		if f.created && f.usersAfterCreate != "" {
			return []byte(f.usersAfterCreate), nil, nil
		}
		return []byte(cmp.Or(f.users, "[]")), nil, nil
	case "list-tokens":
		return []byte(cmp.Or(f.tokens, "[]")), nil, nil
	case "issue-token":
		return []byte(`{"id": 7, "user_id": 3, "name": "wanderwelt", "token": "` + secretToken +
				`", "token_prefix": "GEHEIM", "created_at": "2026-11-23T12:00:00Z"}` + "\n"),
			[]byte("WARNING: this token will NOT be shown again — store it securely now.\n"), nil
	}
	return []byte("{}\n"), nil, nil
}

// setup legt die Konfiguration und ein leeres Ausgabeverzeichnis an.
func setup(t *testing.T, content string) (cfgPath, aus string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "koordinator.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	aus = filepath.Join(dir, "aus")
	if err := os.Mkdir(aus, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfgPath, aus
}

// runMitglied ruft den Befehl auf; der Token darf in keiner Ausgabe stehen.
func runMitglied(t *testing.T, f *fakeRunner, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = mitglied(args, f, &out, &errOut)
	if strings.Contains(out.String()+errOut.String(), secretToken) {
		t.Errorf("Token auf dem Terminal:\nstdout: %s\nstderr: %s", out.String(), errOut.String())
	}
	return code, out.String(), errOut.String()
}

func members(t *testing.T, cfgPath string) []config.Member {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Konfiguration nach dem Befehl: %v", err)
	}
	return cfg.Members
}

func checkCalls(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("Aufrufe:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestMitgliedNeuLegtKontoUndGruppendateiAn(t *testing.T) {
	cfgPath, aus := setup(t, testConfig)
	f := &fakeRunner{users: `[{"id": 1, "email": "admin@example.org", "created_at": "", "disabled_at": ""}]`}
	code, stdout, stderr := runMitglied(t, f, "neu", "--name", "Carla", "--minecraft", "Carla_7", "--op",
		"--aus", aus, "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d, stderr: %s", code, stderr)
	}

	const email = "carla@wanderwelt.invalid"
	checkCalls(t, f.calls, []string{
		tun + "list-users" + tcfg,
		tun + "create-user" + tcfg + " --email " + email + " --password-stdin",
		tun + "mark-verified" + tcfg + " --email " + email,
		tun + "issue-token" + tcfg + " --email " + email + " --name wanderwelt",
		reload,
	})
	// Das Passwort kommt nur über stdin an (eine Zeile, gut abtippbar, mit
	// Bindestrichen 19 Zeichen ≥ 12 wie tunneld verlangt) und erscheint
	// weder in den Argumenten noch in der Ausgabe.
	pw, ok := strings.CutSuffix(string(f.stdin["create-user"]), "\n")
	if !ok || !regexp.MustCompile(`^[a-km-np-zA-HJ-NP-Z2-9]{4}(-[a-km-np-zA-HJ-NP-Z2-9]{4}){3}$`).MatchString(pw) {
		t.Errorf("Passwort auf stdin = %q, want xxxx-xxxx-xxxx-xxxx ohne 0/O/o/1/l/I/i", f.stdin["create-user"])
	}
	if strings.Contains(strings.Join(f.calls, " ")+stdout+stderr, pw) {
		t.Error("Passwort in Argumenten oder Ausgabe")
	}
	if other, _ := randomPassword(); other == pw {
		t.Error("zwei gleiche Passwörter hintereinander")
	}

	groupPath := filepath.Join(aus, "gruppe-carla.yaml")
	g, err := group.LoadGroup(groupPath)
	if err != nil {
		t.Fatalf("Gruppendatei: %v", err)
	}
	if g.Coordinator != "https://wanderwelt.example.org" || g.TunnelServer != "tunnel.example.org:7443" || g.Token != secretToken {
		t.Errorf("Gruppendatei = %+v", g)
	}

	// Zugangsdatei: Web-Anmeldung (Standard aus tunnel_server ohne Port),
	// Login-E-Mail, Passwort, Hinweis zum Ändern.
	accessPath := filepath.Join(aus, "zugang-carla.txt")
	access, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatalf("Zugangsdatei: %v", err)
	}
	for _, want := range []string{
		"Anmeldung im Browser: https://tunnel.example.org\n",
		"E-Mail (Login):       " + email + "\n",
		"Passwort:             " + pw + "\n",
		"Passwort nach der ersten Anmeldung ändern",
	} {
		if !strings.Contains(string(access), want) {
			t.Errorf("Zugangsdatei ohne %q:\n%s", want, access)
		}
	}
	if strings.Contains(string(access), secretToken) {
		t.Error("Token in der Zugangsdatei")
	}

	for _, path := range []string{groupPath, accessPath} {
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s hat Rechte %04o, want 0600", path, fi.Mode().Perm())
			}
		}
		if !strings.Contains(stdout, path) {
			t.Errorf("stdout nennt %s nicht:\n%s", path, stdout)
		}
	}

	// Ob das Neuladen gelang und der Minecraft-Name auflösbar ist, weiß nur
	// der Koordinator: Die Ausgabe verweist auf sein Journal.
	for _, want := range []string{
		"Neuladen ausgelöst – Ergebnis: journalctl -u wanderwelt-koordinator\n",
		"„UUID nicht auflösbar“ mit minecraft=Carla_7",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout ohne %q:\n%s", want, stdout)
		}
	}

	ms := members(t, cfgPath)
	if want := (config.Member{Email: email, Minecraft: "Carla_7", Op: true}); ms[len(ms)-1] != want || len(ms) != 4 {
		t.Errorf("members = %+v, want zuletzt %+v", ms, want)
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "# der Admin") || !strings.Contains(string(data), "# Koordinator für die Tests.") {
		t.Errorf("Kommentare der Konfiguration verloren:\n%s", data)
	}
}

func TestMitgliedNeuMitVorhandenemKonto(t *testing.T) {
	tests := []struct {
		name      string
		email     string
		users     string
		tokens    string
		wantCode  int
		wantCalls []string
		wantOut   string
	}{
		{
			// Ein früherer, abgebrochener Aufruf hat schon einen Token
			// „wanderwelt“ ausgestellt: Er wird widerrufen, bevor der neue
			// kommt; andere Tokens des Kontos bleiben.
			name:  "aktives Konto: freischalten, alten Token widerrufen, Token",
			email: "carla@example.org",
			users: `[{"id": 4, "email": "Carla@Example.org", "created_at": "", "disabled_at": ""}]`,
			tokens: `[{"id": 21, "user_id": 4, "name": "wanderwelt", "token_prefix": "a"},
			          {"id": 22, "user_id": 4, "name": "laptop", "token_prefix": "b"}]`,
			wantCode: 0,
			wantCalls: []string{
				tun + "list-users" + tcfg,
				tun + "approve-user" + tcfg + " --email Carla@Example.org",
				tun + "list-tokens" + tcfg + " --email Carla@Example.org",
				tun + "revoke-token" + tcfg + " --id 21",
				tun + "issue-token" + tcfg + " --email Carla@Example.org --name wanderwelt",
				reload,
			},
			wantOut: "Ältere Tokens „wanderwelt“ des Kontos widerrufen: 1.",
		},
		{
			name:     "gesperrtes Konto @wanderwelt.invalid (nach mitglied entfernen): wieder freigeben",
			users:    `[{"id": 4, "email": "carla@wanderwelt.invalid", "created_at": "", "disabled_at": "2026-11-20T10:00:00Z"}]`,
			wantCode: 0,
			wantCalls: []string{
				tun + "list-users" + tcfg,
				tun + "enable-user" + tcfg + " --email carla@wanderwelt.invalid",
				tun + "approve-user" + tcfg + " --email carla@wanderwelt.invalid",
				tun + "mark-verified" + tcfg + " --email carla@wanderwelt.invalid",
				tun + "list-tokens" + tcfg + " --email carla@wanderwelt.invalid",
				tun + "issue-token" + tcfg + " --email carla@wanderwelt.invalid --name wanderwelt",
				reload,
			},
		},
		{
			name:      "gesperrtes Konto mit eigener E-Mail: Abbruch ohne Änderung",
			email:     "carla@example.org",
			users:     `[{"id": 4, "email": "carla@example.org", "created_at": "", "disabled_at": "2026-11-20T10:00:00Z"}]`,
			wantCode:  1,
			wantCalls: []string{tun + "list-users" + tcfg},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath, aus := setup(t, testConfig)
			f := &fakeRunner{users: tt.users, tokens: tt.tokens}
			args := []string{"neu", "--name", "carla", "--minecraft", "Carla", "--aus", aus, "--config", cfgPath}
			if tt.email != "" {
				args = append(args, "--email", tt.email)
			}
			code, stdout, stderr := runMitglied(t, f, args...)
			if code != tt.wantCode {
				t.Fatalf("Exit %d, want %d; stderr: %s", code, tt.wantCode, stderr)
			}
			checkCalls(t, f.calls, tt.wantCalls)
			if !strings.Contains(stdout, tt.wantOut) {
				t.Errorf("stdout ohne %q:\n%s", tt.wantOut, stdout)
			}
			if n := len(members(t, cfgPath)); (code == 0) != (n == 4) {
				t.Errorf("%d Mitglieder nach Exit %d", n, code)
			}
			// Vorhandenes Konto: kein Passwort, keine Zugangsdatei.
			if f.stdin["create-user"] != nil {
				t.Error("Passwort an tunneld geschickt")
			}
			if _, err := os.Stat(filepath.Join(aus, "zugang-carla.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Zugangsdatei für vorhandenes Konto: %v", err)
			}
			if code == 0 && !strings.Contains(stdout, "kein Passwort gesetzt, keine Zugangsdatei") {
				t.Errorf("stdout sagt nicht, dass es keine Zugangsdatei gibt:\n%s", stdout)
			}
		})
	}
}

func TestMitgliedNeuLehntDuplikatAb(t *testing.T) {
	for name, args := range map[string][]string{
		"E-Mail (Groß/Klein egal)":         {"--name", "zweit", "--minecraft", "Zweit", "--email", "FREUND@example.org"},
		"Standard-E-Mail":                  {"--name", "bob", "--minecraft", "Zweit"},
		"Minecraft-Name (Groß/Klein egal)": {"--name", "zweit", "--minecraft", "adminname"},
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath, aus := setup(t, testConfig)
			f := &fakeRunner{}
			code, _, stderr := runMitglied(t, f, append([]string{"neu", "--aus", aus, "--config", cfgPath}, args...)...)
			if code == 0 {
				t.Fatal("Duplikat angenommen")
			}
			if !strings.Contains(stderr, "nichts geändert") {
				t.Errorf("stderr: %s", stderr)
			}
			checkCalls(t, f.calls, nil)
			if data, _ := os.ReadFile(cfgPath); string(data) != testConfig {
				t.Error("Konfiguration verändert")
			}
		})
	}
}

// Bricht ein Schritt nach dem Anlegen des Kontos ab, sagt die Meldung, was
// schon angelegt ist, und nennt beide Auswege: denselben Aufruf wiederholen
// (Zugangsdatei behalten) oder mit „mitglied entfernen“ aufgeben. Der
// wiederholte Aufruf verwendet das Konto, holt mark-verified nach und lässt
// die Zugangsdatei – die einzige Kopie des Passworts – unverändert.
func TestMitgliedNeuAbbruchNachCreateUser(t *testing.T) {
	cfgPath, aus := setup(t, testConfig)
	args := []string{"neu", "--name", "carla", "--minecraft", "Carla", "--aus", aus, "--config", cfgPath}
	accessPath := filepath.Join(aus, "zugang-carla.txt")
	f := &fakeRunner{fail: "issue-token"}
	code, _, stderr := runMitglied(t, f, args...)
	if code != 1 {
		t.Fatalf("Exit %d, want 1", code)
	}
	for _, want := range []string{
		"error: Datenbank weg", // stderr von tunneld
		"Schon angelegt: tunnel-Konto carla@wanderwelt.invalid, Zugangsdatei " + accessPath,
		"Erneut versuchen: denselben Aufruf wiederholen",
		accessPath + " bleibt gültig (nicht löschen",
		"Aufgeben: wanderwelt-koordinator mitglied entfernen carla@wanderwelt.invalid --config " + cfgPath +
			", dann " + accessPath + " löschen",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr ohne %q:\n%s", want, stderr)
		}
	}
	checkCalls(t, f.calls, []string{
		tun + "list-users" + tcfg,
		tun + "create-user" + tcfg + " --email carla@wanderwelt.invalid --password-stdin",
		tun + "mark-verified" + tcfg + " --email carla@wanderwelt.invalid",
		tun + "issue-token" + tcfg + " --email carla@wanderwelt.invalid --name wanderwelt",
	})
	if _, err := os.Stat(filepath.Join(aus, "gruppe-carla.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Gruppendatei trotz Abbruch: %v", err)
	}
	if data, _ := os.ReadFile(cfgPath); string(data) != testConfig {
		t.Error("Konfiguration trotz Abbruch verändert")
	}
	access, err := os.ReadFile(accessPath)
	if err != nil {
		t.Fatalf("Zugangsdatei nach dem Abbruch: %v", err)
	}

	// Erneut versuchen: Das Konto steht jetzt bei tunneld.
	f = &fakeRunner{users: `[{"id": 9, "email": "carla@wanderwelt.invalid", "created_at": "", "disabled_at": ""}]`}
	code, stdout, stderr := runMitglied(t, f, args...)
	if code != 0 {
		t.Fatalf("Wiederholung: Exit %d; stderr: %s", code, stderr)
	}
	checkCalls(t, f.calls, []string{
		tun + "list-users" + tcfg,
		tun + "approve-user" + tcfg + " --email carla@wanderwelt.invalid",
		tun + "mark-verified" + tcfg + " --email carla@wanderwelt.invalid",
		tun + "list-tokens" + tcfg + " --email carla@wanderwelt.invalid",
		tun + "issue-token" + tcfg + " --email carla@wanderwelt.invalid --name wanderwelt",
		reload,
	})
	if again, _ := os.ReadFile(accessPath); !bytes.Equal(again, access) {
		t.Errorf("Zugangsdatei bei der Wiederholung verändert:\n%s\nvorher:\n%s", again, access)
	}
	if !strings.Contains(stdout, "Zugangsdatei: "+accessPath+" (von vorher, unverändert") {
		t.Errorf("stdout nennt die erhaltene Zugangsdatei nicht:\n%s", stdout)
	}
	if g, err := group.LoadGroup(filepath.Join(aus, "gruppe-carla.yaml")); err != nil || g.Token != secretToken {
		t.Errorf("Gruppendatei nach der Wiederholung: %+v, %v", g, err)
	}
	if n := len(members(t, cfgPath)); n != 4 {
		t.Errorf("%d Mitglieder nach der Wiederholung, want 4", n)
	}
}

// create-user kann scheitern, obwohl tunneld das Konto angelegt hat
// („created but not approved“). Die Zugangsdatei entsteht darum vorher und
// wird nur gelöscht, wenn list-users danach kein Konto zeigt; sonst bleibt
// sie (einzige Kopie des Passworts), und die Wiederholung verwendet sie.
func TestMitgliedNeuCreateUserScheitert(t *testing.T) {
	const (
		email = "carla@example.org"
		acct  = `[{"id": 9, "email": "carla@example.org", "created_at": "", "disabled_at": ""}]`
	)
	tests := []struct {
		name       string
		f          *fakeRunner
		wantAccess bool
		wantErr    []string
	}{
		{
			name:       "Konto trotzdem angelegt: Zugangsdatei bleibt",
			f:          &fakeRunner{fail: "create-user", usersAfterCreate: acct},
			wantAccess: true,
			wantErr: []string{
				"error: Datenbank weg",
				"Schon angelegt: tunnel-Konto " + email + ", Zugangsdatei ",
				"bleibt gültig (nicht löschen",
				// Ohne Mitgliedseintrag räumt entfernen ein Konto mit eigener
				// E-Mail nur mit --auch-fremdes-konto auf.
				"Aufgeben: wanderwelt-koordinator mitglied entfernen " + email + " --auch-fremdes-konto --config ",
			},
		},
		{
			name:    "kein Konto: Zugangsdatei gelöscht",
			f:       &fakeRunner{fail: "create-user"},
			wantErr: []string{"error: Datenbank weg", "Kein tunnel-Konto angelegt; nichts geändert"},
		},
		{
			name:       "unklar: Zugangsdatei bleibt",
			f:          &fakeRunner{fail: "create-user", listFailsAfterCreate: true},
			wantAccess: true,
			wantErr:    []string{"ist unklar", "Gibt es das Konto, steht nur dort sein Passwort", "Aufgeben: "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath, aus := setup(t, testConfig)
			args := []string{"neu", "--name", "carla", "--minecraft", "Carla", "--email", email,
				"--aus", aus, "--config", cfgPath}
			accessPath := filepath.Join(aus, "zugang-carla.txt")
			code, stdout, stderr := runMitglied(t, tt.f, args...)
			if code != 1 {
				t.Fatalf("Exit %d, want 1; stderr: %s", code, stderr)
			}
			checkCalls(t, tt.f.calls, []string{
				tun + "list-users" + tcfg,
				tun + "create-user" + tcfg + " --email " + email + " --password-stdin",
				tun + "list-users" + tcfg,
			})
			for _, want := range tt.wantErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr ohne %q:\n%s", want, stderr)
				}
			}
			pw := strings.TrimSuffix(string(tt.f.stdin["create-user"]), "\n")
			if pw == "" || strings.Contains(stdout+stderr, pw) {
				t.Errorf("Passwort %q fehlt auf stdin oder steht in der Ausgabe", pw)
			}
			access, err := os.ReadFile(accessPath)
			if !tt.wantAccess {
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Zugangsdatei ohne Konto: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(string(access), "Passwort:             "+pw+"\n") ||
				!accessFileFor(accessPath, email) {
				t.Fatalf("Zugangsdatei mit dem Passwort an tunneld fehlt: %v\n%s", err, access)
			}
			if !strings.Contains(stderr, accessPath) {
				t.Errorf("stderr nennt %s nicht:\n%s", accessPath, stderr)
			}
			if data, _ := os.ReadFile(cfgPath); string(data) != testConfig {
				t.Error("Konfiguration trotz Abbruch verändert")
			}

			// Erneut versuchen: Das Konto steht bei tunneld; es wird
			// verwendet, mark-verified nachgeholt (das Konto hat „mitglied
			// neu“ angelegt, die Zugangsdatei passt), die Datei bleibt.
			f := &fakeRunner{users: acct}
			code, stdout, stderr = runMitglied(t, f, args...)
			if code != 0 {
				t.Fatalf("Wiederholung: Exit %d; stderr: %s", code, stderr)
			}
			checkCalls(t, f.calls, []string{
				tun + "list-users" + tcfg,
				tun + "approve-user" + tcfg + " --email " + email,
				tun + "mark-verified" + tcfg + " --email " + email,
				tun + "list-tokens" + tcfg + " --email " + email,
				tun + "issue-token" + tcfg + " --email " + email + " --name wanderwelt",
				reload,
			})
			if again, _ := os.ReadFile(accessPath); !bytes.Equal(again, access) {
				t.Error("Zugangsdatei bei der Wiederholung verändert")
			}
			if !strings.Contains(stdout, "Zugangsdatei: "+accessPath+" (von vorher, unverändert") {
				t.Errorf("stdout nennt die erhaltene Zugangsdatei nicht:\n%s", stdout)
			}
		})
	}
}

// Eine vorhandene zugang-<name>.txt bleibt beim Wiederverwenden eines Kontos
// unverändert; als Zugangsdatei für das Mitglied gilt sie nur, wenn ihr
// Login die E-Mail des Kontos ist (Groß/Klein egal). Eine fremde Datei
// (anderes Konto mit gleichem --name) wird nur gewarnt, ohne Inhalt.
func TestMitgliedNeuVorhandeneZugangsdatei(t *testing.T) {
	const fremdesPasswort = "fremd-GEHEIM-pw42"
	tests := []struct {
		name  string
		login string
		valid bool
	}{
		{name: "fremdes Konto", login: "anders@wanderwelt.invalid", valid: false},
		{name: "dieses Konto", login: "CARLA@example.org", valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath, aus := setup(t, testConfig)
			accessPath := filepath.Join(aus, "zugang-carla.txt")
			content := "Dein tunnel-Zugang\n\nE-Mail (Login):       " + tt.login + "\nPasswort:             " + fremdesPasswort + "\n"
			if err := os.WriteFile(accessPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			f := &fakeRunner{users: `[{"id": 4, "email": "carla@example.org", "created_at": "", "disabled_at": ""}]`}
			code, stdout, stderr := runMitglied(t, f, "neu", "--name", "carla", "--minecraft", "Carla",
				"--email", "carla@example.org", "--aus", aus, "--config", cfgPath)
			if code != 0 {
				t.Fatalf("Exit %d; stderr: %s", code, stderr)
			}
			if data, _ := os.ReadFile(accessPath); string(data) != content {
				t.Errorf("Zugangsdatei verändert:\n%s", data)
			}
			if strings.Contains(stdout+stderr, fremdesPasswort) {
				t.Error("Passwort aus der Zugangsdatei in der Ausgabe")
			}
			reported := strings.Contains(stdout, "Zugangsdatei: "+accessPath)
			warned := strings.Contains(stderr, accessPath+" gehört nicht zu diesem Konto") &&
				strings.Contains(stderr, "nicht weitergeben")
			if tt.valid && (!reported || warned) {
				t.Errorf("passende Zugangsdatei nicht als gültig gemeldet:\nstdout: %s\nstderr: %s", stdout, stderr)
			}
			if !tt.valid && (reported || !warned || !strings.Contains(stdout, "keine Zugangsdatei")) {
				t.Errorf("fremde Zugangsdatei nicht gewarnt oder als gültig gemeldet:\nstdout: %s\nstderr: %s", stdout, stderr)
			}
		})
	}
}

func TestMitgliedNeuNeuladenScheitert(t *testing.T) {
	cfgPath, aus := setup(t, testConfig)
	f := &fakeRunner{fail: "reload"}
	code, _, stderr := runMitglied(t, f, "neu", "--name", "carla", "--minecraft", "Carla", "--aus", aus, "--config", cfgPath)
	if code != 0 {
		t.Fatalf("Exit %d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "Koordinator neu starten") {
		t.Errorf("kein Hinweis „Koordinator neu starten“:\n%s", stderr)
	}
	if n := len(members(t, cfgPath)); n != 4 {
		t.Errorf("%d Mitglieder, want 4 (eingetragen trotz gescheitertem Neuladen)", n)
	}
}

func TestMitgliedEntfernen(t *testing.T) {
	const users = `[
  {"id": 1, "email": "admin@example.org", "created_at": "", "disabled_at": ""},
  {"id": 2, "email": "bob@wanderwelt.invalid", "created_at": "", "disabled_at": ""},
  {"id": 3, "email": "freund@example.org", "created_at": "", "disabled_at": ""}
]`
	const tokens = `[
  {"id": 11, "user_id": 2, "name": "wanderwelt", "token_prefix": "a"},
  {"id": 12, "user_id": 2, "name": "laptop", "token_prefix": "b"},
  {"id": 13, "user_id": 2, "name": "wanderwelt", "token_prefix": "c"}
]`
	revoke := []string{tun + "revoke-token" + tcfg + " --id 11", tun + "revoke-token" + tcfg + " --id 13"}
	const (
		bobEntry    = "  - email: bob@wanderwelt.invalid\n    minecraft: Bob_42\n"
		freundEntry = "  - email: freund@example.org\n    minecraft: Freund\n"
	)
	tests := []struct {
		name        string
		drop        string // dieser Eintrag fehlt in der Konfiguration
		args        []string
		wantCode    int
		wantCalls   []string
		wantMembers int
		wantErr     string
	}{
		{
			name:     "über Minecraft-Name, Konto @wanderwelt.invalid wird gesperrt",
			args:     []string{"bob_42"},
			wantCode: 0,
			wantCalls: slices.Concat(
				[]string{tun + "list-users" + tcfg, tun + "list-tokens" + tcfg + " --email bob@wanderwelt.invalid"},
				revoke,
				[]string{tun + "disable-user" + tcfg + " --email bob@wanderwelt.invalid", reload}),
			wantMembers: 2,
		},
		{
			name:     "eigene E-Mail: Konto bleibt",
			args:     []string{"Freund@Example.org"},
			wantCode: 0,
			wantCalls: slices.Concat(
				[]string{tun + "list-users" + tcfg, tun + "list-tokens" + tcfg + " --email freund@example.org"},
				revoke,
				[]string{reload}),
			wantMembers: 2,
		},
		{
			name:     "eigene E-Mail mit --konto-sperren (nach dem Namen)",
			args:     []string{"Freund", "--konto-sperren"},
			wantCode: 0,
			wantCalls: slices.Concat(
				[]string{tun + "list-users" + tcfg, tun + "list-tokens" + tcfg + " --email freund@example.org"},
				revoke,
				[]string{tun + "disable-user" + tcfg + " --email freund@example.org", reload}),
			wantMembers: 2,
		},
		{
			name:        "letztes Admin-Mitglied nie",
			args:        []string{"admin@example.org"},
			wantCode:    1,
			wantMembers: 3,
		},
		{
			name:     "nicht eingetragen (Abbruch von mitglied neu): nur tunnel-Konto aufräumen",
			drop:     bobEntry,
			args:     []string{"bob@wanderwelt.invalid"},
			wantCode: 0,
			wantCalls: slices.Concat(
				[]string{tun + "list-users" + tcfg, tun + "list-tokens" + tcfg + " --email bob@wanderwelt.invalid"},
				revoke,
				[]string{tun + "disable-user" + tcfg + " --email bob@wanderwelt.invalid"}),
			wantMembers: 2,
		},
		{
			// Ohne Mitgliedseintrag wäre das irgendein tunnel-Konto: nur
			// @wanderwelt.invalid oder mit ausdrücklichem Flag.
			name:        "nicht eingetragen, eigene E-Mail: abgelehnt",
			drop:        freundEntry,
			args:        []string{"freund@example.org", "--konto-sperren"},
			wantCode:    1,
			wantMembers: 2,
			wantErr:     "--auch-fremdes-konto",
		},
		{
			name:     "nicht eingetragen, eigene E-Mail mit --auch-fremdes-konto: Tokens widerrufen, Konto bleibt",
			drop:     freundEntry,
			args:     []string{"freund@example.org", "--auch-fremdes-konto"},
			wantCode: 0,
			wantCalls: slices.Concat(
				[]string{tun + "list-users" + tcfg, tun + "list-tokens" + tcfg + " --email freund@example.org"},
				revoke),
			wantMembers: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := testConfig
			if tt.drop != "" {
				if content = strings.Replace(testConfig, tt.drop, "", 1); content == testConfig {
					t.Fatalf("%q steht nicht in testConfig", tt.drop)
				}
			}
			cfgPath, _ := setup(t, content)
			f := &fakeRunner{users: users, tokens: tokens}
			code, _, stderr := runMitglied(t, f, append([]string{"entfernen", "--config", cfgPath}, tt.args...)...)
			if code != tt.wantCode {
				t.Fatalf("Exit %d, want %d; stderr: %s", code, tt.wantCode, stderr)
			}
			if !strings.Contains(stderr, tt.wantErr) {
				t.Errorf("stderr ohne %q:\n%s", tt.wantErr, stderr)
			}
			checkCalls(t, f.calls, tt.wantCalls)
			ms := members(t, cfgPath)
			if len(ms) != tt.wantMembers {
				t.Errorf("%d Mitglieder, want %d: %+v", len(ms), tt.wantMembers, ms)
			}
			if code == 0 && slices.ContainsFunc(ms, func(m config.Member) bool {
				return strings.EqualFold(m.Minecraft, tt.args[0]) || strings.EqualFold(m.Email, tt.args[0])
			}) {
				t.Errorf("%s steht noch in members", tt.args[0])
			}
		})
	}
}
