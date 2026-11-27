# Hash-Liste der Kartendateien (Pfad;Größe;SHA-256) für den Delta-Vergleich.
#   pwsh -File hashliste.ps1 -Wurzel C:\temp\ww-karte\lauf\web\maps\overworld -Aus C:\temp\ww-karte\ergebnis\hash-1.csv
#   pwsh -File hashliste.ps1 -Vergleich alt.csv,neu.csv   # zählt neu/geändert/gelöscht je Ordnerebene
param(
    [string] $Wurzel,
    [string] $Aus,
    [string[]] $Vergleich
)
$ErrorActionPreference = 'Stop'
if ($Vergleich -and $Vergleich.Count -eq 1) { $Vergleich = $Vergleich[0] -split ',' }
if ($Vergleich) {
    $a = @{}; Import-Csv $Vergleich[0] -Delimiter ';' | ForEach-Object { $a[$_.pfad] = $_ }
    $b = @{}; Import-Csv $Vergleich[1] -Delimiter ';' | ForEach-Object { $b[$_.pfad] = $_ }
    $erg = @{}
    function Merke($art, $pfad, $groesse) {
        $teile = $pfad -split '/'
        $ebene = if ($teile.Count -ge 2) { $teile[0] + '/' + $teile[1] } else { $teile[0] }
        $k = "$art`t$ebene"
        if (-not $erg.ContainsKey($k)) { $erg[$k] = [pscustomobject]@{ art = $art; ebene = $ebene; anzahl = 0; bytes = 0 } }
        $erg[$k].anzahl++; $erg[$k].bytes += [int64]$groesse
    }
    foreach ($k in $b.Keys) {
        if (-not $a.ContainsKey($k)) { Merke 'neu' $k $b[$k].groesse }
        elseif ($a[$k].sha256 -ne $b[$k].sha256) { Merke 'geaendert' $k $b[$k].groesse }
    }
    foreach ($k in $a.Keys) { if (-not $b.ContainsKey($k)) { Merke 'geloescht' $k 0 } }
    $erg.Values | Sort-Object art, ebene | Format-Table -AutoSize | Out-String -Width 200
    $sum = $erg.Values | Where-Object { $_.art -ne 'geloescht' } | Measure-Object bytes -Sum
    "Upload-Delta (neu+geändert): {0} Dateien, {1:N2} MB" -f ($erg.Values | Where-Object { $_.art -ne 'geloescht' } | Measure-Object anzahl -Sum).Sum, ($sum.Sum / 1MB)
    return
}
$basis = (Resolve-Path $Wurzel).Path.TrimEnd('\') + '\'
$sha = [System.Security.Cryptography.SHA256]::Create()
$zeilen = [System.Collections.Generic.List[string]]::new()
$zeilen.Add('pfad;groesse;sha256')
foreach ($f in [System.IO.Directory]::EnumerateFiles($basis, '*', 'AllDirectories')) {
    $bytes = [System.IO.File]::ReadAllBytes($f)
    $h = [System.BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '')
    $zeilen.Add(('{0};{1};{2}' -f $f.Substring($basis.Length).Replace('\', '/'), $bytes.Length, $h))
}
[System.IO.File]::WriteAllLines($Aus, $zeilen)
"{0} Dateien -> {1}" -f ($zeilen.Count - 1), $Aus
