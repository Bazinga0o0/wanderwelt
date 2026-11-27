# Simuliert Änderungen an einer Regionsdatei (.mca) – NUR an der Kopie der Welt benutzen.
#   -Modus zeit     : nur die Änderungszeit der Datei auf jetzt setzen
#   -Modus stempel  : Zeitstempel der ersten N vorhandenen Chunks im Kopf (Bytes 4096..8191) auf jetzt (Inhalt gleich)
#   -Modus loeschen : Eintrag der ersten N vorhandenen Chunks im Kopf auf 0 (Chunks verschwinden = echte Inhaltsänderung)
param(
    [Parameter(Mandatory)] [string] $Datei,
    [ValidateSet('zeit', 'stempel', 'loeschen')] [string] $Modus = 'zeit',
    [int] $Anzahl = 1024
)
$ErrorActionPreference = 'Stop'
if ($Datei -notlike 'C:\temp\ww-karte\*') { throw 'nur unter C:\temp\ww-karte (Kopie) erlaubt' }
if ($Modus -eq 'zeit') { (Get-Item $Datei).LastWriteTime = Get-Date; 'Zeit gesetzt'; return }
$b = [System.IO.File]::ReadAllBytes($Datei)
$jetzt = [uint32][DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
$n = 0
for ($i = 0; $i -lt 1024 -and $n -lt $Anzahl; $i++) {
    $loc = ($b[4 * $i] -shl 16) -bor ($b[4 * $i + 1] -shl 8) -bor $b[4 * $i + 2]
    if ($loc -eq 0) { continue }
    if ($Modus -eq 'stempel') {
        $o = 4096 + 4 * $i
        $b[$o] = [byte](($jetzt -shr 24) -band 255); $b[$o + 1] = [byte](($jetzt -shr 16) -band 255)
        $b[$o + 2] = [byte](($jetzt -shr 8) -band 255); $b[$o + 3] = [byte]($jetzt -band 255)
    } else {
        $b[4 * $i] = 0; $b[4 * $i + 1] = 0; $b[4 * $i + 2] = 0; $b[4 * $i + 3] = 0
    }
    $n++
}
[System.IO.File]::WriteAllBytes($Datei, $b)
"$Modus`: $n Chunks in $Datei"
