# Zählt Chunks einer Dimension (aus den Köpfen der .mca-Dateien) und die zu
# erwartende Zahl der BlueMap-Hires-Kacheln (32x32 Blöcke, Versatz 2, wie
# settings.json "hires": tileSize [32,32], translate [2,2]).
#   pwsh -File kacheln-erwartet.ps1 -Region C:\temp\ww-karte\welt\world\dimensions\minecraft\overworld\region
param([Parameter(Mandatory)] [string] $Region, [int] $Kachel = 32, [int] $Versatz = 2)
$kacheln = [System.Collections.Generic.HashSet[string]]::new()
$chunks = 0
foreach ($f in Get-ChildItem (Join-Path $Region '*.mca')) {
    $t = $f.BaseName.Split('.'); $rx = [int]$t[1]; $rz = [int]$t[2]
    $fs = [IO.File]::OpenRead($f.FullName); $b = New-Object byte[] 4096; [void]$fs.Read($b, 0, 4096); $fs.Close()
    for ($i = 0; $i -lt 1024; $i++) {
        if (($b[4 * $i] -bor $b[4 * $i + 1] -bor $b[4 * $i + 2]) -eq 0) { continue }
        $chunks++
        $cx = $rx * 32 + ($i % 32); $cz = $rz * 32 + [math]::Floor($i / 32)
        foreach ($dx in 0, 15) { foreach ($dz in 0, 15) {
            $tx = [math]::Floor((16 * $cx + $dx - $Versatz) / $Kachel); $tz = [math]::Floor((16 * $cz + $dz - $Versatz) / $Kachel)
            [void]$kacheln.Add("$tx,$tz")
        } }
    }
}
"Chunks: $chunks; erwartete Hires-Kacheln: $($kacheln.Count)"
