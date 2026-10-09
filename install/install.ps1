# Installs the mainplane CLI for the user who runs it, on their PATH, unless this machine has one; with
# an api key it logs that CLI in. With a network name and device code it installs the CLI for the machine instead and
# makes this Windows machine a worker for that user, after a UAC prompt. With server it also makes this
# machine the harness, from the provider keys set in this shell, after a UAC prompt, and logs the CLI
# in to it. The release stamps its version.
#   & ([scriptblock]::Create((irm https://dl.mainplane.ai/@VERSION@/install.ps1))) [api key | <network> <device code> | server]
param([string]$Token, [string]$Code)
$ErrorActionPreference = 'Stop'
if ($args -or ($Token -and !$Code -and $Token -ne 'server' -and $Token -notlike 'mp_key_*')) {
  throw 'usage: install.ps1 [api key | <network> <device code> | server]'
}
$ProgressPreference = 'SilentlyContinue'
$dl = 'https://dl.mainplane.ai/@VERSION@'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$d = New-Item -ItemType Directory (Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid()))
$sums = (Invoke-WebRequest "$dl/SHA256SUMS" -UseBasicParsing).Content -split "`n"
# Fetch gets this machine's build of release binary $name into $d as $name.exe, checked against
# SHA256SUMS. mainplane-server install finds mainplane.exe there.
function Fetch($name) {
  $f = "$name-windows-$arch.exe"
  $t = Join-Path $d "$name.exe"
  Invoke-WebRequest "$dl/$f" -OutFile $t -UseBasicParsing
  $want = ($sums | Where-Object { $_ -match " $([regex]::Escape($f))$" }) -split ' ' | Select-Object -First 1
  if ($want -ne (Get-FileHash $t).Hash.ToLower()) { throw "$f does not match SHA256SUMS" }
  $t
}
if ($Token -eq 'server' -and !$Code) {
  # install places mainplane-server in Program Files, runs it as a service, makes this machine the
  # worker admin with the mainplane beside it, which places that in Program Files, and logs that in
  Fetch mainplane | Out-Null
  & (Fetch mainplane-server) install
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
  $bin = "$env:ProgramFiles\mainplane"
} elseif ($Code) {
  & (Fetch mainplane) install $Token $Code
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
  $bin = "$env:ProgramFiles\mainplane"
} else {
  # A CLI this machine has is used: a worker's or harness's in Program Files, else this user's.
  $bin = "$env:ProgramFiles\mainplane", "$env:LOCALAPPDATA\Programs\mainplane" | Where-Object { Test-Path "$_\mainplane.exe" } | Select-Object -First 1
  if ($bin) {
    if (!$Token) { Write-Output "mainplane is installed: $bin\mainplane.exe" }
  } else {
    $bin = "$env:LOCALAPPDATA\Programs\mainplane"
    New-Item -ItemType Directory -Force $bin | Out-Null
    Move-Item -Force (Fetch mainplane) "$bin\mainplane.exe"
    # SetEnvironmentVariable tells running programs, so a shell Explorer starts next finds mainplane.
    $p = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    if (($p -split ';') -notcontains $bin) {
      [Environment]::SetEnvironmentVariable('Path', "$($p.TrimEnd(';'));$bin".TrimStart(';'), 'User')
    }
    Write-Output 'mainplane @VERSION@ installed'
  }
}
# $bin is on the machine's or the user's PATH now, which this shell read before
if (($env:Path -split ';') -notcontains $bin) { $env:Path += ";$bin" }
Remove-Item -Recurse $d
if ($Token -like 'mp_key_*') {
  & "$bin\mainplane.exe" login $Token
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
}
