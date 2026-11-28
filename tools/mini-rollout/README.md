# Updates auf dem Koordinator-Server vormerken

Ein Update (Koordinator-Programm, Starter-Exe, Manifest, Konfiguration) wird auf
dem Server **vorgemerkt** und erst eingespielt, wenn seit 5 Minuten niemand mehr
hostet. Manifest und neue Version ändern sich dabei zusammen, sodass kein
Mitglied ein Manifest sieht, dessen Datei fehlt.

Einmalig (und nach Änderungen am Skript): `tools/mini-rollout/installieren.sh`
(legt `/usr/local/sbin/wanderwelt-rollout` und den Timer `wanderwelt-rollout.timer`
an; der Timer prüft alle 30 s).

Vormerken, etwa nach einem Release:

    tools/mini-rollout/vormerken.sh 0.1.10 \
      --koordinator wanderwelt-koordinator-0.1.10-linux-arm64 \
      --artifact wanderwelt-0.1.10.exe --starter 0.1.10

Optional `--manifest-py DATEI` / `--config-py DATEI` (Skript „python3 x.py ALT NEU“
für Manifest bzw. `koordinator.yaml`), weitere `--artifact`-Dateien.

Auf dem Server: `wanderwelt-rollout status` (wer hostet, seit wann Ruhe, Warteschlange,
Log) und `wanderwelt-rollout cancel <name>`.

Ablauf beim Einspielen: Prüfsummen → neue Manifest/Konfiguration berechnen →
Dateien sichern → Artefakte ablegen → erneut prüfen, ob gehostet wird → bei Programm
oder Konfiguration: Dienst anhalten, Datensicherung (`sicherung`, Blobs per Hardlink),
einspielen, starten; nur Manifest: `systemctl reload`. Danach Gesundheitsprüfung
(läuft, antwortet, kein Neustart-Zähler); sonst Zurückrollen aus
`/var/backups/wanderwelt/rollout-<name>-<Zeit>` und Eintrag nach `failed/`. Spätere
Einträge warten dann. Es bleiben die 3 neuesten Sicherungen.

Grenzen: Der tunneld (`hide_client_ip`) gehört nicht dazu, er wird getrennt
eingespielt. „Niemand hostet“ ist ein gültiger Host-Lease in `lease.json`; wer nur
gerade mitspielt, ohne dass gehostet wird, gibt es nicht.
