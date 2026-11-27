# Folge der Delta-Messungen an der Probe-Karte (nach dem Erstlauf p1):
#  p2  zweiter Lauf ohne Änderung
#  (Kopie der Karte ohne tiles/0 als „neuer Host“ -> probe\sync)
#  p3a nur Änderungszeit einer Region
#  p3b Zeitstempel von 64 Chunks (Inhalt gleich)
#  p3c 64 Chunks gelöscht (echte Inhaltsänderung)
#  s1  „neuer Host“ rendert dieselben Änderungen ohne Hires-Kacheln
# Nach jedem Lauf Hash-Liste und Vergleich mit der vorigen.
param(
    [string] $Probe = 'C:\temp\ww-karte\probe',
    [string] $Ergebnis = 'C:\temp\ww-karte\ergebnis',
    [string] $Jar = 'C:\temp\ww-karte\bluemap\bluemap-5.29-cli.jar'
)
$ErrorActionPreference = 'Stop'
$hier = $PSScriptRoot
$karte = Join-Path $Probe 'lauf\web\maps\overworld'
$region = Join-Path $Probe 'welt\world\dimensions\minecraft\overworld\region'
function Lauf($name, $arbeit) {
    & (Join-Path $hier 'messen.ps1') -Name $name -Jar $Jar -Arbeit $arbeit -Ergebnis $Ergebnis -Xmx 1g -MaxMinuten 60 -Extra '-r -v 26.2' | Out-Null
    $j = Get-Content (Join-Path $Ergebnis "$name.json") -Raw | ConvertFrom-Json
    "== $name : $($j.dauer_s) s, CPU $($j.cpu_s) s, WS $($j.spitze_ws_mb) MB"
}
function Hashes($name, $wurzel) { & (Join-Path $hier 'hashliste.ps1') -Wurzel $wurzel -Aus (Join-Path $Ergebnis "hash-$name.csv") | Out-Null }
function Vergleich($a, $b) { & (Join-Path $hier 'hashliste.ps1') -Vergleich (Join-Path $Ergebnis "hash-$a.csv"), (Join-Path $Ergebnis "hash-$b.csv") }

Hashes 'p1' $karte
Lauf 'p2-ohne-aenderung' (Join-Path $Probe 'lauf'); Hashes 'p2' $karte; Vergleich 'p1' 'p2'

# „neuer Host“: Karte ohne Hires-Kacheln, eigene config/data
$sync = Join-Path $Probe 'sync'
if (Test-Path $sync) { Remove-Item -Recurse -Force $sync }
New-Item -ItemType Directory (Join-Path $sync 'data'), (Join-Path $sync 'web\maps') | Out-Null
Copy-Item -Recurse (Join-Path $Probe 'lauf\config') (Join-Path $sync 'config')
Copy-Item (Join-Path $Probe 'lauf\data\minecraft-client-26.2.jar') (Join-Path $sync 'data')
Copy-Item -Recurse $karte (Join-Path $sync 'web\maps\overworld')
Remove-Item -Recurse -Force (Join-Path $sync 'web\maps\overworld\tiles\0')
"Sync-Kopie ohne tiles/0 angelegt"

& (Join-Path $hier 'region-anfassen.ps1') -Datei (Join-Path $region 'r.-2.1.mca') -Modus zeit
Lauf 'p3a-nur-mtime' (Join-Path $Probe 'lauf'); Hashes 'p3a' $karte; Vergleich 'p2' 'p3a'

& (Join-Path $hier 'region-anfassen.ps1') -Datei (Join-Path $region 'r.-2.1.mca') -Modus stempel -Anzahl 64
Lauf 'p3b-64-stempel' (Join-Path $Probe 'lauf'); Hashes 'p3b' $karte; Vergleich 'p3a' 'p3b'

& (Join-Path $hier 'region-anfassen.ps1') -Datei (Join-Path $region 'r.-3.1.mca') -Modus loeschen -Anzahl 64
Lauf 'p3c-64-geloescht' (Join-Path $Probe 'lauf'); Hashes 'p3c' $karte; Vergleich 'p3b' 'p3c'

Lauf 's1-neuer-host' $sync; Hashes 's1' (Join-Path $sync 'web\maps\overworld')
"== Vergleich Karte original (p3c) gegen neuer Host (s1), ohne tiles/0 im Original:"
$orig = Import-Csv (Join-Path $Ergebnis 'hash-p3c.csv') -Delimiter ';' | Where-Object { $_.pfad -notlike 'tiles/0/*' }
$orig | Export-Csv (Join-Path $Ergebnis 'hash-p3c-ohne0.csv') -Delimiter ';' -NoTypeInformation -UseQuotes Never
$neu = Import-Csv (Join-Path $Ergebnis 'hash-s1.csv') -Delimiter ';' | Where-Object { $_.pfad -notlike 'tiles/0/*' }
$neu | Export-Csv (Join-Path $Ergebnis 'hash-s1-ohne0.csv') -Delimiter ';' -NoTypeInformation -UseQuotes Never
Vergleich 'p3c-ohne0' 's1-ohne0'
"Hires-Kacheln beim neuen Host: $((Get-ChildItem -Recurse -File (Join-Path $sync 'web\maps\overworld\tiles\0') -ErrorAction SilentlyContinue).Count)"
