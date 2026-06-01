//! `ubag-sidecar` CLI entrypoint.
//!
//! Configuration is resolved from CLI flags first, then environment variables:
//! `UBAG_GATEWAY_URL`, `UBAG_SIDECAR_HOST`, `UBAG_SIDECAR_PORT`,
//! `UBAG_APP_SECRET`. The application secret is held in memory only and is
//! never written to disk.

use std::sync::Arc;

use clap::Parser;

use ubag_sidecar::{
    EnvSecretProvider, SidecarConfig, SecretProvider, DEFAULT_HOST, DEFAULT_PORT,
};

/// Loopback-only localhost sidecar that proxies legacy UBAG clients to the
/// gateway.
#[derive(Parser, Debug)]
#[command(name = "ubag-sidecar", version, about)]
struct Cli {
    /// Local interface to bind. Must be loopback unless --allow-non-loopback.
    #[arg(long, env = "UBAG_SIDECAR_HOST", default_value = DEFAULT_HOST)]
    host: String,

    /// Local port to listen on.
    #[arg(long, env = "UBAG_SIDECAR_PORT", default_value_t = DEFAULT_PORT)]
    port: u16,

    /// Upstream UBAG gateway base URL.
    #[arg(
        long = "gateway",
        env = "UBAG_GATEWAY_URL",
        default_value = "http://127.0.0.1:8080"
    )]
    gateway: String,

    /// Explicitly allow binding a non-loopback interface (firewall review only).
    #[arg(long, default_value_t = false)]
    allow_non_loopback: bool,

    /// Optional application secret injected as `Authorization: Bearer <secret>`
    /// when the client request omits an Authorization header. Never persisted.
    #[arg(long = "app-secret", env = "UBAG_APP_SECRET", hide_env_values = true)]
    app_secret: Option<String>,

    /// Read the application secret from the OS keychain (Windows Credential
    /// Manager / macOS Keychain / libsecret) instead of env/CLI.
    /// Falls back to --app-secret / UBAG_APP_SECRET when no keychain entry
    /// is present.  Requires the `keychain` feature.
    #[arg(
        long = "use-keychain",
        env = "UBAG_SIDECAR_USE_KEYCHAIN",
        default_value_t = false
    )]
    use_keychain: bool,

    /// Directory in which to persist the offline request queue (sled database).
    /// When set, gateway transport failures are buffered to disk and retried on
    /// the next flush rather than surfaced as errors.  Requires the `offline`
    /// feature for the sled-backed store; the in-memory store is used otherwise.
    #[arg(long = "offline-dir", env = "UBAG_SIDECAR_OFFLINE_DIR")]
    offline_dir: Option<String>,

    /// Path to a Unix domain socket to listen on instead of the TCP port.
    /// When provided, the loopback guard is bypassed for the UDS listener.
    /// Unix targets only; requires the `uds` feature.
    #[cfg(unix)]
    #[arg(long = "socket", env = "UBAG_SIDECAR_SOCKET")]
    socket: Option<String>,
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();
    let config = SidecarConfig {
        gateway_base_url: cli.gateway.clone(),
        host: cli.host.clone(),
        port: cli.port,
        allow_non_loopback: cli.allow_non_loopback,
        app_secret: cli.app_secret.clone(),
    };

    // ── Secret provider ───────────────────────────────────────────────────────

    let secrets: Arc<dyn SecretProvider> = build_secret_provider(&cli);

    // ── Unix domain socket path ───────────────────────────────────────────────

    #[cfg(unix)]
    let socket = cli.socket.clone();
    #[cfg(not(unix))]
    let socket: Option<String> = None;

    // ── Offline queue ─────────────────────────────────────────────────────────

    let offline_queue = build_offline_queue(&cli.offline_dir);

    // ── Start listening ───────────────────────────────────────────────────────

    if let Some(ref socket_path) = socket {
        // UDS path: only available on Unix with the `uds` feature.
        #[cfg(all(unix, feature = "uds"))]
        {
            let app = match ubag_sidecar::build_app_with_offline_queue(
                &config,
                secrets,
                offline_queue,
            ) {
                Ok(a) => a,
                Err(err) => {
                    eprintln!("ubag-sidecar: {err}");
                    std::process::exit(1);
                }
            };
            if let Err(err) = ubag_sidecar::uds::serve_uds(socket_path, app).await {
                eprintln!("ubag-sidecar (uds): {err}");
                std::process::exit(1);
            }
        }
        #[cfg(not(all(unix, feature = "uds")))]
        {
            let _ = (&offline_queue, &secrets, socket_path);
            eprintln!(
                "ubag-sidecar: --socket requires the `uds` feature on a Unix target \
                 (socket={socket_path})"
            );
            std::process::exit(1);
        }
    } else {
        // TCP path (default): wire secrets and offline queue.
        if let Err(e) = ubag_sidecar::assert_loopback_host(&config.host, config.allow_non_loopback) {
            eprintln!("ubag-sidecar: {e}");
            std::process::exit(1);
        }
        let app = match ubag_sidecar::build_app_with_offline_queue(&config, secrets, offline_queue) {
            Ok(a) => a,
            Err(err) => {
                eprintln!("ubag-sidecar: {err}");
                std::process::exit(1);
            }
        };
        let bind_addr = format!("{}:{}", config.host, config.port);
        let listener = match tokio::net::TcpListener::bind(&bind_addr).await {
            Ok(l) => l,
            Err(err) => {
                eprintln!("ubag-sidecar: failed to bind {bind_addr}: {err}");
                std::process::exit(1);
            }
        };
        let local = match listener.local_addr() {
            Ok(addr) => addr,
            Err(err) => {
                eprintln!("ubag-sidecar: {err}");
                std::process::exit(1);
            }
        };
        eprintln!("ubag-sidecar listening on http://{local} -> {}", config.gateway_base_url);
        if let Err(err) = axum::serve(listener, app)
            .with_graceful_shutdown(async {
                if let Err(e) = tokio::signal::ctrl_c().await {
                    eprintln!("ubag-sidecar: signal handler error: {e}");
                }
            })
            .await
        {
            eprintln!("ubag-sidecar: server error: {err}");
            std::process::exit(1);
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn build_secret_provider(cli: &Cli) -> Arc<dyn SecretProvider> {
    if cli.use_keychain {
        #[cfg(feature = "keychain")]
        {
            use ubag_sidecar::keyring_provider::KeyringSecretProvider;
            return Arc::new(KeyringSecretProvider::new(
                "ubag-sidecar",
                "app_secret",
                cli.app_secret.clone(),
            ));
        }
        #[cfg(not(feature = "keychain"))]
        {
            eprintln!(
                "ubag-sidecar: --use-keychain requires the `keychain` feature; \
                 falling back to env/CLI secret"
            );
        }
    }
    Arc::new(EnvSecretProvider::new(cli.app_secret.clone()))
}

fn build_offline_queue(
    offline_dir: &Option<String>,
) -> Option<Arc<ubag_sidecar::offline::BoxedOfflineQueue>> {
    let dir = offline_dir.as_deref()?;

    #[cfg(feature = "offline")]
    {
        use ubag_sidecar::offline::{BoxedOfflineQueue, OfflineQueue, SledOfflineStore};
        match SledOfflineStore::open(dir) {
            Ok(store) => {
                eprintln!("ubag-sidecar: offline queue backed by sled at {dir}");
                let q: BoxedOfflineQueue =
                    OfflineQueue::new(Box::new(store) as Box<dyn ubag_sidecar::offline::OfflineStore>);
                return Some(Arc::new(q));
            }
            Err(err) => {
                eprintln!(
                    "ubag-sidecar: failed to open sled at {dir}: {err}; \
                     falling back to in-memory queue"
                );
            }
        }
    }

    // Fallback: in-memory queue (offline feature off, or sled open failed).
    use ubag_sidecar::offline::{BoxedOfflineQueue, MemoryOfflineStore, OfflineQueue};
    let q: BoxedOfflineQueue =
        OfflineQueue::new(Box::new(MemoryOfflineStore::default()) as Box<dyn ubag_sidecar::offline::OfflineStore>);
    Some(Arc::new(q))
}
