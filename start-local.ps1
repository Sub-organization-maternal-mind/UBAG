# UBAG local deployment (no Docker) — production-backed.
#
# Starts on this laptop:
#   1. SSH tunnel to the production server (185.252.233.186):
#        15432 -> platform-postgres (Postgres 17, the UBAG database)
#        15923 -> ubag-vps-browser CDP (the production logged-in Chrome)
#        15990 -> ubag-vps-browser live-browser bridge (streamed into the dashboard)
#   2. The Go gateway on http://127.0.0.1:58080, storing everything in the
#      PRODUCTION Postgres through the tunnel (see .env.local).
#   3. The dashboard (built static bundle) on http://localhost:58180.
#
# No Docker Desktop, WSL, Hyper-V, VirtualBox, or local database involved.
# Modeled on tools/local-launcher/start-ubag.ps1 (the repo's own SQLite-based
# launcher); this variant swaps SQLite for the tunneled production Postgres.
#
# Idempotent: anything already listening on its port is left alone.

$ErrorActionPreference = 'Stop'

$repoRoot    = Resolve-Path (Join-Path $PSScriptRoot '.')
$gatewayDir  = Join-Path $repoRoot 'apps\gateway'
$dashboardDir = Join-Path $repoRoot 'apps\dashboard'
$sshHost     = 'root@185.252.233.186'

$gatewayPort   = 58080
$dashboardPort = 58180
$tunnelPgPort  = 15432

$gatewayUrl   = "http://127.0.0.1:$gatewayPort"
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

function Find-Python {
  $candidates = @(
    "$env:LOCALAPPDATA\Python\bin\python.exe",                # Python install manager
    "$env:LOCALAPPDATA\Programs\Python\Python312\python.exe", # winget classic install
    "$env:LOCALAPPDATA\Programs\Python\Python313\python.exe"
  )
  foreach ($p in $candidates) { if (Test-Path $p) { return $p } }
  return $null
}

# --- 1. SSH tunnel (gateway dies without it: Postgres is production-only) ---
# All three forwards must carry REAL TRAFFIC: a wedged tunnel still listens on
# its local ports (TCP connect succeeds, nothing flows), so a port-open check
# alone cannot detect it (observed 2026-09-26). tunnel-watchdog.ps1 does the
# continuous version of these checks every 30s; this is the start-time gate.
function Test-HttpForward($port, $path) {
  try {
    $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$port$path" -UseBasicParsing -TimeoutSec 6
    return ($resp.StatusCode -eq 200)
  } catch { return $false }
}

function Test-PgForward($port) {
  $client = New-Object System.Net.Sockets.TcpClient
  try {
    $iar = $client.BeginConnect('127.0.0.1', $port, $null, $null)
    if (-not $iar.AsyncWaitHandle.WaitOne(4000)) { return $false }
    $stream = $client.GetStream()
    $stream.ReadTimeout = 4000
    # Postgres SSLRequest: length 8, code 80877103 — answered with 'S' or 'N'.
    $stream.Write([byte[]](0x00, 0x00, 0x00, 0x08, 0x04, 0xD2, 0x16, 0x2F), 0, 8)
    $b = $stream.ReadByte()
    return ($b -eq 0x53 -or $b -eq 0x4E)
  } catch { return $false } finally { $client.Close() }
}

$tunnelHealthy = (Test-PgForward $tunnelPgPort) -and (Test-HttpForward 15923 '/json/version') -and (Test-HttpForward 15990 '/health')
$tunnelListening = Test-PortOpen $tunnelPgPort
if ($tunnelHealthy) {
  Write-Host "Tunnel already up and passing traffic (Postgres 15432, CDP 15923, bridge 15990) - leaving it alone."
} else {
  if ($tunnelListening) {
    # Listening-but-wedged or partially-forwarded tunnel from an older launch:
    # kill it so the relaunch owns every forward (SSH cannot add forwards to a
    # running -N session). This also sweeps up zombie single-forward tunnels.
    Write-Host "Tunnel dead or not forwarding all ports - restarting it with all three..."
    Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" |
      Where-Object { $_.CommandLine -like '*15432:127.0.0.1:15432*' } |
      ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 2
  }
  Write-Host "Opening SSH tunnel to 185.252.233.186 (Postgres 15432, CDP 15923, browser bridge 15990)..."
  $sshArgs = @(
    '-N', '-o', 'BatchMode=yes', '-o', 'ServerAliveInterval=15',
    '-o', 'ServerAliveCountMax=4', '-o', 'ExitOnForwardFailure=yes',
    '-o', 'ConnectTimeout=10', '-i', "$env:USERPROFILE\.ssh\id_ed25519",
    '-L', '15432:127.0.0.1:15432',     # platform-postgres (host-loopback bound)
    '-L', '15923:172.28.0.10:9223',    # browser container socat CDP proxy
    '-L', '15990:172.28.0.10:58090',   # browser container live-browser bridge
    $sshHost
  )
  Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -WindowStyle Hidden
  Start-Sleep -Seconds 4
  $pgOk = Test-PgForward $tunnelPgPort
  $cdpOk = Test-HttpForward 15923 '/json/version'
  $bridgeOk = Test-HttpForward 15990 '/health'
  if (-not ($pgOk -and $cdpOk -and $bridgeOk)) {
    throw "SSH tunnel did not come up passing traffic (pg=$pgOk cdp=$cdpOk bridge=$bridgeOk) - cannot reach the production Postgres. Check 'ssh $sshHost echo ok' manually."
  }
}

# --- 1b. Tunnel watchdog (keeps the above self-healing after this script exits) ---
$mutexAcquired = $false
$watchdogMutex = New-Object System.Threading.Mutex($false, 'Global\UBAG-Tunnel-Watchdog')
try { $mutexAcquired = $watchdogMutex.WaitOne(0) } catch { $mutexAcquired = $false }
if ($mutexAcquired) { $watchdogMutex.ReleaseMutex() }
if ($mutexAcquired) {
  Write-Host "Starting tunnel watchdog (self-heals wedged/dead tunnels every 30s)..."
  Start-Process -FilePath 'powershell' -ArgumentList @(
    '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', (Join-Path $repoRoot 'tunnel-watchdog.ps1')
  ) -WindowStyle Hidden
} else {
  Write-Host "Tunnel watchdog already running - leaving it alone."
}

# --- 2. Gateway (Go binary + .env.local config) ---
if (Test-PortOpen $gatewayPort) {
  Write-Host "Gateway already running on port $gatewayPort - leaving it alone."
} else {
  $exe = Join-Path $gatewayDir 'ubag-gateway.exe'
  if (-not (Test-Path $exe)) {
    Write-Host "Building gateway binary (first run only)..."
    $go = (Get-Command go -ErrorAction SilentlyContinue).Source
    if (-not $go) { $go = "$env:ProgramFiles\Go\bin\go.exe" }
    Push-Location $gatewayDir
    & $go build -o ubag-gateway.exe .\cmd\gateway
    Pop-Location
  }

  # Load .env.local, expanding repo-root-relative paths to absolute ones.
  # NOTE: capture key/value BEFORE any expansion - running $val -match against
  # the value overwrites $Matches, and the old code then set an env var named
  # after the path tail (e.g. "spool") instead of the real key, silently
  # dropping UBAG_EXECUTOR_SPOOL_DIR / UBAG_WORKER_SCRIPT (2026-09-27 incident).
  Get-Content (Join-Path $repoRoot '.env.local') | ForEach-Object {
    if ($_ -match '^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)\s*$') {
      $key = $Matches[1]
      $val = $Matches[2]
      if ($val -match '^\./(.*)$') {
        $val = [System.IO.Path]::GetFullPath((Join-Path $repoRoot $Matches[1]))
      } elseif ($val -match '^\.\.[/\\](.*)$') {
        # "../worker/x" means one level UP from the gateway dir (apps/gateway/../worker).
        $val = [System.IO.Path]::GetFullPath((Join-Path (Join-Path $gatewayDir '..') $Matches[1]))
      }
      Set-Item -Path ("Env:" + $key) -Value $val
    }
  }

  New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'spool') | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'artifacts') | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'chat-ledger') | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'logs') | Out-Null

  # Worker interpreter (auto-detect; jobs only run if Python + playwright exist).
  $py = Find-Python
  if ($py) { $env:UBAG_WORKER_PYTHON = $py }
  else     { $env:UBAG_WORKER_CONSUMER_ENABLED = 'false' }  # fail safe: never spin on a missing interpreter

  # CORS for the locally served dashboard origin.
  $env:UBAG_DEV_CORS_ORIGIN = $dashboardUrl

  # Gateway stdout/stderr MUST land in a file: worker-failure reasons (the
  # "worker process failed" stderr dumps) only exist in this log. A hidden
  # window loses them forever, which is why failed jobs used to show "—" with
  # no reason (2026-09-27 incident).
  $gatewayLog = Join-Path $repoRoot 'logs\gateway.log'
  $gatewayErr = Join-Path $repoRoot 'logs\gateway.err.log'
  Start-Process -FilePath $exe -WorkingDirectory $gatewayDir -WindowStyle Hidden `
    -RedirectStandardOutput $gatewayLog -RedirectStandardError $gatewayErr

  if (-not (Wait-ForHttp "$gatewayUrl/v1/health" 30)) {
    Write-Warning "Gateway did not respond at $gatewayUrl/v1/health within 30s - see $gatewayErr"
    if (Test-Path $gatewayErr) {
      Get-Content $gatewayErr -Tail 15 | ForEach-Object { Write-Warning "  gateway: $_" }
    }
  }
}

# --- 3. Dashboard (static build served by vite preview) ---
if (Test-PortOpen $dashboardPort) {
  Write-Host "Dashboard already running on port $dashboardPort - leaving it alone."
} else {
  $distIndex = Join-Path $dashboardDir 'dist\index.html'
  if (-not (Test-Path $distIndex)) {
    Write-Host "Building dashboard (first run only, ~30s)..."
    # Fresh clone: the SDK's generated contract manifest and its dist/ output
    # are prerequisites of the dashboard build (dashboard imports
    # "@ubag/sdk/contract-manifest", which resolves into packages/sdk-typescript/dist).
    & node (Join-Path $repoRoot 'tools\make-sdks\generate-manifest.mjs')
    Push-Location $repoRoot
    & pnpm --filter @ubag/sdk build
    Pop-Location
    # Use the dashboard's same-origin API proxy. Never bake the production-backed
    # gateway credential into assets served to a browser.
    $env:UBAG_DEV_DEFAULT_GATEWAY_URL = ''
    $env:UBAG_DEV_DEFAULT_APP_SECRET = ''
    # Browser Sessions widget streams the PRODUCTION Chrome on the VPS through
    # the SSH tunnel (not a local bridge on the default 58090 port).
    $env:UBAG_DEV_DEFAULT_LIVE_BROWSER_WS = 'ws://127.0.0.1:15990'
    Push-Location $repoRoot
    & pnpm --filter @ubag/dashboard build
    Pop-Location
  }

  Write-Host "Starting dashboard on port $dashboardPort..."
  $env:PORT = "$dashboardPort"
  $env:UBAG_DASHBOARD_GATEWAY_URL = $gatewayUrl
  Start-Process -FilePath 'node' -ArgumentList 'serve-dashboard.mjs' -WorkingDirectory $repoRoot -WindowStyle Minimized

  if (-not (Wait-ForHttp $dashboardUrl 30)) {
    Write-Warning "Dashboard did not respond at $dashboardUrl within 30s - check the pnpm window output."
  }
}

Write-Host ""
Write-Host "UBAG local deployment:"
Write-Host "  Dashboard : $dashboardUrl"
Write-Host "  Gateway   : $gatewayUrl  (health: $gatewayUrl/v1/health)"
Write-Host "  Database  : production Postgres via SSH tunnel (127.0.0.1:$tunnelPgPort)"
Write-Host "  Browser   : production Chrome via tunnel (CDP 127.0.0.1:15923, stream 127.0.0.1:15990)"
Write-Host ""
Write-Host "If the Browser Sessions widget still shows Offline, it may hold a stale"
Write-Host "bridge URL saved in your browser. The widget's offline panel now shows"
Write-Host "what it is dialing with a 'Reset saved bridge URL' button - or clear it"
Write-Host "manually: localStorage.removeItem('ubag_live_browser_ws') in DevTools."
Write-Host ""
Start-Process $dashboardUrl
