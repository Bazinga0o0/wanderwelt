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
  Einrichtung führt dich zur Webseite, wo du ein Konto anlegst, deine E-Mail
  bestätigst (Link in der Mail), auf die Freischaltung durch einen Admin wartest
  und dann unter „API Tokens“ einen Token erstellst (Name eingeben, „Create
  token“; es gibt nur diese Art von Token), den du dann einfügst. Gleiches gilt,
  wenn du ohne Gruppencode einrichtest („Entdecken“): nach der Server-Adresse
  öffnet die Einrichtung auf Wunsch die Webseite für dich. Danach stellt sie eine
  Beitrittsanfrage und wartet, bis ein Admin dich annimmt.)

Java und den Minecraft-Server lädt wanderwelt selbst.

Einrichten:

1. wanderwelt.exe aus dem zip auspacken (etwa nach Downloads) und
   doppelklicken. Warnt Windows mit „Der Computer wurde durch Windows
   geschützt“, auf „Weitere Informationen“ und dann „Trotzdem ausführen“
   klicken: Die Datei ist nicht signiert.
2. Die Gruppendatei wählen (oder, falls du keine hast, den Gruppencode von einem
   Mitspieler einfügen, oder „Entdecken“: Adresse des Tunnel-Servers und
   deinen Token eingeben und eine der aufgelisteten Gruppen wählen) und der
   Minecraft-EULA zustimmen.
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
  Autostart mit Windows, Dauerbetrieb und „Darf das Hosten übernehmen“, dazu
  die Schalter der [Benachrichtigungen](#benachrichtigungen). Die
  Leistungswerte gelten ab dem nächsten Serverstart; „Darf das Hosten
  übernehmen“ gilt sofort, auch während du mitspielst.
- **Entdecken**: Gruppen, die der Tunnel-Server deiner Gruppen auflistet
  (Betreiber entscheiden, welche; nicht gelistete, private Gruppen erreichst
  du nur mit dem Gruppencode). Je Gruppe siehst du Titel, Beschreibung,
  Mitgliederzahl und deinen Zustand („Mitglied“, „angefragt“). „Beitritt
  anfragen“ fragt deinen Minecraft-Namen und stellt die Anfrage; ein Admin der
  Gruppe nimmt dich an. Danach erscheint die Gruppe in der Auswahl im Kopf des
  Fensters. Die Texte kommen von den Servern und werden nur als Text gezeigt.
- **Mitglieder** (nur Admins): alle Mitglieder mit Admin- und Op-Schalter und
  wer gerade online ist. Mitglieder anlegen oder entfernen macht der Admin auf
  dem Server, nicht im Fenster.
- **Log**: die letzten Zeilen des Logs, folgt automatisch; „Log-Ordner öffnen“
  zeigt den Ordner, „Fehlerbericht erstellen“ packt die Logs für den Admin
  (siehe „Fehlerbericht und Logs der Welt“).
- **Statistik** (jedes Mitglied): je Mitglied die Spielzeit (Zeit auf dem
  Server), die Hostzeit (Zeit, in der er den Server bereitgestellt hat), die
  Zahl der Host-Sitzungen und sein Anteil an der gesamten Hostzeit als Balken
  („wer trägt es“). Mit „Gesamt“ und „Letzte 30 Tage“ wechselst du den
  Zeitraum. Die Zeiten sind auf etwa 10 Sekunden je Beitritt, Austritt und
  Hostbeginn genau; fällt ein Host aus, zählt nur die Zeit bis zu seinem
  letzten Lebenszeichen. Gezählt wird ab dem Update auf diese Version, davor
  gibt es nichts. Es werden nur Zeiten und Zähler je Mitglied gespeichert
  (Minecraft-Name wie in der Mitgliederliste), nichts zu IP, Hardware, Chat
  oder Position.
- **Zustand** (nur Admins, letzter Reiter): was du sonst per SSH im Log des
  Servers nachsiehst. Oben die Ampel (OK, Warnung, Fehler) mit „Aktualisieren“,
  darunter die Warnungen, dann Karten mit Koordinator-Version und Laufzeit,
  Speicherplatz und Größe des Speichers, Weltstand (Stand, Zeit des letzten
  Uploads), aktuellem Host (letztes Lebenszeichen), den Starter-Versionen der
  zuletzt gesehenen PCs, offenen Beitrittsanfragen, Wartung, letzter Sicherung
  und dem letzten Aufräumen. Der Reiter aktualisiert sich alle 30 s, solange er
  offen ist. Ein älterer Koordinator ohne `GET /v1/health` zeigt „zu alt“.

- **Karte** (jedes Mitglied, ganz hinten): die Oberwelt von oben, siehe
  [Karte (Mitglieder)](#karte-mitglieder). Den Reiter gibt es nur, wenn der
  Koordinator für die gewählte Welt eine Karte erzeugt hat.

### Karte (Mitglieder)

Der Reiter **Karte** zeigt die Oberwelt als Draufsicht. Er erscheint nur, wenn
der Koordinator für die gewählte Welt eine Karte hat (Status nennt `karte`);
ein älterer Koordinator oder eine Welt ohne Karte zeigt keinen Reiter. Du
siehst die Landschaft (Farben der Blöcke von oben, keine Texturen), **keine
Spieler und keine Positionen**.

- **Bedienung**: Ziehen mit der Maus verschiebt, das Mausrad zoomt um die
  Mausposition, die Knöpfe **+** und **−** zoomen um die Mitte (stufenlos,
  1/64 bis 8 Pixel je Block; beim Vergrößern bleiben die Blockpixel scharf).
  **Zur Mitte** springt in die Mitte der Karte, **Neu laden** holt sofort das
  aktuelle Manifest und versucht gescheiterte Kacheln neu.
- **Statusleiste**: „Stand: vor 14 Min.“ (wann der Koordinator die Karte
  zuletzt erzeugt hat), Zoom und Kachelstufe, die Blockkoordinate (x, z) unter
  der Maus und ein Hinweis, solange Kacheln laden. Fehler stehen als Meldung
  da: „Die Karte ist noch nicht erzeugt“, „Der Koordinator ist nicht
  erreichbar“ (eine schon geladene Karte bleibt dabei stehen) oder „Die Karte
  ist ungültig“.
- **Was geladen wird**: nur die Kacheln (256×256 Pixel, PNG) der gerade
  sichtbaren Fläche, höchstens acht Abrufe gleichzeitig; verschiebst du die
  Karte, werden Abrufe nicht mehr sichtbarer Kacheln abgebrochen. Solange der
  Reiter offen ist, prüft das Fenster alle 60 Sekunden das Manifest der
  Karte; nur Kacheln mit geändertem Hash werden neu geholt.
- **Zwischenspeicher**: im Arbeitsspeicher höchstens 64 Kacheln; auf der Platte
  unter `%LOCALAPPDATA%\wanderwelt\karte-cache\<Gruppe>\<Welt>\<sha256>.png`
  höchstens 200 MB je Welt (die ältesten Dateien fliegen zuerst raus). Jede
  Datei wird beim Lesen gegen ihren Hash aus dem Manifest geprüft; eine
  veränderte oder halb geschriebene Datei wird verworfen und neu geholt. Der
  Ordner lässt sich jederzeit löschen.
- **Netz**: Das Fenster spricht nur mit der eigenen wanderwelt-Instanz; sie
  holt Manifest und Kacheln mit dem Token der Gruppe vom Koordinator. Das
  Token sieht das Fenster nie und es steht in keinem Log.

### Benachrichtigungen

wanderwelt kann dir Bescheid sagen, was in der Gruppe passiert, auch wenn das
Fenster zu ist. Es gibt zwei Wege: Windows-Hinweise auf deinem PC (du stellst
sie selbst ein) und, wenn euer Admin es eingerichtet hat, einen Discord-Kanal
für die ganze Gruppe.

**Windows-Hinweise** stellst du im Fenster auf dem Reiter **Hosten** im Bereich
„Benachrichtigungen“ ein, ein Schalter je Ereignis; sie gelten für diese
Gruppe auf diesem PC (gespeichert in `benachrichtigungen.json` im Ordner der
Gruppe). Standard:

| Ereignis | Windows | Discord |
| --- | --- | --- |
| „X hostet jetzt – die Welt ist gleich bereit“ | an | an |
| „Die Welt ist online (Host X)“ | an | an |
| „A ist beigetreten / hat die Welt verlassen“ | aus | an |
| „Niemand hostet mehr“ | aus | aus |
| Wartung angekündigt / vorbei | an | an |
| Host-Übergabe läuft / abgeschlossen | aus | an |
| „Neue Beitrittsanfrage von Y“ (nur Admins) | an | gibt es nicht |

Dein eigenes Hosten und dein eigener Beitritt melden die Hinweise nicht noch
einmal (dazu zeigt wanderwelt ohnehin eigene Hinweise). Beitritte und Abgänge,
die zusammenfallen, kommen in einer Meldung. Verglichen wird alle 30 Sekunden
mit dem Stand des Koordinators: Beim Start von wanderwelt und nach einem
Ausfall des Koordinators gibt es keine Hinweise zu dem, was vorher schon war.
Ist das Fenster offen, erscheinen die Hinweise dort statt als Windows-Meldung.
Im Bereich steht auch, ob die Gruppe einen **Discord-Kanal** hat („an“/„aus“);
die Adresse des Kanals erfährt dein PC nie.

Der **Discord-Kanal** ist ein Kanal eures Discord-Servers, in den der
Koordinator kurze deutsche Texte schreibt (mit dem Titel der Gruppe, bei
mehreren Welten auch dem Namen der Welt), etwa „Unsere Wandergruppe: Mara
hostet jetzt – die Welt ist gleich bereit.“ Es stehen nur Minecraft-Namen
darin, die in der Gruppe ohnehin sichtbar sind, nie E-Mail-Adressen, Adressen
oder Standorte. Beitritte und Abgänge innerhalb von etwa 10 Sekunden stehen in
einer Nachricht. Niemand wird je mit @everyone, @here oder einer Erwähnung
angeschrieben.

### Mehrere Gruppen

Du kannst in mehreren Gruppen Mitglied sein (jede mit eigener Welt, eigenem
Host und eigenem Stand); Hosten und Spielen gelten immer für die aktive
Gruppe. Ab der zweiten Gruppe zeigt das Fenster oben die Zeile „Gruppe“ mit
einem Knopf je Gruppe, das Symbol im Infobereich nennt die aktive Gruppe in
eckigen Klammern. Der Gruppenname bleibt auf deinem PC; Discord erfährt ihn
nicht. Wechseln geht nur, solange du weder hostest noch mitspielst und nichts
hochgeladen oder geladen wird; sonst sagt das Fenster, was zuerst zu beenden
ist. Eine Gruppendatei einer anderen Gruppe („Gruppendatei laden“ im Menü)
fügt die Gruppe hinzu, ohne die aktive zu ersetzen. Die Notfallkopie hat je
Gruppe einen eigenen Ordner (`wanderwelt-Notfallkopie-<Gruppe>`). Beim ersten
Start dieser Version verschiebt wanderwelt die bisherigen Daten einmalig in
den Ordner der Gruppe (nichts wird gelöscht; geht das nicht, bleibt alles wie
es war, und es gibt weiter nur eine Gruppe). Der Link `wanderwelt://beitreten`
öffnet bei mehreren Gruppen den Reiter „Entdecken“.

### Mehrere Welten

Eine Gruppe kann mehrere Welten haben (zum Beispiel „Survival“ und
„Creative“). **Es läuft immer nur eine Welt**: ein Host, ein Zuschlag, ein
Minecraft-Port.

- Sobald die Gruppe zwei oder mehr Welten hat, zeigt der Reiter **Start** die
  Karte „Welt“ mit einem Knopf je Welt. Gespielt wird die Welt, die gerade
  läuft („Läuft gerade: <Name>“); hostet niemand, die gewählte (sonst die
  erste). Gewählt wird nur, solange niemand hostet; die Wahl gilt nur auf
  deinem PC.
- **Nur Admins** legen Welten an, benennen sie um, ändern Spielmodus
  (Überleben, Kreativ, Abenteuer) und Schwierigkeit (Friedlich, Leicht,
  Normal, Schwer) und löschen sie: Reiter **Welt**, Karte „Welten verwalten“.
  Der Host schreibt Spielmodus und Schwierigkeit vor jedem Start in die
  `server.properties`; „Gruppenstandard“ lässt `world_settings` des
  Koordinators gelten. Der Spielmodus gilt für neue Spieler; wer schon in der
  Welt gespielt hat, behält seinen gespeicherten Modus. Mods, Datenpakete und
  Gamerules sind Sache der jeweiligen Welt.
- Jede Welt hat ihren **eigenen Stand und Verlauf**; Inventare und Spieler-IDs
  liegen in der Welt und werden nie zwischen Welten vermischt. Whitelist und
  Ops gelten für die ganze Gruppe.
- **Löschen** geht nur, wenn die Welt nicht läuft, nur mit Rückfrage und nie
  für die letzte Welt. Die Stände bleiben auf dem Koordinator liegen; erst
  das Aufräumen nimmt sie unter Platzdruck als Erste weg.
- Eine neue Welt ist leer. Den ersten Start macht ein Admin: Minecraft legt
  die Welt frisch an, der erste Zwischenstand füllt sie. „Welt übernehmen“ ersetzt die
  gerade gewählte Welt (die bisherige bleibt als Archiv der Welt).
- Auf dem PC liegt die erste Welt wie bisher im Gruppenordner (`server\`),
  jede weitere in `gruppen\<Gruppe>\welten\<w2, w3 …>\` mit eigenem
  Server-Ordner, Zustand und `verworfen-*`; die Notfallkopie jeder Welt hat
  einen eigenen Ordner (`wanderwelt-Notfallkopie[-<Gruppe>]-<Welt>`, die erste
  Welt behält den bisherigen Namen).

### Chat-Verlauf

Mit der Server-Mod `wanderwelt-chatverlauf` (Quelle: `mods/chatverlauf`; der
Admin trägt sie ins Manifest ein, siehe „Chat-Verlauf-Mod ins Manifest
eintragen“) bleibt der Chat über Neustarts und Hostwechsel erhalten: Wer der
Welt beitritt, sieht die letzten Nachrichten (Standard 50) als graue
Verlaufszeilen mit Zeit im Tooltip am Namen. Du brauchst dafür nichts zu
installieren.

- **Was gespeichert wird:** Chat der Spieler sowie `/me` und `/say` von
  Spielern, mit Name, Zeit und Text, im Klartext. Nicht gespeichert werden
  `/msg`, Team-Chat, Systemmeldungen und Ansagen des Starters.
- **Wo:** im Weltordner (`world/wanderwelt-chatverlauf/`), in Segmenten von je
  256 KiB. Der Ordner wandert mit jedem Stand zum nächsten Host. Je Welt der
  Gruppe gibt es einen eigenen Verlauf.
- **Größengrenze:** höchstens 5 MiB (einstellbar, 1 bis 50 MiB). Wird sie
  überschritten, fallen die ältesten Segmente weg. Bei normalem Freundeschat
  (wenige KiB am Tag) wird sie lange nicht erreicht. Wie viele Zeilen beim
  Beitritt kommen, ist zweitrangig und bleibt bei 50.
- **Datenschutz – bitte alle Mitglieder wissen lassen:** Der Chat liegt
  dauerhaft (auf die Größengrenze gekürzt) in der Welt und damit bis zu
  30 Tage in den Ständen auf dem Koordinator (Aufbewahrung siehe „Aufbewahrung
  der Stände“), lesbar für den Betreiber des Koordinators und für jeden Host,
  der die Welt hostet. Wer das nicht will, bittet einen Admin, den Verlauf der
  Welt auszuschalten (nächster Punkt). Schon gespeicherter Chat wird beim
  Ausschalten **nicht** gelöscht; er bleibt in der Welt liegen und verschwindet
  erst mit den Ständen, die ihn enthalten, oder wenn ein Admin die Dateien von
  Hand entfernt und die Welt neu speichert.
- **Einstellen (nur Admins):** Reiter **Welt**, Karte „Welten verwalten“,
  bei einer Welt „Ändern“ (oder „Neue Welt“): **Chat-Verlauf** An/Aus und
  **Größengrenze** in MiB. Die Einstellung gilt je Welt und für die ganze
  Gruppe, ab dem nächsten Start der Welt (der Host trägt sie vor dem
  Serverstart in `config/wanderwelt-chatverlauf.json` ein; andere Schlüssel
  dieser Datei, etwa `wiedergabe_zeilen` oder `zeit`, bleiben unangetastet).
  Mitglieder sehen auf dem Reiter Start in der Welt-Karte, wenn der Verlauf
  einer Welt aus ist oder eine andere Grenze hat. Bei „Aus“ schreibt die Mod
  nichts mehr und zeigt nichts mehr; Standard ist An mit 5 MiB.
- **Grenzen:**
  - Stürzt der Host ab, wird die Welt auf den letzten Stand zurückgesetzt –
    und mit ihr der Chat seit diesem Stand (höchstens ein Stand-Abstand, in
    der Regel 5 Minuten). Dasselbe gilt für „Stände zurückholen“: Der Chat
    geht auf den Stand des gewählten Standes zurück.
  - Die Wiedergabe beim Beitritt ist eine Servernachricht ohne Signatur: Sie
    lässt sich nicht melden, trägt keine Spieler-UUID, und die Blockliste des
    Clients greift nicht – wen du blockiert hast, siehst du im Verlauf
    trotzdem. Bei der Chat-Einstellung „Nur Befehle“ siehst du nur die Kopf-
    und Schlusszeile.
  - Formatierungen anderer Chat-Mods gehen verloren (gespeichert wird nur der
    Klartext). Chat, der bei einer Host-Übergabe nach dem Bereitstellen des
    Standes anfällt (die Spieler warten dann als Zuschauer), geht verloren.
  - Eine ältere Mod-Version ohne den Schlüssel `aktiv` ignoriert „Aus“: Die
    Jar im Manifest muss mindestens 1.1.0 sein.

### Fehlerbericht und Logs der Welt

Zwei Hilfen, damit sich Fehler leichter finden lassen. Beide säubern vorher
alles, was geheim sein könnte (Tokens, Passwörter, RCON-Passwort, Webhook-
Adressen, Zugangsdaten in Adressen, Gruppencodes, IP-Adressen von Spielern; den
Windows-Benutzernamen in Pfaden ersetzt `%USERPROFILE%`).

- **Fehlerbericht erstellen** (Reiter **Log** im Fenster oder Rechtsklick auf
  das Symbol): packt ein Zip `wanderwelt-fehlerbericht-<Datum-Zeit>.zip` nach
  **Downloads** und öffnet den Ordner. Es entsteht nur auf deinem PC, nichts
  wird hochgeladen; du gibst es weiter, wem du willst. Inhalt: Log des Starters
  von heute und gestern, Log des Servers (`wanderwelt-server.log`,
  `latest.log`, je die letzten 2 MiB), Version, Windows und Hardware-Kurzinfo,
  Zustand (Phase, Fehler, Status), Manifest-Version und Mods sowie die mit der
  Welt gewanderten Logs der früheren Hosts. **Nicht** dabei: Weltdaten,
  Spielerdateien, Gruppendatei. Der Serverlog kann Spielernamen und Chat
  enthalten – bitte vor dem Weitergeben ansehen.
- **Logs wandern mit der Welt:** Beim Erzeugen jedes Stands legt der Host die
  wanderwelt-Zeilen des Tages (Host, Server, Stände, Zuschlag, Übergabe,
  Wartung, Fortsetzen, Selbst-Update, Fehler; **nicht** den Minecraft-Serverlog,
  nicht den Chat, nicht Beitritte zur Gruppe) in
  `world/wanderwelt-logs/<JJJJ-MM-TT>-<Kurzname>.log`. Der nächste Host bekommt
  sie mit dem Stand, der Fehlerbericht legt sie bei, und ein Admin kann sie im
  Weltordner lesen. Der Kurzname sind 6 Zeichen eines Hashes aus der
  PC-Kennung, nie der Rechnername.
- **Datenschutz – bitte alle Mitglieder wissen lassen:** In den Logs stehen
  Zeitpunkte und Vorgänge des Servers (wer wann gehostet hat, Stände, Fehler)
  und können Spielernamen vorkommen, etwa bei einer Host-Übergabe an einen
  Mitspieler oder wenn ein Vorgang namentlich genannt wird. Sehen kann sie
  jeder Host, der die Welt hostet (sie liegen im Weltordner auf seinem PC),
  und der Betreiber des Koordinators über die Stände. Grenzen: je Datei
  höchstens 256 KiB, im Ordner insgesamt höchstens 1 MiB, Dateien älter als
  3 Tage werden beim nächsten Stand gelöscht (die älteste Zeile geht zuerst).
  Mit „Stände zurückholen“ gehen auch die Logs auf den alten Stand zurück. Ein
  Admin kann den Ordner `wanderwelt-logs` von Hand löschen und die Welt neu
  speichern; schon erzeugte Stände auf dem Koordinator behalten ihn bis zum
  Ablauf der Aufbewahrung.

### Rollen

- **Mitglied**: spielen und hosten.
- **Op**: wird im Minecraft-Server als Operator eingetragen und darf Befehle
  wie `/gamemode` benutzen.
- **Admin**: alles oben Genannte plus Stände zurückholen, das Speicherbudget
  setzen, Welten verwalten und die Rollen der anderen ändern (Reiter
  Mitglieder).

Admin- und Op-Schalter wirken live: Ein laufender Server gleicht sich danach
an. Das letzte Admin-Mitglied kann sich das Admin-Recht nicht selbst nehmen.

### Datenschutz: E-Mail-Adressen

Deine E-Mail-Adresse (die des tunnel-Kontos) sieht nur der **Serverbetreiber**:
wer den Koordinator betreibt, mit der Befehlszeile (`wanderwelt-koordinator
mitglied …`), den Dateien und dem Log auf dem Server. Alle anderen sehen sie
nie, auch Admins nicht, weder im Fenster noch in Hinweisen noch in der
Schnittstelle des Koordinators:

| Wer | sieht |
|---|---|
| du selbst | deine eigene Adresse (Einrichtung, „Ich“) |
| andere Mitglieder | deinen Minecraft-Namen und deine Kennung |
| Admins | deinen Minecraft-Namen und deine Kennung (Reiter Mitglieder, Beitrittsanfragen, Server), nicht deine Adresse |
| Serverbetreiber | alles, auch die Adresse zu jeder Kennung (`mitglied liste`) |

Die **Kennung** ist ein 16-stelliger Hex-Wert (zum Beispiel `9d3b71c8a05e24f6`),
den der Koordinator aus der Adresse mit einem geheimen Schlüssel bildet: Er
bleibt für ein Mitglied gleich (auch wenn die Adresse anders geschrieben ist),
lässt sich aber nicht auf die Adresse zurückrechnen. Dein Starter erkennt dich
(zum Beispiel als Host) an deiner eigenen Kennung.

**Für Admins:** Mitglieder und Beitrittsanfragen erkennst du am Minecraft-Namen;
die Kennung steht klein daneben. Brauchst du die Adresse (etwa um ein Konto bei
tunnel zu prüfen, oder weil jemand noch keinen Minecraft-Namen hat), nenne dem
Serverbetreiber die Kennung: Er sucht sie mit `mitglied liste` heraus.

### Server-Einstellungen (Reiter „Server“, nur Admins)

Statt einer Konsole verwaltet ein Admin im Reiter „Server“:

- **Whitelist**: Mitglieder stehen immer darauf (hinzufügen/entfernen nur
  über die Mitgliederliste). Zusätzlich lassen sich **Gäste** eintragen –
  Spieler ohne Mitgliedschaft, die mitspielen dürfen, wenn jemand hostet.
- **Ops**: Op wird nur, wer auf der Whitelist steht. Für Mitglieder ist es
  dasselbe Op-Recht wie im Reiter „Mitglieder“.
- **Banns**: Gebannte kommen nie herein, auch Mitglieder nicht (sie bleiben
  Mitglied). Ein gebannter Gast fällt von Whitelist und Ops. Sich selbst
  bannen geht nicht.
- **Spielregeln** (`gamerule`): Liste der laufenden Welt laden und je Zeile
  ändern (Schalter oder Zahl + „Setzen“). Sie gehören zur Welt und wandern
  mit dem Stand; ändern geht nur, solange die Welt läuft.
- **Befehl senden**: ein Minecraft-Befehl an den laufenden Server, die
  Antwort steht darunter.

Jede Zeile wirkt sofort. Läuft die Welt, übernimmt der Server Änderungen an
den Listen in wenigen Sekunden; sonst heißt es „Läuft gerade nicht –
gespeichert für den nächsten Start“. Namen: 3 bis 16 Buchstaben, Ziffern oder
`_`; der Koordinator prüft sie bei Mojang.

Nicht erlaubt sind Befehle, die Speichern, Stände oder die Übergabe stören
(`stop`, `save-all`, `save-off`, `save-on`, `reload`, `tick`, `transfer`,
`datapack`), Dateien am Host schreiben (`debug`, `perf`, `jfr`, `test`), ins
Netz gehen (`fetchprofile`), die Listen umgehen (`whitelist`, `op`, `deop`,
`ban`, `pardon`, IP-Banns) oder Einstellungen des Reiters „Welt“ betreffen
(`difficulty`, `defaultgamemode`, `setidletimeout`); Befehle von Mods gehen
gar nicht. Auch hinter `execute … run` gilt das.

**Datenschutz und Sicherheit:** Jeder Befehl, jede Spielregel und jede
Listenänderung steht mit E-Mail und Minecraft-Name des Admins und der Zeit im
Log des Koordinators (auf dem Server). Die Listen (Gäste, Banns mit
Minecraft-Name, UUID, wer und wann) liegen in `serverlisten.json` beim
Koordinator. Befehle laufen nur im Minecraft-Server des aktuellen Hosts
(über RCON, nur lokal), nie als Programm auf dessen PC. Wer Admin ist, kann
damit im Spiel viel ändern (Inventare, Welt, Spieler) – Admin-Recht nur an
Vertraute geben.

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

Probleme: Rechtsklick auf das Symbol, „Fehlerbericht erstellen“, und das Zip
aus Downloads dem Admin schicken (siehe „Fehlerbericht und Logs der Welt“).

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
Der Token landet nur in `%LOCALAPPDATA%\wanderwelt\gruppen\<id>\gruppe.yaml` (Zugriff
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

Ablage in `%LOCALAPPDATA%\wanderwelt` (bzw. `WANDERWELT_HOME`), gemeinsam für
alle Gruppen: `wanderwelt.exe` (die installierte Kopie), `aktiv.json` (die
aktive Gruppe), `control.json` (Port und Token der Steuerung, solange der Tray
läuft), `logs/wanderwelt.log` (ab 10 MB nach `wanderwelt.log.1`), `deps/`,
`recipes/`, `hashcache.json`, `update/`. Je Gruppe ein Ordner
`gruppen\<id>\` (`<id>` ist der Host des Koordinators in Kleinbuchstaben, ein
Port als `-<port>`) mit `gruppe.yaml` (mit Token), `state.json`, `profil.yaml`,
`tunnel.yaml` (mit Token), `notfallkopie.json`, `server/` und `verworfen-*`.
Das Einzel-Layout früherer Versionen (alles direkt im Datenordner) wird beim
Start einmalig dorthin verschoben, nur per Umbenennen, mit Rücknahme bei
einem Fehler. Entscheidungen und Grenzen:
Autostart: `HKCU\Software\Microsoft\Windows\CurrentVersion\Run\wanderwelt`.

Der Reiter „Entdecken“ fragt `GET <web_url>/api/v1/directory` des
tunnel-Servers (mit dem Token der Gruppe; `web_url` steht optional in der
Gruppendatei, sonst `https://<Host von tunnel_server>`) und je Eintrag
`GET /v1/info` des Koordinators. Antworten werden streng geprüft (Größe,
Längen, Zeichen, Adresse unterhalb der Domain des tunnel-Servers), die
Abfrage hat Zeitgrenzen, 429/503 zeigen nur einen Hinweis. Die lokale
Steuerung bietet dafür `GET /v1/entdecken` sowie `POST /v1/gruppe` (Wechsel)
und `POST /v1/entdeckenbeitritt` (Anfrage).

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
Stände oder Zuschlag. Die Reiter (Start, Welt, Hosten, Entdecken, Mitglieder, Log, Statistik) sind
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

### Wartung und Sicherung für ein Update

Für ein Update beendet der Admin das Hosten vom Server aus (nur über den
Befehl, nicht im Fenster). Auf dem Mini-Server als root:

```
wanderwelt-koordinator wartung start --in 60s --grund "Update 0.2.0"
wanderwelt-koordinator sicherung /var/lib/wanderwelt-sicherung-20261126
# Programm austauschen, systemctl restart wanderwelt-koordinator, prüfen
wanderwelt-koordinator wartung ende
```

- `wartung start`: Ab sofort lehnt der Koordinator „Spielen“ mit dem Grund ab
  (auch Hochladen und Host-Übergaben). Der Host sagt im Chat an („Wartung:
  Server stoppt in 60 Sekunden – Update 0.2.0 …“, weitere Ansagen 60 und 10 s
  vor dem Stopp), stoppt nach `--in` sauber (Schluss-Stand, Zuschlag zurück)
  und gibt nichts weiter; `--in 0s` gibt der Ansage 3 s. Alle Starter zeigen
  „Wartung am Server – Spielen ist gleich wieder möglich“ im Tray und im
  Fenster; Dauerbetrieb-PCs starten nicht von selbst. Hostet ein älterer
  Starter weiter, entzieht der Koordinator ihm nach `--in` + 60 s den Zuschlag.
- Der Befehl wartet bis `--frist` (Standard 5 min) nach Ablauf der Ansage, bis
  kein Zuschlag und kein Upload mehr besteht, und meldet „Bereit für das
  Update“. Hängt noch jemand, endet er mit Ausgang 3 und nennt, wer.
  `--nicht-warten` kündigt nur an; `wartung status` zeigt den Stand.
- Der Zustand steht in `<data_dir>/wartung.json` und übersteht einen Neustart;
  der Koordinator liest ihn alle 5 s und bei `reload` (der Bericht dazu:
  `wartung-bericht.json`). Solange die Wartung läuft, räumt der stündliche
  Aufräumlauf nur als Probe auf: Das Log nennt in „AUFRÄUM-PROBE WÄHREND DER
  WARTUNG“, was er löschen würde. Gelöscht wird erst wieder nach `wartung ende`
  – ein Update mit neuen Aufbewahrungsregeln nimmt so nie unbemerkt Stände weg.
- `wartung ende`: Spielen ist wieder möglich, die Starter melden „Wartung
  vorbei, ihr könnt wieder spielen.“ Kam die Wartung bis zum Stopp, öffnet
  der Koordinator zusätzlich ein Fortsetzen-Fenster (siehe unten).
- Hosten nach der Wartung fortsetzen: Starter, die bei Wartungsbeginn hosteten
  (der Server endete wegen der Wartung) oder mitspielten, merken sich das
  (`fortsetzen.json` im Gruppenordner). Nach `wartung ende` nennt der Status
  60 s lang „Beginn in …“ (`resume`, Verfall nach 10 min, gespeichert in
  `wartung-fortsetzen.json`); alle Starter mit Merker bereiten ihre Bewerbung
  vor und bewerben sich zum Beginn gemeinsam wie bei „Spielen“. Das
  gewöhnliche Sammelfenster wählt den besten PC (Upload 15 %, CPU 30 %, RAM 30 %, Ping 25 %; Mindestwerte:
  Upload 30 Mbit/s, 16-GB-PC mit genug freiem RAM; Upload über 100 Mbit/s und RAM über 64 GB zählen nicht mehr;
  der Ping – Median von 7 Round-Trips zum Koordinator – zählt 1,0 bis 20 ms und 0,0 ab 200 ms, dazwischen linear,
  fehlt er (ältere Starter), gilt er neutral als 0,5; kein Mindestwert für den Ping); er holt den Stand und hostet wie sonst, die anderen bekommen den
  Beitrittshinweis (ohne den Launcher von selbst zu öffnen). Es entfällt,
  wenn schon jemand hostet, die Wartung wieder läuft, der Nutzer „Fortsetzen
  abbrechen“ (Tray, Reiter Start), „Spielen“ oder „Nicht mehr mitspielen“
  gewählt, den Starter beendet oder Minecraft geschlossen hat. Ältere Starter
  setzen nicht fort (wie bisher); Koordinator zuerst aktualisieren.
- `sicherung <ziel>` (bei gestopptem Dienst oder in der Wartung nach „bereit“):
  Blobs als Hardlink (kein zusätzlicher Platz), Metadaten und Konfiguration
  als echte Kopie, dazu `<ziel>/konfiguration/`. Das Ziel muss auf demselben
  Dateisystem liegen. Rückweg: Dienst stoppen, altes Programm zurück,
  `data_dir` wegsichern und die Sicherung als `data_dir` einsetzen.
- Nach gelungener Sicherung schreibt der Befehl `<data_dir>/sicherung.json`
  (`{"zeit": …, "ziel": …}`, Zeit in UTC) und aktualisiert sie bei jeder
  weiteren; die Gesundheit (nächster Abschnitt) liest sie. Ohne die Datei gilt
  die letzte Sicherung als „unbekannt“.
- Der Speicher trägt seine Format-Version (`store/format.json`). Beim Start
  laufen ausstehende Migrationen vorwärts (nur Metadaten, nie Blobs); eine
  unbekannte neuere Version startet nicht – dann das neuere Programm oder die
  Sicherung davor benutzen.
- **Format 2 (mehrere Welten):** Der erste Start des neuen Koordinators
  schreibt `store/current.json` zur Weltliste um. **Vorher `sicherung`
  machen** – ein altes Programm startet auf Format 2 nicht mehr. Die
  bisherige Welt wird Welt `w1` („Welt“) mit derselben Weltkennung und
  denselben Ständen; Archive gehören zu ihr; die alte Datei bleibt als
  `store/current.json.format1` daneben. Koordinator vor den Startern
  aktualisieren; ältere Starter spielen die erste Welt und bekommen „denied“
  mit dem Namen der laufenden Welt, wenn eine andere läuft.

### Gesundheit (nur Admins)

`GET /v1/health` (nur Admins, sonst 403) liefert, wofür man sonst ins Log
schaut; das Fenster zeigt es im Reiter „Zustand“. Der Koordinator rechnet die
Warnungen selbst (Paket `internal/koord/gesundheit`), jede mit Stufe `info`,
`warnung` oder `fehler` und deutschem Text; die Ampel (`status`: `ok`,
`warnung`, `fehler`) ist die höchste Stufe, `info` zählt als OK.

- Werte: Version (`-X main.version`, im Release gesetzt, sonst „dev“) und
  Laufzeit; Speicherplatz des Dateisystems von `data_dir` (frei/gesamt; unter
  Linux per `statfs`, sonst unbekannt) und Größe des Speichers; Weltstand
  (Head, Zeit des letzten Uploads); Host mit Zeit des letzten Lebenszeichens;
  Starter-Version je gesehenem PC (kommt mit Lebenszeichen und Bereitschaft,
  nur im Speicher, nach einem Neustart füllt sie sich neu; ältere Starter
  senden sie nicht); offene Beitrittsanfragen; Wartung; letzte Sicherung
  (`sicherung.json`); Ergebnis des letzten Aufräumlaufs seit dem Start (bei
  Wartung als Probe).
- Warnungen: freier Speicher unter 5 GB (Fehler) bzw. unter 15 % (Warnung);
  letzte Sicherung älter als 7 Tage oder unbekannt (Warnung); Host ohne
  Lebenszeichen seit über 25 s (Warnung) bzw. über 2 Minuten (Fehler); Starter
  älter als `starter:` im Manifest (Hinweis); offene Beitrittsanfragen
  (Hinweis); fehlgeschlagenes Aufräumen (Fehler); Speicher trotz Aufräumen über
  dem Budget (Warnung).

### Reiter „Server“ (Koordinator)

Endpunkte, alle nur für Admins (sonst 403):

- `GET /v1/server/lists` – Whitelist (Mitglieder und Gäste), Ops, Banns,
  `running`.
- `POST /v1/server/lists` mit `{"list": "whitelist"|"ops"|"bans", "action":
  "add"|"remove", "name": …}` – Antwort: deutscher Text und die Listen.
- `GET /v1/server/gamerules`, `POST /v1/server/gamerules` (`{"name", "value"}`)
  und `POST /v1/server/command` (`{"command"}`) – nur bei laufender Welt
  (sonst 409); der Koordinator wartet bis 15 s auf den Host (sonst 504).

Gäste und Banns stehen in `data_dir/serverlisten.json` (0600, atomar; eine
kaputte Datei gilt nicht, die zuletzt gültigen Listen bleiben, Fehler im Log;
`systemctl reload` liest sie neu). Zusammen mit den Mitgliedern und
`roles.json` ist sie die einzige Wahrheit: Die Host-Einstellungen tragen
Gäste in Whitelist/Ops und neu `bans`; der Host schreibt `whitelist.json`,
`ops.json` und `banned-players.json` bei jedem Start neu und gleicht einen
laufenden Server über `members_version` an (`pardon`, `whitelist add`, `op`,
`deop`, `whitelist remove`+`kick`, `ban`).

Befehle, Spielregeln und das Lesen der Spielregeln sind Aufträge im Speicher
des Koordinators (höchstens 8 offen) für den Haupt-Zuschlag eines bereiten
Hosts. Der Host holt sie per Long-Poll (`POST /v1/host/jobs`, bis 20 s) und
meldet das Ergebnis (`POST /v1/host/jobs/result`); beide nur mit gültigem
Zuschlag und PC des Hosts. Der Befehlsfilter (`internal/serverbefehl`,
Positivliste der Vanilla-Befehle von 26.2, höchstens 256 Bytes, keine
Steuerzeichen) läuft im Koordinator und noch einmal beim Host; der Host führt
Aufträge über eine eigene, kurzlebige RCON-Verbindung aus, Antworten werden auf
4 KiB gekürzt. Log-Zeilen: `Admin-Befehl`, `Admin-Befehl abgelehnt`,
`Spielregel ändern`, `Serverliste geändert` / `nicht geändert`.

Ausrollen: erst der Koordinator, dann die Starter. Ein älterer Starter als
Host übernimmt Gäste, aber keine Banns (ein gebannter Gast fällt trotzdem von
der Whitelist) und keine Befehle (die Aufträge laufen dann nach 15 s ab).

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
jede Welt der Gruppe (ihre aktuelle Weltkennung) bleiben:

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

### Mehrere Welten (Koordinator)

Die Weltliste steht in `store/current.json` (Format 2): je Welt Kennung `w<N>`
(nie wiederverwendet), Name, `gamemode`, `difficulty`, die aktuelle
Weltkennung des Speichers (`store_world`) mit Head, die früheren Weltkennungen
(`previous`) und bei gelöschten Welten `deleted`. `worlds/<Weltkennung>/…` und
`blobs/` bleiben unverändert. Der Zuschlag (`lease.json`) trägt die Welt
(`world`); ohne Angabe gilt die erste Welt.

- `GET /v1/worlds` (Mitglieder): `{"worlds": […], "running": "w1"}`; der
  Status trägt dieselbe Liste (`worlds`, `running_world`). `POST /v1/worlds`
  `{"name", "gamemode", "difficulty"}`, `PATCH /v1/worlds/{id}` und
  `DELETE /v1/worlds/{id}?confirm=<Name>` nur für Admins (403 sonst). Löschen
  scheitert mit 409, solange die Welt läuft (Zuschlag, auch zum Hochladen,
  wartender Zuschlag, Zurückholen) und für die letzte Welt. Namen: 1–40
  Zeichen, je Gruppe eindeutig.
- Chat-Verlauf je Welt: `chat_off` (Verlauf aus) und `chat_mib` (Größengrenze
  in MiB, 1–50, 0 oder fehlend = Standard 5) in `POST` und `PATCH`
  (`PATCH` mit `{"chat_off": true, "chat_mib": 10}`), ebenso in der Weltliste;
  beide Felder fehlen im JSON, solange sie dem Standard entsprechen (an,
  5 MiB). Ungültige Größen ergeben 400, Nicht-Admins 403. `GET
  /v1/host/settings` liefert dem Host `chat: {"off", "mib"}` der Welt seines
  Zuschlags. Ein Koordinator vor Version dieser Funktion lehnt die Felder im
  `PATCH` als unbekannt ab: **erst den Koordinator, dann die Starter
  aktualisieren**; ein älterer Starter (ohne die Felder) ändert nichts und
  schreibt die Konfig-Datei der Mod nicht, ein neuer Starter an einem
  älteren Koordinator lässt sie unberührt.
- `POST /v1/play` nimmt `group_world` (ohne Angabe: erste Welt). Eine andere
  Welt als die laufende ergibt `denied` mit Grund, ebenso eine leere Welt für
  Nicht-Admins. `GET /v1/generations/head?world=<id>` liefert den Head einer
  Welt, ohne Angabe den der ersten.
- Aufbewahrung: Jede Welt der Liste wird wie die bisherige aktive Welt
  behandelt; gelöschte Welten zählen als Archive, die beim Budget zuerst
  fallen.
- Zurückholen aus einem Archiv bindet die zugehörige Welt wieder an diese
  Weltkennung; für eine gelöschte Welt ist es 409.
- `wanderwelt-koordinator world activate --world <Weltkennung>` bindet ebenso
  eine archivierte Weltkennung wieder an ihre Welt.

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
- Stände aus einem Archiv: Der neue Stand macht deren Weltkennung wieder zur
  aktuellen ihrer Welt; die bisherige wird Archiv dieser Welt.
- Die Minecraft-Version des Stands muss zum Manifest passen (sonst 409). Ein
  unbekannter Stand ergibt 404.
- Ein ausstehendes Zurückholen steht nur im Speicher; siehe „Koordinator
  aktualisieren“.

### Datenschutz: Kennungen der Mitglieder (Betreiber)

Der Koordinator bildet für jedes Mitglied eine Kennung (`ID` in `GET
/v1/members`, `/v1/status` und weiteren Antworten): Hex von
HMAC-SHA256(Schlüssel, E-Mail in Kleinbuchstaben), auf 16 Zeichen gekürzt. Der
Schlüssel ist eine zufällige Datei `idkey` in `data_dir` (0600, Besitzer
`wanderwelt`), die der Koordinator **beim ersten Start** anlegt; er steht nie
in der Schnittstelle oder im Log. Die Datei gehört zur Sicherung und darf nicht
gelöscht werden (sonst ändern sich alle Kennungen, und die Rückfragen der
Admins lassen sich nicht mehr zuordnen); ist sie kaputt, meldet der
Koordinator „KENNUNGS-SCHLÜSSEL (idkey) NICHT LESBAR“ im Journal und
vergibt bis zum Neustart flüchtige Kennungen, ohne die Datei anzufassen.

`wanderwelt-koordinator mitglied liste` zeigt die Kennung neben der Adresse
(sobald der Schlüssel existiert), und `mitglied entfernen` nimmt statt der
Adresse auch die Kennung. So ordnest du die Kennung, die ein Admin nennt, einer
Adresse zu. Ältere Starter (bis 0.1.9) funktionieren weiter: Die
Mitgliederliste trägt in fremden Zeilen die Kennung im Feld `email`, damit
Rollen und Anfragen über den Pfad weiter gehen (sie zeigen die Kennung dort,
wo bisher die Adresse stand). **Beim Aktualisieren zuerst den Koordinator,
dann die Starter.**

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
  Admins; an der Stelle `{email}` steht die **Kennung** aus der
  Mitgliederliste, siehe „Datenschutz: E-Mail-Adressen“; die API nimmt dort
  nur die Kennung oder die eigene Adresse, nie die eines anderen).
  Entspricht eine Rolle wieder der Konfiguration, fällt der Eintrag
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

### Chat-Verlauf-Mod ins Manifest eintragen

Die Mod `wanderwelt-chatverlauf` (Quelle `mods/chatverlauf`, Elastic License
2.0 wie das Repo) ist eine reine Server-Mod: Spieler brauchen nichts. Es gibt
keine stabile externe URL, die Jar liegt deshalb als `wanderwelt:`-Datei auf
dem Koordinator. Sie ist **nicht** `optional`: Ein Host ohne sie würde den
Chat der Welt nicht mitschreiben; fehlt sie oder passt ihr Hash nicht, startet
der Server nicht, bis das behoben ist.

1. Bauen (JDK 25, im Ordner `mods/chatverlauf`):
   `./gradlew build` (Windows: `gradlew.bat build`) ergibt
   `build/libs/wanderwelt-chatverlauf-26.2-<version>.jar`. Der Gradle-Wrapper
   lädt Gradle und Loom selbst; `build/` und `.gradle/` gehören nie ins
   Repo.
2. Ablegen und Hash bilden wie oben (unter neuem Dateinamen je Version):

   ```
   install -o wanderwelt -g wanderwelt -m 0640 wanderwelt-chatverlauf-26.2-1.1.0.jar /var/lib/wanderwelt/artifacts/
   echo "sha512:$(sha512sum /var/lib/wanderwelt/artifacts/wanderwelt-chatverlauf-26.2-1.1.0.jar | cut -d' ' -f1)"
   ```
3. Eintrag unter `mods:` (Vorlage in `deploy/manifest.example.yaml`), dann
   `systemctl reload wanderwelt-koordinator`:

   ```yaml
   mods:
     - name: wanderwelt-chatverlauf
       url: "wanderwelt:wanderwelt-chatverlauf-26.2-1.1.0.jar"
       hash: "sha512:HASH-DER-ABGELEGTEN-DATEI"
   ```

Fabric API ist Voraussetzung und steht schon im Manifest. Die Jar gilt für
genau eine Minecraft-Version (`"minecraft": "~26.2"`); beim Versionswechsel
wird sie zusammen mit den anderen Mods ersetzt. Ab Version 1.1.0 kennt die
Mod den Schalter „Verlauf aus“; der Chat-Verlauf lässt sich dann je Welt im
Fenster einstellen (siehe „Chat-Verlauf“). Die Standardwerte (5 MiB, 50
Zeilen, Zeit im Tooltip) gelten ohne weitere Konfig-Datei; wer abweichende
Werte für alle Hosts will, verteilt `config/wanderwelt-chatverlauf.json` über
`configs` (nicht als `enforce` mit `aktiv` oder `max_bytes`, denn die
Gruppeneinstellung schreibt diese beiden Schlüssel bei jedem Start danach
ohnehin neu; `wiedergabe_zeilen`, `zeit` und `segment_bytes` bleiben).

### Konfigdateien im Manifest (`configs`)

Der optionale Abschnitt `configs` verteilt Konfigdateien für den
Server-Ordner, die auf allen Hosts gleich sein sollen (zum Beispiel
`config/viper_spawn_control.json` der Server-Mod `viper-spawn-control`):

```yaml
configs:
  - path: config/viper_spawn_control.json   # relativ zum Server-Ordner
    url: "wanderwelt:viper_spawn_control.json"
    hash: "sha256:HEX-AUS-SHA256SUM"
    mode: enforce                           # oder default; ohne Angabe enforce
```

- `url` und `hash` wie bei den Mods (`https://…` oder `wanderwelt:<datei>`,
  `sha256:` oder `sha512:`). Die Datei ablegen und den Hash bilden wie oben.
- `mode: enforce` – der Starter schreibt die Datei bei jedem Start des
  Servers neu, wenn sie fehlt oder ihr Hash abweicht (atomar über Temp-Datei
  und Umbenennen). Lokale Änderungen gehen dabei verloren.
- `mode: default` – der Starter legt die Datei nur an, wenn sie fehlt; der
  Host darf sie danach lokal ändern.
- `path` ist streng begrenzt: nur unterhalb von `config/`, mit `/` getrennt;
  kein absoluter Pfad, kein `..`, keine Laufwerksbuchstaben, UNC-Pfade oder
  Datenströme (`:`), höchstens 200 Zeichen; Endung nur `json`, `toml`,
  `yaml`, `yml`, `properties`, `txt`, `cfg` oder `conf`. Dateien über 1 MiB
  und mehr als 64 Einträge gehen nicht.
- Ein ungültiger Eintrag lässt den Koordinator das Manifest beim Laden
  ablehnen (wie jeden anderen Manifest-Fehler: beim Neuladen gilt der
  bisherige Stand weiter, im Journal steht „VERSIONS-MANIFEST NICHT NEU
  GELADEN“ mit dem Eintrag und dem Grund). Der Koordinator prüft außerdem
  `wanderwelt:`-Dateien der Konfigs wie die anderen Artefakte.
- Der Starter wendet die Konfigs nach dem Laden der Mods und vor dem
  Serverstart an, nur im Server-Ordner dieses Hosts. Scheitert eine (Download,
  Hash), steht das auf Deutsch im Log; der Server startet trotzdem.
- Der Koordinator muss zuerst aktualisiert werden: Ein älterer kennt den
  Schlüssel `configs` nicht und lädt so ein Manifest nicht.

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


### Gruppeninfo und Discord-Schalter

In `koordinator.yaml`:

```yaml
gruppe:
  titel: Unsere Wandergruppe                # höchstens 80 Zeichen
  beschreibung: Gemeinsame Survival-Welt.   # höchstens 400 Zeichen
beitritt_ueber_discord: true                # false: Knopf „Beitreten“ in Discord entfällt
```

Titel und Beschreibung sind reiner Text (keine Steuerzeichen, keine
Zeilenumbrüche). `GET /v1/info` liefert sie jedem gültigen tunnel-Konto, auch
Nicht-Mitgliedern: `{"title": …, "description": …, "members": N, "you":
"mitglied"|"angefragt"|"keiner"}` (keine Namen, keine Adressen). Mit
`beitritt_ueber_discord: false` setzt der Koordinator `no_discord_join: true`
in `GET /v1/status`; die Starter lassen dann den Knopf „Beitreten“ in der
Discord-Anzeige weg (der Download-Knopf bleibt). Beides gilt nach
`systemctl restart wanderwelt-koordinator`, nicht schon nach `reload`.
Details zum Knopf: [`docs/discord-einrichtung.md`](docs/discord-einrichtung.md).

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
`POST /v1/join-requests/{email}/accept` bzw. `/decline` (204; `{email}` ist die
Kennung der Anfrage; die Liste nennt Minecraft-Name und Kennung, nie die
Adresse). Annehmen trägt
das Konto in `members.json` ein und erhöht `MembersVersion`: Der laufende Host
nimmt den Spieler ohne Neustart in die Whitelist auf. Die tunnel-Freischaltung
des Kontos bleibt Sache des Admins im tunneld-Admin-Panel. Anfragen und
angenommene Mitglieder überstehen einen Neustart; beim Neuladen werden die
Dateien neu gelesen.

### Benachrichtigungen (Discord-Webhook)

Der Koordinator kann Ereignisse der Gruppe in einen Discord-Kanal schreiben
(Übersicht für Mitglieder: [Benachrichtigungen](#benachrichtigungen)). Das ist
**standardmäßig aus** und wird vom Admin des Koordinators einmal eingerichtet:

1. In Discord den Kanal öffnen, in den geschrieben werden soll: **Kanal →
   Integrationen → Webhooks → Neuer Webhook**, Name und Bild nach Wunsch, dann
   **„Webhook-URL kopieren“**. (Dazu brauchst du auf dem Discord-Server das
   Recht „Webhooks verwalten“.) Die Adresse sieht so aus:
   `https://discord.com/api/webhooks/<Nummer>/<langer Schlüssel>`.
2. Auf dem Server in `/etc/wanderwelt/koordinator.yaml` eintragen (Beispiel:
   `deploy/koordinator.example.yaml`):

   ```yaml
   discord_webhook: https://discord.com/api/webhooks/123456789012345678/xxxxxxxx   # Geheimnis!
   # Optional: einzelne Ereignisse umstellen (true/false); fehlende gelten nach Standard.
   discord_ereignisse:
     spieler: true           # Beitritt/Verlassen (Standard an)
     niemand_hostet: false   # Standard aus
     # weitere: host_startet, welt_online, wartung, uebergabe (alle Standard an)
   ```

   Die Datei sollte nur für root und die Gruppe `wanderwelt` lesbar sein
   (`chown root:wanderwelt`, `chmod 0640`).
3. `systemctl restart wanderwelt-koordinator` (der Webhook gilt erst nach
   einem Neustart, nicht schon nach `reload`). Eine ungültige Adresse verhindert
   den Start mit einer Fehlermeldung (die Adresse selbst steht nie darin).

**Die Webhook-Adresse ist ein Geheimnis:** Wer sie kennt, kann im Kanal als
„wanderwelt“ schreiben. Gib sie nicht weiter, poste sie nicht in Chats oder
Tickets und checke sie nicht in ein Repository ein. Der Koordinator schreibt
sie nie in Log oder Fehlermeldungen, und weder Starter noch Fenster erfahren
sie; sie sehen nur `discord_channel: true` im Status („Discord-Kanal der Gruppe:
an“). Ist sie doch einmal bekannt geworden: in Discord den Webhook löschen und
einen neuen anlegen, dann die Konfiguration ändern. Erlaubt sind nur Adressen
von `discord.com`/`discordapp.com` (auch `ptb.`/`canary.`).

Wie es sich verhält:

- Der Koordinator vergleicht alle 5 Sekunden den Status. Nach einem Neustart
  des Koordinators gibt es 30 Sekunden lang keine Nachrichten (Spielerliste
  und „bereit“ kommen erst mit dem nächsten Lebenszeichen des Hosts zurück);
  es werden also nur Änderungen nach dem Start gemeldet, nie eine Flut alter
  Ereignisse.
- Höchstens eine Nachricht alle 5 Sekunden, höchstens 20 warten (bei Überlauf
  fallen die ältesten weg). Antwortet Discord mit 429 oder einem 5xx-Fehler,
  wartet der Koordinator die Zeit aus `Retry-After` (höchstens 2 Minuten) und
  versucht es bis zu dreimal. Der Koordinator wartet nie auf Discord; Fehler
  stehen nur im Debug-Log (Umgebungsvariable `WANDERWELT_DEBUG=1` in der
  systemd-Unit, dann `journalctl -u wanderwelt-koordinator`).
- Nachrichten sind reiner Text mit `allowed_mentions: {"parse": []}`; Namen,
  Titel (`gruppe.titel`) und Wartungsgrund werden gesäubert (Markdown, Links und
  Erwähnungen unschädlich gemacht, Länge begrenzt).
- Windows-Hinweise für Beitrittsanfragen gibt es nur für Admins im Starter,
  nie auf Discord.

**Ausprobieren:** Mit eingetragenem Webhook und neu gestartetem Koordinator
auf einem PC „Spielen“ klicken: Nach etwa einer Minute (Start des Servers)
stehen im Kanal „… hostet jetzt“ und „Die Welt ist online“. Ein weiterer
Spieler, der beitritt, erscheint nach rund 10 Sekunden. Einen Webhook kannst
du auch ohne wanderwelt prüfen (die Adresse dabei nicht in geteilte
Shell-Verläufe schreiben):

```sh
curl -H "Content-Type: application/json" \
  -d '{"content":"Test","allowed_mentions":{"parse":[]}}' "<Webhook-Adresse>"
```

### Statistik

`GET /v1/stats` (jedes Mitglied, sonst 403) liefert je Mitglied (Minecraft-Name,
nie die E-Mail) `play_s`, `host_s`, `host_sessions` und `host_share` (Anteil an
der Hostzeit aller Mitglieder, 0 bis 1), für `total` und `last_30_days`
(heute und die 29 Tage davor, Tage nach UTC); `since` nennt, seit wann gezählt
wird. Der Koordinator zählt bei jedem Lebenszeichen (alle 10 s) des
Haupt-Zuschlags: Hostzeit für den Host, Spielzeit für jeden Namen aus der
Spielerliste, dazu je Zuschlag eine Sitzung. Ungenauigkeit: bis etwa 10 s je
Beitritt, Austritt und Hostbeginn (Trapezregel, im Mittel genau). Es zählt nie
mehr als zwischen zwei Lebenszeichen höchstens 30 s auseinander liegt: Stirbt
ein Host, endet die Zählung mit seinem letzten Lebenszeichen (die 90 s bis zum
Ablauf des Zuschlags zählen nicht); nach einem Neustart des Koordinators
beginnt sie mit dem nächsten Lebenszeichen neu (die Zeit dazwischen fehlt, die
Sitzung zählt nicht doppelt). Bei einer Übergabe zählt der wartende Zuschlag
nicht, erst der beförderte ab seinem ersten Lebenszeichen als Haupt-Zuschlag.
Ein Zuschlag nur zum Hochladen zählt nicht.

Die Zahlen liegen in `<data_dir>/stats.json` (atomar geschrieben, höchstens
einmal je Minute und beim Beenden; ein Absturz verliert bis zu eine Minute):
Tageswerte je Mitglied für die letzten 45 Tage, ältere Tage nur als Summe je
Mitglied, damit die Datei klein bleibt (unter 100 kB). Schlüssel ist die
E-Mail (klein), damit eine Namensänderung die Zahlen nicht trennt; die Datei
verlässt den Server nicht. Ausgetretene Mitglieder bleiben in der Datei, aber
nicht in der Antwort und nicht im Anteil. Eine kaputte Datei wird als
`stats.json.kaputt-<Zeit>` beiseitegelegt, die Zählung beginnt leer. Löschen
der Datei setzt die Statistik zurück (bei gestopptem Koordinator).

### Karte (Admin)

Der Koordinator kann eine Karte der Oberwelt jeder Welt erzeugen und den
Mitgliedern ausliefern (Karten-Reiter des Starters; Vertrag
Eingeschaltet wird sie mit dem Abschnitt `karte:` in der
Koordinator-Konfiguration; Pflicht ist nur `binary`, der absolute Pfad des
Programms `karte` (Rust, Release-Build von `tools/karte-rs`, nur auf den Server
kopieren, kein Dienst):

```yaml
karte:
  binary: /opt/wanderwelt/karte
  # colors: /opt/wanderwelt/farben.json   # Farbtabelle (JSON), Standard: eingebaute
  nice: 10             # 0–19, Priorität des Programms (nur Linux, über nice -n)
  threads: 1           # 1–4
  min_interval: 30m    # Mindestabstand zweier Läufe je Welt, 1m–24h
  max_bytes: 1GiB      # größte erlaubte Karte je Welt (1MiB–1TiB, auch reine Bytes)
```

- **Wann gerendert wird.** Der Dienst prüft alle 20 s (und bei jedem
  angenommenen Stand), ob der Head einer Welt neuer ist als der gerenderte
  Kartenstand, und rendert dann höchstens alle `min_interval`; einmal zusätzlich
  gleich nach dem Schluss-Stand eines Hosts (Rückgabe des Zuschlags), ohne
  diesen Abstand. Nie während einer Wartung (ein laufender Lauf wird
  abgebrochen, nach dem Ende der Wartung geht es weiter), nie im Pfad von
  Lebenszeichen oder Commit (eigene Goroutine, Fehler werden gefangen).
- **Woraus.** Nur die Regionsdateien der Oberwelt
  (`dimensions/minecraft/overworld/region/r.X.Z.mca`) aus dem Speicher. Der
  Dienst vergleicht die Hashes der Regionsdateien des Heads mit denen des
  zuletzt gerenderten Stands (gespeichert in `state.json` im Kartenordner), setzt
  nur die geänderten Regionen samt ihren Nachbarn aus den Segment-Blobs
  zusammen (Temp-Ordner `data_dir/karte/.work-<welt>`, danach gelöscht) und
  ruft `karte pyramid … --only-regions …` auf; der erste Lauf und jeder Lauf, nach
  dem eine Region verschwunden ist, rendert alles. Zusammengesetzt wird ein
  paar Dutzend MB je Lauf (ganze Welt: Größe der Regionsdateien, z. B.
  685 MB), gelesen werden nur Blobs, nichts wird in `blobs/` geschrieben.
- **Wo.** `data_dir/karte/<welt>/` (`<welt>` = Kennung der Welt der Gruppe,
  `w1`, …) mit `karte.json`, `tiles/<z>/<x>/<y>.png` und `state.json`; immer nur
  der neueste Stand. Der Lauf schreibt in `data_dir/karte/.staging-<welt>`; ist
  er gut (Manifest gültig, jede Kachel da, Größe ≤ `max_bytes`), wird der Ordner
  gegen den alten getauscht (zwei Umbenennungen unter Sperre; ein Absturz
  dazwischen wird beim Start zurückgenommen). Scheitert ein Lauf oder bricht
  er ab, bleibt der alte Stand unverändert und wird weiter ausgeliefert.
- **Neustart.** Vorhandene Karten werden immer ausgeliefert. Fehlt `karte:`
  oder das Programm, ist nur die Erzeugung aus (ein Hinweis im Log).
- **Endpunkte** (nur Mitglieder, sonst 403; höchstens 50 Anfragen/s im Mittel
  je Mitglied, kurz bis 150, sonst 429): `GET /v1/karte/<welt>/manifest.json`
  und `GET /v1/karte/<welt>/t/<z>/<x>/<y>.png` (ETag = SHA-256, `If-None-Match`
  → 304, `Cache-Control: private, max-age=60`, 404 für Fehlendes, 400 für
  ungültige Zahlen). Der Status nennt je Welt `karte` (Stand, Zahl der Kacheln,
  Stufen, Grenzen der Stufe 0).
- **Prüfen.** `GET /v1/health` (Admin) hat den Block `karte` (Erzeugung an,
  Zeit und Dauer des letzten Laufs, Größe, Kacheln, letzter Fehler); ein Fehler
  ist eine Warnung. Das Programm schreibt Fehler auf stderr; der Dienst loggt
  Exit-Code und die letzten 1500 Zeichen (nie Tokens oder Umgebung). Nach einem
  Fehler wartet er 10 Minuten bis zum nächsten Versuch.
- **Last auf einem kleinen Server (Schätzung).** Ganze Oberwelt der Testwelt (150
  Regionen, 685 MB): etwa 15–30 s CPU, ~40 MB RAM beim Programm, ~20 MB Karte;
  eine Nachführung (wenige Regionen mit Nachbarn) unter einer Sekunde bis
  wenigen Sekunden. Die Karte hängt nicht von der Zahl der Spieler ab.

### Weitere Gruppe anlegen (Betreiber)

Neue Gruppen legt **nur der Betreiber des Servers** an, nie ein Nutzer. Eine
Gruppe ist ein eigener Koordinator-Dienst hinter demselben tunneld-Server. Je
Gruppe nötig:

1. **tunneld**: eine zweite Zeile unter `services` mit eigenem Namen, eigener
   Subdomain, eigenem `upstream` (Loopback, eigener Port, siehe unten), eigenem
   Dienst-Geheimnis (`secret_file`) und eigenem öffentlichen TCP-Port für
   Minecraft (`tcp_ports`). Dann tunneld neu laden (SIGHUP) bzw. neu starten,
   wie in der tunneld-Dokumentation beschrieben.
2. **Koordinator-Konfiguration**: Kopie von `deploy/koordinator.example.yaml`
   als `/etc/wanderwelt/koordinator-<name>.yaml` (Rechte wie bei der ersten:
   `root:wanderwelt`, `0640`) mit eigenem `listen` (anderer Loopback-Port),
   eigenem `data_dir` (etwa `/var/lib/wanderwelt-<name>`, nie dasselbe
   Verzeichnis), `minecraft_address` und `port_holder.port`/`secret_file` der
   neuen Dienstzeile, eigenem `manifest_file` und `provisioning.coordinator_url`
   (Subdomain der neuen Gruppe); dazu `gruppe:` mit Titel und Beschreibung und
   die Mitglieder der Gruppe.
3. **Manifest und Artefakte**: ein eigenes Manifest
   (`/etc/wanderwelt/manifest-<name>.yaml`); die Artefakte liegen im
   `artifacts/` des neuen `data_dir` (siehe „Manifest aktualisieren“).
4. **systemd-Unit**: `deploy/wanderwelt-koordinator.service` als
   `wanderwelt-koordinator-<name>.service` kopieren und anpassen:
   `StateDirectory=wanderwelt-<name>` und `--config
   /etc/wanderwelt/koordinator-<name>.yaml`; danach `systemctl daemon-reload
   && systemctl enable --now wanderwelt-koordinator-<name>`. Das Programm
   (`/opt/wanderwelt/wanderwelt-koordinator`) lässt sich gemeinsam nutzen;
   beim Aktualisieren alle Instanzen neu starten.
5. Mitglieder der neuen Gruppe wie gewohnt anlegen (`mitglied neu … --config
   /etc/wanderwelt/koordinator-<name>.yaml`).

Soll die Gruppe im Entdecken-Reiter erscheinen, trägt der Betreiber sie
zusätzlich in der tunneld-Konfiguration als gelistet ein (Standard: nicht
gelistet).

## Lizenz

Elastic License 2.0; siehe [LICENSE](LICENSE) und [NOTICE](NOTICE).
Original: https://github.com/Bazinga0o0/wanderwelt
