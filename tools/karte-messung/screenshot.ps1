# Screenshot der BlueMap-Weboberfläche mit Edge im Headless-Modus (eigenes,
# temporäres Profil; der Browser des Nutzers bleibt unberührt).
#   pwsh -File screenshot.ps1 -Url 'http://127.0.0.1:8101/#overworld:-1024:80:1024:500:0:0.8:0:0:perspective' -Aus C:\temp\ww-karte\ergebnis\karte-3d.png
param(
    [Parameter(Mandatory)] [string] $Url,
    [Parameter(Mandatory)] [string] $Aus,
    [string] $Edge = 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe',
    [int] $WarteMs = 25000
)
$profil = Join-Path ([IO.Path]::GetTempPath()) ('ww-karte-edge-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $profil | Out-Null
try {
    $argumente = @('--headless=new', "--user-data-dir=$profil", '--no-first-run', '--disable-extensions',
        '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--window-size=1600,900',
        "--virtual-time-budget=$WarteMs", "--screenshot=$Aus", $Url)
    $p = Start-Process -FilePath $Edge -ArgumentList $argumente -PassThru -WindowStyle Hidden
    if (-not $p.WaitForExit(120000)) { Stop-Process -Id $p.Id -Force; throw 'Edge hat nicht rechtzeitig beendet' }
    Get-Item $Aus | Select-Object FullName, Length
} finally {
    Start-Sleep -Seconds 2
    Remove-Item -Recurse -Force $profil -ErrorAction SilentlyContinue
}
