//! Integration test for the Unix domain socket listener.
//!
//! Only compiled and run on Unix targets with the `uds` feature enabled.

#![cfg(all(unix, feature = "uds"))]

use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;

/// Verifies that the sidecar router responds to a raw HTTP/1.0 GET over a
/// Unix domain socket.
#[tokio::test]
async fn uds_health_responds_200() {
    // Build a unique socket path under the system temp directory.
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let socket_path = std::env::temp_dir()
        .join(format!("ubag_test_{nanos}.sock"))
        .to_string_lossy()
        .into_owned();

    let config = ubag_sidecar::SidecarConfig {
        host: "127.0.0.1".to_string(),
        port: 0,
        ..Default::default()
    };
    let app = ubag_sidecar::build_app(&config).unwrap();

    let path_clone = socket_path.clone();
    tokio::spawn(async move {
        ubag_sidecar::uds::serve_uds(&path_clone, app)
            .await
            .ok();
    });

    // Give the listener a moment to bind.
    tokio::time::sleep(Duration::from_millis(100)).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    stream
        .write_all(b"GET /health HTTP/1.0\r\nHost: localhost\r\n\r\n")
        .await
        .unwrap();

    let mut buf = vec![0u8; 4096];
    let n = stream.read(&mut buf).await.unwrap();
    let response = String::from_utf8_lossy(&buf[..n]);

    assert!(
        response.contains("200"),
        "expected HTTP 200, got:\n{response}"
    );
    assert!(
        response.contains("ubag-sidecar"),
        "expected JSON body with service key, got:\n{response}"
    );

    // Cleanup stale socket.
    let _ = std::fs::remove_file(&socket_path);
}
