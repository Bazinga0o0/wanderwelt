package fenster

import (
	"strings"
	"testing"
	"time"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
)

// Chat-Verlauf im Formular „Welten verwalten“ (nur Admins): Anzeige der
// aktuellen Werte, nur geänderte Felder werden gesendet, Größe wird geprüft.
func TestWeltenChatFormular(t *testing.T) {
	jetzt := time.Date(2026, 11, 23, 15, 30, 0, 0, time.Local)
	admin := func() *bedienTest {
		z := mitWelten(beispielZustand(jetzt, true, false), "")
		z.Status.Worlds[0].ChatOff, z.Status.Worlds[0].ChatMiB = true, 12
		b := weltBedienung(t, z, reiterWelt)
		b.bild()
		b.u.welt.verwaltung.auf.Click()
		b.bild()
		return b
	}

	t.Run("Formular zeigt die Werte der Welt", func(t *testing.T) {
		b := admin()
		v := &b.u.welt.verwaltung
		klick(&v.aendern, "w1").Click()
		b.bild()
		if !v.chatAus || v.chatMiBEd.Text() != "12" {
			t.Fatalf("w1: aus %v, Größe %q", v.chatAus, v.chatMiBEd.Text())
		}
		klick(&v.aendern, "w2").Click()
		b.bild()
		if v.chatAus || v.chatMiBEd.Text() != "5" {
			t.Fatalf("w2 (Standard): aus %v, Größe %q", v.chatAus, v.chatMiBEd.Text())
		}
		v.neu.Click()
		b.bild()
		if v.chatAus || v.chatMiBEd.Text() != "5" {
			t.Fatalf("neue Welt: aus %v, Größe %q", v.chatAus, v.chatMiBEd.Text())
		}
	})
	t.Run("ändern: nur geänderte Chat-Felder", func(t *testing.T) {
		b := admin()
		v := &b.u.welt.verwaltung
		klick(&v.aendern, "w1").Click()
		b.bild()
		klick(&v.chatKn, chatAn).Click()
		v.chatMiBEd.SetText("30")
		b.bild()
		v.speichern.Click()
		b.bild()
		got := b.warte(1)
		want := `weltaendern {"id":"w1","patch":{"name":"Survival","gamemode":"survival","difficulty":"hard","chat_off":false,"chat_mib":30}}`
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Aktionen: %q, erwartet %q", got, want)
		}
	})
	t.Run("ändern: nur der Schalter", func(t *testing.T) {
		b := admin()
		v := &b.u.welt.verwaltung
		klick(&v.aendern, "w2").Click()
		b.bild()
		klick(&v.chatKn, chatAusWert).Click()
		b.bild()
		v.speichern.Click()
		b.bild()
		got := b.warte(1)
		if len(got) != 1 || !strings.Contains(got[0], `"chat_off":true`) || strings.Contains(got[0], "chat_mib") {
			t.Fatalf("Aktionen: %q", got)
		}
	})
	t.Run("anlegen mit Chat aus und eigener Größe", func(t *testing.T) {
		b := admin()
		v := &b.u.welt.verwaltung
		v.neu.Click()
		b.bild()
		v.nameEd.SetText("Privat")
		klick(&v.chatKn, chatAusWert).Click()
		v.chatMiBEd.SetText("2")
		b.bild()
		v.speichern.Click()
		b.bild()
		got := b.warte(1)
		if len(got) != 1 || got[0] != `weltneu {"name":"Privat","chat_off":true,"chat_mib":2}` {
			t.Fatalf("Aktionen: %q", got)
		}
	})
	t.Run("ungültige Größe schickt nichts", func(t *testing.T) {
		for _, s := range []string{"", "0", "51", "abc"} {
			b := admin()
			v := &b.u.welt.verwaltung
			klick(&v.aendern, "w1").Click()
			b.bild()
			v.chatMiBEd.SetText(s)
			b.bild()
			v.speichern.Click()
			b.bild()
			time.Sleep(30 * time.Millisecond)
			if !strings.Contains(v.fehler, "Größengrenze") || len(b.q.liste()) != 0 {
				t.Fatalf("Größe %q: Fehler %q, Aktionen %q", s, v.fehler, b.q.liste())
			}
		}
	})
}

// Die Kurzbeschreibung einer Welt nennt den Chat-Verlauf nur, wenn er vom
// Standard abweicht (so sehen ihn auch Mitglieder auf dem Reiter „Start“).
func TestWeltEigenschaftenChat(t *testing.T) {
	for _, c := range []struct {
		w    api.GroupWorld
		want string
	}{
		{api.GroupWorld{}, ""},
		{api.GroupWorld{ChatMiB: 5}, ""},
		{api.GroupWorld{ChatOff: true}, "Chat-Verlauf aus"},
		{api.GroupWorld{ChatMiB: 20}, "Chat-Verlauf bis 20 MiB"},
		{api.GroupWorld{Gamemode: "creative", ChatOff: true, ChatMiB: 20}, "Spielmodus Kreativ · Chat-Verlauf aus"},
	} {
		if got := weltEigenschaften(c.w); got != c.want {
			t.Errorf("%+v: %q, erwartet %q", c.w, got, c.want)
		}
	}
}
