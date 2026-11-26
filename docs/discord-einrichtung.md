# Discord-Anzeige (Rich Presence) – Einrichtung

Nachschlagewerk. Die Einrichtung ist erledigt: Anwendung „wanderwelt“, Client-ID
und Art Asset stehen im Discord Developer Portal und im Code.

## Was angezeigt wird

Nur beim Hosten und Mitspielen (Aktivität wird sonst gelöscht):

- Zeile 1: „wanderwelt“, Zeile 2: „mit N Freunden“ / „mit 1 Freund“ /
  „ohne Freunde :(“ (andere Spieler auf dem Server, keine Namen)
- Spielzeit ab Hosten/Mitspielen, Symbol als großes Bild, Party „N von M“
  (M immer N+1; nur Anzeige, ohne Geheimnis), zwei Knöpfe: „Beitreten“
  (Seite docs/beitreten) und „wanderwelt herunterladen“ (GitHub-Releases)
- Läuft Discord nicht, passiert nichts (stilles Wiederverbinden, höchstens
  alle 60 s; Fehler nur im Log).

## Wo im Code

- `internal/starter/discord/discord.go`: Konstante `ClientID` (aktuell
  `1575453522638544927`; "" oder "0" schaltet die Anzeige ab), `LargeImage`
  (Schlüssel des Art Assets), Knopf-Text und -Adresse.
- `internal/starter/app/anzeige.go`: Anbindung an den Starter.

## Einmalige Schritte (falls neu aufzusetzen)

1. https://discord.com/developers/applications → *New Application*, Name
   „wanderwelt“ (der Name erscheint als „Spielt wanderwelt“).
2. Symbol erzeugen: `go run ./tools/makeicon -png wanderwelt-discord-1024.png`
   (1024x1024).
3. *Rich Presence → Art Assets → Add Image(s)*: die PNG hochladen. Der
   **Dateiname bestimmt den Schlüssel**, aktuell `wanderwelt-discord-1024`; er
   muss mit `LargeImage` im Code übereinstimmen. Nach dem Hochladen speichern;
   neue Bilder brauchen einige Minuten.
4. *General Information → Application ID* kopieren und als `ClientID` in
   `internal/starter/discord/discord.go` eintragen (kein Geheimnis).
5. Starter neu bauen; mit Discord-Desktop auf demselben PC testen (nicht im
   Browser). „Aktivitätsstatus anzeigen“ muss in Discord an sein.

## Beitreten: Knopf, Seite, Gruppencode

Discord lehnt SET_ACTIVITY ab, wenn ein Beitritts-Geheimnis (`secrets.join`)
und Knöpfe zugleich gesendet werden („secrets cannot currently be sent with
buttons“). Darum gibt es kein Discord-Beitreten (kein `secrets`, kein
`ACTIVITY_JOIN`-Abo), sondern zwei Knöpfe:

1. „Beitreten“ führt auf https://bazinga0o0.github.io/wanderwelt/beitreten/
   (`discord.JoinPage`, ohne Fragment, ohne Gruppendaten).
2. „wanderwelt herunterladen“ führt auf die neueste GitHub-Release.

**Seite** (`docs/beitreten/index.html`, statisch, eine Datei, kein Tracking,
keine externen Ressourcen): Sie öffnet `wanderwelt://beitreten` (ohne Argument).
Verliert die Seite innerhalb von ca. 3 s nicht den Fokus, sagt sie „Hat sich
nichts geöffnet?“ mit Download, Kurzanleitung, dem Hinweis „Du brauchst einen
Gruppencode“ und „Nochmal versuchen“. Wurde wanderwelt geöffnet, versucht sie
`window.close()` und zeigt sonst „Du kannst diesen Tab schließen.“ Ohne
JavaScript bleiben Download-Link und Text.

**GitHub Pages** einschalten (öffentliches Repo): Settings → Pages → Branch
`main`, Ordner `/docs`. `tools/export-public.sh` behält `docs/beitreten` (und
diese Datei); nur STAND.md und `docs/superpowers` bleiben privat.

**Starter**: trägt `wanderwelt://` einmalig in HKCU ein (ohne Adminrechte, nur
aus dem Installationsort, `winsys.RegisterURLProtocol`). Der Eintrag
`discord-<ClientID>` aus der ersten Fassung entfällt; ein alter Eintrag bleibt
wirkungslos liegen. Das Argument von `wanderwelt://…` wird nie ausgewertet
(jede Webseite kann es auslösen, ein gefälschter Link kann so nichts
unterschieben):

- Läuft wanderwelt mit Gruppe: Die Instanz öffnet nur das Fenster (höchstens
  alle 5 s); eine vorhandene Gruppe wird nie ersetzt. Der Aufruf geht über die
  Steuerung (`/v1/beitreten`) an die laufende Instanz; sonst startet der
  Starter und öffnet das Fenster.
- Ohne Gruppe fragt die Einrichtung (auch beim normalen ersten Start ohne
  Gruppendatei und ohne Gruppen-Build) nach dem **Gruppencode** oder einer
  Gruppendatei. Der Code (`w1.…`, höchstens 128 Zeichen, nur Base64url,
  `group.DecodeJoin`) enthält nur die öffentlichen Adressen, nie einen Token.
  Die Adressen werden zur Bestätigung gezeigt; erst danach läuft die
  Einrichtung wie beim Gruppen-Build (Konto auf `web_url`, eigenen Token
  einfügen, Beitrittsanfrage, Admin nimmt an). `gruppe.yaml` entsteht ohne
  Token.

**Weitergeben**: Im Fenster (Reiter Start) kopiert „Gruppencode kopieren“ den
Code aus der vorhandenen Gruppen-Konfiguration (jedes Mitglied). Hinweis:
nur an Leute geben, die mitspielen sollen; der Admin muss sie noch annehmen.

**Tests**: Mit einem zweiten Windows-Konto/PC: Knopf in Discord klicken, Seite
öffnet wanderwelt (Browser fragt eventuell nach), ohne Installation erscheint der
Download-Hinweis. Im Browser noch nicht getestet.
