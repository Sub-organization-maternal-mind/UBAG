//! Unix domain socket listener for the UBAG sidecar.
//!
//! Binds an axum router to a Unix domain socket so that local tooling can
//! communicate with the sidecar without occupying a TCP port.  The filesystem
//! socket is inherently local, so the loopback guard (`assert_loopback_host`)
//! is not applied.
//!
//! Only compiled on Unix targets with the `uds` feature enabled.

#![cfg(unix)]

use tokio::net::UnixListener;

/// Binds `socket_path` as a Unix domain socket and serves `app` over it.
///
/// Any stale socket file at `socket_path` is removed before binding so that
/// the server can restart without `EADDRINUSE`.
pub async fn serve_uds(
    socket_path: &str,
    app: axum::Router,
) -> Result<(), Box<dyn std::error::Error>> {
    // Remove a stale socket from a previous run so we don't get EADDRINUSE.
    let _ = std::fs::remove_file(socket_path);

    let listener = UnixListener::bind(socket_path)?;
    eprintln!("ubag-sidecar listening on unix:{socket_path}");
    axum::serve(listener, app).await?;
    Ok(())
}
