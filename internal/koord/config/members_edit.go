package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// AddMember trägt m am Ende von members in die Konfigurationsdatei path ein,
// RemoveMember nimmt das Mitglied mit der E-Mail email (Groß/Klein egal)
// heraus. Geändert wird nur die Mitgliederliste; Kommentare und alle übrigen
// Schlüssel bleiben stehen.
//
// Ist members eine Blockliste (der Normalfall), ändern beide nur die Zeilen
// des betroffenen Eintrags, sodass auch Leerzeilen und Einrückung bleiben.
// Sonst schreiben sie das Dokument über den yaml.v3-Knoten neu (Kommentare
// bleiben, Leerzeilen nicht). Das Ergebnis muss Load bestehen und genau die
// erwartete Liste bei sonst gleicher Konfiguration ergeben; erst dann
// ersetzt es die Datei atomar, mit Rechten und Besitzer der alten.
func AddMember(path string, m Member) error {
	item := memberNode(m)
	return editMembers(path, func(old []Member) (memberEdit, error) {
		return memberEdit{
			want:   append(slices.Clone(old), m),
			splice: func(data []byte, seq *yaml.Node) ([]byte, bool) { return spliceAppend(data, seq, item) },
			node:   func(root *yaml.Node) error { return appendMemberNode(root, item) },
		}, nil
	})
}

// RemoveMember: siehe AddMember.
func RemoveMember(path, email string) error {
	return editMembers(path, func(old []Member) (memberEdit, error) {
		want := strings.ToLower(email)
		i := slices.IndexFunc(old, func(m Member) bool { return strings.ToLower(m.Email) == want })
		if i < 0 {
			return memberEdit{}, fmt.Errorf("%s: kein Mitglied %q in members", path, email)
		}
		return memberEdit{
			want:   slices.Delete(slices.Clone(old), i, i+1),
			splice: func(data []byte, seq *yaml.Node) ([]byte, bool) { return spliceRemove(data, seq, i) },
			node: func(root *yaml.Node) error {
				seq := mappingValue(root, "members")
				if seq == nil || seq.Kind != yaml.SequenceNode || i >= len(seq.Content) {
					return errors.New("members ist keine einfache Liste")
				}
				seq.Content = slices.Delete(seq.Content, i, i+1)
				return nil
			},
		}, nil
	})
}

// memberEdit ist eine Änderung der Mitgliederliste: das erwartete Ergebnis,
// die Änderung als Zeilen-Eingriff (wenn möglich) und dieselbe Änderung am
// yaml.v3-Knoten als Rückfall.
type memberEdit struct {
	want   []Member
	splice func(data []byte, seq *yaml.Node) ([]byte, bool)
	node   func(root *yaml.Node) error
}

func editMembers(path string, plan func(old []Member) (memberEdit, error)) error {
	// Ein Symlink bleibt einer; ersetzt wird sein Ziel.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	before, err := Load(real)
	if err != nil {
		return err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: oberste Ebene ist keine Zuordnung", path)
	}
	root := doc.Content[0]

	e, err := plan(before.Members)
	if err != nil {
		return err
	}
	var candidates [][]byte
	if out, ok := e.splice(data, mappingValue(root, "members")); ok {
		candidates = append(candidates, out)
	}
	if err := e.node(root); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("%s: neu schreiben: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: neu schreiben: %w", path, err)
	}
	candidates = append(candidates, buf.Bytes())

	check := func(tmp string) error { return checkEdited(before, tmp, e.want) }
	for _, out := range candidates {
		if err = replaceFile(real, out, fi, check); err == nil {
			return nil
		}
	}
	return fmt.Errorf("%s: %w", path, err)
}

// checkEdited prüft die geänderte Datei tmp: gültig, members gleich want,
// alles andere wie in before.
func checkEdited(before *Config, tmp string, want []Member) error {
	after, err := Load(tmp)
	if err != nil {
		return fmt.Errorf("geänderte Konfiguration ungültig: %w", err)
	}
	if !slices.Equal(after.Members, want) {
		return errors.New("geänderte Konfiguration hat nicht die erwartete Mitgliederliste")
	}
	a, b := *before, *after
	a.Members, b.Members = nil, nil
	if !reflect.DeepEqual(a, b) {
		return errors.New("geänderte Konfiguration weicht außerhalb von members ab")
	}
	return nil
}

// replaceFile schreibt data neben path, übernimmt Rechte und Besitzer aus
// fi, lässt check die neue Datei prüfen und ersetzt path dann atomar.
func replaceFile(path string, data []byte, fi os.FileInfo, check func(tmp string) error) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return err
	}
	if err = copyOwner(tmp, fi); err != nil {
		return err
	}
	if err = check(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// memberNode baut den Eintrag für m; minecraft, admin und op nur, wenn
// gesetzt.
// Werte mit Tag !!str setzt der Encoder bei Bedarf in Anführungszeichen
// (etwa ein Minecraft-Name „null“).
func memberNode(m Member) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(key, tag, value string) {
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	}
	add("email", "!!str", m.Email)
	if m.Minecraft != "" {
		add("minecraft", "!!str", m.Minecraft)
	}
	if m.Admin {
		add("admin", "!!bool", "true")
	}
	if m.Op {
		add("op", "!!bool", "true")
	}
	return n
}

// appendMemberNode hängt item an members an; fehlt members oder ist es
// leer (null), entsteht die Liste.
func appendMemberNode(root, item *yaml.Node) error {
	seq := mappingValue(root, "members")
	switch {
	case seq == nil:
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "members"},
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{item}})
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		seq.Kind, seq.Tag, seq.Value, seq.Style = yaml.SequenceNode, "!!seq", "", 0
		seq.Content = []*yaml.Node{item}
	case seq.Kind == yaml.SequenceNode:
		seq.Content = append(seq.Content, item)
	default:
		return errors.New("members ist keine Liste")
	}
	return nil
}

// mappingValue liefert den Wert zu key in der Zuordnung m oder nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// spliceAppend fügt item als neuen Eintrag hinter der letzten Zeile des
// letzten Eintrags der Blockliste seq ein, eingerückt wie der erste.
func spliceAppend(data []byte, seq, item *yaml.Node) ([]byte, bool) {
	if !blockList(data, seq) {
		return nil, false
	}
	lines := strings.SplitAfter(string(data), "\n")
	prefix, ok := dashPrefix(lines, seq.Content[0])
	if !ok {
		return nil, false
	}
	end := lastLine(seq.Content[len(seq.Content)-1])
	if end > len(lines) {
		return nil, false
	}
	body, err := yaml.Marshal(item)
	if err != nil {
		return nil, false
	}
	var add []string
	for i, l := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if i == 0 {
			add = append(add, prefix+l+"\n")
		} else {
			add = append(add, strings.Repeat(" ", len(prefix))+l+"\n")
		}
	}
	head := slices.Clone(lines[:end])
	if !strings.HasSuffix(head[end-1], "\n") {
		head[end-1] += "\n"
	}
	return []byte(strings.Join(slices.Concat(head, add, lines[end:]), "")), true
}

// spliceRemove entfernt die Zeilen des Eintrags i der Blockliste seq.
func spliceRemove(data []byte, seq *yaml.Node, i int) ([]byte, bool) {
	if !blockList(data, seq) || i >= len(seq.Content) {
		return nil, false
	}
	lines := strings.SplitAfter(string(data), "\n")
	item := seq.Content[i]
	if _, ok := dashPrefix(lines, item); !ok {
		return nil, false
	}
	start, end := item.Line, lastLine(item)
	if end > len(lines) {
		return nil, false
	}
	return []byte(strings.Join(slices.Concat(lines[:start-1], lines[end:]), "")), true
}

// blockList: seq ist eine nicht leere Blockliste in einer Datei mit
// Unix-Zeilenenden – nur dann greifen die Zeilen-Eingriffe.
func blockList(data []byte, seq *yaml.Node) bool {
	return seq != nil && seq.Kind == yaml.SequenceNode && seq.Style&yaml.FlowStyle == 0 &&
		len(seq.Content) > 0 && !bytes.ContainsRune(data, '\r')
}

// dashPrefix liefert den Zeilenanfang vor dem Eintrag item ("  - "), wenn
// item in derselben Zeile wie sein Strich beginnt.
func dashPrefix(lines []string, item *yaml.Node) (string, bool) {
	if item.Line < 1 || item.Line > len(lines) {
		return "", false
	}
	line, col := lines[item.Line-1], item.Column-1
	if col < 2 || col > len(line) {
		return "", false
	}
	p := line[:col]
	t := strings.TrimLeft(p, " ")
	if !strings.HasPrefix(t, "-") || strings.Trim(t[1:], " ") != "" || len(t) < 2 {
		return "", false
	}
	return p, true
}

// lastLine ist die letzte Zeile (ab 1), in der n oder ein Kind von n steht.
func lastLine(n *yaml.Node) int {
	last := n.Line
	for _, c := range n.Content {
		last = max(last, lastLine(c))
	}
	return last
}
