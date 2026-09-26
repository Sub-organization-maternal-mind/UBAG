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

param([switch]$Apply)

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

$headers = @{
  Authorization     = "Bearer $secret"
  'Ubag-Api-Version' = '2026-05-22'
  'Content-Type'    = 'application/json'
}

$res = Invoke-RestMethod -Uri "$gatewayUrl/v1/jobs?limit=100" -Headers $headers -TimeoutSec 20
$failed = @($res.jobs | Where-Object { $_.status -eq 'failed_retryable' })

Write-Host ("Found {0} failed_retryable job(s) in the recent-100 window." -f $failed.Count)
$targets = @()
foreach ($j in $failed) {
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
  $postHeaders = @{
    Authorization      = "Bearer $secret"
    'Ubag-Api-Version' = '2026-05-22'
    'Content-Type'     = 'application/json'
    'Idempotency-Key'  = $idem
  }
  try {
    $r = Invoke-RestMethod -Method Post -Uri "$gatewayUrl/v1/jobs/$($j.job_id)/retry" `
      -Headers $postHeaders -Body '{}' -TimeoutSec 20
    Write-Host ("  -> {0} created as {1} ({2})" -f $j.job_id, $r.job_id, $r.status)
  } catch {
    Write-Warning ("  -> {0} retry failed: {1}" -f $j.job_id, $_.Exception.Message)
  }
  Start-Sleep -Milliseconds 500
}
Write-Host ''
Write-Host 'Done. Jobs run sequentially on the worker (concurrency 1); watch them on the Jobs page.'
