# Rendert dieselbe Region mit mehreren Karten-Einstellungen nacheinander
# (je ein Arbeitsordner mit eigener config) und misst jeden Lauf mit messen.ps1.
#   pwsh -File varianten.ps1 -Basis C:\temp\ww-karte\probe -Namen v0-standard,v1-nur-flach
param(
    [Parameter(Mandatory)] [string] $Basis,
    [Parameter(Mandatory)] [string[]] $Namen,
    [string] $Jar = 'C:\temp\ww-karte\bluemap\bluemap-5.29-cli.jar',
    [string] $Ergebnis = 'C:\temp\ww-karte\ergebnis'
)
$messen = Join-Path $PSScriptRoot 'messen.ps1'
foreach ($n in ($Namen -split ',')) {
    & $messen -Name $n -Jar $Jar -Arbeit (Join-Path $Basis $n) -Ergebnis $Ergebnis -Xmx 1g -MaxMinuten 60 -Extra '-r -v 26.2' | Out-Null
    $t = Get-ChildItem -Recurse -File (Join-Path $Basis "$n\web\maps\overworld\tiles") -ErrorAction SilentlyContinue
    $j = Get-Content (Join-Path $Ergebnis "$n.json") -Raw | ConvertFrom-Json
    '{0}: {1} s, CPU {2} s, WS {3} MB, Kacheln {4} Dateien {5:N1} MB (hires {6:N1} MB)' -f $n, $j.dauer_s, $j.cpu_s, $j.spitze_ws_mb, $t.Count,
        (($t | Measure-Object Length -Sum).Sum / 1MB),
        ((($t | Where-Object { $_.FullName -match '\\tiles\\0\\' }) | Measure-Object Length -Sum).Sum / 1MB)
}
