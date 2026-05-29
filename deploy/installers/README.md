# UBAG Installers

Cross-platform install helpers for the UBAG gateway. Two install modes:

- **compose** — bring up the small-profile Docker Compose stack
  (`docker-compose.small.yml`). Best for single-node and evaluation.
- **binary** — install a pre-built `ubag-gateway` binary to a prefix and wire it
  into the OS service manager (systemd / launchd / Windows service).

## Files

```
deploy/installers/
├── install.sh                         # Linux / macOS installer
├── install.ps1                        # Windows installer
├── gateway.env.example                # env file template (UBAG_* subset)
├── systemd/ubag-gateway.service       # Linux systemd unit (hardened)
├── launchd/com.ubag.gateway.plist     # macOS LaunchDaemon
└── README.md
```

## Safety model

- **No `curl | bash` of untrusted code.** When a binary URL is supplied
  (`--url` / `-Url`), it is downloaded to a temp file and verified against a
  **required** SHA-256 checksum (`--sha256` / `-Sha256`) before install. Without
  a checksum the URL is rejected.
- **Idempotent.** Re-running converges: existing `env.local` is reused, the
  service user/dirs/unit are created only if missing.
- **Secrets stay out of the repo.** The compose mode refuses to start while
  `replace-with-local` / `set-a-local` placeholders remain in `env.local`.
  The binary mode installs an env-file template you must edit and `chmod 0600`.

## Linux / macOS

```bash
# Compose stack (default)
deploy/installers/install.sh --mode compose

# Install a locally-built binary + systemd unit (Linux)
deploy/installers/install.sh --mode binary \
  --binary ./ubag-gateway --prefix /usr/local --systemd

# Install from a URL with checksum verification
deploy/installers/install.sh --mode binary \
  --url https://example.com/ubag-gateway --sha256 <hex> \
  --prefix /usr/local --systemd

sudo systemctl enable --now ubag-gateway   # after editing /etc/ubag/gateway.env
```

### macOS service (launchd)

```bash
deploy/installers/install.sh --mode binary --binary ./ubag-gateway --prefix /usr/local
sudo cp deploy/installers/launchd/com.ubag.gateway.plist /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/com.ubag.gateway.plist
sudo launchctl load -w /Library/LaunchDaemons/com.ubag.gateway.plist
```

## Windows

```powershell
# Compose stack (default)
deploy\installers\install.ps1 -Mode Compose

# Install a binary
deploy\installers\install.ps1 -Mode Binary -Binary .\ubag-gateway.exe `
  -Prefix "C:\Program Files\UBAG"
```

### Windows service wrapper notes

The gateway is a console process; Windows needs a service wrapper. Two options:

1. **NSSM (recommended):**
   ```powershell
   nssm install UBAGGateway "C:\Program Files\UBAG\bin\ubag-gateway.exe"
   nssm set UBAGGateway AppDirectory "C:\Program Files\UBAG"
   nssm set UBAGGateway Start SERVICE_AUTO_START
   # Set environment from the env file contents (NSSM AppEnvironmentExtra),
   # or via [Environment]::SetEnvironmentVariable for the service account.
   nssm start UBAGGateway
   ```
2. **sc.exe (no env-file support; set machine env vars first):**
   ```powershell
   sc.exe create UBAGGateway binPath= "C:\Program Files\UBAG\bin\ubag-gateway.exe" start= auto
   sc.exe start UBAGGateway
   ```

Store `UBAG_APP_SECRET` and other secrets as service-account environment
variables or via a secret manager — do not place them on the command line.

## Validation status

| Artifact | Validates offline | Requires external infra |
| --- | --- | --- |
| `install.sh` syntax | `bash -n install.sh` | Docker (compose mode) or a real binary/URL |
| `install.ps1` syntax | PSScriptAnalyzer / parse | Docker (compose mode) or a real binary/URL |
| systemd unit | `systemd-analyze verify` (on Linux) | a running systemd host to enable |
| launchd plist | `plutil -lint` (on macOS) | a macOS host to load |
