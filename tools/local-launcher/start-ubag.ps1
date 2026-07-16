# Starts the UBAG gateway and dashboard for local development (no Docker) and
# opens the dashboard in the default browser. Idempotent: if either service is
# already listening on its port, it is left alone and only the browser opens.
#
# Not part of the build/test pipeline - a convenience launcher for local
# development only, intended to be double-clicked via the desktop shortcut
# created by create-desktop-shortcut.ps1.

$ErrorActionPreference = 'Stop'

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot '..\..')
$gatewayDir = Join-Path $repoRoot 'apps\gateway'
$dashboardDir = Join-Path $repoRoot 'apps\dashboard'

$gatewayPort = 8080
$dashboardPort = 4179
$gatewayUrl = "http://127.0.0.1:$gatewayPort"
$dashboardUrl = "http://localhost:$dashboardPort"

function Test-PortOpen($port) {
  $conn = Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue
  return $null -ne $conn
}

function Wait-ForHttp($url, $timeoutSeconds) {
  $deadline = (Get-Date).AddSeconds($timeoutSeconds)
  while ((Get-Date) -lt $deadline) {
    try {
      $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3
      if ($resp.StatusCode -eq 200) { return $true }
    } catch {
      Start-Sleep -Milliseconds 500
    }
  }
  return $false
}

# --- Gateway ---
if (Test-PortOpen $gatewayPort) {
  Write-Host "Gateway already running on port $gatewayPort - leaving it alone."
} else {
  Write-Host "Starting gateway..."
  $exe = Join-Path $gatewayDir 'ubag-gateway.exe'
  if (-not (Test-Path $exe)) {
    Write-Host "Building gateway binary (first run only)..."
    Push-Location $gatewayDir
    & go build -o ubag-gateway.exe .\cmd\gateway
    Pop-Location
  }

  $artifactsDir = Join-Path $gatewayDir 'artifacts'
  New-Item -ItemType Directory -Force -Path $artifactsDir | Out-Null

  $env:UBAG_GATEWAY_ADDR = ":$gatewayPort"
  $env:UBAG_APP_SECRET = 'dev_local_secret_12345678'
  $env:UBAG_APP_ID = 'dev-app'
  $env:UBAG_CONVERSATIONS_ENABLED = 'true'
  $env:UBAG_DEV_CORS_ORIGIN = $dashboardUrl
  $env:UBAG_GATEWAY_STORE = 'sqlite'
  $env:UBAG_ARTIFACT_STORE = 'localfs'
  $env:UBAG_ARTIFACT_DIR = './artifacts'

  Start-Process -FilePath $exe -WorkingDirectory $gatewayDir -WindowStyle Minimized

  if (-not (Wait-ForHttp "$gatewayUrl/v1/health" 20)) {
    Write-Warning "Gateway did not respond at $gatewayUrl/v1/health within 20s - check apps\gateway\gateway.log"
  }
}

# --- Dashboard ---
if (Test-PortOpen $dashboardPort) {
  Write-Host "Dashboard already running on port $dashboardPort - leaving it alone."
} else {
  $distIndex = Join-Path $dashboardDir 'dist\index.html'
  if (-not (Test-Path $distIndex)) {
    Write-Host "Building dashboard (first run only, ~30s)..."
    Push-Location $dashboardDir
    & pnpm build
    Pop-Location
  }

  Write-Host "Starting dashboard..."
  $env:PORT = "$dashboardPort"
  Start-Process -FilePath 'node' -ArgumentList 'scripts/serve-static.mjs' -WorkingDirectory $dashboardDir -WindowStyle Minimized

  if (-not (Wait-ForHttp $dashboardUrl 20)) {
    Write-Warning "Dashboard did not respond at $dashboardUrl within 20s - check apps\dashboard\static-serve.log"
  }
}

Write-Host "Opening $dashboardUrl ..."
Start-Process $dashboardUrl
