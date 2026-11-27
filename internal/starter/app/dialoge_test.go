package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ncruces/zenity"
)

func TestEineZeile(t *testing.T) {
	lang := strings.Repeat("ä", maxZeile+5)
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"kurz bleibt", "Token einfügen (Strg+V):", maxZeile, "Token einfügen (Strg+V):"},
		{"genau am Rand", strings.Repeat("ü", 10), 10, strings.Repeat("ü", 10)},
		{"eine Rune zu lang: Ellipse in der Grenze", strings.Repeat("ü", 11), 10, strings.Repeat("ü", 9) + "…"},
		{"Umlaute zählen als eine Rune", lang, maxZeile, strings.Repeat("ä", maxZeile-1) + "…"},
		{"mehrzeilig: nur die erste Zeile", "Erste Zeile\n\nZweite Zeile", maxZeile, "Erste Zeile"},
		{"führende Leerzeilen überspringen", "\r\n  \n  Dann diese  \r\nund jene", maxZeile, "Dann diese"},
		{"mehrzeilig und lang", lang + "\nzweite", maxZeile, strings.Repeat("ä", maxZeile-1) + "…"},
		{"Leerraum vor der Ellipse entfällt", "abcd efgh", 6, "abcd…"},
		{"leer", "  \n ", maxZeile, ""},
	}
	for _, c := range cases {
		got := eineZeile(c.in, c.max)
		if got != c.want {
			t.Errorf("%s: %q, erwartet %q", c.name, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > c.max || strings.ContainsAny(got, "\r\n") {
			t.Errorf("%s: %q hat %d Zeichen oder einen Umbruch", c.name, got, n)
		}
	}
}

func TestZeileMitNameKuerztDenNamen(t *testing.T) {
	// Passt es, bleibt alles.
	if got := zeileMitName("Lade ", "fabric-api", " (2,1 MB) …", maxZeile); got != "Lade fabric-api (2,1 MB) …" {
		t.Errorf("kurz: %q", got)
	}
	// Zu lang: Der Name wird gekürzt, die Größe dahinter bleibt.
	got := zeileMitName("Lade ", "fabric-api-0.100.0+1.21-mit-sehr-langem-Namen.jar", " (2,1 MB) …", maxZeile)
	if utf8.RuneCountInString(got) > maxZeile || !strings.HasPrefix(got, "Lade fabric-api") || !strings.HasSuffix(got, "… (2,1 MB) …") {
		t.Errorf("lang: %q (%d Zeichen)", got, utf8.RuneCountInString(got))
	}
	// Bleibt für den Namen kein Platz, greift die Notbremse.
	got = zeileMitName(strings.Repeat("v", 38), "Name", " (1 KB)", maxZeile)
	if utf8.RuneCountInString(got) > maxZeile || !strings.HasSuffix(got, "…") {
		t.Errorf("kein Platz: %q", got)
	}
	// Mehrzeiliger Name: eine Zeile.
	if got := zeileMitName("Lade ", "a\nb", " (1 KB) …", maxZeile); strings.Contains(got, "\n") {
		t.Errorf("Umbruch: %q", got)
	}
}

func TestKurzeEintraegeEindeutig(t *testing.T) {
	lang := strings.Repeat("x", maxEintrag+10)
	in := []string{"kurz", lang + "A", lang + "B", lang + "C", "kurz"}
	out := kurzeEintraege(in)
	seen := map[string]bool{}
	for i, s := range out {
		if utf8.RuneCountInString(s) > maxEintrag {
			t.Errorf("Eintrag %d zu lang: %q", i, s)
		}
		if seen[s] {
			t.Errorf("Eintrag %d doppelt: %q (alle: %q)", i, s, out)
		}
		seen[s] = true
	}
	if out[0] != "kurz" || out[4] != "kurz [2]" {
		t.Errorf("Doppelte: %q", out)
	}
}

// stubRaw ersetzt die echten Fenster und die Meldungsfenster; labels und
// items sammeln, was an zenity gehen würde.
type rohDialoge struct {
	labels   []string
	items    [][]string
	infos    []string
	progress []string
	// Antworten
	pick      string // Eintrag, den die Liste „wählt“ (leer: der erste)
	listErr   error
	entryText string
	entryErr  error
	fenster   zenity.ProgressDialog
}

func stubRaw(t *testing.T) *rohDialoge {
	t.Helper()
	r := &rohDialoge{fenster: &fakeProgress{done: make(chan struct{})}}
	oL, oE, oP, oI := rawListe, rawEingabe, rawFortschritt, showInfo
	t.Cleanup(func() { rawListe, rawEingabe, rawFortschritt, showInfo = oL, oE, oP, oI })
	rawListe = func(text string, items []string, _ ...zenity.Option) (string, error) {
		r.labels = append(r.labels, text)
		r.items = append(r.items, slices.Clone(items))
		if r.listErr != nil {
			return "", r.listErr
		}
		if r.pick != "" {
			return r.pick, nil
		}
		return items[0], nil
	}
	rawEingabe = func(text string, _ ...zenity.Option) (string, error) {
		r.labels = append(r.labels, text)
		return r.entryText, r.entryErr
	}
	rawFortschritt = func(...zenity.Option) (zenity.ProgressDialog, error) {
		r.progress = append(r.progress, "geöffnet")
		return r.fenster, nil
	}
	showInfo = func(text string) { r.infos = append(r.infos, text) }
	return r
}

func kurzeZeile(t *testing.T, was, s string) {
	t.Helper()
	if n := utf8.RuneCountInString(s); n == 0 || n > maxZeile || strings.ContainsAny(s, "\r\n") {
		t.Errorf("%s: %q ist keine kurze einzelne Zeile (%d Zeichen, höchstens %d)", was, s, n, maxZeile)
	}
}

// Die Aufteilung: Eine lange Erklärung geht ans Meldungsfenster, das
// Eingabefenster bekommt nur die kurze Zeile.
func TestEingabeMitMeldungTeilt(t *testing.T) {
	r := stubRaw(t)
	r.entryText = "eingabe"
	erkl := strings.Repeat("Das ist eine lange Erklärung. ", 20) + "\n\nUnd noch ein Absatz."
	s, err := eingabeMitMeldung(erkl, labelServer)
	if err != nil || s != "eingabe" {
		t.Fatalf("%q %v", s, err)
	}
	if len(r.infos) != 1 || r.infos[0] != erkl {
		t.Errorf("Meldung nicht vollständig im Meldungsfenster: %q", r.infos)
	}
	if len(r.labels) != 1 || r.labels[0] != labelServer {
		t.Fatalf("Eingabefenster: %q", r.labels)
	}
	kurzeZeile(t, "Eingabe", r.labels[0])

	// Ohne Hinweis kein Meldungsfenster.
	r.infos = nil
	if _, err := eingabeMitMeldung("", labelToken); err != nil || len(r.infos) != 0 {
		t.Errorf("Meldungen ohne Hinweis: %q %v", r.infos, err)
	}

	// Ein zu langes Label wird trotzdem eine kurze Zeile (nicht auf Disziplin
	// des Aufrufers verlassen).
	r.labels = nil
	zeigeEingabe(erkl)
	kurzeZeile(t, "zu langes Label", r.labels[0])
}

func TestListeMitMeldungTeiltUndLiefertIndex(t *testing.T) {
	r := stubRaw(t)
	lang := strings.Repeat("W", 2*maxEintrag)
	items := []string{"eins", lang + "1", lang + "2"}
	kurz := kurzeEintraege(items)
	r.pick = kurz[2]
	i, err := listeMitMeldung("Lange Anleitung\n\nmit Absätzen", "Wie geht es weiter?\nzweite Zeile", items)
	if err != nil || i != 2 {
		t.Fatalf("Index %d, %v", i, err)
	}
	if len(r.infos) != 1 || r.infos[0] != "Lange Anleitung\n\nmit Absätzen" {
		t.Errorf("Meldungen: %q", r.infos)
	}
	kurzeZeile(t, "Liste", r.labels[0])
	for _, it := range r.items[0] {
		if utf8.RuneCountInString(it) > maxEintrag {
			t.Errorf("Eintrag zu lang: %q", it)
		}
	}
	// Abbruch.
	r.listErr = zenity.ErrCanceled
	if i, err := zeigeListe("x", items); err == nil || i != -1 {
		t.Errorf("Abbruch: %d %v", i, err)
	}
	// Antwort, die zu keinem Eintrag passt.
	r.listErr, r.pick = nil, "nicht in der Liste"
	if i, err := zeigeListe("x", items); err == nil || i != -1 {
		t.Errorf("fremde Antwort: %d %v", i, err)
	}
}

type merkText struct {
	*fakeProgress
	texte []string
}

func (m *merkText) Text(s string) error { m.texte = append(m.texte, s); return nil }

func TestSetzeFortschrittTextEinzeilig(t *testing.T) {
	setzeFortschrittText(nil, "kein Fenster") // darf nicht abstürzen
	m := &merkText{fakeProgress: &fakeProgress{done: make(chan struct{})}}
	setzeFortschrittText(m, "Erste Zeile\n\nZweite Zeile, die nie ankommt")
	setzeFortschrittText(m, strings.Repeat("Lang ", 30))
	if len(m.texte) != 2 || m.texte[0] != "Erste Zeile" {
		t.Fatalf("Texte: %q", m.texte)
	}
	for _, s := range m.texte {
		kurzeZeile(t, "Fortschritt", s)
	}
}

// Alle Beschriftungen und Fortschrittstexte sind kurz, die Menüeinträge
// passen ins Listenfeld.
func TestBeschriftungenSindKurz(t *testing.T) {
	for _, l := range []string{labelWeiter, labelGruppeWahl, labelGruppeBeitr, labelToken, labelGruppencode,
		labelServer, labelMinecraftNam, labelWelt, textWartenAufFreigabe, textAbfrageKlemmt, textPruefeDeps} {
		kurzeZeile(t, "Beschriftung", l)
		if strings.HasSuffix(l, "…") && utf8.RuneCountInString(l) > maxZeile-1 {
			t.Errorf("%q läge an der Grenze", l)
		}
	}
	items := []string{menuCode, menuEntdecken, menuDatei}
	for _, it := range tokenMenuItems {
		items = append(items, it)
	}
	for _, it := range items {
		if n := utf8.RuneCountInString(it); n > maxEintrag {
			t.Errorf("Menüeintrag %q hat %d Zeichen (höchstens %d)", it, n, maxEintrag)
		}
		if eineZeile(it, maxEintrag) != it {
			t.Errorf("Menüeintrag %q würde gekürzt", it)
		}
	}
}

// Die echten Dialoge der Einrichtung: Was an zenity geht, ist eine kurze
// Zeile; die Erklärungen kommen vorher als Meldungsfenster, die Anleitung
// nur beim ersten Mal.
func TestEinrichtungsDialogeTeilenText(t *testing.T) {
	r := stubRaw(t)

	// Token-Menü: Anleitung nur mit intro.
	if c, ok := tokenMenu(msgTokenAnleitung); !ok || c != choiceAccount {
		t.Fatalf("Menü: %v %v", c, ok)
	}
	if len(r.infos) != 1 || r.infos[0] != msgTokenAnleitung {
		t.Errorf("Anleitung nicht als Meldung: %q", r.infos)
	}
	r.infos = nil
	if _, ok := tokenMenu(""); !ok || len(r.infos) != 0 {
		t.Errorf("Anleitung beim zweiten Mal: %q", r.infos)
	}

	// Token: Fehler als Meldung, Label kurz.
	r.entryText = "token"
	r.infos = nil
	tokenEntry("Der Token ist leer.")
	if len(r.infos) != 1 || r.infos[0] != "Der Token ist leer." {
		t.Errorf("Token-Hinweis: %q", r.infos)
	}
	tokenEntry("")
	if len(r.infos) != 1 {
		t.Errorf("Token ohne Hinweis zeigt Meldung: %q", r.infos)
	}

	// Server-Adresse: erster Aufruf erklärt, spätere zeigen nur den Hinweis.
	r.infos = nil
	askTunnelServer("", "")
	askTunnelServer("x.de", "Das ist keine Server-Adresse.")
	if len(r.infos) != 2 || r.infos[0] != msgServerErklaerung || r.infos[1] != "Das ist keine Server-Adresse." {
		t.Errorf("Server-Meldungen: %q", r.infos)
	}

	// Startmenü und Gruppenwahl.
	r.infos = nil
	if s, ok := setupMenu(); !ok || s != menuCode {
		t.Errorf("setupMenu: %q %v", s, ok)
	}
	lang := strings.Repeat("Titel ", 12)
	r.pick = ""
	if i, ok := pickGroup([]string{lang + "A", lang + "B"}); !ok || i != 0 {
		t.Errorf("pickGroup: %d %v", i, ok)
	}

	// Gruppencode: erster Aufruf (nach dem Menü) mit Erklärung, Hinweis ersetzt sie.
	oMenu := setupMenu
	t.Cleanup(func() { setupMenu = oMenu })
	setupMenu = func() (string, bool) { return menuCode, true }
	r.infos = nil
	askGroupCode("")
	askGroupCode("Der Gruppencode ist ungültig.")
	if len(r.infos) != 2 || r.infos[0] != msgGruppencodeErklaerung || r.infos[1] != "Der Gruppencode ist ungültig." {
		t.Errorf("Gruppencode-Meldungen: %q", r.infos)
	}

	// Minecraft-Name.
	r.infos = nil
	nameEntry(msgNameErklaerung)
	if len(r.infos) != 1 || r.infos[0] != msgNameErklaerung {
		t.Errorf("Name-Meldungen: %q", r.infos)
	}

	// Wartefenster: Erklärung einmal vor dem Fenster.
	r.infos, r.progress = nil, nil
	if _, err := openJoinWait(); err != nil || len(r.infos) != 1 || r.infos[0] != msgWartenErklaerung || len(r.progress) != 1 {
		t.Errorf("Wartefenster: %v infos=%q progress=%q", err, r.infos, r.progress)
	}

	// Alles, was als Label an die Fenster ging, ist kurz; Listen sind kurz.
	if len(r.labels) < 8 {
		t.Fatalf("zu wenige Fenster erfasst: %q", r.labels)
	}
	for _, l := range r.labels {
		kurzeZeile(t, "Label", l)
	}
	for _, items := range r.items {
		for _, it := range items {
			if utf8.RuneCountInString(it) > maxEintrag {
				t.Errorf("Listeneintrag zu lang: %q", it)
			}
		}
	}
	// Die Erklärungen selbst sind lang genug, dass sie nie in eine Zeile passten.
	for _, e := range []string{msgTokenAnleitung, msgServerErklaerung, msgGruppencodeErklaerung, msgWartenErklaerung} {
		if utf8.RuneCountInString(e) <= maxZeile {
			t.Errorf("%q ist kurz genug für eine Zeile: gehört nicht in ein Meldungsfenster", e)
		}
	}
}

func TestFortschrittLadeZeileKurz(t *testing.T) {
	m := &merkText{fakeProgress: &fakeProgress{max: 1000, done: make(chan struct{})}}
	p := newDepsProgress(m, []string{"fabric-api-0.100.0+1.21-mit-langem-Namen.jar", "tunnel"})
	p.report("fabric-api-0.100.0+1.21-mit-langem-Namen.jar", 100, 2_200_000)
	p.report("tunnel", 5, -1)
	if len(m.texte) != 2 {
		t.Fatalf("Texte: %q", m.texte)
	}
	for _, s := range m.texte {
		kurzeZeile(t, "Lade", s)
		if !strings.HasPrefix(s, "Lade ") || !strings.HasSuffix(s, " …") || !strings.Contains(s, "(") {
			t.Errorf("Text %q ohne Name/Größe", s)
		}
	}
	if !strings.Contains(m.texte[0], "2,1 MB") || !strings.Contains(m.texte[1], "KB") {
		t.Errorf("Größen: %q", m.texte)
	}
}

func TestListenLabelKurz(t *testing.T) {
	s := SaveInfo{Folder: strings.Repeat("Meine Welt ", 8), Version: "1.21.4", Size: 120 << 20}
	got := s.ListenLabel()
	if n := utf8.RuneCountInString(got); n > maxEintrag || !strings.HasSuffix(got, "· 1.21.4 · 120,0 MB") {
		t.Errorf("%q (%d Zeichen)", got, n)
	}
	if got := (SaveInfo{Folder: "Welt", Err: os.ErrNotExist}).ListenLabel(); got != "Welt · unlesbar · 0,0 KB" {
		t.Errorf("%q", got)
	}
}

// ---- Wächter ----

// Außerhalb von dialoge.go darf niemand zenity.List, zenity.Entry,
// zenity.Progress (und die unter Windows wirkungslosen zenity.Width/Height)
// benutzen noch ProgressDialog.Text mit ungeprüftem Text aufrufen: Die
// Fenster sind unter Windows fest und haben nur eine kurze Zeile.
var verboteneZenity = map[string]string{
	"List": "zeigeListe/listeMitMeldung", "Entry": "zeigeEingabe/eingabeMitMeldung",
	"Progress": "oeffneFortschritt", "Width": "(wirkt unter Windows nicht)", "Height": "(wirkt unter Windows nicht)",
}

// pruefeDialogQuelle liefert die Verstöße einer Quelldatei.
func pruefeDialogQuelle(name string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	zenityName := "zenity"
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/ncruces/zenity" && imp.Name != nil {
			zenityName = imp.Name.Name
		}
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == zenityName {
				if ersatz, bad := verboteneZenity[x.Sel.Name]; bad {
					out = append(out, fmt.Sprintf("%s: zenity.%s – nimm %s", fset.Position(x.Pos()), x.Sel.Name, ersatz))
				}
			}
		case *ast.CallExpr:
			// ProgressDialog.Text hat genau ein Argument (andere .Text-Methoden
			// im Paket haben keins oder zwei).
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Text" && len(x.Args) == 1 {
				out = append(out, fmt.Sprintf("%s: .Text(…) auf einem Fortschrittsfenster – nimm setzeFortschrittText", fset.Position(x.Pos())))
			}
		}
		return true
	})
	return out, nil
}

func TestWaechterKeineRohenDialogaufrufe(t *testing.T) {
	dateien, err := filepath.Glob("*.go")
	if err != nil || len(dateien) == 0 {
		t.Fatalf("Quelldateien: %v %v", dateien, err)
	}
	geprueft := 0
	for _, d := range dateien {
		if strings.HasSuffix(d, "_test.go") || d == "dialoge.go" {
			continue
		}
		geprueft++
		verstoesse, err := pruefeDialogQuelle(d, nil)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		for _, v := range verstoesse {
			t.Errorf("%s", v)
		}
	}
	if geprueft < 10 {
		t.Fatalf("nur %d Dateien geprüft", geprueft)
	}
}

// Der Wächter selbst erkennt die Verstöße (und lässt die Hilfsfunktionen zu).
func TestWaechterErkenntVerstoesse(t *testing.T) {
	schlecht := map[string]string{
		"List":     `zenity.List("a\nb", nil)`,
		"Entry":    `zenity.Entry("langer Text")`,
		"Progress": `zenity.Progress()`,
		"Width":    `zenity.Width(720)`,
		"Wert":     `f := zenity.List; _ = f`,
		"Text":     `dlg.Text("langer Text")`,
		"Text2":    `p.dlg.Text(fmt.Sprintf("Lade %s", x))`,
	}
	for name, code := range schlecht {
		src := "package app\nimport (\"fmt\"; \"github.com/ncruces/zenity\")\nvar _ = fmt.Sprint\nfunc f(dlg zenity.ProgressDialog, p struct{ dlg zenity.ProgressDialog }, x string) {\n" + code + "\n}\n"
		v, err := pruefeDialogQuelle(name+".go", src)
		if err != nil || len(v) == 0 {
			t.Errorf("%s: nicht erkannt (%v, %v)", name, v, err)
		}
	}
	gut := `package app
import ("github.com/ncruces/zenity"; "bufio")
func f(dlg zenity.ProgressDialog, sc *bufio.Scanner) {
	zeigeListe("x", nil, zenity.Title("t"), zenity.EntryText("e"))
	eingabeMitMeldung("", "x")
	setzeFortschrittText(dlg, "text")
	_ = sc.Text()
	_ = ereignis.Text(e, f)
	var _ zenity.ProgressDialog
	zenity.Info("lang", zenity.Title("t"))
}`
	if v, err := pruefeDialogQuelle("gut.go", gut); err != nil || len(v) != 0 {
		t.Errorf("zu Unrecht beanstandet: %v %v", v, err)
	}
}
