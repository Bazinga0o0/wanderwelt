package main

// „mitglied neu|entfernen|liste“ verwaltet die Mitglieder auf dem Server:
// tunnel-Konto und persönlicher Token über die tunneld-Admin-CLI, Eintrag in
// der Konfiguration, Neuladen im laufenden Koordinator und die Gruppendatei
// für den Starter. Läuft als root; die CLI läuft als provisioning.run_as:
//
//	runuser -u <run_as> -- <tunneld_bin> admin <befehl> --config <tunneld_config> …
//
// Für ein neues Konto entsteht außerdem die Zugangsdatei für die
// Web-Anmeldung (E-Mail, zufälliges Passwort). Token und Passwort erscheinen
// nie auf dem Terminal: Die Ausgabe von issue-token liest nur dieser
// Befehl, das Passwort geht nur über stdin an tunneld und in die
// Zugangsdatei, und Fehlermeldungen enthalten nur stderr von tunneld.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Bazinga0o0/wanderwelt/internal/koord/config"
	"github.com/Bazinga0o0/wanderwelt/internal/koord/kennung"
)

const mitgliedUsage = `  wanderwelt-koordinator mitglied neu --name NAME [--minecraft MCNAME] [--email E] [--admin] [--op] [--aus DIR] [--config PATH]
      legt tunnel-Konto und Token an, trägt das Mitglied ein und schreibt DIR/gruppe-NAME.yaml
      (bei neuem Konto auch DIR/zugang-NAME.txt mit Passwort für die Web-Anmeldung);
      ohne --minecraft meldet das Mitglied seinen Namen beim Einrichten des Starters selbst
  wanderwelt-koordinator mitglied entfernen <minecraft-name|e-mail|kennung> [--konto-sperren] [--auch-fremdes-konto] [--config PATH]
      widerruft den Token, trägt das Mitglied aus (sperrt Konten @wanderwelt.invalid);
      ein Konto ohne Mitgliedseintrag nur bei @wanderwelt.invalid oder mit --auch-fremdes-konto
  wanderwelt-koordinator mitglied liste [--config PATH]
      zeigt die Mitglieder mit E-Mail, Kennung (die im Fenster erscheint) und den wirksamen Namen und Rollen
      (auch den im Fenster geänderten); E-Mail-Adressen sieht nur der Betreiber
`

const (
	// tokenName heißt der Token, den „mitglied neu“ ausstellt und
	// „mitglied entfernen“ widerruft.
	tokenName = "wanderwelt"
	// invalidDomain: E-Mail-Domain der Konten ohne eigene Adresse.
	invalidDomain = "@wanderwelt.invalid"
	// runTimeout begrenzt jeden Aufruf der tunneld-CLI und des Neuladens.
	runTimeout = 2 * time.Minute
	// runWaitDelay: So lange wartet ein beendeter oder abgebrochener Aufruf
	// noch auf seine Ausgaben, bevor die Pipes geschlossen werden.
	runWaitDelay = 5 * time.Second
	// maxStderr begrenzt, wie viel stderr von tunneld in einer Meldung steht.
	maxStderr = 2000
)

var (
	nameRe      = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	minecraftRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)
)

// runner führt ein Programm aus (stdin darf nil sein) und liefert seine
// Ausgaben getrennt; in Tests ein Fake.
type runner interface {
	Run(stdin []byte, name string, args ...string) (stdout, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) Run(stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// Hält ein Kindprozess die Pipes offen (tunneld unter runuser, wenn
	// runuser selbst schon beendet ist), wartete Run sonst über
	// runTimeout hinaus.
	cmd.WaitDelay = runWaitDelay
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// mitgliedCmd hält, was die Unterbefehle brauchen.
type mitgliedCmd struct {
	run    runner
	out    io.Writer
	errOut io.Writer
}

func mitglied(args []string, run runner, stdout, stderr io.Writer) int {
	c := &mitgliedCmd{run: run, out: stdout, errOut: stderr}
	if len(args) == 0 {
		fmt.Fprint(stderr, "Aufruf:\n"+mitgliedUsage)
		return 2
	}
	switch args[0] {
	case "neu":
		return c.neu(args[1:])
	case "entfernen":
		return c.entfernen(args[1:])
	case "liste":
		return c.liste(args[1:])
	default:
		fmt.Fprintf(stderr, "mitglied: unbekannter Unterbefehl %q\nAufruf:\n%s", args[0], mitgliedUsage)
		return 2
	}
}

// --- mitglied neu ---

type neuArgs struct {
	cfgPath, name, minecraft, email, aus string
	admin, op                            bool
}

func (c *mitgliedCmd) neu(args []string) int {
	fs := flag.NewFlagSet("mitglied neu", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	var a neuArgs
	fs.StringVar(&a.cfgPath, "config", defaultConfig, "Konfigurationsdatei des Koordinators")
	fs.StringVar(&a.name, "name", "", "Kurzname (a-z, 0-9, -; für Dateiname und Standard-E-Mail)")
	fs.StringVar(&a.minecraft, "minecraft", "", "Minecraft-Name (ohne: das Mitglied meldet ihn beim Einrichten selbst)")
	fs.StringVar(&a.email, "email", "", "E-Mail des tunnel-Kontos (Standard NAME"+invalidDomain+")")
	fs.BoolVar(&a.admin, "admin", false, "darf Welten anlegen und übernehmen")
	fs.BoolVar(&a.op, "op", false, "Operator im Spiel")
	fs.StringVar(&a.aus, "aus", ".", "Verzeichnis für die Gruppendatei")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.errOut, "mitglied neu: unerwartetes Argument %q\n", fs.Arg(0))
		return 2
	}
	if err := c.doNeu(a); err != nil {
		fmt.Fprintln(c.errOut, "mitglied neu:", err)
		return 1
	}
	return 0
}

func (c *mitgliedCmd) doNeu(a neuArgs) error {
	name := strings.ToLower(strings.TrimSpace(a.name))
	if !nameRe.MatchString(name) {
		return fmt.Errorf("--name %q: erlaubt sind 1–32 Zeichen a-z, 0-9 und -", a.name)
	}
	mc := strings.TrimSpace(a.minecraft)
	if mc != "" && !minecraftRe.MatchString(mc) {
		return fmt.Errorf("--minecraft %q: ein Minecraft-Name hat 3–16 Zeichen aus A-Z, a-z, 0-9 und _", a.minecraft)
	}
	email := strings.TrimSpace(a.email)
	if email == "" {
		email = name + invalidDomain
	}
	if at := strings.IndexByte(email, '@'); at < 1 || at == len(email)-1 || strings.Count(email, "@") != 1 ||
		strings.ContainsAny(email, " \t\r\n") {
		return fmt.Errorf("--email %q ist keine E-Mail-Adresse", email)
	}

	if err := checkConfigFile(a.cfgPath); err != nil {
		return err
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		return err
	}
	p := cfg.Provisioning.WithDefaults()
	if err := p.Check(true); err != nil {
		return fmt.Errorf("%s: %w", a.cfgPath, err)
	}
	for _, m := range c.effective(cfg) {
		if strings.EqualFold(m.Email, email) {
			return fmt.Errorf("%s ist schon Mitglied (Minecraft %s); nichts geändert", m.Email, cmp.Or(m.Minecraft, "noch keiner"))
		}
		if mc != "" && strings.EqualFold(m.Minecraft, mc) {
			return fmt.Errorf("Minecraft-Name %s gehört schon zum Mitglied %s; nichts geändert", m.Minecraft, m.Email)
		}
	}
	if fi, err := os.Stat(a.aus); err != nil || !fi.IsDir() {
		return fmt.Errorf("--aus %s ist kein Verzeichnis", a.aus)
	}
	groupPath, err := freePath(a.aus, "gruppe-"+name+".yaml")
	if err != nil {
		return err
	}
	accessPath, err := filepath.Abs(filepath.Join(a.aus, "zugang-"+name+".txt"))
	if err != nil {
		return err
	}

	t := tunneldCLI{p: p, run: c.run}
	users, err := t.listUsers()
	if err != nil {
		return err
	}
	// done: was schon angelegt ist, written: welche Dateien. Bricht danach
	// ein Schritt ab, nennt die Meldung beide Auswege: denselben Aufruf
	// wiederholen (das Konto wird wiederverwendet, die Zugangsdatei bleibt –
	// sie hält die einzige Kopie des Passworts) oder mit „mitglied
	// entfernen“ aufgeben.
	var done, written []string
	var accessWritten, groupWritten bool
	abort := func(err error) error {
		if len(done) == 0 {
			return err
		}
		retry := "denselben Aufruf wiederholen; das tunnel-Konto wird wiederverwendet"
		if accessWritten {
			retry += ", " + accessPath + " bleibt gültig (nicht löschen: nur dort steht das Passwort)"
		}
		if groupWritten {
			retry += "; vorher " + groupPath + " löschen"
		}
		return fmt.Errorf("%w\nSchon angelegt: %s.\nErneut versuchen: %s.\nAufgeben: %s.",
			err, strings.Join(done, ", "), retry, cleanupCommand(email, a.cfgPath, written))
	}
	newAccount, keptAccess := false, false

	if acct, ok := findUser(users, email); ok {
		email = acct.Email // Schreibweise wie in tunneld
		if acct.DisabledAt != "" {
			if !strings.HasSuffix(strings.ToLower(email), invalidDomain) {
				return fmt.Errorf("das tunnel-Konto %s ist gesperrt; wenn es wieder gelten soll, erst "+
					"„%s admin enable-user --config %s --email %s“ (Achtung: seine nicht widerrufenen Tokens gelten dann wieder)",
					email, p.TunneldBin, p.TunneldConfig, email)
			}
			// Ein Konto @wanderwelt.invalid hat „mitglied neu“ angelegt und
			// „mitglied entfernen“ gesperrt; wer wieder aufgenommen wird,
			// bekommt es zurück. Wie bei enable-user gelten dann auch Tokens
			// wieder, die das Mitglied selbst angelegt und nicht widerrufen hat.
			if err := t.admin(nil, "enable-user", "--email", email); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "tunnel-Konto %s war gesperrt und ist wieder freigegeben.\n", email)
		}
		if err := t.admin(nil, "approve-user", "--email", email); err != nil {
			return err
		}
		// Eine Zugangsdatei von einem früheren (abgebrochenen) Aufruf bleibt
		// unverändert: Ein neues Passwort setzt dieser Weg nicht. Als gültig
		// gilt sie nur, wenn ihr Login die E-Mail dieses Kontos ist; sonst
		// stammt sie von einem anderen Konto mit gleichem --name.
		if _, err := os.Lstat(accessPath); err == nil {
			if accessFileFor(accessPath, email) {
				keptAccess = true
			} else {
				fmt.Fprintf(c.errOut, "Warnung: %s gehört nicht zu diesem Konto (%s) – unverändert gelassen, "+
					"nicht weitergeben.\n", accessPath, email)
			}
		}
		if keptAccess || strings.HasSuffix(strings.ToLower(email), invalidDomain) {
			// Dieses Konto hat „mitglied neu“ angelegt (Konten
			// @wanderwelt.invalid legt nur es an, sonst zeigt es die
			// passende Zugangsdatei); brach es vor mark-verified ab, holt
			// der neue Aufruf das nach (idempotent).
			if err := t.admin(nil, "mark-verified", "--email", email); err != nil {
				return err
			}
		}
		// Ein früherer Aufruf kann schon einen Token ausgestellt haben (Abbruch
		// danach); gelten soll nur der aus der neuen Gruppendatei.
		revoked, err := t.revokeTokens(email)
		if err != nil {
			return err
		}
		if revoked > 0 {
			fmt.Fprintf(c.out, "Ältere Tokens „%s“ des Kontos widerrufen: %d.\n", tokenName, revoked)
		}
	} else {
		newAccount = true
		if _, err := freePath(a.aus, filepath.Base(accessPath)); err != nil {
			return err
		}
		pw, err := randomPassword()
		if err != nil {
			return err
		}
		// Die Zugangsdatei entsteht vor dem Konto: Meldet create-user einen
		// Fehler, obwohl tunneld das Konto angelegt hat („created but not
		// approved“), wäre das Passwort sonst verloren.
		if err := writeAccessFile(accessPath, p, email, pw, name); err != nil {
			return fmt.Errorf("Zugangsdatei: %w", err)
		}
		// Nur über die Pipe (--password-stdin), nie als Argument.
		createErr := t.admin([]byte(pw+"\n"), "create-user", "--email", email, "--password-stdin")
		if createErr != nil {
			// Ob das Konto trotzdem entstanden ist, sagt nur list-users.
			after, err := t.listUsers()
			if err != nil {
				return fmt.Errorf("%w\nOb tunneld das Konto %s trotzdem angelegt hat, ist unklar (%v). %s bleibt: "+
					"Gibt es das Konto, steht nur dort sein Passwort.\nErneut versuchen: denselben Aufruf wiederholen "+
					"(ein vorhandenes Konto wird wiederverwendet; meldet er, dass %s schon da ist, gibt es kein "+
					"Konto – dann die Datei löschen).\nAufgeben: %s.",
					createErr, email, err, accessPath, accessPath, cleanupCommand(email, a.cfgPath, []string{accessPath}))
			}
			if _, ok := findUser(after, email); !ok {
				// Kein Konto: Das Passwort der Zugangsdatei gilt für nichts.
				if err := os.Remove(accessPath); err != nil {
					return fmt.Errorf("%w\nKein tunnel-Konto angelegt; %s bitte löschen (sein Passwort gilt nicht): %v",
						createErr, accessPath, err)
				}
				return fmt.Errorf("%w\nKein tunnel-Konto angelegt; nichts geändert", createErr)
			}
		}
		done = append(done, "tunnel-Konto "+email, "Zugangsdatei "+accessPath)
		written = append(written, accessPath)
		accessWritten = true
		if createErr != nil {
			return abort(createErr)
		}
		if err := t.admin(nil, "mark-verified", "--email", email); err != nil {
			return abort(err)
		}
	}

	token, err := t.issueToken(email)
	if err != nil {
		return abort(err)
	}
	done = append(done, "Token „"+tokenName+"“")
	if err := writeGroupFile(groupPath, p, token, name, mc); err != nil {
		return abort(fmt.Errorf("Gruppendatei: %w", err))
	}
	done = append(done, "Gruppendatei "+groupPath)
	written = append(written, groupPath)
	groupWritten = true
	m := config.Member{Email: email, Minecraft: mc, Admin: a.admin, Op: a.op}
	if err := config.AddMember(a.cfgPath, m); err != nil {
		return abort(fmt.Errorf("Mitglied eintragen: %w", err))
	}

	if mc != "" {
		fmt.Fprintf(c.out, "Mitglied eingetragen: %s (tunnel-Konto %s%s).\n", mc, email, roles(m, ", "))
	} else {
		fmt.Fprintf(c.out, "Mitglied eingetragen: tunnel-Konto %s%s, noch ohne Minecraft-Namen – "+
			"den meldet %s beim Einrichten des Starters (bis dahin fehlt er in der Whitelist).\n", email, roles(m, ", "), name)
	}
	c.reload(p, a.cfgPath, mc)
	fmt.Fprintf(c.out, "Gruppendatei: %s\n", groupPath)
	if newAccount || keptAccess {
		if keptAccess {
			fmt.Fprintf(c.out, "Zugangsdatei: %s (von vorher, unverändert; kein neues Passwort gesetzt)\n", accessPath)
		} else {
			fmt.Fprintf(c.out, "Zugangsdatei: %s\n", accessPath)
		}
		fmt.Fprintf(c.out, "Beide Dateien nur %s geben: die Gruppendatei für den Starter (er fragt bei der "+
			"Installation nach ihr), die Zugangsdatei für die Anmeldung im Browser.\n", name)
	} else {
		fmt.Fprintln(c.out, "Vorhandenes tunnel-Konto verwendet: kein Passwort gesetzt, keine Zugangsdatei.")
		fmt.Fprintf(c.out, "Die Gruppendatei nur %s geben (sie enthält den persönlichen tunnel-Token); "+
			"der Starter fragt bei der Installation nach ihr.\n", name)
	}
	return nil
}

// freePath liefert den absoluten Pfad von name in dir; gibt es die Datei
// schon, ist das ein Fehler (sie wird nie überschrieben).
func freePath(dir, name string) (string, error) {
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s gibt es schon – löschen oder ein anderes --aus wählen; nichts geändert", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return path, nil
}

// cleanupCommand ist der Aufruf, der nach einem Abbruch aufräumt (wer
// aufgibt), samt den schon geschriebenen Dateien, die zu löschen sind. Das
// Konto steht dann noch nicht in der Konfiguration; außer bei
// @wanderwelt.invalid braucht „entfernen“ dafür --auch-fremdes-konto.
func cleanupCommand(email, cfgPath string, written []string) string {
	cmd := "wanderwelt-koordinator mitglied entfernen " + email
	if !strings.HasSuffix(strings.ToLower(email), invalidDomain) {
		cmd += " --auch-fremdes-konto"
	}
	if cfgPath != defaultConfig {
		cmd += " --config " + cfgPath
	}
	if len(written) > 0 {
		cmd += ", dann " + strings.Join(written, " und ") + " löschen"
	}
	return cmd
}

// passwordAlphabet: Buchstaben und Ziffern ohne leicht verwechselbare
// (0/O/o, 1/l/I/i).
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// randomPassword ist das Passwort eines neuen Kontos für die Web-Anmeldung:
// 16 Zeichen aus passwordAlphabet in Vierergruppen mit Bindestrich
// (abcd-EFGH-2345-wxyz), etwa 92 Bit. Mit Bindestrichen 19 Zeichen: Das
// erfüllt die Regel von tunneld für Passwörter, die ein Admin setzt
// (limits.min_admin_password_length, Standard 12; höchstens 1024 Byte). Es
// geht nur über stdin an tunneld und in die Zugangsdatei.
func randomPassword() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(passwordAlphabet)))
	for i := range 16 {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b.WriteByte(passwordAlphabet[k.Int64()])
	}
	return b.String(), nil
}

// accessLoginLabel leitet in der Zugangsdatei die Zeile mit dem Login ein.
const accessLoginLabel = "E-Mail (Login):"

// accessFileFor meldet, ob die Zugangsdatei path zum Konto email gehört:
// Ihre Login-Zeile nennt genau diese E-Mail (Groß/Klein egal). Eine
// unlesbare oder fremde Datei gehört nicht dazu.
func accessFileFor(path, email string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if login, ok := strings.CutPrefix(strings.TrimSpace(line), accessLoginLabel); ok {
			return strings.EqualFold(strings.TrimSpace(login), email)
		}
	}
	return false
}

// writeAccessFile schreibt die Zugangsdatei für die Web-Anmeldung bei
// tunneld (0600, nie überschrieben).
func writeAccessFile(path string, p config.Provisioning, email, pw, name string) error {
	note := ""
	if strings.HasSuffix(strings.ToLower(email), invalidDomain) {
		note = "\n(Die E-Mail ist nur dein Login-Name: Mails kommen dort nicht an, auch\n" +
			"kein Zurücksetzen des Passworts – darum gut aufheben.)\n"
	}
	return writeNewFile(path, fmt.Sprintf(`Dein tunnel-Zugang für wanderwelt (%s)

Anmeldung im Browser: %s
E-Mail (Login):       %s
Passwort:             %s
%s
Bitte das Passwort nach der ersten Anmeldung ändern.

Zum Spielen brauchst du die Anmeldung nicht: Der Starter nimmt die
Gruppendatei. Im Browser siehst du dein Konto und deine Tokens.
`, name, p.WebURL, email, pw, note))
}

// writeGroupFile schreibt die Gruppendatei (Format wie
// deploy/gruppe.example.yaml) mit Rechten 0600; eine vorhandene Datei wird
// nie überschrieben.
func writeGroupFile(path string, p config.Provisioning, token, name, mc string) error {
	who := name
	if mc != "" {
		who += " (Minecraft " + mc + ")"
	}
	content := fmt.Sprintf(`# Gruppendatei für den Starter von %s, erzeugt mit
# „wanderwelt-koordinator mitglied neu“. Sie enthält den persönlichen
# tunnel-Token: nur an %s geben, nicht weiterreichen. Der Starter fragt bei
# der Installation nach ihr.

# Adresse des Koordinators.
coordinator: %s

# tunneld-Server, über den der Host den Minecraft-Port freigibt.
tunnel_server: %s

# Persönlicher tunnel-API-Token (geheim halten!).
token: %s
`, who, name, yamlString(p.CoordinatorURL), yamlString(p.TunnelServer), yamlString(token))
	return writeNewFile(path, content)
}

// writeNewFile legt path mit Rechten 0600 an und schreibt content; eine
// vorhandene Datei wird nie überschrieben, eine halb geschriebene entfernt.
func writeNewFile(path, content string) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(path)
		}
	}()
	if _, err = f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// yamlString setzt s als YAML-Wert, in Anführungszeichen, wo nötig.
func yamlString(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return strings.TrimSuffix(string(b), "\n")
}

// --- mitglied entfernen ---

func (c *mitgliedCmd) entfernen(args []string) int {
	fs := flag.NewFlagSet("mitglied entfernen", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei des Koordinators")
	lock := fs.Bool("konto-sperren", false, "tunnel-Konto auch sperren (bei "+invalidDomain+" immer)")
	foreign := fs.Bool("auch-fremdes-konto", false, "auch ein tunnel-Konto ohne Mitgliedseintrag, das nicht auf "+
		invalidDomain+" endet, aufräumen (Tokens „"+tokenName+"“ widerrufen)")
	// Flags dürfen vor und nach dem Namen stehen.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != 1 {
		fmt.Fprintln(c.errOut, "mitglied entfernen: genau ein Minecraft-Name oder eine E-Mail erwartet")
		return 2
	}
	if err := c.doEntfernen(*cfgPath, pos[0], *lock, *foreign); err != nil {
		fmt.Fprintln(c.errOut, "mitglied entfernen:", err)
		return 1
	}
	return 0
}

func (c *mitgliedCmd) doEntfernen(cfgPath, who string, lock, foreign bool) error {
	if err := checkConfigFile(cfgPath); err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	p := cfg.Provisioning.WithDefaults()
	if err := p.Check(false); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}
	who = strings.TrimSpace(who)
	if who == "" {
		return errors.New("leerer Minecraft-Name bzw. leere E-Mail")
	}
	// Wirksame Namen und Rollen: auch die im Fenster geänderten zählen.
	effective := c.effective(cfg)
	var (
		m     config.Member
		found bool
	)
	ids := c.idKey(cfg)
	for _, x := range effective {
		if strings.EqualFold(x.Email, who) || strings.EqualFold(x.Minecraft, who) ||
			(ids != nil && kennung.IstID(strings.ToLower(who)) && ids.ID(x.Email) == strings.ToLower(who)) {
			m, found = x, true
			break
		}
	}
	email := m.Email
	if !found {
		// Nach einem Abbruch von „mitglied neu“ steht das Konto noch nicht
		// in der Konfiguration; über die E-Mail lässt es sich aufräumen.
		if !strings.Contains(who, "@") {
			return fmt.Errorf("kein Mitglied mit E-Mail, Kennung oder Minecraft-Name %q", who)
		}
		// Solche Reste haben die Domain @wanderwelt.invalid, außer „mitglied
		// neu“ lief mit --email. Jedes andere tunnel-Konto (auch das eines
		// Fremden) nur auf ausdrücklichen Wunsch.
		if !foreign && !strings.HasSuffix(strings.ToLower(who), invalidDomain) {
			return fmt.Errorf("%s ist kein Mitglied. Ohne Mitgliedseintrag räumt „mitglied entfernen“ nur Konten "+
				"%s auf (Rest eines abgebrochenen „mitglied neu“); ein anderes tunnel-Konto nur mit "+
				"--auch-fremdes-konto (widerruft seine Tokens „%s“, sperrt es nur mit --konto-sperren); nichts geändert",
				who, invalidDomain, tokenName)
		}
		email = who
	}
	if found && m.Admin {
		admins := 0
		for _, x := range effective {
			if x.Admin {
				admins++
			}
		}
		if admins == 1 {
			return fmt.Errorf("%s ist das letzte Admin-Mitglied und wird nie entfernt; nichts geändert", m.Email)
		}
	}

	t := tunneldCLI{p: p, run: c.run}
	users, err := t.listUsers()
	if err != nil {
		return err
	}
	var report []string
	if acct, ok := findUser(users, email); ok {
		revoked, err := t.revokeTokens(acct.Email)
		if err != nil {
			return err
		}
		report = append(report, fmt.Sprintf("Token „%s“ widerrufen: %d", tokenName, revoked))
		switch {
		case acct.DisabledAt != "":
			report = append(report, "tunnel-Konto war schon gesperrt")
		case lock || strings.HasSuffix(strings.ToLower(acct.Email), invalidDomain):
			if err := t.admin(nil, "disable-user", "--email", acct.Email); err != nil {
				return err
			}
			report = append(report, "tunnel-Konto gesperrt")
		default:
			report = append(report, "tunnel-Konto bleibt (eigene E-Mail; sperren mit --konto-sperren)")
		}
	} else {
		report = append(report, "kein tunnel-Konto "+email+" bei tunneld")
	}

	if !found {
		fmt.Fprintf(c.out, "%s steht nicht in der Konfiguration; nur das tunnel-Konto aufgeräumt: %s.\n",
			email, strings.Join(report, ", "))
		return nil
	}
	// Das Mitglied kann in der Konfiguration, in members.json (per
	// Beitrittsanfrage angenommen) oder in beiden stehen: überall austragen,
	// sonst gälte es nach dem Neuladen weiter. Auch eine Anfrage des Kontos
	// (joins.json) fällt weg.
	var where []string
	blocked := false
	if slices.ContainsFunc(cfg.Members, func(x config.Member) bool { return strings.EqualFold(x.Email, m.Email) }) {
		if err := config.RemoveMember(cfgPath, m.Email); err != nil {
			return fmt.Errorf("Mitglied austragen (tunnel-Seite ist erledigt: %s): %w", strings.Join(report, ", "), err)
		}
		where = append(where, "aus der Konfiguration")
	}
	if cfg.DataDir != "" {
		// Name des selbst beigetretenen Mitglieds, bevor sein Eintrag fällt.
		joinedMC := m.Minecraft
		if jm, err := config.LoadJoined(filepath.Join(cfg.DataDir, config.MembersFile)); err == nil {
			if e, ok := jm[strings.ToLower(m.Email)]; ok && e.Minecraft != "" {
				joinedMC = e.Minecraft
			}
		}
		selfJoined := false
		if ok, err := config.RemoveJoinedIfPresent(cfg.DataDir, m.Email); err != nil {
			return fmt.Errorf("Mitglied aus members.json austragen (tunnel-Seite ist erledigt: %s): %w", strings.Join(report, ", "), err)
		} else if ok {
			selfJoined = true
			where = append(where, "aus "+config.MembersFile)
		}
		if selfJoined && config.ValidMinecraftName(joinedMC) {
			// Selbst beigetreten: Das tunnel-Konto bleibt, also darf es nicht
			// sofort wieder anfragen – 24 h gesperrt wie nach einer Ablehnung.
			if err := config.BlockJoin(cfg.DataDir, m.Email, joinedMC, time.Now().UTC(), "mitglied entfernen"); err != nil {
				return fmt.Errorf("Beitrittssperre eintragen (tunnel-Seite ist erledigt: %s): %w", strings.Join(report, ", "), err)
			}
			where = append(where, "Beitrittsanfragen des Kontos 24 Stunden gesperrt ("+config.JoinsFile+")")
			blocked = true
		} else if ok, err := config.RemoveJoinRequest(cfg.DataDir, m.Email); err != nil {
			return fmt.Errorf("Beitrittsanfrage austragen (tunnel-Seite ist erledigt: %s): %w", strings.Join(report, ", "), err)
		} else if ok {
			where = append(where, "Beitrittsanfrage aus "+config.JoinsFile)
		}
	}
	if m.Minecraft != "" {
		fmt.Fprintf(c.out, "Mitglied entfernt: %s (%s), %s. %s.\n", m.Minecraft, m.Email, strings.Join(where, " und "), strings.Join(report, ", "))
	} else {
		fmt.Fprintf(c.out, "Mitglied entfernt: %s, %s. %s.\n", m.Email, strings.Join(where, " und "), strings.Join(report, ", "))
	}
	if blocked && !lock {
		fmt.Fprintf(c.out, "Hinweis: Das tunnel-Konto %s bleibt bestehen (widerrufen wurden nur Tokens „%s“); es kann nach 24 Stunden "+
			"wieder eine Beitrittsanfrage stellen. Dauerhaft sperren: im tunneld-Admin-Panel das Konto deaktivieren "+
			"oder „mitglied entfernen %s --konto-sperren“.\n", m.Email, tokenName, m.Email)
	}
	c.reload(p, cfgPath, "")
	return nil
}

// --- mitglied liste ---

func (c *mitgliedCmd) liste(args []string) int {
	fs := flag.NewFlagSet("mitglied liste", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	cfgPath := fs.String("config", defaultConfig, "Konfigurationsdatei des Koordinators")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(c.errOut, "mitglied liste:", err)
		return 1
	}
	effective := c.effective(cfg)
	// Grundlage zum Vergleichen: Konfiguration plus angenommene Mitglieder
	// (gleiche Reihenfolge wie effective).
	base, _ := config.BaseMembers(cfg.Members, cfg.DataDir)
	ids := c.idKey(cfg)
	tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	// Die Spalte KENNUNG gibt es, sobald der Koordinator seinen Schlüssel
	// angelegt hat (erster Start nach dem Update).
	if ids != nil {
		fmt.Fprintln(tw, "E-MAIL\tKENNUNG\tMINECRAFT\tROLLEN")
	} else {
		fmt.Fprintln(tw, "E-MAIL\tMINECRAFT\tROLLEN")
	}
	changed := false
	for i, m := range effective {
		cm := base[i] // gleiche Reihenfolge
		mc := cmp.Or(m.Minecraft, "-")
		if m.Minecraft != cm.Minecraft {
			mc += " *"
			changed = true
		}
		r := cmp.Or(strings.TrimPrefix(roles(m, ", "), ", "), "-")
		if m.Admin != cm.Admin || m.Op != cm.Op {
			r += " *"
			changed = true
		}
		email := m.Email
		if i >= len(cfg.Members) {
			email += " (beigetreten)"
		}
		if ids != nil {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", email, ids.ID(m.Email), mc, r)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", email, mc, r)
		}
	}
	tw.Flush()
	if changed {
		fmt.Fprintf(c.out, "* weicht von der Konfiguration ab: im Fenster oder vom Starter gesetzt (%s, %s in %s)\n",
			config.NamesFile, config.RolesFile, cfg.DataDir)
	}
	return 0
}

// idKey liest den Schlüssel der Mitglieds-Kennungen aus data_dir (nur lesen:
// Der Koordinator legt ihn beim Start an, eine Datei von root wäre für den
// Dienst vielleicht nicht lesbar); nil, wenn es ihn nicht gibt. Die Kennung
// ist es, was Mitglieder und Admins im Fenster sehen; nur der Betreiber kann
// sie hier einer Adresse zuordnen.
func (c *mitgliedCmd) idKey(cfg *config.Config) *kennung.Schluessel {
	if cfg.DataDir == "" {
		return nil
	}
	k, ok, err := kennung.Lesen(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(c.errOut, "Warnung: Kennungs-Schlüssel nicht lesbar:", err)
		return nil
	}
	if !ok {
		return nil
	}
	return k
}

// effective liefert die Mitglieder aus cfg mit den Überlagerungen aus
// data_dir (Namen und Rollen, wie der Koordinator sie sieht; Reihenfolge wie
// cfg.Members). Eine kaputte Überlagerung zählt nicht – wie beim
// Koordinator –, das sagt eine Warnung.
func (c *mitgliedCmd) effective(cfg *config.Config) []config.Member {
	ms, err := config.EffectiveMembers(cfg.Members, cfg.DataDir)
	if err != nil {
		fmt.Fprintf(c.errOut, "Warnung: Überlagerung nicht lesbar, dafür gelten die Werte aus der Konfiguration: %v\n", err)
	}
	return ms
}

// roles liefert „<sep>admin, op“ usw. oder "".
func roles(m config.Member, sep string) string {
	var r []string
	if m.Admin {
		r = append(r, "admin")
	}
	if m.Op {
		r = append(r, "op")
	}
	if len(r) == 0 {
		return ""
	}
	return sep + strings.Join(r, ", ")
}

// reload lässt den laufenden Koordinator die Mitglieder neu lesen
// (reload_command); scheitert das, sagt ein Hinweis, was zu tun ist. Die
// Änderung ist dann trotzdem gespeichert. Ob das Neuladen gelungen ist,
// weiß nur der Koordinator: reload_command schickt nur das Signal, das
// Ergebnis steht im Journal. mc (darf leer sein) ist der Minecraft-Name
// eines neuen Mitglieds; ob er auflösbar ist, steht ebenfalls dort.
func (c *mitgliedCmd) reload(p config.Provisioning, cfgPath, mc string) {
	argv := strings.Fields(p.ReloadCommand)
	_, stderr, err := c.run.Run(nil, argv[0], argv[1:]...)
	if err == nil {
		fmt.Fprintln(c.out, "Neuladen ausgelöst – Ergebnis: journalctl -u wanderwelt-koordinator")
		if mc != "" {
			fmt.Fprintf(c.out, "Dort steht nach ein paar Sekunden auch, ob es den Minecraft-Namen %s gibt: "+
				"„UUID nicht auflösbar“ mit minecraft=%s heißt meist ein Tippfehler (dann fehlt er in der Whitelist) – "+
				"den Namen in %s korrigieren und erneut „%s“.\n", mc, mc, cfgPath, p.ReloadCommand)
		}
		return
	}
	fmt.Fprintf(c.errOut, "Hinweis: Neuladen fehlgeschlagen (%s: %v%s). Koordinator neu starten "+
		"(systemctl restart wanderwelt-koordinator), sonst gilt die Änderung erst beim nächsten Start.\n",
		p.ReloadCommand, err, stderrText(stderr))
}

// --- tunneld-Admin-CLI ---

type tunneldCLI struct {
	p   config.Provisioning
	run runner
}

// tunnelUser ist eine Zeile von list-users.
type tunnelUser struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	DisabledAt string `json:"disabled_at"`
}

// call ruft „runuser -u <run_as> -- <tunneld_bin> admin <sub> --config
// <tunneld_config> <args…>“ auf und liefert stdout. Ein Fehler enthält nur
// stderr von tunneld, nie stdout (dort steht bei issue-token der Token).
func (t tunneldCLI) call(stdin []byte, sub string, args ...string) ([]byte, error) {
	argv := append([]string{"-u", t.p.RunAs, "--", t.p.TunneldBin, "admin", sub, "--config", t.p.TunneldConfig}, args...)
	stdout, stderr, err := t.run.Run(stdin, "runuser", argv...)
	if err != nil {
		return nil, fmt.Errorf("tunneld admin %s: %v%s", sub, err, stderrText(stderr))
	}
	return stdout, nil
}

// admin ist call ohne Blick auf die Ausgabe.
func (t tunneldCLI) admin(stdin []byte, sub string, args ...string) error {
	_, err := t.call(stdin, sub, args...)
	return err
}

func (t tunneldCLI) listUsers() ([]tunnelUser, error) {
	out, err := t.call(nil, "list-users")
	if err != nil {
		return nil, err
	}
	var users []tunnelUser
	if err := json.Unmarshal(out, &users); err != nil {
		return nil, fmt.Errorf("tunneld admin list-users: Ausgabe nicht lesbar: %w", err)
	}
	return users, nil
}

// issueToken stellt den Token tokenName für email aus und liefert ihn.
// Die Ausgabe von tunneld kommt in keine Meldung.
func (t tunneldCLI) issueToken(email string) (string, error) {
	out, err := t.call(nil, "issue-token", "--email", email, "--name", tokenName)
	if err != nil {
		return "", err
	}
	var res struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(out, &res) != nil || strings.TrimSpace(res.Token) == "" {
		return "", errors.New("tunneld admin issue-token: Ausgabe ohne lesbaren Token (Token ist vermutlich ausgestellt)")
	}
	return strings.TrimSpace(res.Token), nil
}

// revokeTokens widerruft alle aktiven Tokens namens tokenName des Kontos
// email und liefert ihre Anzahl.
func (t tunneldCLI) revokeTokens(email string) (int, error) {
	out, err := t.call(nil, "list-tokens", "--email", email)
	if err != nil {
		return 0, err
	}
	var toks []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &toks); err != nil {
		return 0, fmt.Errorf("tunneld admin list-tokens: Ausgabe nicht lesbar: %w", err)
	}
	n := 0
	for _, tok := range toks {
		if tok.Name != tokenName {
			continue
		}
		if err := t.admin(nil, "revoke-token", "--id", strconv.FormatInt(tok.ID, 10)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func findUser(users []tunnelUser, email string) (tunnelUser, bool) {
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return u, true
		}
	}
	return tunnelUser{}, false
}

// stderrText ist stderr eines Programms für eine Meldung: „: <Text>“ oder "".
func stderrText(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if len(s) > maxStderr {
		s = "…" + s[len(s)-maxStderr:]
	}
	return ": " + s
}
