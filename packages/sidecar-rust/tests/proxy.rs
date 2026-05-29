//! Integration tests for the UBAG Rust sidecar.
//!
//! Every test spins up an in-process mock upstream (a tiny axum server bound to
//! an ephemeral loopback port) so the suite runs fully offline.

use std::sync::Arc;

use axum::body::{to_bytes, Body};
use axum::extract::State;
use axum::http::{Method, Request, StatusCode};
use axum::response::Response;
use axum::Router;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::Mutex;

use ubag_sidecar::{
    assert_loopback_host, build_app, generate_idempotency_key, is_loopback_host, SidecarConfig,
};

#[derive(Debug, Clone)]
struct Entry {
    method: String,
    path: String,
    query: Option<String>,
    idempotency_key: Option<String>,
    sidecar: Option<String>,
    authorization: Option<String>,
    api_version: Option<String>,
    body: Vec<u8>,
}

#[derive(Clone, Default)]
struct Recorded {
    entries: Arc<Mutex<Vec<Entry>>>,
}

async fn mock_upstream(State(recorded): State<Recorded>, request: Request<Body>) -> Response {
    let (parts, body) = request.into_parts();
    let bytes = to_bytes(body, 1 << 20).await.unwrap();
    let header = |name: &str| {
        parts
            .headers
            .get(name)
            .and_then(|value| value.to_str().ok())
            .map(str::to_string)
    };

    recorded.entries.lock().await.push(Entry {
        method: parts.method.to_string(),
        path: parts.uri.path().to_string(),
        query: parts.uri.query().map(str::to_string),
        idempotency_key: header("idempotency-key"),
        sidecar: header("x-ubag-sidecar"),
        authorization: header("authorization"),
        api_version: header("ubag-api-version"),
        body: bytes.to_vec(),
    });

    let status = if parts.method == Method::DELETE {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::ACCEPTED
    };

    let payload = serde_json::json!({
        "api_version": "2026-05-22",
        "job_id": "job_sidecar",
        "status": "queued",
        "trace_id": "trace_gateway"
    })
    .to_string();

    Response::builder()
        .status(status)
        .header("content-type", "application/json")
        .header("x-gateway-custom", "kept")
        // Hop-by-hop headers the sidecar must strip from the response.
        .header("proxy-authenticate", "Basic")
        .header("te", "trailers")
        .body(Body::from(payload))
        .unwrap()
}

async fn spawn(app: Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    format!("http://{addr}")
}

async fn start_mock() -> (String, Recorded) {
    let recorded = Recorded::default();
    let app = Router::new()
        .fallback(mock_upstream)
        .with_state(recorded.clone());
    let url = spawn(app).await;
    (url, recorded)
}

async fn start_sidecar(config: SidecarConfig) -> String {
    let app = build_app(&config).unwrap();
    spawn(app).await
}

fn is_ulid(value: &str) -> bool {
    const ALPHABET: &str = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
    value.len() == 26 && value.chars().all(|c| ALPHABET.contains(c))
}

#[tokio::test]
async fn health_is_loopback_aware() {
    let (gateway, _recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway.clone(),
        ..Default::default()
    })
    .await;

    let response = reqwest::get(format!("{sidecar}/health")).await.unwrap();
    assert_eq!(response.status(), 200);
    let body: serde_json::Value = response.json().await.unwrap();
    assert_eq!(body["service"], "ubag-sidecar");
    assert_eq!(body["status"], "ok");
    assert_eq!(body["loopback_only"], true);
    assert_eq!(body["gateway_base_url"], format!("{gateway}/"));
}

#[tokio::test]
async fn proxies_jobs_and_injects_idempotency() {
    let (gateway, recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        ..Default::default()
    })
    .await;

    let client = reqwest::Client::new();
    let response = client
        .post(format!("{sidecar}/v1/jobs"))
        .header("content-type", "application/json")
        .header("authorization", "Bearer client-token")
        .header("ubag-api-version", "2026-05-22")
        .body(serde_json::json!({ "hello": "sidecar" }).to_string())
        .send()
        .await
        .unwrap();

    assert_eq!(response.status(), 202);
    // Hop-by-hop response headers are stripped, custom headers preserved.
    assert_eq!(response.headers().get("x-gateway-custom").unwrap(), "kept");
    assert!(response.headers().get("proxy-authenticate").is_none());
    assert!(response.headers().get("te").is_none());
    let body: serde_json::Value = response.json().await.unwrap();
    assert_eq!(body["job_id"], "job_sidecar");

    let entries = recorded.entries.lock().await;
    let entry = entries.last().unwrap();
    assert_eq!(entry.method, "POST");
    assert_eq!(entry.path, "/v1/jobs");
    assert_eq!(entry.sidecar.as_deref(), Some("loopback"));
    assert_eq!(entry.authorization.as_deref(), Some("Bearer client-token"));
    assert_eq!(entry.api_version.as_deref(), Some("2026-05-22"));

    let key = entry.idempotency_key.as_deref().unwrap();
    assert!(is_ulid(key), "key={key}");

    let forwarded: serde_json::Value = serde_json::from_slice(&entry.body).unwrap();
    assert_eq!(forwarded["hello"], "sidecar");
    assert_eq!(forwarded["idempotency_key"], key);
}

#[tokio::test]
async fn injects_idempotency_for_artifact_routes() {
    let (gateway, recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        ..Default::default()
    })
    .await;

    let client = reqwest::Client::new();

    let put = client
        .put(format!("{sidecar}/v1/jobs/job_123/artifacts/report.txt"))
        .header("content-type", "text/plain")
        .body("artifact")
        .send()
        .await
        .unwrap();
    assert_eq!(put.status(), 202);

    let del = client
        .delete(format!("{sidecar}/v1/jobs/job_123/artifacts/report.txt"))
        .send()
        .await
        .unwrap();
    assert_eq!(del.status(), 204);

    let entries = recorded.entries.lock().await;
    assert_eq!(entries.len(), 2);
    assert_eq!(entries[0].method, "PUT");
    assert_eq!(entries[1].method, "DELETE");
    assert!(is_ulid(entries[0].idempotency_key.as_deref().unwrap()));
    assert!(is_ulid(entries[1].idempotency_key.as_deref().unwrap()));
    // The non-JSON artifact body is forwarded verbatim, not mutated.
    assert_eq!(entries[0].body, b"artifact");
}

#[tokio::test]
async fn does_not_inject_idempotency_for_reads() {
    let (gateway, recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        ..Default::default()
    })
    .await;

    let response = reqwest::get(format!("{sidecar}/v1/jobs?status=queued"))
        .await
        .unwrap();
    assert_eq!(response.status(), 202);

    let entries = recorded.entries.lock().await;
    let entry = entries.last().unwrap();
    assert_eq!(entry.method, "GET");
    assert_eq!(entry.path, "/v1/jobs");
    assert_eq!(entry.query.as_deref(), Some("status=queued"));
    assert!(entry.idempotency_key.is_none());
}

#[tokio::test]
async fn forwards_app_secret_only_when_authorization_absent() {
    let (gateway, recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        app_secret: Some("sekret".to_string()),
        ..Default::default()
    })
    .await;

    let client = reqwest::Client::new();

    // No Authorization header -> sidecar injects the configured app secret.
    client
        .get(format!("{sidecar}/v1/jobs"))
        .send()
        .await
        .unwrap();

    // Client-supplied Authorization is preserved untouched.
    client
        .get(format!("{sidecar}/v1/jobs"))
        .header("authorization", "Bearer client-token")
        .send()
        .await
        .unwrap();

    let entries = recorded.entries.lock().await;
    assert_eq!(
        entries[0].authorization.as_deref(),
        Some("Bearer sekret"),
        "app secret should fill in missing Authorization"
    );
    assert_eq!(
        entries[1].authorization.as_deref(),
        Some("Bearer client-token"),
        "client Authorization must be preserved"
    );
}

#[tokio::test]
async fn rejects_non_loopback_absolute_form_targets() {
    let (gateway, recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        ..Default::default()
    })
    .await;

    let (status, body) = raw_request(&sidecar, "http://example.invalid/v1/health").await;
    assert_eq!(status, 502);
    assert!(
        body.contains("relative /v1 routes"),
        "unexpected body: {body}"
    );
    assert!(recorded.entries.lock().await.is_empty());
}

#[tokio::test]
async fn unknown_routes_return_404() {
    let (gateway, _recorded) = start_mock().await;
    let sidecar = start_sidecar(SidecarConfig {
        gateway_base_url: gateway,
        ..Default::default()
    })
    .await;

    let response = reqwest::get(format!("{sidecar}/not-a-route")).await.unwrap();
    assert_eq!(response.status(), 404);
}

#[tokio::test]
async fn loopback_guard_rejects_public_binding() {
    // The bind-time guard refuses non-loopback hosts unless explicitly allowed.
    assert!(assert_loopback_host("0.0.0.0", false).is_err());
    assert!(assert_loopback_host("0.0.0.0", true).is_ok());
    assert!(is_loopback_host("127.0.0.1"));
    assert!(!is_loopback_host("0.0.0.0"));
}

#[test]
fn idempotency_keys_are_unique_ulids() {
    let a = generate_idempotency_key();
    let b = generate_idempotency_key();
    assert!(is_ulid(&a));
    assert!(is_ulid(&b));
    assert_ne!(a, b);
}

async fn raw_request(base_url: &str, target: &str) -> (u16, String) {
    let host = base_url.trim_start_matches("http://");
    let mut stream = tokio::net::TcpStream::connect(host).await.unwrap();
    let request =
        format!("GET {target} HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n");
    stream.write_all(request.as_bytes()).await.unwrap();

    let mut buffer = Vec::new();
    stream.read_to_end(&mut buffer).await.unwrap();
    let text = String::from_utf8_lossy(&buffer).to_string();
    let status = text
        .lines()
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse::<u16>().ok())
        .unwrap();
    (status, text)
}
