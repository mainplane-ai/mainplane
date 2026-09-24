# Installs the mainplane CLI for the user who runs it, on their PATH; with a join token it also makes
# this Windows machine a worker for that user. The release stamps its version.
#   & ([scriptblock]::Create((irm https://dl.mainplane.ai/@VERSION@/install.ps1))) [join token]
param([string]$Token)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$dl = 'https://dl.mainplane.ai/@VERSION@'
$bin = "$env:LOCALAPPDATA\Programs\mainplane"
$f = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'mainplane-windows-arm64.exe' } else { 'mainplane-windows-amd64.exe' }
$d = New-Item -ItemType Directory (Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid()))
$t = Join-Path $d $f
Invoke-WebRequest "$dl/$f" -OutFile $t -UseBasicParsing
$want = ((Invoke-WebRequest "$dl/SHA256SUMS" -UseBasicParsing).Content -split "`n" | Where-Object { $_ -match " $([regex]::Escape($f))$" }) -split ' ' | Select-Object -First 1
if ($want -ne (Get-FileHash $t).Hash.ToLower()) { throw "$f does not match SHA256SUMS" }
if ($Token) {
  & $t install $Token
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
} else {
  New-Item -ItemType Directory -Force $bin | Out-Null
  Move-Item -Force $t "$bin\mainplane.exe"
}
Remove-Item -Recurse $d
# SetEnvironmentVariable tells running programs, so a shell Explorer starts next finds mainplane.
$p = [string][Environment]::GetEnvironmentVariable('Path', 'User')
if (($p -split ';') -notcontains $bin) {
  [Environment]::SetEnvironmentVariable('Path', "$($p.TrimEnd(';'));$bin".TrimStart(';'), 'User')
}
if (($env:Path -split ';') -notcontains $bin) { $env:Path += ";$bin" }
Write-Output "mainplane installed: $(mainplane version)"
