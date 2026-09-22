# Makes this Windows machine a worker, for the user who runs it. The release stamps its version.
#   & ([scriptblock]::Create((irm https://dl.mainplane.ai/@VERSION@/install.ps1))) <join token>
param([Parameter(Mandatory)][string]$Token)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$dl = 'https://dl.mainplane.ai/@VERSION@'
$f = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'mainplane-windows-arm64.exe' } else { 'mainplane-windows-amd64.exe' }
$d = New-Item -ItemType Directory (Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid()))
$t = Join-Path $d $f
Invoke-WebRequest "$dl/$f" -OutFile $t -UseBasicParsing
$want = ((Invoke-WebRequest "$dl/SHA256SUMS" -UseBasicParsing).Content -split "`n" | Where-Object { $_ -match " $([regex]::Escape($f))$" }) -split ' ' | Select-Object -First 1
if ($want -ne (Get-FileHash $t SHA256).Hash.ToLower()) { throw "$f does not match SHA256SUMS" }
& $t install $Token
if ($LASTEXITCODE) { exit $LASTEXITCODE }
Remove-Item -Recurse $d
Write-Output "mainplane installed: $(& "$env:LOCALAPPDATA\Programs\mainplane\mainplane.exe" version)"
