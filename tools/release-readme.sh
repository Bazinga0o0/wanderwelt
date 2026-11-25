#!/bin/sh
# Baut bin/release/README.md für das Windows-zip: Titel, dann der Abschnitt
# „## Für Mitspieler“ aus README.md (ohne die Überschrift und die Leerzeilen
# davor, bis zur nächsten „## “-Überschrift), am Ende Link und Lizenz.
# Fehlt der Abschnitt, scheitert das Release. Läuft als before-Hook von
# GoReleaser; lokal aus Git Bash aufrufen (braucht sh und awk).
set -e
mkdir -p bin/release
awk '/^## / { if (drin) exit; if (/^## Für Mitspieler$/) drin = gefunden = 1; next } drin && (NF || begonnen) { begonnen = 1; print } END { if (!gefunden) exit 1 }' README.md > bin/release/abschnitt.md
{
  printf '# wanderwelt\n\n'
  cat bin/release/abschnitt.md
  printf '\n---\n\nProjekt: https://github.com/Bazinga0o0/wanderwelt · Lizenz: Elastic License 2.0 (siehe LICENSE und NOTICE)\n'
} > bin/release/README.md
