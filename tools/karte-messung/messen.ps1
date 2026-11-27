# Messlauf für die BlueMap-CLI (Konzept Karte, 2026-11-27).
# Startet java mit niedriger Priorität, misst Dauer, Working Set (Spitze),
# CPU-Zeit und schreibt eine Zeile je Sekunde in eine CSV.
# Nur der selbst gestartete Prozess (gemerkte PID) wird gemessen/beendet.
#
# Beispiel:
#   pwsh -File messen.ps1 -Name erstlauf -Jar C:\temp\ww-karte\bluemap\bluemap-5.29-cli.jar `
#        -Arbeit C:\temp\ww-karte\lauf -Ergebnis C:\temp\ww-karte\ergebnis -Xmx 2g -Extra '-r -v 26.2'
param(
    [Parameter(Mandatory)] [string] $Name,
    [Parameter(Mandatory)] [string] $Jar,
    [Parameter(Mandatory)] [string] $Arbeit,
    [Parameter(Mandatory)] [string] $Ergebnis,
    [string] $Java = 'C:\temp\ww-jdk25\jdk-25.0.5.1+2\bin\java.exe',
    [string] $Xmx = '2g',
    [string] $Prioritaet = 'BelowNormal',
    [int] $MaxMinuten = 120,
    [string] $Extra = '-r'   # Leerzeichen-getrennt, z. B. '-r -v 26.2'
)
$ErrorActionPreference = 'Stop'
$extraListe = @($Extra -split '\s+' | Where-Object { $_ })
New-Item -ItemType Directory -Force $Ergebnis | Out-Null
$csv = Join-Path $Ergebnis "$Name.csv"
$out = Join-Path $Ergebnis "$Name.out.txt"
$err = Join-Path $Ergebnis "$Name.err.txt"
$argListe = @("-Xmx$Xmx", '-jar', $Jar, '-c', (Join-Path $Arbeit 'config')) + $extraListe
$start = Get-Date
$p = Start-Process -FilePath $Java -ArgumentList $argListe -WorkingDirectory $Arbeit `
    -RedirectStandardOutput $out -RedirectStandardError $err -PassThru -NoNewWindow
$p.PriorityClass = $Prioritaet
Set-Content (Join-Path $Ergebnis "$Name.pid") $p.Id
'zeit_s;ws_mb;cpu_s;cpu_prozent_eines_kerns' | Set-Content $csv
$spitze = 0; $letzteCpu = 0.0; $letzteZeit = $start
while (-not $p.HasExited) {
    Start-Sleep -Milliseconds 1000
    try { $p.Refresh() } catch { break }
    if ($p.HasExited) { break }
    $jetzt = Get-Date
    $ws = $p.WorkingSet64 / 1MB
    $cpu = $p.TotalProcessorTime.TotalSeconds
    $proz = 100 * ($cpu - $letzteCpu) / [math]::Max(0.001, ($jetzt - $letzteZeit).TotalSeconds)
    $letzteCpu = $cpu; $letzteZeit = $jetzt
    if ($p.PeakWorkingSet64 -gt $spitze) { $spitze = $p.PeakWorkingSet64 }
    '{0:F0};{1:F0};{2:F1};{3:F0}' -f ($jetzt - $start).TotalSeconds, $ws, $cpu, $proz | Add-Content $csv
    if (($jetzt - $start).TotalMinutes -gt $MaxMinuten) {
        Stop-Process -Id $p.Id -Force
        'ABGEBROCHEN (Zeitgrenze)' | Add-Content $csv
        break
    }
}
$p.WaitForExit()
$dauer = ((Get-Date) - $start).TotalSeconds
$zus = [ordered]@{
    name = $Name; dauer_s = [math]::Round($dauer, 1); exitcode = $p.ExitCode
    spitze_ws_mb = [math]::Round($spitze / 1MB); cpu_s = [math]::Round($letzteCpu, 1)
    mittel_kerne = [math]::Round($letzteCpu / [math]::Max(1, $dauer), 2)
    prioritaet = $Prioritaet; xmx = $Xmx; args = $Extra
}
$zus | ConvertTo-Json | Set-Content (Join-Path $Ergebnis "$Name.json")
$zus | ConvertTo-Json
