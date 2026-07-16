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

# Deliberately uncommon ports (not 8080/3000/5173/etc.) so this never collides
# with some other local dev server on the machine.
$gatewayPort = 58080
$dashboardPort = 58179
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
  # Empty UBAG_ACTOR_ROLE normalizes to "service" (job:* actions only), which
  # denies Browser Sessions, Webhooks, Audit, Users & Roles, etc. This is a
  # single-user local dev gateway with no real tenant boundary to protect, so
  # grant full access rather than hitting the same 403 on every other page.
  $env:UBAG_ACTOR_ROLE = 'superadmin'
  $env:UBAG_CONVERSATIONS_ENABLED = 'true'
  $env:UBAG_DEV_CORS_ORIGIN = $dashboardUrl
  $env:UBAG_GATEWAY_STORE = 'sqlite'
  $env:UBAG_ARTIFACT_STORE = 'localfs'
  $env:UBAG_ARTIFACT_DIR = './artifacts'

  # --- Live-provider job dispatch ---
  # Route jobs through the embedded worker consumer to the live Playwright
  # engine, which attaches over CDP to the operator's logged-in Chrome (the
  # live-browser bridge on 58091) and drives chatgpt_web/deepseek_web/gemini_web.
  # Mock jobs still work (run_live_worker.py routes target=mock to the mock adapter).
  New-Item -ItemType Directory -Force -Path (Join-Path $gatewayDir 'spool') | Out-Null
  $env:UBAG_EXECUTOR_MODE = 'file'
  $env:UBAG_EXECUTOR_SPOOL_DIR = (Join-Path $gatewayDir 'spool')
  $env:UBAG_WORKER_CONSUMER_ENABLED = 'true'
  # Real interpreter: bare "python" is a broken Windows Store alias here.
  $workerPython = 'C:\Users\Admin\AppData\Local\Python\bin\python.exe'
  if (-not (Test-Path $workerPython)) { $workerPython = 'python' }
  $env:UBAG_WORKER_PYTHON = $workerPython
  $env:UBAG_WORKER_SCRIPT = (Join-Path $repoRoot 'apps\worker\run_live_worker.py')
  $env:UBAG_WORKER_MAX_RUNTIME_MS = '180000'
  # The live-browser bridge's Chrome DevTools Protocol port (bridge WS is 58090,
  # its Chrome CDP is 58091). The worker attaches here to inherit the logins.
  $env:UBAG_REMOTE_BROWSER_ENDPOINT = 'http://127.0.0.1:58091'
  $env:UBAG_PROFILE_DIR = (Join-Path $repoRoot 'tools\live-browser\chrome-profile')

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
    # Baked into the build (see vite.config.ts's `define` + settings.ts) so a
    # fresh browser profile/Incognito/cleared localStorage still opens already
    # pointed at the right gateway, instead of defaulting to the dashboard's
    # own origin.
    $env:UBAG_DEV_DEFAULT_GATEWAY_URL = $gatewayUrl
    $env:UBAG_DEV_DEFAULT_APP_SECRET = 'dev_local_secret_12345678'
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

# --- Live-browser bridge (streams a real Chrome into the dashboard's Browser
# Sessions page so the operator can log into providers interactively). ---
$liveBrowserPort = 58090
$liveBrowserDir = Join-Path $repoRoot 'tools\live-browser'
if (Test-PortOpen $liveBrowserPort) {
  Write-Host "Live-browser bridge already running on port $liveBrowserPort - leaving it alone."
} else {
  Write-Host "Starting live-browser bridge (launches a Chrome with a persistent profile)..."
  Start-Process -FilePath 'node' -ArgumentList 'bridge.mjs' -WorkingDirectory $liveBrowserDir -WindowStyle Minimized
  # Non-fatal if it does not come up quickly; the dashboard shows a clear
  # "bridge not connected" panel and auto-reconnects.
  Wait-ForHttp "http://127.0.0.1:$liveBrowserPort/health" 15 | Out-Null
}

Write-Host "Opening $dashboardUrl ..."
Start-Process $dashboardUrl
