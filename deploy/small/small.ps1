[CmdletBinding()]
param(
  [ValidateSet("config", "up", "down", "ps", "logs", "smoke", "migrate")]
  [string]$Action = "config",

  [string[]]$Profile = @(),

  [switch]$UseExampleEnv,

  [switch]$AllowSecretConfigOutput
)

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = (Resolve-Path -LiteralPath (Join-Path $ScriptDir "..\..")).Path
$ComposeFile = Join-Path $RepoRoot "docker-compose.small.yml"
$LocalEnvFile = Join-Path $ScriptDir "env.local"
$ExampleEnvFile = Join-Path $ScriptDir "env.example"
$EnvFile = $LocalEnvFile

if ($UseExampleEnv -and $AllowSecretConfigOutput) {
  throw "-UseExampleEnv and -AllowSecretConfigOutput cannot be combined."
}

if ($UseExampleEnv) {
  if ($Action -ne "config") {
    throw "-UseExampleEnv is only supported with -Action config."
  }
  $EnvFile = $ExampleEnvFile
} elseif ($Action -eq "config" -and -not $AllowSecretConfigOutput) {
  $EnvFile = $ExampleEnvFile
} elseif (-not (Test-Path -LiteralPath $EnvFile)) {
  $EnvFile = $ExampleEnvFile
}

function Assert-SafeRunEnv {
  if ($Action -notin @("up", "smoke", "migrate")) {
    return
  }

  if ($EnvFile -ne $LocalEnvFile) {
    throw "Create deploy/small/env.local from deploy/small/env.example before running '$Action'."
  }

  $placeholders = Select-String -LiteralPath $EnvFile -Pattern "replace-with-local-","set-a-local-","change-me","changeme" -SimpleMatch
  if ($placeholders) {
    throw "Replace placeholder values in deploy/small/env.local before running '$Action'."
  }

  $backingBindHost = Get-SmallEnvValue -Name "UBAG_BACKING_BIND_HOST" -Fallback (Get-SmallEnvValue -Name "UBAG_BIND_HOST" -Fallback "127.0.0.1")
  $allowPublicBackingPorts = Get-SmallEnvValue -Name "UBAG_ALLOW_PUBLIC_BACKING_PORTS" -Fallback "false"
  if ($backingBindHost -notin @("127.0.0.1", "localhost", "::1") -and $allowPublicBackingPorts -ne "true") {
    throw "UBAG_BACKING_BIND_HOST must stay loopback for small profile backing services. Put Caddy behind the public edge, or set UBAG_ALLOW_PUBLIC_BACKING_PORTS=true after firewall review."
  }

  foreach ($requiredName in @("UBAG_APP_SECRET", "POSTGRES_PASSWORD", "MINIO_ROOT_PASSWORD", "UBAG_MINIO_ACCESS_KEY", "UBAG_MINIO_SECRET_KEY", "UBAG_MINIO_BUCKET", "GRAFANA_ADMIN_PASSWORD")) {
    $value = Get-SmallEnvValue -Name $requiredName -Fallback ""
    if (-not $value -or (Test-PlaceholderValue -Value $value)) {
      throw "$requiredName must be set to a non-placeholder value in deploy/small/env.local before running '$Action'."
    }
  }

  $executorMode = (Get-SmallEnvValue -Name "UBAG_EXECUTOR_MODE" -Fallback "noop").Trim().ToLowerInvariant()
  if ($executorMode -eq "nats" -and $ResolvedProfiles -notcontains "queue") {
    throw "UBAG_EXECUTOR_MODE=nats requires the queue profile. Re-run with -Profile queue."
  }
  if ($executorMode -eq "nats" -and (Test-SmallEnvBool -Name "UBAG_WORKER_CONSUMER_ENABLED")) {
    foreach ($requiredName in @("UBAG_NATS_WORKER_DURABLE", "UBAG_NATS_WORKER_ACK_WAIT_MS", "UBAG_NATS_WORKER_NAK_DELAY_MS", "UBAG_NATS_WORKER_MAX_DELIVER")) {
      $value = Get-SmallEnvValue -Name $requiredName -Fallback ""
      if (-not $value -or (Test-PlaceholderValue -Value $value)) {
        throw "$requiredName must be set to a non-placeholder value when NATS worker consumption is enabled."
      }
    }
  }

  $artifactStore = (Get-SmallEnvValue -Name "UBAG_ARTIFACT_STORE" -Fallback "memory").Trim().ToLowerInvariant()
  if ($artifactStore -eq "minio") {
    foreach ($requiredName in @("UBAG_MINIO_ENDPOINT", "UBAG_MINIO_ACCESS_KEY", "UBAG_MINIO_SECRET_KEY", "UBAG_MINIO_BUCKET")) {
      $value = Get-SmallEnvValue -Name $requiredName -Fallback ""
      if (-not $value -or (Test-PlaceholderValue -Value $value)) {
        throw "$requiredName must be set to a non-placeholder value when UBAG_ARTIFACT_STORE=minio."
      }
    }
  }

  $gatewayStore = (Get-SmallEnvValue -Name "UBAG_GATEWAY_STORE" -Fallback "memory").Trim().ToLowerInvariant()
  $webhookOutbox = (Get-SmallEnvValue -Name "UBAG_WEBHOOK_OUTBOX" -Fallback "").Trim().ToLowerInvariant()
  if (-not $webhookOutbox) {
    if ($gatewayStore -eq "postgres") {
      $webhookOutbox = "postgres"
    } else {
      $webhookOutbox = "memory"
    }
  }
  if ($webhookOutbox -eq "postgres" -and $gatewayStore -ne "postgres") {
    throw "UBAG_WEBHOOK_OUTBOX=postgres requires UBAG_GATEWAY_STORE=postgres and a valid UBAG_POSTGRES_DSN."
  }
  if (Test-SmallEnvBool -Name "UBAG_WEBHOOK_WORKER_ENABLED") {
    if ($webhookOutbox -ne "postgres") {
      throw "UBAG_WEBHOOK_WORKER_ENABLED=true requires UBAG_WEBHOOK_OUTBOX=postgres for durable retries in the small profile."
    }
    foreach ($requiredName in @("UBAG_WEBHOOK_SECRET", "UBAG_WEBHOOK_MAX_ATTEMPTS", "UBAG_WEBHOOK_BATCH_SIZE", "UBAG_WEBHOOK_LEASE_MS", "UBAG_WEBHOOK_REQUEST_TIMEOUT_MS", "UBAG_WEBHOOK_RETRY_BASE_MS", "UBAG_WEBHOOK_RETRY_MAX_MS", "UBAG_WEBHOOK_WORKER_ID")) {
      $value = Get-SmallEnvValue -Name $requiredName -Fallback ""
      if (-not $value -or (Test-PlaceholderValue -Value $value)) {
        throw "$requiredName must be set to a non-placeholder value when UBAG_WEBHOOK_WORKER_ENABLED=true."
      }
    }
    $postgresDsn = Get-SmallEnvValue -Name "UBAG_POSTGRES_DSN" -Fallback ""
    if (-not $postgresDsn -or (Test-PlaceholderValue -Value $postgresDsn)) {
      throw "UBAG_POSTGRES_DSN must be set to a non-placeholder value when webhook delivery is Postgres-backed."
    }
    $allowedWebhookHosts = Get-SmallEnvValue -Name "UBAG_WEBHOOK_ALLOWED_HOSTS" -Fallback ""
    if (-not $allowedWebhookHosts -and -not (Test-SmallEnvBool -Name "UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST")) {
      throw "UBAG_WEBHOOK_ALLOWED_HOSTS must list outbound callback hosts when UBAG_WEBHOOK_WORKER_ENABLED=true, or set UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true after outbound SSRF review."
    }
  }
}

function Test-PlaceholderValue {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Value
  )

  $trimmed = $Value.Trim().ToLowerInvariant()
  return $trimmed.StartsWith("replace-with-local-") -or
    $trimmed.StartsWith("set-a-local-") -or
    $trimmed -eq "change-me" -or
    $trimmed -eq "changeme"
}

function Test-SmallEnvBool {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Name
  )

  $value = (Get-SmallEnvValue -Name $Name -Fallback "false").Trim().ToLowerInvariant()
  return $value -in @("1", "true", "yes")
}

function Get-SmallEnvValue {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Name,

    [Parameter(Mandatory = $true)]
    [AllowEmptyString()]
    [string]$Fallback
  )

  $processValue = [Environment]::GetEnvironmentVariable($Name)
  if ($processValue) {
    return $processValue
  }

  if (Test-Path -LiteralPath $EnvFile) {
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
      if ($line -match "^\s*$([Regex]::Escape($Name))\s*=\s*(.+?)\s*$") {
        return $Matches[1].Trim().Trim('"').Trim("'")
      }
    }
  }

  return $Fallback
}

function Invoke-SmallCompose {
  param(
    [Parameter(Mandatory = $true)]
    [string[]]$Arguments
  )

  & docker @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "docker $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

$AllowedProfiles = @("observability", "queue", "smoke", "migrate")
$ResolvedProfiles = @()
foreach ($profileValue in $Profile) {
  foreach ($profileName in ($profileValue -split ",")) {
    $trimmedProfile = $profileName.Trim()
    if ($trimmedProfile) {
      if ($AllowedProfiles -notcontains $trimmedProfile) {
        throw "Unknown small profile '$trimmedProfile'. Allowed profiles: $($AllowedProfiles -join ', ')"
      }
      $ResolvedProfiles += $trimmedProfile
    }
  }
}

$ComposeArgs = @("compose", "--env-file", $EnvFile, "-f", $ComposeFile)
foreach ($profileName in $ResolvedProfiles) {
  $ComposeArgs += @("--profile", $profileName)
}

Assert-SafeRunEnv

switch ($Action) {
  "config" {
    Invoke-SmallCompose ($ComposeArgs + @("config"))
  }
  "up" {
    Invoke-SmallCompose ($ComposeArgs + @("up", "-d", "--build"))
  }
  "down" {
    Invoke-SmallCompose ($ComposeArgs + @("down"))
  }
  "ps" {
    Invoke-SmallCompose ($ComposeArgs + @("ps"))
  }
  "logs" {
    Invoke-SmallCompose ($ComposeArgs + @("logs", "-f", "--tail=200"))
  }
  "migrate" {
    Invoke-SmallCompose ($ComposeArgs + @("--profile", "migrate", "run", "--rm", "postgres-migrate"))
  }
  "smoke" {
    Invoke-SmallCompose ($ComposeArgs + @("up", "-d", "--build", "gateway", "nginx-dashboard"))

    $hostName = Get-SmallEnvValue -Name "UBAG_BACKING_BIND_HOST" -Fallback (Get-SmallEnvValue -Name "UBAG_BIND_HOST" -Fallback "127.0.0.1")
    if ($hostName -eq "0.0.0.0") {
      $hostName = "127.0.0.1"
    }

    $gatewayPort = Get-SmallEnvValue -Name "UBAG_GATEWAY_PORT" -Fallback "8080"
    $healthUri = "http://${hostName}:${gatewayPort}/v1/health"
    Invoke-RestMethod -Uri $healthUri -TimeoutSec 10 | ConvertTo-Json -Depth 8
    $readyUri = "http://${hostName}:${gatewayPort}/v1/ready"
    Invoke-RestMethod -Uri $readyUri -TimeoutSec 10 | ConvertTo-Json -Depth 8

    $edgeHostName = Get-SmallEnvValue -Name "UBAG_EDGE_BIND_HOST" -Fallback "127.0.0.1"
    if ($edgeHostName -eq "0.0.0.0") {
      $edgeHostName = "127.0.0.1"
    }
    $nginxPort = Get-SmallEnvValue -Name "UBAG_NGINX_HTTP_PORT" -Fallback "8083"
    $ingressHealthUri = "http://${edgeHostName}:${nginxPort}/v1/health"
    Invoke-RestMethod -Uri $ingressHealthUri -TimeoutSec 10 | ConvertTo-Json -Depth 8

    Invoke-SmallCompose ($ComposeArgs + @("--profile", "smoke", "run", "--rm", "mock-worker-smoke"))
  }
}
