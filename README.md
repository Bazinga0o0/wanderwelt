# wanderwelt

Eine Freundesgruppe teilt sich **eine** Minecraft-Java-Welt und hostet sie
abwechselnd. Wer spielen will, startet `wanderwelt.exe` (Windows); der
Koordinator auf dem Mini-Server (Linux, hinter tunneld) vergibt den
Hosting-Zuschlag, hält die Stände der Welt und das Versions-Manifest.

- `cmd/wanderwelt` – Starter für Windows (Tray, Installation, Host)
- `cmd/wanderwelt-koordinator` – Koordinator (Zuschlag, Wahl, Speicher)
- `internal/api` – JSON-Typen zwischen Starter und Koordinator
- `deploy/` – Beispiel-Konfiguration, Beispiel-Manifest, Beispiel-Gruppendatei

Bauen und testen (Go 1.27.4):

```
make build   # bin/wanderwelt-koordinator, bin/wanderwelt.exe
make test
```

## Für Mitspieler

wanderwelt hält eine gemeinsame Minecraft-Welt für die Gruppe bereit. Wer
spielen will, klickt auf „Spielen“: Hostet gerade jemand, verbindest du
dich mit ihm; sonst wird dein PC der Host, und die anderen verbinden sich
mit dir.

Du brauchst:

- Windows 10 oder 11 (64 Bit),
- Minecraft Java Edition mit dem offiziellen Minecraft-Launcher,
- deine Gruppendatei vom Admin (etwa gruppe-anna.yaml). Sie ist dein
  Zugang, gib sie nicht weiter. (Hat dir der Admin eine wanderwelt.exe
  für deine Gruppe gegeben, brauchst du keine Gruppendatei: Die
  Einrichtung führt dich zur Webseite, wo du ein Konto anlegst und einen
  Token erstellst, den du dann einfügst. Danach stellt sie eine
  Beitrittsanfrage und wartet, bis ein Admin dich annimmt.)

Java und den Minecraft-Server lädt wanderwelt selbst.

Einrichten:

1. wanderwelt.exe aus dem zip auspacken (etwa nach Downloads) und
   doppelklicken. Warnt Windows mit „Der Computer wurde durch Windows
   geschützt“, auf „Weitere Informationen“ und dann „Trotzdem ausführen“
   klicken: Die Datei ist nicht signiert.
2. Die Gruppendatei wählen und der Minecraft-EULA zustimmen.
3. wanderwelt lädt, was es braucht (beim ersten Mal einige Minuten), legt
   im Minecraft-Launcher das Profil „wanderwelt“ an und auf dem Desktop die
   Verknüpfung „wanderwelt – Spielen“. Danach läuft es als Symbol im
   Infobereich der Taskleiste (unten rechts, eventuell hinter dem Pfeil)
   und startet mit Windows.

wanderwelt hat sich dabei nach %LOCALAPPDATA%\wanderwelt kopiert; die
ausgepackte wanderwelt.exe kannst du löschen.

Spielen:

- Desktop-Verknüpfung „wanderwelt – Spielen“ oder Rechtsklick auf das
  Symbol und „Spielen“. wanderwelt öffnet den Minecraft-Launcher und sagt
  in einem Hinweis, mit welcher Adresse du dich verbindest. Im Launcher das
  Profil „wanderwelt“ wählen. War der Launcher schon offen, ihn einmal neu
  starten, damit er das Profil sieht.
- Bist du der Host, läuft der Server auf deinem PC. „Spiel für mich
  beenden“ (Fenster oder Menü) beendet dein Spiel: Sind andere online, fragt
  wanderwelt, ob die Welt an einen Mitspieler-PC übergeben werden soll. Dann
  bleiben alle verbunden und wechseln nach kurzer Pause auf den neuen Host.
  Ist niemand sonst da, stoppt der Server, und der Stand wird hochgeladen; das
  dauert etwas. „Hosten ohne Weitergabe beenden“ stoppt den Server für alle,
  ohne Übergabe. Fährst du Windows herunter, während du hostest, hält
  wanderwelt Windows kurz auf, bis die Welt gesichert ist.
- Spielst du nur mit, heißt der Knopf „Nicht mehr mitspielen“. Minecraft
  schließt du selbst.
- „Beenden“ im Menü beendet wanderwelt ganz.

Neue Version: wanderwelt aktualisiert sich selbst, sobald der Admin eine
neue Version bereitstellt und dein PC gerade nicht hostet (Hinweis
„wanderwelt aktualisiert sich auf …“). Eine neue wanderwelt.exe vom Admin
kannst du auch einfach starten. Sie ersetzt die installierte Kopie; hostest
du gerade, beendet sie vorher das Hosten sauber.

Minecraft-Name: wanderwelt liest deinen Namen aus dem Minecraft-Launcher
(das aktive Konto) und fragt nach, ob er stimmt. Findet es keinen, fragt es
dich danach. Einmal eingetragen, ändert ihn nur der Admin.

### Das Fenster

Öffnen: Ein Klick auf das Symbol im Infobereich, oder Rechtsklick und
„wanderwelt öffnen“. Ist das Fenster schon offen, holt ein zweites Öffnen es
nach vorn. Läuft wanderwelt gerade nicht, zeigt das Fenster „wanderwelt läuft
nicht“ mit dem Knopf „Starten“. Schließt du das Fenster, belegt es keinen
Speicher mehr; wanderwelt läuft im Infobereich weiter.

- **Start**: wer gerade hostet und wer online ist, die Adresse zum Verbinden
  (Kopieren-Knopf), „Spielen“ bzw. „Spiel für mich beenden“, letzter und
  nächster Stand, Fortschritt laufender Vorgänge und Hinweise.
- **Welt**: die Stände der aktiven Welt und der archivierten Welten (die
  Archive sind eingeklappt): Zeit, Host, Größe, wer damals online war. Mit
  „Welt übernehmen“ machst du einen Einzelspieler-Speicherstand zur
  Gruppenwelt. Admins sehen bei jedem Stand „Zurückholen“ und das
  Speicherbudget („Budget ändern“).
- **Hosten**: Arbeitsspeicher und Sichtweite des Servers auf diesem PC, C2ME,
  Autostart mit Windows, Dauerbetrieb und „Darf das Hosten übernehmen“. Die
  Leistungswerte gelten ab dem nächsten Serverstart; „Darf das Hosten
  übernehmen“ gilt sofort, auch während du mitspielst.
- **Mitglieder** (nur Admins): alle Mitglieder mit Admin- und Op-Schalter und
  wer gerade online ist. Mitglieder anlegen oder entfernen macht der Admin auf
  dem Server, nicht im Fenster.
- **Log**: die letzten Zeilen des Logs, folgt automatisch; „Log-Ordner öffnen“
  zeigt den Ordner.

### Rollen

- **Mitglied**: spielen und hosten.
- **Op**: wird im Minecraft-Server als Operator eingetragen und darf Befehle
  wie `/gamemode` benutzen.
- **Admin**: alles oben Genannte plus Stände zurückholen, das Speicherbudget
  setzen und die Rollen der anderen ändern (Reiter Mitglieder).

Admin- und Op-Schalter wirken live: Ein laufender Server gleicht sich danach
an. Das letzte Admin-Mitglied kann sich das Admin-Recht nicht selbst nehmen.

### Stände zurückholen (nur Admins)

Ein Admin holt im Reiter Welt bei einem Stand „Zurückholen“. Vorher kommt
eine Rückfrage, was verloren geht (alles seit diesem Stand, auch Inventare
und Positionen). Zurückgeholt wird nichts gelöscht: Der Inhalt des Stands
wird als neuer, aktueller Stand angelegt. Ein erneutes Zurückholen macht das
rückgängig.

- Hostet gerade niemand, passiert es sofort.
- Hostet gerade jemand, kommt im Spiel die Ansage „… holt den Stand von
  <Zeit> zurück – Neustart in 30 Sekunden“. Danach stoppt der Server sauber,
  der Stand wird hochgeladen, und der Server startet mit dem zurückgeholten
  Stand neu. Ihr verbindet euch dann neu. Bis dahin kann niemand neu starten
  („Stand wird gerade zurückgeholt, gleich nochmal“).
- Auch Stände aus archivierten Welten lassen sich zurückholen. Dann wird
  diese Welt wieder die aktive, und die bisherige wird archiviert. So lässt
  sich auch ein versehentliches „Welt übernehmen“ rückgängig machen.
- Die Minecraft-Version des Stands muss zur aktuellen Version passen; sonst
  lehnt wanderwelt das Zurückholen ab.

Stände: Die Welt wird während des Spielens regelmäßig gesichert (standardmäßig
alle 5 Minuten). Die letzten Stände bleiben erhalten, dazu je Stunde und Tag
einer der älteren.

Probleme: Rechtsklick auf das Symbol, „Log öffnen“, und das Log dem Admin
schicken.

## Releases

Jeder Push auf einen Branch und jeder Pull Request läuft durch
`.github/workflows/ci.yml`: unter Linux `go vet`, alle Tests, `-race` für
den Koordinator und die Release-Ziele bauen (Starter für Windows,
Koordinator für linux/arm64); unter Windows `go vet` und die Tests des
Starters. Beide Jobs prüfen, dass Go genau die Toolchain aus `go.mod` ist.

Ein Release entsteht aus einem Tag `vMAJOR.MINOR.PATCH` (SemVer) auf einem
Commit, dessen CI grün ist:

```
git tag -a v0.5.0 -m "wanderwelt 0.5.0"
git push origin v0.5.0
```

Der Tag startet `.github/workflows/release.yml`; GoReleaser
(`.goreleaser.yaml`, fest v2.20.2) legt ein GitHub-Release an mit:

| Datei | Inhalt |
|---|---|
| `wanderwelt-<version>-windows-amd64.zip` | `wanderwelt.exe` (GUI-Programm, mit Symbol), `README.md` (der Abschnitt „Für Mitspieler“ oben), `LICENSE` und `NOTICE` |
| `wanderwelt-koordinator-<version>-linux-amd64.tar.gz`, `…-linux-arm64.tar.gz` | ein Ordner mit `wanderwelt-koordinator`, `wanderwelt-koordinator.service` und den `*.example.yaml` aus `deploy/` |
| `checksums.txt` | SHA-256 aller Archive |

`<version>` ist der Tag ohne `v` (Tag `v0.5.0` → `0.5.0`); dieselbe
Zeichenfolge zeigt `wanderwelt.exe version`. Ein Tag mit Zusatz (etwa
`v0.5.0-rc.1`) wird als Vorabversion markiert. Der Changelog lässt Commits
mit `docs:` und `test:` weg. Den Abschnitt „Für Mitspieler“ nicht
umbenennen: Ohne ihn scheitert das Release.

### Gruppen-Build

Ein Starter, der ohne Gruppendatei auskommt (Einrichtung mit Token-Eingabe
und Beitrittsanfrage), trägt die Gruppen-Adressen in der exe. Sie stehen
als Base64 eines YAML in `main.gruppeEingebettet` (leer im Repo; dann
verhält sich die Einrichtung wie bisher und fragt nach einer
Gruppendatei):

```
coordinator: https://wanderwelt.example.org
tunnel_server: tunnel.example.org:7443
web_url: https://tunnel.example.org      # Konto anlegen
# token_url: https://tunnel.example.org/token   # optional: Seite zum Token-Erstellen (sonst web_url)
```

Kein `token` hineinschreiben (ein solcher Schlüssel ist ein Fehler: die
exe gäbe ihn jedem). Bauen:

```
export WANDERWELT_GRUPPE=$(base64 -w0 gruppe-adressen.yaml)
goreleaser release --snapshot --clean   # oder im Release-Job mit dem Secret
```

`.goreleaser.yaml` setzt `-X main.gruppeEingebettet=…` nur, wenn
`WANDERWELT_GRUPPE` gesetzt ist. Der Mitspieler legt auf `web_url` ein
Konto an, der Admin schaltet es in tunneld frei, der Mitspieler erstellt
einen Token und fügt ihn bei der Einrichtung ein (verdeckte Eingabe).
Der Token landet nur in `%LOCALAPPDATA%\wanderwelt\gruppe.yaml` (Zugriff
nur für das eigene Windows-Konto), nie im Log. Gehört das Konto noch nicht
zur Gruppe, fragt die Einrichtung den Minecraft-Namen (erkannt aus dem
Launcher, sonst Eingabe), stellt die Beitrittsanfrage (`POST /v1/join`)
und wartet in einem Fenster, das alle 60 s nachfragt; angenommene
Mitglieder machen ohne Neustart weiter. „Später fortsetzen“ schließt das
Fenster; der nächste Start macht beim Warten weiter.

Ohne Tag gebaute Starter tragen keine Release-Version: `make build` setzt
die Commit-Kurzform (etwa `5963b1b`, mit `-dirty` bei offenen Änderungen),
ein `go build` ohne `-X` lässt `dev`.

Vor dem Taggen lokal prüfen, unter Windows aus Git Bash (der Hook für
`README.md` im zip braucht `sh` und `awk`):

```
go run github.com/goreleaser/goreleaser/v2@v2.20.2 check
go run github.com/goreleaser/goreleaser/v2@v2.20.2 release --snapshot --clean
```

Das zweite baut alles nach `dist/` (git-ignoriert), ohne etwas
hochzuladen. Mit make: `make release-check`, `make snapshot`.

## Starter `wanderwelt.exe`

Bauen ohne make (GUI-Programm, kein Konsolenfenster beim Tray):

```
GOOS=windows go build -ldflags "-H=windowsgui" -o bin/wanderwelt.exe ./cmd/wanderwelt
```

Das Symbol der exe (`cmd/wanderwelt/rsrc_windows_amd64.syso`, dazu
`wanderwelt.ico`) ist aus `internal/starter/symbol` erzeugt und
eingecheckt; nach Änderungen am Symbol `go run ./tools/makeicon`.

Aufrufe:

| Aufruf | Wirkung |
|---|---|
| `wanderwelt.exe` | nicht eingerichtet: Installation; sonst Tray (läuft schon einer: Hinweis, Ende – außer es ist eine neue Version, siehe „Starter aktualisieren“) |
| `wanderwelt.exe play` | „Spielen“ in der laufenden Instanz; läuft keine, startet sie und spielt (Desktop-Verknüpfung „wanderwelt – Spielen“) |
| `wanderwelt.exe import [-ja] [-ohne-spielerdatei] <weltordner>` | wie „Welt übernehmen …“ ohne Dialoge; ohne `-ja` nur Prüfung (Exit 3); läuft der Tray, übernimmt er die Welt (Übergabe über die Steuerung, Ergebnis im Tray) |
| `wanderwelt.exe fenster` | öffnet das Fenster (siehe „Fenster“); läuft keine Instanz, zeigt es „wanderwelt läuft nicht“ mit Knopf „Starten“ |
| `wanderwelt.exe version` | Version |

Die Unterbefehle hängen sich an die Konsole des Aufrufers. Ein GUI-Programm
wartet `cmd.exe` nicht ab; in PowerShell `Start-Process -Wait -NoNewWindow`
oder Git-Bash nehmen, wenn der Exit-Code zählt.

Ablage in `%LOCALAPPDATA%\wanderwelt` (bzw. `WANDERWELT_HOME`):
`wanderwelt.exe` (die installierte Kopie), `gruppe.yaml`, `state.json`,
`profil.yaml`, `control.json` (Port und Token der Steuerung, solange der Tray
läuft), `logs/wanderwelt.log` (ab 10 MB nach `wanderwelt.log.1`), `deps/`,
`server/`. Autostart: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\wanderwelt`.

Nur eine Instanz pro Datenordner (Mutex `Local\wanderwelt-<Hash>`); ein zweiter
Start schickt seinen Wunsch über `127.0.0.1:<Port>` mit dem Token aus
`control.json` an die laufende Instanz.

### Starter aktualisieren

Release-Versionen aktualisieren sich selbst über den Koordinator
(Selbst-Update, `internal/starter/app/selfupdate.go`; ausrollen:
„Starter-Update ausrollen“ unter Koordinator):

- Beim Start und dann alle 6 Stunden holt der Starter das Manifest. Nennt es
  unter `starter` eine größere Version als die eigene, lädt er die exe über
  den angemeldeten Koordinator-Client nach
  `%LOCALAPPDATA%\wanderwelt\update\wanderwelt-<version>.exe` und prüft
  SHA-256 und Größe.
- Dann wartet er, bis dieser PC weder hostet noch mitspielt und nichts
  läuft (kein „Spielen“, kein Nachreichen, kein „Welt übernehmen“), prüft
  den Hash noch einmal und startet die neue exe. Sie nimmt den Weg unten
  (Aktion `update`, Kopie nach `wanderwelt.exe`, Hinweis „wanderwelt wurde
  aktualisiert“). Einmal pro Version kommt vorher der Hinweis „wanderwelt
  aktualisiert sich auf <version> …“.
- Nur Release-Versionen (`MAJOR.MINOR.PATCH` aus einem Tag) nehmen teil:
  `dev`, Commit-Kurzformen, Snapshots (`0.0.0-SNAPSHOT-…`) und
  Vorabversionen aktualisieren sich nie von selbst (eine Zeile im Log).
- Fehler (Datei fehlt, Hash oder Größe passt nicht, die neue Version
  übernimmt nicht): wanderwelt läuft mit der alten Version weiter, Zeile im
  Log, höchstens ein Hinweis pro Tag. Denselben Stand (SHA-256) versucht ein
  Starter höchstens einmal am Tag (`update\zustand.json`): nach einer
  angestoßenen Übergabe, einer fehlenden Datei (4xx) oder falschem Hash bzw.
  falscher Größe; nach einem Netzfehler lädt die nächste Prüfung wieder. So
  entsteht auch kein Kreislauf, wenn die neue exe eine andere Version meldet
  als das Manifest.
- Im Dauerbetrieb endet das Hosten nicht von selbst; ein solcher PC
  aktualisiert sich erst, wenn er nicht mehr hostet (etwa nach „Spiel für
  mich beenden“).

Von Hand: Der Admin verteilt die neue `wanderwelt.exe` (vorher den
Koordinator aktualisieren). Jeder startet sie einfach, egal von wo (etwa
aus Downloads):

- Läuft wanderwelt nicht, ersetzt die neue Datei die installierte Kopie in
  `%LOCALAPPDATA%\wanderwelt\wanderwelt.exe` und startet von dort.
- Läuft wanderwelt schon und hat die neue Datei andere Bytes als die
  installierte Kopie, bittet sie die laufende Instanz zu enden (Aktion
  `update`). Hostet dieser PC gerade, beendet die laufende Instanz das Hosten
  vorher sauber – Server stoppen, Schluss-Stand hochladen, Zuschlag
  freigeben; das kann einige Minuten dauern und stoppt den Server für alle.
  Danach ersetzt die neue Version die installierte Kopie (atomar, mit
  Wiederholen, solange die alte Datei noch in Benutzung ist) und startet von
  dort. Ein Hinweis meldet „wanderwelt wurde aktualisiert“.
- Startet man dieselbe Datei noch einmal, passiert nichts Neues (nur der
  Hinweis, dass wanderwelt läuft).
- Versionen ohne die Aktion `update` (vor dieser Änderung) lassen sich so
  nicht ersetzen: Die neue Version meldet das; dann wanderwelt über das
  Tray-Menü „Beenden“ und die neue Datei noch einmal starten.

Verglichen werden nur die Bytes: Auch eine ältere Datei würde so
installiert.

Lokal testen, ohne das eigene Profil anzufassen:

- `WANDERWELT_HOME=C:\temp\ww-a` – eigener Datenordner (mehrere Starter
  nebeneinander mit verschiedenen Ordnern).
- `WANDERWELT_MINECRAFT_DIR=C:\temp\ww-mc` – statt `%APPDATA%\.minecraft`:
  Launcher-Profil und Speicherstände für „Welt übernehmen“ kommen von dort.
- Autostart-Eintrag und Desktop-Verknüpfung entstehen trotzdem; danach von
  Hand entfernen.

### Herunterfahren abfangen

Hostet der PC, wenn Windows herunterfährt oder sich abmeldet, antwortet ein
verstecktes Fenster auf `WM_QUERYENDSESSION` mit FALSE und setzt
`ShutdownBlockReasonCreate("wanderwelt speichert gerade die Welt …")`. Der
Starter beendet das Hosten sauber (Server stoppen, Schluss-Stand hochladen,
Zuschlag zurückgeben) und nimmt den Grund danach wieder weg, spätestens nach
90 Sekunden. Hostet er nicht, hält er nichts auf.

Stand der Prüfung (2026-11-23): Nur synthetisch geprüft, mit
`wanderwelt.exe winsys-check` und mit Nachrichten von außen an das Fenster
des laufenden Trays. Ergebnis: Antwort FALSE mit gesetztem Grund, ein zweites
`WM_QUERYENDSESSION` während des Aufhaltens wird ebenfalls abgelehnt, nach dem
Ende ist der Grund weg, ohne Hosten kommt TRUE.

**Offen:** Was Windows nach dem Freigeben tut, ist noch nicht an einem echten
Herunterfahren beobachtet. Laut Dokumentation zeigt Windows während des
Aufhaltens den Bildschirm „Diese App verhindert das Herunterfahren“ mit dem
Grund und den Knöpfen „Trotzdem herunterfahren“ und „Abbrechen“. Ob Windows
nach dem Wegnehmen des Grunds von selbst weiterfährt oder ob man noch einmal
auf „Herunterfahren“ klicken muss, wird beim ersten echten Test hier
eingetragen. Im Dauerbetrieb startet der Starter nach dem Aufhalten nichts
mehr von selbst, bis ein abgebrochenes Herunterfahren gemeldet wird
(`WM_ENDSESSION(FALSE)`).

Eigenheit von `fyne.io/systray`: Bei jedem `WM_ENDSESSION`, auch wenn das
Herunterfahren abgebrochen wurde, entfernt die Bibliothek das Tray-Symbol.
Der Starter holt es danach zurück (Nachricht „TaskbarCreated“ an das
Tray-Fenster). Geprüft mit einem synthetischen `WM_ENDSESSION(FALSE)`.

### Dauerbetrieb

Ein PC, der gern immer hostet (etwa ein Desktop, der ohnehin läuft), bekommt
im Tray den Haken „Dauerbetrieb (Server auch ohne Spieler)“; gespeichert wird
er als `dauerbetrieb: true` in `profil.yaml` (von Hand geändert, gilt er nach
spätestens einer Minute). Standard ist aus. Dann:

- hostet der PC von selbst, sobald niemand hostet: gleich nach dem Start des
  Starters und danach jede Minute, auf demselben Weg wie „Spielen“ (nur ohne
  Launcher). Hostet schon ein anderer PC oder läuft gerade eine Aktion,
  passiert nichts, auch kein Hinweis. Klickt jemand „Spielen“, während das
  automatische Hosten läuft, wird es zu gewöhnlichem Spielen (mit Launcher).
- endet seine Sitzung nicht mangels Spielern (gilt auch, wenn er über
  „Spielen“ Host wurde). Ein Zwischenstand kommt nur, wenn seit dem letzten
  Stand jemand online war, sonst steht im Log
  `host: Dauerbetrieb – seit dem letzten Stand war niemand online, Zwischenstand übersprungen`;
  so verdrängen leere Stände nicht die aufbewahrten.
- endet sie wie sonst beim Beenden des Starters, Abmelden/Herunterfahren,
  Fencing oder „Spiel für mich beenden“. „Spiel für mich beenden“ pausiert
  den Dauerbetrieb bis zum nächsten „Spielen“ oder Neustart des Starters.
  Fährt Windows herunter, während das automatische Hosten gerade startet,
  hält der Starter das Herunterfahren auf, bricht ab bzw. beendet sauber.
- Scheitert das Hosten wiederholt, wartet der Dauerbetrieb nach jedem
  Fehlschlag länger (1, 2, 4, 8, dann 15 Minuten) und zeigt je Fehlerserie nur
  einen Hinweis; sonst Log und Tray. Läuft ein Server wieder, beginnt es von
  vorn.

### Fenster

Das Fenster ist ein eigener Prozess derselben exe, den das Tray-Menü
(„wanderwelt öffnen“, Klick aufs Symbol) oder `wanderwelt.exe fenster`
startet. Es spricht über die lokale Steuerung mit der laufenden Instanz
(127.0.0.1, Token aus `control.json`) und hat keine eigene Logik fürs Hosten:
Aktionen lösen dieselben Wege aus wie das Tray-Menü. Schließt man es, gibt es
allen Speicher frei; ein Absturz des Fensters hat keinen Einfluss auf Server,
Stände oder Zuschlag. Die Reiter (Start, Welt, Hosten, Mitglieder, Log) sind
im Abschnitt „Für Mitspieler“ beschrieben.

### Hosten übergeben

Beendet der Host sein Spiel („Spiel für mich beenden“) und sind andere
Spieler online, fragt der Starter, ob die Welt an den besten Mitspieler-PC
übergeben werden soll; die Spieler wechseln nach kurzer Pause auf den neuen
Host. Dafür friert der Server die Welt ein (`tick freeze`), schaltet das
Speichern ab, merkt sich die Spielmodi und speichert alles (`save-all flush`).
Nach einer kurzen Ruhezeit (die Dateien der Welt sind eine Sekunde lang
unverändert) wird der Stand bereitgestellt; ändert sich dabei eine Datei,
wird erneut gespeichert und bis zu dreimal von vorn versucht. Erst danach
werden alle Spieler Zuschauer (`gamemode spectator`), so dass nichts Neues
entsteht und der Stand nie Zuschauer-Daten enthält. Wer während der Übergabe
dazukommt, wird ebenfalls Zuschauer. Der neue Host hebt das Einfrieren nach
seinem Start auf. Scheitert die Übergabe, bevor der Stand eingereicht ist,
bekommen die Spieler ihre Modi zurück, das Einfrieren endet, der Koordinator
erfährt vom Abbruch, und es geht normal weiter: der Server stoppt nicht, der
Host darf „Spiel für mich beenden“ erneut versuchen. Die Vorbereitung hat eine Gesamtfrist von 4 Minuten; danach (oder wenn zu wenig Zeit bis zur 5-Minuten-Frist des Koordinators bleibt) wird sie abgebrochen, bevor jemand Zuschauer wird. Die Spieler werden gleich nach dem Einfrieren gewarnt, bis zum Zuschauer-Setzen entstandene Änderungen können verloren gehen. Scheitert sie erst nach
dem Einreichen des Standes, stoppt der Server wie sonst (ohne neuen Stand,
alle raus). Ist vor dem Einfrieren kein Kandidat da, endet es gleich wie
früher. „Hosten ohne Weitergabe
beenden“ (Tray-Menü und Start-Reiter, nur als Host) stoppt den Server wie
früher für alle. Ein PC meldet dem Koordinator alle 5 s, dass er übernehmen
kann, solange er mitspielt oder im Dauerbetrieb untätig ist; Standard ist an.
Abgeschaltet wird das im Reiter Hosten mit „Darf das Hosten übernehmen“
(`keine_uebernahme: true` in `profil.yaml`). Die Übergabe braucht einen
Koordinator mit `handover_ports`; teilnehmen können nur PCs mit neuer exe.
Ältere Starter melden keine Bereitschaft und kommen nicht als Kandidat in
Frage.

Der Dauerbetrieb wirkt nur am Netz. Läuft ein Notebook auf Akku, hostet es
wie ohne Dauerbetrieb und startet nichts von selbst (Log `Dauerbetrieb ruht:
…` beim Wechsel). Geht der Dauerbetrieb während einer Sitzung aus (Haken weg
oder Akku), gilt wieder die Leerlauf-Frist, gezählt ab dem letzten Spieler
oder ab dem Abschalten (bemerkt bei der nächsten Spielerabfrage, alle 15 s),
je nachdem, was später war: Ein kurz abgezogenes Notebook beendet die
Sitzung nicht.

`dauerbetrieb` steht nur in `profil.yaml` und in der Profilmeldung an den
Koordinator, wenn es an ist; ältere Koordinatoren und Starter stören sich
also nur an einem eingeschalteten Dauerbetrieb.

### Notfallkopie

Nach jeder eigenen Host-Sitzung legt der Starter den zuletzt eingereichten
Stand als normalen Weltordner nach `<Dokumente>\wanderwelt-Notfallkopie\`
(Windows-Ordner „Dokumente“, auch wenn er umgeleitet ist), falls der
Koordinator ausfällt. Es gibt genau eine Kopie; die nächste ersetzt sie: Sie
entsteht erst als `wanderwelt-Notfallkopie.neu` daneben und wird dann
umbenannt, die alte wird erst danach gelöscht. Scheitert das Kopieren, bleibt
die alte Kopie unverändert (Log, einmal je Start ein Hinweis).

Kopiert wird, was die Sitzung zuletzt eingereicht hat: der Schluss-Stand, nach
einer Übergabe der Übergabe-Stand (nicht der lokale Weltordner, der danach
Spectator-Spielerdaten hat). Das läuft im Hintergrund nach dem Ende der
Sitzung; Zuschlag-Rückgabe und Übergabe warten nicht darauf. Eine
`LIESMICH.txt` im Ordner nennt Welt-ID, Generation, Zeit und Minecraft-Version
und erklärt die Nutzung: den Ordner nach `.minecraft\saves` kopieren
(Einzelspieler) oder in einen neuen Koordinator mit „Welt übernehmen“ laden.

Der Haken „Notfallkopie behalten“ (Reiter Hosten, Standard an) gilt pro PC und
steht lokal in `notfallkopie.json` im Datenverzeichnis, nicht beim
Koordinator. Darunter stehen Datum und Größe der Kopie und der Knopf „Ordner
öffnen“. Ohne Haken entsteht keine neue Kopie; eine vorhandene bleibt liegen.
Für Tests und Entwicklung überschreibt `WANDERWELT_DOKUMENTE` den
Dokumente-Ordner.

Liegt „Dokumente“ unter OneDrive, legt der Starter die Kopie stattdessen in
`%USERPROFILE%\wanderwelt-Notfallkopie` (die Weltdaten sollen nicht in die
Cloud); der Reiter zeigt immer den tatsächlichen Pfad. Vorher prüft er den
freien Platz (Größe plus 10 %), bei zu wenig gibt es einen Hinweis.

Die Kopie läuft mit Frist (10 Minuten) und bricht ab, wenn eine neue Sitzung,
das Nachreichen oder „Welt übernehmen“ den Platz für Welt-Dateien braucht; die
alte Kopie bleibt dann unverändert. Beim Beenden von wanderwelt wartet der
Starter höchstens 2 Minuten auf eine laufende Kopie (beim Herunterfahren nur
so lange, wie Windows das Herunterfahren ohnehin aufhält). Scheitert der
Schluss-Upload (etwa weil der Koordinator ausfällt), kopiert der Starter den
bereitgestellten, nur lokalen Stand trotzdem – `LIESMICH.txt` vermerkt dann
„NICHT hochgeladen (eventuell nur hier vorhanden)“. Auch nach einem nachgereichten
Stand entsteht eine Kopie.

## Koordinator

Einrichtung: Kommentare in `deploy/wanderwelt-koordinator.service` und
`deploy/koordinator.example.yaml`. Den Koordinator immer vor den Startern
aktualisieren (er liest JSON streng; neue Felder der Starter muss er kennen).

### Koordinator aktualisieren

Das Programm immer vor der Konfiguration: Ein älteres Programm liest die
Konfiguration streng und startet mit neuen Schlüsseln (etwa
`snapshot_interval`, `empty_timeout`, `provisioning`) nicht mehr – auch nicht bei einem
Neustart durch systemd. Auf dem Mini-Server als root:

```
install -m 0755 wanderwelt-koordinator /opt/wanderwelt/
cp wanderwelt-koordinator.service /etc/systemd/system/
systemctl daemon-reload
systemctl restart wanderwelt-koordinator
```

Erst danach neue Schlüssel in `/etc/wanderwelt/koordinator.yaml` eintragen
und noch einmal `systemctl restart wanderwelt-koordinator` (`reload` liest
nur die Mitglieder und das Versions-Manifest). Nach dem Aktualisieren
`restart`, nicht `reload`: Ein
laufendes älteres Programm fängt SIGHUP vielleicht nicht ab und wird davon
beendet. Die neue Unit startet es dann zwar neu
(`RestartForceExitStatus=SIGHUP`), aber `systemctl reload` meldet Erfolg,
obwohl nichts neu geladen wurde.

Ein ausstehendes Zurückholen (ein Admin hat einen Stand zurückgeholt,
während jemand hostet) steht nur im Speicher und übersteht einen Neustart
des Koordinators nicht; danach den Stand im Fenster noch einmal zurückholen.

### Host-Übergabe

Drückt der Host „Spiel für mich beenden“, während andere spielen, kann die
Welt ohne Rauswurf an einen Mitspieler-PC wandern. Der Koordinator hilft
dabei so:

- Starter melden alle 5 s ihre Bereitschaft (`POST /v1/presence`). Kandidat
  ist ein PC mit Bereitschaft von höchstens 15 s, der nicht der PC des Hosts
  ist, dessen Profil die Übernahme nicht verbietet (`keine_uebernahme`) und
  dessen Minecraft-Name gerade auf dem Server ist – oder der untätig im
  Dauerbetrieb läuft. Den besten wählt der Koordinator wie bei der Host-Wahl.
- `POST /v1/handover/start` (nur der Host) beginnt die Übergabe
  („vorbereitung“); mit `POST /v1/handover/ready` meldet der alte Host den
  hochgeladenen Stand. Der Kandidat bekommt einen zweiten, wartenden Zuschlag
  und ein Angebot mit einem Ersatz-Port („start“); ab dann reicht der alte
  Zuschlag nichts mehr ein (409). Meldet der neue Host ready, sieht der alte
  Host „bereit“ mit der Adresse Host:Ersatz-Port und schickt die Spieler
  hinüber. Gibt der alte Host den Zuschlag zurück (oder läuft er ab), wird der
  wartende befördert: Er bekommt den Haupt-Port (`main_port` im Lebenszeichen)
  und behält den Ersatz-Port bis zum Ende seines Zuschlags; die nächste
  Übergabe nimmt den anderen.
- Scheitert die Übergabe (5 min ohne ready nach dem Start, 5 min ohne
  „bereit“ nach dem Angebot, der wartende Zuschlag verfällt oder wird
  zurückgegeben), wird der wartende Zuschlag verworfen, der Ersatz-Port
  geräumt, und der alte Host sieht 2 min lang „gescheitert“ mit Grund; er darf
  dann wieder einreichen. Bricht der alte Host die Übergabe vor dem
  Einreichen selbst ab (`POST /v1/handover/abort` mit `handover_id`,
  `lease_id`, `machine_id`, 204; nur der Host), räumt der Koordinator wie bei
  „gescheitert“, vergisst die Übergabe aber gleich, so dass eine neue
  beginnen darf. Während einer Übergabe wird ein Zurückholen mit 409
  abgelehnt, und umgekehrt beginnt keine Übergabe, solange eins aussteht.

Konfiguration: `port_holder.handover_ports` mit genau zwei Ersatz-Ports
(verschieden, ungleich `port_holder.port`), die auch in tunneld freigegeben
sein müssen:

```
port_holder:
  port: 15565
  handover_ports: [15566, 15567]
```

Ohne `handover_ports` gibt es keine Übergabe (409, der Starter endet wie
bisher). Die Übergabe und der Ersatz-Port des beförderten Hosts stehen in
`handover.json` im Datenordner und überstehen einen Neustart des
Koordinators; die Fristen (5 min, 5 min) zählen danach neu. Der erste Takt
räumt nur Ersatz-Ports, die keinem gültigen Zuschlag und keiner laufenden
Übergabe gehören.

Hinweise: Gibt der alte Host ab, nachdem er `ready` gemeldet hat, wird der
wartende Zuschlag befördert, auch wenn der neue Host noch nicht „bereit“
gemeldet hat; der Übergabe-Stand ist dann der Schluss-Stand. Startet der PC
des Kandidaten den Starter neu und fragt zu spielen, gibt der Koordinator
dessen wartenden Zuschlag frei, und die Übergabe scheitert sofort.

### Stand-Abstand

`snapshot_interval` (Go-Dauer, z. B. `5m`; erlaubt 1m–30m, Standard 5m) legt
fest, wie oft jeder Host einen Zwischenstand einreicht. Der Koordinator gibt
den Wert mit den Host-Einstellungen aus (`snapshot_interval_s`); er gilt ab
der nächsten Host-Sitzung nach einem Neustart des Koordinators. Ein Starter,
der den Wert nicht kennt oder einen außerhalb von 1–30 Minuten bekommt,
nimmt 5 Minuten.

Für die Wahl des Abstands schreibt der Host je eingereichtem Stand eine
Zeile in sein Log (`logs/wanderwelt.log`):

```
host: Stand 7: speichern 1,2 s, bereitstellen 3,4 s, hashen 0,5 s, hochgeladen 12,5 MB in 2,1 s, gesamt 7,3 s
```

- `speichern`: save-off und save-all flush (beim Schluss-Stand `stoppen`:
  Dauer von Stop, dabei speichert der Server),
- `bereitstellen`: Kopie in den Bereitstellungsordner (so lange ist das
  automatische Speichern aus),
- `hashen`: Manifest bilden, geänderte Regionsdateien zerlegen,
- `hochgeladen … in …`: nur die Blobs, die dem Koordinator fehlten (nicht die
  Größe der Welt), samt Einreichen,
- `gesamt`: vom Beginn des Stands (beim Schluss-Stand: vom Beginn von Stop)
  bis zum Einreichen.

### Leerlauf-Frist

`empty_timeout` (Go-Dauer, z. B. `10m`; erlaubt 1m–60m, Standard 10m) legt
fest, wie lange ein Host ohne Spieler weiterläuft, bevor er seine Sitzung
sauber beendet. Gemessen wird ab der ersten Abfrage, die niemanden mehr zeigt;
wer vorher wiederkommt, setzt sie zurück. Der Koordinator gibt den Wert mit
den Host-Einstellungen aus (`empty_timeout_s`); er gilt ab der nächsten
Host-Sitzung nach einem Neustart des Koordinators. Ein Starter, der einen
Wert außerhalb von 1–60 Minuten bekommt oder einen älteren Koordinator
fragt, nimmt 10 Minuten; Starter vor dieser Änderung bleiben bei 2 Minuten.
Im Dauerbetrieb (siehe dort) endet eine Sitzung mangels Spielern nie.

### Aufbewahrung der Stände

Aufgeräumt wird jede Stunde und nach jeder Änderung des Speicherbudgets. Für
die aktive Welt bleiben:

- die letzten 24 Stände, immer (bei 5 Minuten Abstand etwa zwei Stunden),
- für jede der letzten 48 UTC-Stunden der letzte Stand dieser Stunde,
- für jeden der letzten 30 UTC-Tage der letzte Stand dieses Tages.

Archivierte Welten behalten alle Stände. Überschreiten die behaltenen Stände
das Speicherbudget (Standard `storage_budget`, 15 GiB; Überlagerung im
Fenster), fallen zuerst die ältesten Stunden- und Tagesstände der aktiven
Welt, danach die ältesten Stände der Archive – je Archiv bleibt der neueste.
Die letzten 24 der aktiven Welt fallen nie; gepinnte Stände (solange ein
Zurückholen aussteht) auch nicht. Bleibt der Speicher trotzdem über dem
Budget, meldet das Fenster „über dem Budget“. Angefangene Uploads, an denen
24 Stunden lang nichts mehr geschrieben wurde, räumt der Koordinator weg.

Budget ändern: Fenster, Reiter Welt (nur Admins), mindestens 1 GiB und
höchstens belegt plus frei auf dem Server.

### Stände zurückholen

Nur Admins, im Fenster (Reiter Welt, „Zurückholen“) oder über `POST
/v1/restore`, auch wenn gerade jemand hostet. Der Koordinator legt den
Inhalt des gewählten Stands als **neuen** Stand an (Elternteil ist der bisher
aktuelle Stand der Welt, Vermerk `restored_from`); nichts wird gelöscht, ein
erneutes Zurückholen macht es rückgängig.

- Hostet niemand, wirkt es sofort.
- Hostet jemand, antwortet der Koordinator mit `pending`. Der Host bekommt die
  Ansage („… holt den Stand von <Zeit> zurück – Neustart in 30 Sekunden“) mit
  der Antwort auf sein nächstes Lebenszeichen, sagt sie im Chat an, stoppt
  nach 30 s sauber, reicht seinen Schluss-Stand ein und gibt den Zuschlag
  zurück. Danach wird der zurückgeholte Stand angelegt, und der nächste Host
  startet damit. Hat der Host nach 90 Sekunden (30 s Ansage plus 60 s Puffer)
  seinen Server noch nicht gestoppt, entzieht der Koordinator den Zuschlag.
  Stürzt der Host ab oder hört er auf, wird das Zurückholen angewandt, sobald
  sein Zuschlag abläuft.
- Während ein Zurückholen aussteht, lehnt der Koordinator Spielanfragen mit
  „Stand wird gerade zurückgeholt, gleich nochmal“ ab und ein zweites
  Zurückholen mit 409. Auch während einer Host-Übergabe gibt es 409.
- Stände aus einem Archiv: Der neue Stand macht deren Welt wieder zur aktiven;
  die bisher aktive wird Archiv.
- Die Minecraft-Version des Stands muss zum Manifest passen (sonst 409). Ein
  unbekannter Stand ergibt 404.
- Ein ausstehendes Zurückholen steht nur im Speicher; siehe „Koordinator
  aktualisieren“.

### Mitglieder hinzufügen

Auf dem Mini-Server als root, nachdem `provisioning:` in der Konfiguration
steht (Vorlage in `deploy/koordinator.example.yaml`; Pflicht sind
`tunneld_config`, `coordinator_url` und `tunnel_server`):

```
wanderwelt-koordinator mitglied neu --name anna [--minecraft Anna_42] [--email anna@example.org] [--admin] [--op] [--aus /root]
wanderwelt-koordinator mitglied entfernen Anna_42 [--konto-sperren] [--auch-fremdes-konto]
wanderwelt-koordinator mitglied liste
```

`--minecraft` darf fehlen: Dann meldet das Mitglied seinen Minecraft-Namen
selbst, wenn es den Starter einrichtet (siehe „Rollen, Minecraft-Namen und
Speicherbudget“); bis dahin fehlt es in der Whitelist.

`mitglied neu` und `mitglied entfernen` führen `reload_command` (als root)
und `tunneld_bin` aus der Konfiguration aus. Darum verlangen sie, dass die
Konfiguration root gehört und weder für die Gruppe noch für andere
beschreibbar ist (`chown root:wanderwelt`, `chmod 0640`), und brechen sonst
vor jeder Änderung ab.

`mitglied neu` prüft Name (`a-z`, `0-9`, `-`, höchstens 32 Zeichen) und
Minecraft-Namen und lehnt ein vorhandenes Mitglied ab (E-Mail oder
Minecraft-Name, Groß/Klein egal; auch ein im Fenster oder vom Starter
gemeldeter Name zählt). Ohne `--email` heißt das tunnel-Konto
`<name>@wanderwelt.invalid`. Dann über die tunneld-Admin-CLI (`runuser -u
tunneld -- /opt/tunneld/tunneld admin …`): ein vorhandenes Konto wird
verwendet und freigeschaltet, sonst wird es mit zufälligem Passwort angelegt
und als bestätigt markiert; danach der persönliche Token `wanderwelt`. Es
entstehen (0600, nie überschrieben):

- `gruppe-<name>.yaml` – die Gruppendatei für den Starter, mit dem Token;
- `zugang-<name>.txt` – nur bei neuem Konto: Web-Anmeldung
  (`provisioning.web_url`), E-Mail und Passwort, mit der Bitte, das Passwort
  nach der ersten Anmeldung zu ändern.

Beide Dateien nur dem Mitglied geben. Token und Passwort erscheinen nie auf
dem Terminal. Das Mitglied wird in die Konfiguration eingetragen (nur die
Zeilen des Eintrags ändern sich, Kommentare bleiben) und der laufende
Koordinator liest die Liste per `systemctl reload wanderwelt-koordinator`
neu (SIGHUP, `ExecReload` in der Unit). Der Befehl löst das Neuladen nur aus
(„Neuladen ausgelöst – Ergebnis: journalctl -u wanderwelt-koordinator“); ob
es gelang, steht im Journal: „Mitglieder neu geladen“ oder, bei ungültiger
Datei, „MITGLIEDER NICHT NEU GELADEN“ (die alte Liste gilt weiter). Dort
steht nach ein paar Sekunden auch, ob es den Minecraft-Namen gibt: „UUID
nicht auflösbar“ mit `minecraft=<name>` heißt meist ein Tippfehler – dann
fehlt das Mitglied in der Whitelist; den Namen in der Konfiguration
korrigieren und neu laden. Scheitert schon das Auslösen, den Koordinator
neu starten.

Wann eine Änderung wirkt: Wer spielen oder hosten darf, prüft der
Koordinator gleich nach dem Neuladen. Whitelist und Operatoren (`--op`)
bekommt der Minecraft-Server beim Start einer Host-Sitzung. Jedes
erfolgreiche Neuladen (und jede Änderung an Rollen oder Minecraft-Namen)
ändert außerdem `members_version` in der Antwort auf das Lebenszeichen des
Hosts; Starter, die das kennen, gleichen Whitelist und Ops des laufenden
Servers dann gleich an. Bei älteren Startern kennt ein laufender Server ein
neues Mitglied (oder eine geänderte Rolle) erst ab der nächsten
Host-Sitzung, und ein entferntes Mitglied steht bis dahin noch in seiner
Whitelist.

Die Zugangsdatei entsteht vor dem Konto. Meldet `create-user` einen Fehler,
fragt der Befehl tunneld, ob das Konto trotzdem angelegt wurde (tunneld kann
„created but not approved“ melden): Wenn nicht, wird die Zugangsdatei wieder
gelöscht; wenn ja oder wenn sich das nicht klären lässt, bleibt sie, und die
Meldung sagt das. Bricht `mitglied neu` nach dem Anlegen des Kontos ab,
nennt die Meldung, was schon angelegt ist, und zwei Auswege:

- **Erneut versuchen** (meist richtig): denselben Aufruf wiederholen. Das
  Konto wird wiederverwendet, ein fehlendes `mark-verified` nachgeholt, und
  eine schon geschriebene `zugang-<name>.txt` bleibt unverändert und gültig.
  Sie nicht löschen: Nur dort steht das Passwort, und tunneld kann es
  nicht zurücksetzen (an `@wanderwelt.invalid` geht auch keine Mail). Eine
  schon geschriebene Gruppendatei vorher löschen. Hatte der abgebrochene
  Aufruf schon einen Token ausgestellt, widerruft die Wiederholung ihn
  (bei einem vorhandenen Konto widerruft `mitglied neu` immer ältere Tokens
  `wanderwelt`; anders benannte bleiben).
- **Aufgeben**: `mitglied entfernen <e-mail>` (geht auch, wenn das Mitglied
  noch nicht eingetragen ist; bei einer eigenen E-Mail mit
  `--auch-fremdes-konto`, siehe unten), dann die genannten Dateien löschen.

`mitglied entfernen` (Minecraft-Name oder E-Mail) widerruft die Tokens
namens `wanderwelt`, sperrt das tunnel-Konto bei `@wanderwelt.invalid` oder
mit `--konto-sperren`, trägt das Mitglied aus und lädt neu. Steht die
E-Mail nicht in der Konfiguration, räumt es nur das tunnel-Konto auf – ohne
Weiteres nur bei `@wanderwelt.invalid` (Rest eines abgebrochenen `mitglied
neu`); jedes andere tunnel-Konto nur mit `--auch-fremdes-konto`, damit ein
Tippfehler nicht die Tokens eines Fremden widerruft. Das letzte
Admin-Mitglied wird nie entfernt; dabei zählen die wirksamen Rollen (auch die im
Fenster geänderten, siehe unten). Wer später wieder aufgenommen wird,
bekommt sein gesperrtes Konto `@wanderwelt.invalid` von `mitglied neu`
zurück (Gruppendatei neu, kein neues Passwort: das alte gilt; eine noch
vorhandene `zugang-<name>.txt` im `--aus`-Verzeichnis bleibt unverändert).
Eine vorhandene Zugangsdatei nennt `mitglied neu` nur dann als gültig, wenn
ihr Login die E-Mail des Kontos ist. Sonst warnt es, dass sie nicht zu diesem
Konto gehört und nicht weitergegeben werden soll, und lässt sie stehen.

### Rollen, Minecraft-Namen und Speicherbudget

Was Admins im Fenster des Starters ändern, schreibt der Koordinator nicht in
seine Konfiguration (die ist für den Dienst nur lesbar, `ProtectSystem=strict`),
sondern in Überlagerungen in `data_dir` (atomar, 0600, Besitzer
`wanderwelt`). Sie gelten vor den Werten aus der Konfiguration:

- `roles.json` – Admin- und Op-Recht je Mitglied (E-Mail klein →
  `{"admin": …, "op": …}`), gesetzt mit `PUT /v1/members/{email}` (nur
  Admins). Entspricht eine Rolle wieder der Konfiguration, fällt der Eintrag
  weg. Das Admin-Recht des letzten Admins lässt sich nicht wegnehmen (409).
  Bliebe mit `roles.json` beim Start oder Neuladen kein Admin übrig (von Hand
  geändert, oder der einzige darüber ernannte Admin wurde entfernt), gilt
  sie nicht: „ROLES.JSON LÄSST KEINEN ADMIN ÜBRIG“ im Journal, es gelten die
  Rollen aus der Konfiguration.
- `names.json` – Minecraft-Namen (E-Mail klein → `{"minecraft": …,
  "by_admin": …}`). Ein Mitglied ohne Namen meldet seinen einmal selbst
  (`POST /v1/me/minecraft`, sonst 409); ein Admin setzt ihn jederzeit (`PUT
  /v1/members/{email}/minecraft`). Abgelehnt (400) wird ein ungültiger Name,
  einer, den Mojang nicht kennt, und einer, der schon einem anderen Mitglied
  gehört; antwortet Mojang nicht, 503. Ein selbst gemeldeter Name gilt nur,
  solange in der Konfiguration keiner steht; einer vom Admin gilt immer.
  Im Fenster (Reiter Mitglieder, nur Admins) steht neben jedem Namen „ändern“:
  Eingabe mit Prüfung (3 bis 16 Zeichen, `A-Z a-z 0-9 _`), bei Ablehnung zeigt
  das Fenster den Text des Koordinators. Bei der Einrichtung und beim Start
  fragt der Starter selbst nach dem Namen (erkannt aus dem Minecraft-Launcher,
  mit Rückfrage); ein falscher, aber bei Mojang vorhandener Name lässt sich
  nur so vom Admin korrigieren.
- `settings.json` – `{"storage_budget_bytes": n}` überlagert
  `storage_budget`; gesetzt mit `PUT /v1/storage/budget` (nur Admins,
  mindestens 1 GiB und höchstens belegt + frei auf dem Server). Danach räumt
  der Koordinator gleich mit dem neuen Budget auf; ein kleineres Budget
  löscht dabei die ältesten Stunden-, Tages- und Archivstände. „Belegt“ ist
  die Belegung der behaltenen Stände aus der letzten Rechnung (Aufräumen,
  sonst die Rechnung beim Start); solange die beim Start noch läuft, kommt
  503 („gleich nochmal“). Beim Beenden wartet der Koordinator, bis ein
  solches Aufräumen fertig ist.

- `members.json` und `joins.json` – Beitrittsanfragen (siehe unten): `joins.json`
  hält offene und abgelehnte Anfragen, `members.json` die angenommenen
  Mitglieder (E-Mail, Minecraft-Name, angenommen von, Zeit). Wirksame
  Mitglieder sind die aus der Konfiguration plus die aus `members.json`
  (bei gleicher E-Mail gewinnt die Konfiguration). Rollen und Namen darauf
  laufen wie bei allen Mitgliedern über `roles.json` und `names.json`.

Im Fenster ändern Admins die Rollen im Reiter Mitglieder (sofort wirksam; ein
laufender Server gleicht Whitelist und Ops nach dem nächsten Lebenszeichen
an) und das Speicherbudget im Reiter Welt. Minecraft-Namen setzt ein Admin
über `PUT /v1/members/{email}/minecraft`.

`mitglied liste` zeigt die wirksamen Namen und Rollen und markiert mit `*`,
was von der Konfiguration abweicht (angenommene Mitglieder mit „(beigetreten)“;
`mitglied entfernen` nimmt sie aus `members.json`). Beim Neuladen (`systemctl reload`) liest
der Koordinator `roles.json` und `names.json` neu (eine von Hand berichtigte
Datei gilt also ohne Neustart) und nimmt Einträge für Mitglieder heraus, die
nicht mehr in der Konfiguration stehen – wer wieder aufgenommen wird, beginnt
mit den Werten aus der Konfiguration. Das gilt nicht, wenn ein Mitglied
entfernt und wieder aufgenommen wird, während der Koordinator nicht läuft.

Ist eine Überlagerung kaputt, gilt sie nicht: Im Journal steht
„ÜBERLAGERUNG KAPUTT“, es gelten die Werte aus der Konfiguration, und die
Datei bleibt unverändert liegen. Erst die nächste Änderung der betroffenen
Werte legt sie als `<datei>.kaputt-<Zeit>` beiseite und schreibt sie neu.

### Manifest aktualisieren

Das Versions-Manifest (`manifest_file`, Vorlage
`deploy/manifest.example.yaml`) nennt, was ein Host lädt. Der Koordinator
liest es beim Start und bei `systemctl reload wanderwelt-koordinator`
(SIGHUP) und liefert dazwischen immer diesen Stand aus: **Nach jeder
Änderung `systemctl reload wanderwelt-koordinator`.** Ist die Datei beim
Neuladen kaputt (YAML-Fehler, unbekannter Schlüssel, `minecraft_version`
fehlt), gilt der bisherige Stand weiter; im Journal steht „VERSIONS-MANIFEST
NICHT NEU GELADEN“. Nach einem gelungenen Neuladen steht dort
„Versions-Manifest geladen“ mit Minecraft- und Starter-Version.

Jeder Eintrag hat `url` und `hash` (`sha256:<hex>` oder `sha512:<hex>`). Für
die URL gibt es zwei Arten:

- `https://…` – eine stabile, versionierte Quelle (Adoptium, Modrinth).
- `wanderwelt:<datei>` – der Koordinator liefert die Datei selbst aus, aus
  `<data_dir>/artifacts/<datei>` (`GET /v1/artifacts/<datei>`, nur für
  Mitglieder). So liegen tunnel.exe (das tunnel-Repo ist privat) und der
  Fabric-Server-Launcher (Fabric-Meta erzeugt ihn bei Bedarf neu, der Hash
  kann sich ändern). `<datei>` besteht nur aus Buchstaben, Ziffern, `.`, `_`
  und `-`.

Eine Datei ablegen und den Hash bilden (auf dem Mini-Server, mit
`data_dir: /var/lib/wanderwelt`):

```
cd /var/lib/wanderwelt/artifacts      # legt der Koordinator beim Start an
curl -fL -o fabric-server-launch-26.2-0.20.1-1.1.2.jar \
  https://meta.fabricmc.net/v2/versions/loader/26.2/0.20.1/1.1.2/server/jar
install -o wanderwelt -g wanderwelt -m 0640 /pfad/zu/tunnel.exe tunnel-windows-amd64.exe
chown wanderwelt:wanderwelt fabric-server-launch-26.2-0.20.1-1.1.2.jar
echo "sha256:$(sha256sum tunnel-windows-amd64.exe | cut -d' ' -f1)"
echo "sha256:$(sha256sum fabric-server-launch-26.2-0.20.1-1.1.2.jar | cut -d' ' -f1)"
```

Die ausgegebenen Werte als `hash` eintragen, `url` als
`wanderwelt:tunnel-windows-amd64.exe` usw. Eine neue Version immer unter
neuem Dateinamen ablegen und dann `url` und `hash` umstellen: Starter, die
gerade das alte Manifest haben, laden so nie eine halb ersetzte Datei. Alte
Dateien danach löschen.

Beim Start und bei jedem Neuladen prüft der Koordinator alle
`wanderwelt:`-Einträge (Datei da, Hash passt) und meldet Abweichungen im Log
(`journalctl -u wanderwelt-koordinator`, Zeile „ARTEFAKT AUS DEM MANIFEST
NICHT AUSLIEFERBAR“) – so ein Eintrag verhindert jedes Hosten.

### Starter-Update ausrollen

Die Starter aktualisieren sich selbst, sobald das Manifest unter `starter`
eine größere Release-Version nennt (Ablauf beim Starter: „Starter
aktualisieren“). Der Koordinator muss dafür mindestens den Stand mit
`starter` haben – ein älterer kennt den Schlüssel nicht und startet mit so
einem Manifest nicht mehr. Ausrollen, auf dem Mini-Server als root:

1. Die exe aus dem Release-zip (`wanderwelt-<version>-windows-amd64.zip`,
   darin `wanderwelt.exe`) unter einem Namen mit Version in
   `<data_dir>/artifacts/` ablegen und Hash und Größe bilden:

   ```
   cd /var/lib/wanderwelt/artifacts
   unzip -p /pfad/zu/wanderwelt-0.5.0-windows-amd64.zip wanderwelt.exe > wanderwelt-0.5.0.exe
   chown wanderwelt:wanderwelt wanderwelt-0.5.0.exe
   chmod 0640 wanderwelt-0.5.0.exe
   sha256sum wanderwelt-0.5.0.exe | cut -d' ' -f1
   stat -c %s wanderwelt-0.5.0.exe
   ```

2. Im Manifest den Eintrag `starter` setzen (`version` ist der Tag ohne
   `v`, so wie `wanderwelt.exe version` ihn zeigt; `sha256` und `size` aus
   Schritt 1):

   ```yaml
   starter:
     version: "0.5.0"
     url: "wanderwelt:wanderwelt-0.5.0.exe"
     sha256: "<Ausgabe von sha256sum>"
     size: <Ausgabe von stat>
   ```

3. `systemctl reload wanderwelt-koordinator`. Im Journal steht dann
   „Versions-Manifest geladen … starter=0.5.0“. Fehlt die Datei, passen Hash
   oder Größe nicht, ist `url` keine `wanderwelt:`-Adresse oder `version`
   keine Release-Version, steht dort „STARTER-UPDATE NICHT AUSLIEFERBAR“ mit
   dem Befund, und das Manifest geht ohne `starter` hinaus – kein Starter
   versucht dann ein Update. Berichtigen und noch einmal `reload`.

Die Starter fragen beim Start und alle 6 Stunden nach; wer gerade hostet,
aktualisiert sich danach. Ältere exe-Dateien im Artefakt-Ordner nach einem
Tag löschen (ein Starter, der gerade lädt, braucht sie noch). Zurück auf
eine ältere Version geht nicht über das Manifest – Starter aktualisieren
sich nur auf größere Versionen.


### Beitrittsanfragen

Ein Konto, das tunneld kennt (gültiger Token), aber nicht in der
Mitgliederliste steht, darf **nur** `POST /v1/join` (Body
`{"minecraft_name": …}`, Antwort 201 `{"state": "offen", "since": …}`) und
`GET /v1/join` (`{"state": "keine"|"offen"|"abgelehnt"|"angenommen", "since": …}`);
jeder andere Endpunkt antwortet 403. Der Minecraft-Name wird bei Mojang
geprüft (400 ungültig oder unbekannt, 503 Mojang antwortet nicht); gehört er
schon einem Mitglied oder ist er in einer offenen Anfrage genannt, 409.
Grenzen (je 429): eine offene Anfrage je Konto, 50 offene insgesamt, nach
einer Ablehnung 24 Stunden Wartezeit.

Admins sehen die offenen Anfragen mit `GET /v1/join-requests` (älteste zuerst;
die Zahl steht auch in `GET /v1/me` als `pending_joins`) und entscheiden mit
`POST /v1/join-requests/{email}/accept` bzw. `/decline` (204). Annehmen trägt
das Konto in `members.json` ein und erhöht `MembersVersion`: Der laufende Host
nimmt den Spieler ohne Neustart in die Whitelist auf. Die tunnel-Freischaltung
des Kontos bleibt Sache des Admins im tunneld-Admin-Panel. Anfragen und
angenommene Mitglieder überstehen einen Neustart; beim Neuladen werden die
Dateien neu gelesen.

## Lizenz

Elastic License 2.0; siehe [LICENSE](LICENSE) und [NOTICE](NOTICE).
Original: https://github.com/Bazinga0o0/wanderwelt
