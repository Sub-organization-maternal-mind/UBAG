#Requires -Version 5.1
<#
.SYNOPSIS
  Generate a development mTLS trust chain (CA + server + client) on Windows, or
  with -Node a Helper Node certificate.

.DESCRIPTION
  DEV ONLY. Requires openssl on PATH (Git for Windows ships it). Output goes to
  .\out which is gitignored. Never commit private keys. For Helper Nodes the
  production CA belongs to the fleet manager; see "Helper Node certificates" in
  README.md.

.EXAMPLE
  .\gen-certs.ps1 -Cn ubag.example.com -Client ubag-client

.EXAMPLE
  .\gen-certs.ps1 -Node helper-1                 # first certificate for node helper-1
  .\gen-certs.ps1 -Node helper-1 -Label next     # rotation: second certificate, same CA
#>
[CmdletBinding()]
param(
  [string]$Out = (Join-Path $PSScriptRoot 'out'),
  [string]$Cn = 'ubag.example.com',
  [string]$Client = 'ubag-client',
  [int]$Days = 365,
  [string]$Node = '',
  [ValidateRange(1, 3)][int]$NodeDays = 3,
  [string]$Label = ''
)

$ErrorActionPreference = 'Stop'
if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
  Write-Error 'openssl is required (install Git for Windows or OpenSSL).'; exit 1
}

# Native commands do not stop on a non-zero exit; make every openssl call fatal.
function Invoke-OpenSsl {
  & openssl @args
  if ($LASTEXITCODE -ne 0) { throw "openssl $($args -join ' ') failed ($LASTEXITCODE)" }
}

New-Item -ItemType Directory -Force -Path $Out | Out-Null

function New-DevCa {
  Write-Host '[mtls] generating CA'
  Invoke-OpenSsl genrsa -out "$Out\ca.key" 4096
  Invoke-OpenSsl req -x509 -new -nodes -key "$Out\ca.key" -sha256 -days ($Days * 3) `
    -subj '/CN=UBAG Dev Root CA/O=UBAG' -out "$Out\ca.crt"
}

if ($Node) {
  # Helper Node certificate: URI SAN spiffe://ubag/node/<id> is the node identity
  # (UBAG ignores the CN), at most 72h of validity (decision D3), clientAuth +
  # serverAuth, EC P-256 key. The CA in $Out is reused (created if missing) so a
  # rotation issues a second certificate under the same trust anchor.
  if ($Node -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') {
    Write-Error '-Node must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'; exit 1
  }
  if ($Label -and $Label -notmatch '^[A-Za-z0-9_-]+$') {
    Write-Error '-Label may contain only letters, digits, - and _'; exit 1
  }
  if (-not ((Test-Path "$Out\ca.crt") -and (Test-Path "$Out\ca.key"))) { New-DevCa }

  $suffix = if ($Label) { ".$Label" } else { '' }
  $base = "$Out\node-$Node$suffix"
  $uri = "spiffe://ubag/node/$Node"
  Write-Host "[mtls] issuing Helper Node cert $uri ($NodeDays day(s))"
  Invoke-OpenSsl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$base.key"
  Invoke-OpenSsl req -new -key "$base.key" -subj "/CN=$Node/O=UBAG" -out "$base.csr"
  @"
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth,serverAuth
subjectAltName=URI:$uri,DNS:localhost,IP:127.0.0.1
"@ | Set-Content -NoNewline "$base.ext"
  Invoke-OpenSsl x509 -req -in "$base.csr" -CA "$Out\ca.crt" -CAkey "$Out\ca.key" `
    -CAcreateserial -days $NodeDays -sha256 -extfile "$base.ext" -out "$base.crt"
  Invoke-OpenSsl verify -CAfile "$Out\ca.crt" "$base.crt" | Out-Null

  # SPKI pin: SHA-256 of the DER SubjectPublicKeyInfo, lowercase hex. Computed
  # from the key into a file (PowerShell pipelines corrupt binary data).
  Invoke-OpenSsl pkey -in "$base.key" -pubout -outform DER -out "$base.pub.der"
  $pin = ((& openssl dgst -sha256 -r "$base.pub.der") -split '\s+')[0]
  Set-Content -NoNewline -Path "$base.spki.sha256" -Value "$pin`n"
  Remove-Item "$base.csr", "$base.ext", "$base.pub.der", "$Out\*.srl" -ErrorAction SilentlyContinue

  Write-Host '[mtls] done.'
  Write-Host "  cert       $base.crt"
  Write-Host "  key        $base.key"
  Write-Host "  CA         $Out\ca.crt   (the helper plane's UBAG_HELPER_CA_FILE)"
  Write-Host "  uri_san    $uri"
  Write-Host "  spki_sha256=$pin"
  Write-Host "Register the pin as the node's current (first issue) or next (rotation) SPKI."
  exit 0
}

New-DevCa

Write-Host "[mtls] generating server cert for CN=$Cn"
openssl genrsa -out "$Out\server.key" 2048
openssl req -new -key "$Out\server.key" -subj "/CN=$Cn/O=UBAG" -out "$Out\server.csr"
@"
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:$Cn,DNS:localhost,IP:127.0.0.1
"@ | Set-Content -NoNewline "$Out\server.ext"
openssl x509 -req -in "$Out\server.csr" -CA "$Out\ca.crt" -CAkey "$Out\ca.key" `
  -CAcreateserial -days $Days -sha256 -extfile "$Out\server.ext" -out "$Out\server.crt"

Write-Host "[mtls] generating client cert CN=$Client"
openssl genrsa -out "$Out\client.key" 2048
openssl req -new -key "$Out\client.key" -subj "/CN=$Client/O=UBAG" -out "$Out\client.csr"
@"
basicConstraints=CA:FALSE
keyUsage=digitalSignature
extendedKeyUsage=clientAuth
"@ | Set-Content -NoNewline "$Out\client.ext"
openssl x509 -req -in "$Out\client.csr" -CA "$Out\ca.crt" -CAkey "$Out\ca.key" `
  -CAcreateserial -days $Days -sha256 -extfile "$Out\client.ext" -out "$Out\client.crt"

openssl pkcs12 -export -inkey "$Out\client.key" -in "$Out\client.crt" `
  -certfile "$Out\ca.crt" -passout pass: -out "$Out\client.p12"

Remove-Item "$Out\*.csr", "$Out\*.ext", "$Out\*.srl" -ErrorAction SilentlyContinue

Write-Host "[mtls] done. Files in: $Out"
Write-Host "Test: curl --cacert $Out\ca.crt --cert $Out\client.crt --key $Out\client.key https://$Cn/v1/health"
