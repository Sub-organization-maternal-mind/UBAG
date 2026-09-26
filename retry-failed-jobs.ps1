# Retries every retryable-failed UBAG job in the dashboard's recent-100 window
# EXCEPT the known-impossible ones, after the operator has logged back in to the
# providers via the Browser Sessions page.
#
# Usage (from the repo root):
#   powershell -File retry-failed-jobs.ps1          # dry run: shows what would be retried
#   powershell -File retry-failed-jobs.ps1 -Apply   # actually POSTs the retries
#
# Prerequisite: complete the provider logins first (Dashboard -> Browser
# Sessions -> click a provider -> log in inside the streamed VPS Chrome).
# Until then every retry will pause again with manual_login_required.

param(
  [switch]$Apply,
  # Optional comma-separated target filter, e.g. -Targets duckai_web,gemini_web.
  # Useful when a provider's login is not restored yet: its jobs would only
  # pause again at manual_login_required.
  [string]$Targets
)

$ErrorActionPreference = 'Stop'
$repoRoot = $PSScriptRoot
$gatewayUrl = 'http://127.0.0.1:58080'

$secret = (Get-Content (Join-Path $repoRoot '.env.local') |
  Where-Object { $_ -match '^UBAG_APP_SECRET=' } |
  ForEach-Object { $_ -replace '^UBAG_APP_SECRET=', '' } |
  Select-Object -First 1)
if (-not $secret) { throw 'UBAG_APP_SECRET not found in .env.local' }

# Known-impossible jobs: target never existed in the adapter registry, so a
# retry can only fail again. They stay as history on purpose.
$skip = @{ 'job_000000000247' = 'invalid target `gemini` (e2e-test artifact; registry only has gemini_web)' }

$targetFilter = $null
if ($Targets) { $targetFilter = ($Targets.Split(',') | ForEach-Object { $_.Trim() }) }

$headers = @{
  Authorization     = "Bearer $secret"
  'Ubag-Api-Version' = '2026-05-22'
  'Content-Type'    = 'application/json'
}

$res = Invoke-RestMethod -Uri "$gatewayUrl/v1/jobs?limit=100" -Headers $headers -TimeoutSec 60
# timed_out jobs are retryable too: the 25-min runtime cap / stale-job reaper
# catches jobs that sat queued during a wedged-consumer window.
$failed = @($res.jobs | Where-Object { @('failed_retryable', 'timed_out') -contains $_.status })

Write-Host ("Found {0} failed_retryable job(s) in the recent-100 window." -f $failed.Count)
$targets = @()
foreach ($j in $failed) {
  if ($targetFilter -and ($targetFilter -notcontains $j.target)) { continue }
  if ($skip.ContainsKey($j.job_id)) {
    Write-Host ("  SKIP {0}  target={1}  ({2})" -f $j.job_id, $j.target, $skip[$j.job_id])
    continue
  }
  $targets += $j
  Write-Host ("  RETRY {0}  target={1}  created={2}" -f $j.job_id, $j.target, $j.created_at)
}

if (-not $Apply) {
  Write-Host ''
  Write-Host 'Dry run only. Re-run with -Apply to POST the retries.'
  exit 0
}

foreach ($j in $targets) {
  $idem = "retry-{0}-{1}" -f $j.job_id, (Get-Date -Format 'yyyyMMddHHmmss')
  # POST via curl.exe: Invoke-RestMethod 5.1 intermittently returned 405 here
  # (header/verb quirk) where curl against the same endpoint succeeds.
  $out = & curl.exe -s --max-time 60 -X POST `
    -H "Authorization: Bearer $secret" `
    -H "Content-Type: application/json" `
    -H "Ubag-Api-Version: 2026-05-22" `
    -H "Idempotency-Key: $idem" `
    -d '{}' `
    "$gatewayUrl/v1/jobs/$($j.job_id)/retry" 2>&1
  $outText = ($out | Out-String).Trim()
  try {
    $r = $outText | ConvertFrom-Json
    if ($r.job_id) {
      Write-Host ("  -> {0} created as {1} ({2})" -f $j.job_id, $r.job_id, $r.status)
    } else {
      Write-Warning ("  -> {0} retry failed: {1}" -f $j.job_id, $outText.Substring(0, [Math]::Min(160, $outText.Length)))
    }
  } catch {
    Write-Warning ("  -> {0} retry failed: {1}" -f $j.job_id, $outText.Substring(0, [Math]::Min(160, $outText.Length)))
  }
  Start-Sleep -Milliseconds 500
}
Write-Host ''
Write-Host 'Done. Jobs run sequentially on the worker (concurrency 1); watch them on the Jobs page.'
