package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ncruces/zenity"

	"github.com/Bazinga0o0/wanderwelt/internal/api"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/client"
	"github.com/Bazinga0o0/wanderwelt/internal/starter/deps"
)

// launcherAccountFiles: Konto-Dateien des Minecraft-Launchers (Store-Fassung
// zuerst).
var launcherAccountFiles = []string{"launcher_accounts_microsoft_store.json", "launcher_accounts.json"}

// launcherAccounts enthält absichtlich nur Namen und Kennung: Token-Felder
// der Datei werden nie eingelesen.
type launcherAccounts struct {
	Active   string                     `json:"activeAccountLocalId"`
	Accounts map[string]launcherAccount `json:"accounts"`
}

type launcherAccount struct {
	Profile struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"minecraftProfile"`
}

// detectMinecraftName liest aus den Launcher-Dateien in mcDir den Namen des
// aktiven Kontos (sonst des einzigen Kontos mit Profil).
func detectMinecraftName(mcDir string) (name, uuid string, ok bool) {
	for _, f := range launcherAccountFiles {
		b, err := os.ReadFile(filepath.Join(mcDir, f))
		if err != nil {
			continue
		}
		var la launcherAccounts
		if json.Unmarshal(b, &la) != nil {
			continue
		}
		if acc, found := la.Accounts[la.Active]; found && acc.Profile.Name != "" {
			return acc.Profile.Name, acc.Profile.ID, true
		}
		var only *launcherAccount
		n := 0
		for _, acc := range la.Accounts {
			if acc.Profile.Name != "" {
				acc := acc
				only = &acc
				n++
			}
		}
		if n == 1 {
			return only.Profile.Name, only.Profile.ID, true
		}
	}
	return "", "", false
}

var mcNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

const nameTries = 3

// nameConfirm und nameEntry sind die Dialoge der Namensabfrage; im Test
// austauschbar.
var nameConfirm = func(text string) bool { return confirm(text, "Ja", "Anderer Name") }

var nameEntry = func(text string) (string, bool) {
	s, err := zenity.Entry(text, zenity.Title(notifyTitle), windowIcon())
	return strings.TrimSpace(s), err == nil
}

// askMinecraftName fragt nach dem Minecraft-Namen, wenn der Koordinator noch
// keinen hat. Läuft im Hintergrund und blockiert nichts.
func (a *App) askMinecraftName() {
	if a.nameAsked.Swap(false) {
		return // die Einrichtung hat eben gefragt
	}
	c := a.client()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()
	if me := a.askNameFor(ctx, c); me != nil {
		a.mu.Lock()
		if a.c == c {
			a.me = me
		}
		a.mu.Unlock()
		a.refresh()
	}
}

// askNameFor stellt die Namensfrage für den Client c und liefert danach das
// aktuelle Mitglied, nil wenn nichts mehr abzufragen war oder es scheiterte.
func (a *App) askNameFor(ctx context.Context, c *client.Client) *api.Me {
	me, err := retry401(ctx, func() (*api.Me, error) { return c.Me(ctx) })
	if err != nil {
		log.Printf("Minecraft-Name: Me: %v", err)
		return nil
	}
	if me.MinecraftName != "" {
		return nil
	}
	// done: abgeschlossen (gesetzt, schon gesetzt oder Mojang nicht da).
	submit := func(name string) (done bool, msg string) {
		err := c.SetMyMinecraft(ctx, name)
		switch {
		case err == nil:
			log.Printf("Minecraft-Name gemeldet: %s", name)
			return true, ""
		case errors.Is(err, client.ErrNameAlreadySet):
			return true, ""
		}
		if se, ok := errors.AsType[*client.StatusError](err); ok && se.StatusCode == 400 {
			return false, se.Message
		}
		log.Printf("Minecraft-Name: %v", err)
		showInfo("Mojang gerade nicht erreichbar – wanderwelt fragt beim nächsten Start noch einmal.")
		return true, ""
	}
	finished := false
	if name, _, ok := detectMinecraftName(deps.MinecraftDir()); ok && mcNameRE.MatchString(name) {
		if nameConfirm(fmt.Sprintf("Du spielst Minecraft als „%s“ – stimmt das?", name)) {
			var msg string
			if finished, msg = submit(name); !finished {
				showInfo(msg)
			}
		}
	}
	prompt := "Dein Minecraft-Java-Name:"
	for i := 0; !finished && i < nameTries; i++ {
		name, ok := nameEntry(prompt)
		if !ok {
			return nil
		}
		if !mcNameRE.MatchString(name) {
			prompt = "Der Name besteht aus 3 bis 16 Buchstaben, Ziffern oder Unterstrichen.\n\nDein Minecraft-Java-Name:"
			continue
		}
		var msg string
		if finished, msg = submit(name); !finished {
			prompt = msg + "\n\nDein Minecraft-Java-Name:"
		}
	}
	if !finished {
		showInfo("Der Minecraft-Name ließ sich nicht eintragen. Frag den Admin; wanderwelt fragt beim nächsten Start noch einmal.")
		return nil
	}
	if me, err := c.Me(ctx); err == nil {
		return me
	}
	return nil
}
