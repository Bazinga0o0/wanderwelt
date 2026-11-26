# Discord-Anzeige (Rich Presence) – Einrichtung

Nachschlagewerk. Die Einrichtung ist erledigt: Anwendung „wanderwelt“, Client-ID
und Art Asset stehen im Discord Developer Portal und im Code.

## Was angezeigt wird

Nur beim Hosten und Mitspielen (Aktivität wird sonst gelöscht):

- Zeile 1: „wanderwelt“, Zeile 2: „mit N Freunden“ / „mit 1 Freund“ /
  „ohne Freunde :(“ (andere Spieler auf dem Server, keine Namen)
- Spielzeit ab Hosten/Mitspielen, Symbol als großes Bild, Party „N von M“
  (M immer N+1, damit „Beitreten“ nie als voll gesperrt ist), Knöpfe „Beitreten“
  und „wanderwelt herunterladen“ (GitHub-Releases)
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

## Beitreten-Knopf

- Die Aktivität trägt ein Beitritts-Geheimnis (`w1.…`, höchstens 128 Bytes) mit
  den öffentlichen Gruppen-Adressen (coordinator, tunnel_server, web_url,
  token_url), nie einen Token (`internal/starter/group/joinsecret.go`).
- Zeigt Discord Beitreten und Download nicht zugleich, in
  `internal/starter/discord/discord.go` `JoinEnabled = false` setzen: dann bleibt
  nur der Download-Knopf.
- Der Starter trägt `discord-<ClientID>://` einmalig in HKCU ein (ohne
  Adminrechte, nur aus dem Installationsort), damit Discord ihn starten kann.
- Klick bei vorhandener Gruppe: nur das Fenster öffnet sich, die Gruppe wird nie
  ersetzt. Ohne Gruppe (erste Einrichtung): Dialog mit sichtbaren Adressen, erst
  nach Bestätigung läuft die Einrichtung wie beim Gruppen-Build (Konto, Token,
  Beitrittsanfrage); `gruppe.yaml` entsteht ohne Token.
- Test: zweiter Discord-Account (Beitreten-Knopf erscheint nicht beim eigenen
  Profil).
