# UBAG tunnel watchdog — self-heals the local SSH tunnel to the production box.
#
# Companion to start-local.ps1. Why this exists: a wedged SSH tunnel still
# LISTENS on its local ports (TCP connect succeeds) while no data flows —
# every "is the port open" check passes and the dashboard's Live Browser
# silently dies (observed 2026-09-26: listeners up, channels dead, two zombie
# ssh processes competing). start-local.ps1's port check cannot detect that.
#
# This watchdog verifies the forwards with REAL TRAFFIC every cycle:
#   - 15432 (Postgres): raw SSLRequest handshake, expects an 'S'/'N' reply
#   - 15923 (CDP):      GET /json/version -> HTTP 200
#   - 15990 (bridge):   GET /health        -> HTTP 200
# and on any failure kills every ssh process matching the tunnel signature
# (zombies included) and relaunches ONE clean tunnel with all three forwards.
#
# Single instance: held via a named mutex; a second launch exits immediately.
# Started by start-local.ps1 (hidden). Log: logs/tunnel-watchdog.log

$ErrorActionPreference = 'Stop'

$repoRoot = $PSScriptRoot  # this script lives at the repo root, next to start-local.ps1
$logDir = Join-Path $repoRoot 'logs'
$logFile = Join-Path $logDir 'tunnel-watchdog.log'
$sshHost = 'root@185.252.233.186'
$sshKey = Join-Path $env:USERPROFILE '.ssh\id_ed25519'
$signature = '15432:127.0.0.1:15432'

$created = $false
$mutex = New-Object System.Threading.Mutex($true, 'Global\UBAG-Tunnel-Watchdog', [ref]$created)
if (-not $created) { exit 0 }  # already running

New-Item -ItemType Directory -Force -Path $logDir | Out-Null

function Log($msg) {
  $line = "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $msg"
  Add-Content -Path $logFile -Value $line
}

function Test-HttpForward($port, $path) {
  try {
    $resp = Invoke-WebRequest -Uri "http://127.0.0.1:$port$path" -UseBasicParsing -TimeoutSec 6
    return ($resp.StatusCode -eq 200)
  } catch { return $false }
}

# A wedged forward still accepts the TCP connection (the listener is local),
# so Postgres is verified with a real SSLRequest handshake, not a port probe.
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

function Test-TunnelHealthy {
  $pg = Test-PgForward 15432
  $cdp = Test-HttpForward 15923 '/json/version'
  $bridge = Test-HttpForward 15990 '/health'
  return @{ pg = $pg; cdp = $cdp; bridge = $bridge }
}

function Restart-Tunnel {
  Log "tunnel unhealthy - killing matching ssh processes and relaunching"
  Get-CimInstance Win32_Process -Filter "Name='ssh.exe'" |
    Where-Object { $_.CommandLine -like "*$signature*" } |
    ForEach-Object {
      Log "  killing ssh pid $($_.ProcessId)"
      Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
    }
  Start-Sleep -Seconds 2
  $sshArgs = @(
    '-N', '-o', 'BatchMode=yes', '-o', 'ServerAliveInterval=15',
    '-o', 'ServerAliveCountMax=4', '-o', 'ExitOnForwardFailure=yes',
    '-o', 'ConnectTimeout=10', '-i', $sshKey,
    '-L', '15432:127.0.0.1:15432',
    '-L', '15923:172.28.0.10:9223',
    '-L', '15990:172.28.0.10:58090',
    $sshHost
  )
  Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -WindowStyle Hidden
  Start-Sleep -Seconds 6
  $state = Test-TunnelHealthy
  Log "after restart: pg=$($state.pg) cdp=$($state.cdp) bridge=$($state.bridge)"
}

Log "watchdog started (pid $PID, host $sshHost)"
while ($true) {
  try {
    $state = Test-TunnelHealthy
    if (-not ($state.pg -and $state.cdp -and $state.bridge)) {
      Log "check failed: pg=$($state.pg) cdp=$($state.cdp) bridge=$($state.bridge)"
      Restart-Tunnel
    }
  } catch {
    Log "watchdog cycle error: $_"
  }
  Start-Sleep -Seconds 30
}
