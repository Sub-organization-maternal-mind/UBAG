# Persistent auto-reconnecting SSH tunnel to the production VPS.
#
# Keeps these forwards alive (restarting ssh within seconds whenever it dies —
# the VPS connection on this laptop is flaky, and every CDP/Postgres consumer
# dies with it):
#   15432 -> platform-postgres (UBAG database)
#   15923 -> prod Chrome CDP (worker attach)
#   15990 -> prod Chrome live-browser bridge (dashboard streaming)
#
# Liveness is checked FUNCTIONALLY (an HTTP probe through the CDP forward),
# not by port binding: a zombie ssh keeps its local ports bound while serving
# nothing, which a port-only check would call "up" forever.
#
# Usage: powershell -File start-tunnel.ps1   (leave running, or start detached
# hidden — start-local.ps1 does this automatically)

$ErrorActionPreference = 'Continue'
$sshHost = 'root@185.252.233.186'
$key = "$env:USERPROFILE\.ssh\id_ed25519"
$sshArgs = @(
  '-N', '-o', 'BatchMode=yes', '-o', 'ServerAliveInterval=15',
  '-o', 'ServerAliveCountMax=4', '-o', 'ExitOnForwardFailure=yes',
  '-o', 'ConnectTimeout=10', '-i', $key,
  '-L', '15432:127.0.0.1:15432',
  '-L', '15923:172.28.0.10:9223',
  '-L', '15990:172.28.0.10:58090',
  $sshHost
)

function Test-TunnelAlive {
  try {
    $r = Invoke-WebRequest -Uri 'http://127.0.0.1:15923/json/version' -UseBasicParsing -TimeoutSec 6
    return ($r.StatusCode -eq 200)
  } catch { return $false }
}

# One-time startup cleanup: this box's ssh.exe processes are our tunnels; a
# zombie left by a previous watchdog would hold the local ports and every
# fresh ssh would die on "Address already in use".
Get-Process ssh -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Seconds 1

$proc = $null
while ($true) {
  if (-not (Test-TunnelAlive)) {
    if ($proc -and -not $proc.HasExited) {
      Write-Host ("[{0}] tunnel zombie - killing ssh pid {1}" -f (Get-Date -Format 'HH:mm:ss'), $proc.Id)
      try { $proc.Kill() } catch {}
      Start-Sleep -Seconds 2
    }
    Write-Host ("[{0}] starting ssh tunnel..." -f (Get-Date -Format 'HH:mm:ss'))
    $proc = Start-Process -FilePath 'ssh' -ArgumentList $sshArgs -PassThru -WindowStyle Hidden
    Start-Sleep -Seconds 8
  }
  Start-Sleep -Seconds 10
}
