#Requires -Version 5.1
<#
.SYNOPSIS
  UBAG installer (Windows).

.DESCRIPTION
  Modes:
    Compose  Run the small-profile Docker Compose stack (default).
    Binary   Install a pre-built ubag-gateway.exe to a prefix and print
             Windows service wrapper instructions.

  Safety:
    - No piping of remote scripts into the shell. A binary URL may be provided
      but is downloaded to a temp file and verified against a required SHA-256
      checksum before installation.
    - Idempotent: re-running converges to the same state.

.EXAMPLE
  .\install.ps1 -Mode Compose

.EXAMPLE
  .\install.ps1 -Mode Binary -Binary .\ubag-gateway.exe -Prefix "C:\Program Files\UBAG"

.EXAMPLE
  .\install.ps1 -Mode Binary -Url https://host/ubag-gateway.exe -Sha256 <hex> -Prefix "C:\Program Files\UBAG"
#>
[CmdletBinding()]
param(
  [ValidateSet('Compose', 'Binary')]
  [string]$Mode = 'Compose',
  [string]$Binary = '',
  [string]$Url = '',
  [string]$Sha256 = '',
  [string]$Prefix = "$env:ProgramFiles\UBAG",
  [string]$EnvFile = ''
)

$ErrorActionPreference = 'Stop'
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path (Join-Path $ScriptDir '..\..')).Path

function Write-Log { param([string]$Message) Write-Host "[ubag-install] $Message" }
function Fail { param([string]$Message) Write-Error "[ubag-install] ERROR: $Message"; exit 1 }

function Test-Sha256 {
  param([string]$Path, [string]$Expected)
  $actual = (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLower()
  if ($actual -ne $Expected.ToLower()) {
    Fail "checksum mismatch: expected $Expected got $actual"
  }
  Write-Log "checksum verified: $actual"
}

function Install-Compose {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { Fail 'docker is required' }
  $envLocal = Join-Path $RepoRoot 'deploy\small\env.local'
  if (-not (Test-Path $envLocal)) {
    Write-Log "creating $envLocal from env.example (replace placeholder secrets before sharing)"
    Copy-Item (Join-Path $RepoRoot 'deploy\small\env.example') $envLocal
  } else {
    Write-Log "reusing existing $envLocal"
  }
  if (Select-String -Path $envLocal -Pattern 'replace-with-local|set-a-local' -Quiet) {
    Fail "placeholder secrets remain in $envLocal; edit it before starting the stack"
  }
  Write-Log 'starting small-profile stack'
  docker compose --env-file $envLocal -f (Join-Path $RepoRoot 'docker-compose.small.yml') up -d --build
  if ($LASTEXITCODE -ne 0) { Fail 'docker compose up failed' }
  Write-Log 'stack started. Gateway health: http://127.0.0.1:8080/v1/health'
}

function Install-Binary {
  $tmp = $null
  if ($Url) {
    if (-not $Sha256) { Fail '-Sha256 is required with -Url' }
    $tmp = [System.IO.Path]::GetTempFileName()
    Write-Log "downloading $Url"
    Invoke-WebRequest -Uri $Url -OutFile $tmp -UseBasicParsing
    Test-Sha256 -Path $tmp -Expected $Sha256
    $src = $tmp
  } elseif ($Binary) {
    if (-not (Test-Path $Binary)) { Fail "binary not found: $Binary" }
    if ($Sha256) { Test-Sha256 -Path $Binary -Expected $Sha256 }
    $src = $Binary
  } else {
    Fail 'binary mode requires -Binary <path> or -Url <url> -Sha256 <hex>'
  }

  $binDir = Join-Path $Prefix 'bin'
  New-Item -ItemType Directory -Force -Path $binDir | Out-Null
  $dest = Join-Path $binDir 'ubag-gateway.exe'
  Write-Log "installing to $dest"
  Copy-Item -Force $src $dest
  if ($tmp) { Remove-Item -Force $tmp }

  if (-not $EnvFile) { $EnvFile = Join-Path $Prefix 'gateway.env' }
  if (-not (Test-Path $EnvFile)) {
    Write-Log "creating env template $EnvFile (edit before starting)"
    Copy-Item (Join-Path $ScriptDir 'gateway.env.example') $EnvFile
  }

  Write-Log 'installed. Register as a Windows service with one of:'
  Write-Log "  NSSM:  nssm install UBAGGateway `"$dest`""
  Write-Log "         nssm set UBAGGateway AppEnvironmentExtra (load $EnvFile manually)"
  Write-Log "  sc.exe create UBAGGateway binPath= `"$dest`" start= auto"
  Write-Log 'See deploy/installers/README.md (Windows service wrapper notes).'
}

switch ($Mode) {
  'Compose' { Install-Compose }
  'Binary'  { Install-Binary }
}
