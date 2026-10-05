# Installs the mainplane CLI for the user who runs it, on their PATH; with a join token it installs it
# for the machine instead and makes this Windows machine a worker for that user, after a UAC prompt.
# With server it also makes this machine the harness, from the provider keys set in this shell, after a
# UAC prompt, and logs the CLI in to it. The release stamps its version.
#   & ([scriptblock]::Create((irm https://dl.mainplane.ai/@VERSION@/install.ps1))) [join token | server]
param([string]$Token)
$ErrorActionPreference = 'Stop'
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
$t = Fetch mainplane
if ($Token) {
  $bin = "$env:ProgramFiles\mainplane"
} else {
  $bin = "$env:LOCALAPPDATA\Programs\mainplane"
  New-Item -ItemType Directory -Force $bin | Out-Null
  Move-Item -Force $t "$bin\mainplane.exe"
  # SetEnvironmentVariable tells running programs, so a shell Explorer starts next finds mainplane.
  $p = [string][Environment]::GetEnvironmentVariable('Path', 'User')
  if (($p -split ';') -notcontains $bin) {
    [Environment]::SetEnvironmentVariable('Path', "$($p.TrimEnd(';'));$bin".TrimStart(';'), 'User')
  }
  Write-Output 'mainplane @VERSION@ installed'
}
# install puts Program Files\mainplane on the machine PATH, which this shell read before
if (($env:Path -split ';') -notcontains $bin) { $env:Path += ";$bin" }
if ($Token -eq 'server') {
  # install places mainplane-server in Program Files, runs it as a service, makes this machine the
  # worker admin with the mainplane beside it, which places that in $bin, and logs it in
  $s = Fetch mainplane-server
  & $s install
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
} elseif ($Token) {
  & $t install $Token
  if ($LASTEXITCODE) { exit $LASTEXITCODE }
}
Remove-Item -Recurse $d
