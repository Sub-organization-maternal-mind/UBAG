//! `ubag-sidecar` CLI entrypoint.
//!
//! Configuration is resolved from CLI flags first, then environment variables:
//! `UBAG_GATEWAY_URL`, `UBAG_SIDECAR_HOST`, `UBAG_SIDECAR_PORT`,
//! `UBAG_APP_SECRET`. The application secret is held in memory only and is
//! never written to disk.

use clap::Parser;

use ubag_sidecar::{run, SidecarConfig, DEFAULT_HOST, DEFAULT_PORT};

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
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();
    let config = SidecarConfig {
        gateway_base_url: cli.gateway,
        host: cli.host,
        port: cli.port,
        allow_non_loopback: cli.allow_non_loopback,
        app_secret: cli.app_secret,
    };

    if let Err(error) = run(config).await {
        eprintln!("ubag-sidecar: {error}");
        std::process::exit(1);
    }
}
