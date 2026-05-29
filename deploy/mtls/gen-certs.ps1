#Requires -Version 5.1
<#
.SYNOPSIS
  Generate a development mTLS trust chain (CA + server + client) on Windows.

.DESCRIPTION
  DEV ONLY. Requires openssl on PATH (Git for Windows ships it). Output goes to
  .\out which is gitignored. Never commit private keys.

.EXAMPLE
  .\gen-certs.ps1 -Cn ubag.example.com -Client ubag-client
#>
[CmdletBinding()]
param(
  [string]$Out = (Join-Path $PSScriptRoot 'out'),
  [string]$Cn = 'ubag.example.com',
  [string]$Client = 'ubag-client',
  [int]$Days = 365
)

$ErrorActionPreference = 'Stop'
if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
  Write-Error 'openssl is required (install Git for Windows or OpenSSL).'; exit 1
}

New-Item -ItemType Directory -Force -Path $Out | Out-Null

Write-Host '[mtls] generating CA'
openssl genrsa -out "$Out\ca.key" 4096
openssl req -x509 -new -nodes -key "$Out\ca.key" -sha256 -days ($Days * 3) `
  -subj '/CN=UBAG Dev Root CA/O=UBAG' -out "$Out\ca.crt"

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
