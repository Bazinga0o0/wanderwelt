# Discord-Anzeige (Rich Presence) – Einrichtung

Nachschlagewerk. Die Einrichtung ist erledigt: Anwendung „wanderwelt“, Client-ID
und Art Asset stehen im Discord Developer Portal und im Code.

## Was angezeigt wird

Nur beim Hosten und Mitspielen (Aktivität wird sonst gelöscht):

- Zeile 1: „wanderwelt“, Zeile 2: „mit N Freunden“ / „mit 1 Freund“ /
  „ohne Freunde :(“ (andere Spieler auf dem Server, keine Namen)
- Spielzeit ab Hosten/Mitspielen, Symbol als großes Bild, Party „N von M“
  (M immer N+1; nur Anzeige, ohne Geheimnis), zwei Knöpfe: „Beitreten“
  (`wanderwelt://beitreten`, nur wenn der Admin den Beitritt über Discord
  erlaubt) und „wanderwelt herunterladen“ (GitHub-Releases, immer)
- Läuft Discord nicht, passiert nichts (stilles Wiederverbinden, höchstens
  alle 60 s; Fehler nur im Log).

## Wo im Code

- `internal/starter/discord/discord.go`: Konstante `ClientID` (aktuell
  `1575453522638544927`; "" oder "0" schaltet die Anzeige ab), `LargeImage`
  (Schlüssel des Art Assets), Knopf-Text und -Adresse (`JoinURL`,
  Rückfall `JoinPage`).
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

1. „Beitreten“ führt direkt auf `wanderwelt://beitreten`
   (`discord.JoinURL`, ohne Gruppendaten); Discord nimmt das Schema an (am
   2026-11-26 geprüft; ob es beim Empfänger geöffnet wird, ist am zweiten
   Account zu prüfen). **Rückfall**, falls das nicht klappt: die Seite
   https://bazinga0o0.github.io/wanderwelt/beitreten/ (Konstante
   `discord.JoinPage`, wird derzeit nicht gesendet; für den Rückfall in
   `proto.go` `JoinURL` durch `JoinPage` ersetzen).
2. „wanderwelt herunterladen“ führt auf die neueste GitHub-Release (immer da).

**Admin-Schalter**: `beitritt_ueber_discord: false` in der Koordinator-Konfig
(Standard `true`) lässt den Knopf „Beitreten“ weg. Der Koordinator meldet das
im Status als `no_discord_join: true` (fehlendes Feld = erlaubt); der Starter
sendet den Knopf nur, wenn das Feld nicht gesetzt ist. Ohne Status (noch nicht
abgerufen, älterer Koordinator) bleibt der Knopf an. Der Schalter gilt nach
einem Neustart des Koordinators und erreicht die Starter mit dem nächsten
Status-Abruf.

**Seite** (`docs/beitreten/index.html`, statisch, eine Datei, kein Tracking,
keine externen Ressourcen): Sie öffnet `wanderwelt://beitreten` (ohne Argument).
Verliert die Seite innerhalb von ca. 3 s nicht den Fokus, sagt sie „Hat sich
nichts geöffnet?“ mit Download, Kurzanleitung, dem Hinweis „Du brauchst einen
Gruppencode“ und „Nochmal versuchen“. Wurde wanderwelt geöffnet, versucht sie
`window.close()` und zeigt sonst „Du kannst diesen Tab schließen.“ Ohne
JavaScript bleiben Download-Link und Text.

**GitHub Pages** einschalten (öffentliches Repo): Settings → Pages → Branch
`main`, Ordner `/docs`. Der Export ins öffentliche Repo behält `docs/beitreten` (und
diese Datei); interne Unterlagen bleiben privat.

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
