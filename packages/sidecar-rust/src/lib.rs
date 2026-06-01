//! UBAG loopback sidecar — Rust port of the TypeScript `@ubag/sidecar`.
//!
//! The sidecar is a loopback-only localhost bridge for legacy desktop apps and
//! scripts that cannot use the full UBAG SDK directly. It exposes:
//!
//! * `GET /health` — sidecar health and the configured gateway target.
//! * `/v1/*` — a transparent reverse proxy to the configured UBAG gateway.
//!
//! Behaviour mirrors the TypeScript reference implementation 1:1:
//!
//! * refuses to bind to a non-loopback interface unless explicitly opted in,
//! * auto-generates a ULID-style `Idempotency-Key` for mutating proxy routes
//!   (job creation, webhook replay, job cancel/retry, and artifact PUT/DELETE),
//! * injects the generated key into JSON request bodies when absent,
//! * strips hop-by-hop response headers,
//! * forwards `Authorization` and `UBAG-Api-Version` (and every other client
//!   header except `Host`/`Content-Length`) to the gateway.
//!
//! Secrets are never written to disk. An optional application secret can be
//! supplied via env/CLI and is injected as a bearer token only when the
//! incoming request omits `Authorization`. See [`SecretProvider`] for the
//! OS-keychain integration seam.

use std::net::SocketAddr;
use std::sync::Arc;

use axum::body::{to_bytes, Body};
use axum::extract::State;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, Request, StatusCode, Uri};
use axum::response::Response;
use axum::Router;
use bytes::Bytes;
use url::Url;

// ── Feature-gated sub-modules ─────────────────────────────────────────────────

/// OS keychain-backed [`SecretProvider`] (compiled with the `keychain` feature).
#[cfg(feature = "keychain")]
pub mod keyring_provider;

/// Disk-backed offline request queue (always available; sled store requires
/// the `offline` feature).
pub mod offline;

/// Unix domain socket listener (Unix only, requires the `uds` feature).
#[cfg(all(unix, feature = "uds"))]
pub mod uds;

use offline::BoxedOfflineQueue;

/// Default loopback host the sidecar binds to.
pub const DEFAULT_HOST: &str = "127.0.0.1";
/// Default loopback port the sidecar listens on.
pub const DEFAULT_PORT: u16 = 7878;

/// Maximum request body the sidecar will buffer before proxying (16 MiB).
const MAX_BODY_BYTES: usize = 16 * 1024 * 1024;

/// Configuration for a sidecar instance.
#[derive(Clone, Debug)]
pub struct SidecarConfig {
    /// Upstream UBAG gateway base URL (e.g. `http://127.0.0.1:8080`).
    pub gateway_base_url: String,
    /// Local interface to bind. Must be loopback unless `allow_non_loopback`.
    pub host: String,
    /// Local port to bind.
    pub port: u16,
    /// Explicit opt-in to bind a non-loopback interface (firewall review only).
    pub allow_non_loopback: bool,
    /// Optional application secret injected as `Authorization: Bearer <secret>`
    /// when the incoming request has no `Authorization` header. Never persisted.
    pub app_secret: Option<String>,
}

impl Default for SidecarConfig {
    fn default() -> Self {
        Self {
            gateway_base_url: "http://127.0.0.1:8080".to_string(),
            host: DEFAULT_HOST.to_string(),
            port: DEFAULT_PORT,
            allow_non_loopback: false,
            app_secret: None,
        }
    }
}

/// Seam for sourcing the application secret without persisting it to disk.
///
/// The default [`EnvSecretProvider`] reads the secret straight from the
/// in-memory configuration (env/CLI). OS keychain integration (Windows
/// Credential Manager, macOS Keychain, libsecret) is a documented TODO and can
/// be added by implementing this trait — no other call sites change.
pub trait SecretProvider: Send + Sync {
    /// Returns the application secret, if one is available.
    fn app_secret(&self) -> Option<String>;
}

/// Default [`SecretProvider`] backed by the in-memory configuration value.
#[derive(Clone, Debug, Default)]
pub struct EnvSecretProvider {
    secret: Option<String>,
}

impl EnvSecretProvider {
    /// Builds a provider from an in-memory secret value.
    pub fn new(secret: Option<String>) -> Self {
        Self { secret }
    }
}

impl SecretProvider for EnvSecretProvider {
    fn app_secret(&self) -> Option<String> {
        self.secret.clone()
    }
}

#[derive(Clone)]
struct SidecarState {
    /// Normalized base URL, guaranteed to end with `/` so absolute-path joins
    /// replace the path the same way `new URL("/v1/...", base)` does in JS.
    base_url: Url,
    /// Display form of the gateway base URL (matches the TS health payload).
    gateway_display: String,
    loopback_only: bool,
    client: reqwest::Client,
    secrets: Arc<dyn SecretProvider>,
    /// Optional offline queue.  When `Some`, transport failures in
    /// `proxy_gateway` are buffered instead of surfaced as errors.
    offline_queue: Option<Arc<BoxedOfflineQueue>>,
}

/// Returns `true` for loopback hosts the sidecar may bind without opt-in.
pub fn is_loopback_host(host: &str) -> bool {
    host == "127.0.0.1" || host == "localhost" || host == "::1"
}

/// Errors unless `host` is loopback or `allow_non_loopback` is set.
pub fn assert_loopback_host(host: &str, allow_non_loopback: bool) -> Result<(), String> {
    if !allow_non_loopback && !is_loopback_host(host) {
        return Err(
            "UBAG sidecar must bind to loopback unless --allow-non-loopback is explicitly set."
                .to_string(),
        );
    }
    Ok(())
}

/// Mirrors the TS `requiresIdempotency` rules for mutating proxy routes.
pub fn requires_idempotency(method: &Method, path: &str) -> bool {
    let segments: Vec<&str> = path.trim_matches('/').split('/').collect();
    match *method {
        Method::POST => {
            if path == "/v1/jobs" || path == "/v1/webhooks/replay" {
                return true;
            }
            // /v1/jobs/{id}/cancel | /v1/jobs/{id}/retry
            segments.len() == 4
                && segments[0] == "v1"
                && segments[1] == "jobs"
                && !segments[2].is_empty()
                && (segments[3] == "cancel" || segments[3] == "retry")
        }
        Method::PUT | Method::DELETE => {
            // /v1/jobs/{id}/artifacts/{name}
            segments.len() == 5
                && segments[0] == "v1"
                && segments[1] == "jobs"
                && !segments[2].is_empty()
                && segments[3] == "artifacts"
                && !segments[4].is_empty()
        }
        _ => false,
    }
}

/// Generates a ULID-style idempotency key: 48-bit millisecond timestamp plus
/// 80 bits of randomness, encoded as 26 Crockford base32 characters.
pub fn generate_idempotency_key() -> String {
    let millis = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0) as u128;

    let mut random = [0u8; 10];
    // getrandom failures are treated as non-fatal: the timestamp prefix still
    // makes the key unique enough for idempotency dedup; randomness is best
    // effort. We never want a getrandom hiccup to fail a proxied request.
    let _ = getrandom::getrandom(&mut random);
    let mut random_value: u128 = 0;
    for byte in random {
        random_value = (random_value << 8) | u128::from(byte);
    }

    let value: u128 = ((millis & 0xFFFF_FFFF_FFFF) << 80) | random_value;
    encode_crockford_u128(value)
}

fn encode_crockford_u128(value: u128) -> String {
    const ALPHABET: &[u8; 32] = b"0123456789ABCDEFGHJKMNPQRSTVWXYZ";
    let mut out = [0u8; 26];
    let mut current = value;
    for slot in out.iter_mut().rev() {
        *slot = ALPHABET[(current & 0x1f) as usize];
        current >>= 5;
    }
    // Every byte is from the ASCII alphabet, so this is valid UTF-8.
    String::from_utf8(out.to_vec()).expect("crockford alphabet is ASCII")
}

/// Injects `idempotency_key` into a JSON object body when it is absent.
///
/// Returns the body unchanged for empty bodies, non-JSON content types, JSON
/// that is not an object, or objects that already carry the key.
pub fn inject_idempotency_key(body: Bytes, key: &str, headers: &HeaderMap) -> Bytes {
    if body.is_empty() {
        return body;
    }
    let content_type = headers
        .get("content-type")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("");
    if !content_type.to_ascii_lowercase().contains("application/json") {
        return body;
    }

    match serde_json::from_slice::<serde_json::Value>(&body) {
        Ok(serde_json::Value::Object(mut map)) => {
            if !map.contains_key("idempotency_key") {
                map.insert(
                    "idempotency_key".to_string(),
                    serde_json::Value::String(key.to_string()),
                );
                if let Ok(encoded) = serde_json::to_vec(&serde_json::Value::Object(map)) {
                    return Bytes::from(encoded);
                }
            }
            body
        }
        _ => body,
    }
}

/// Hop-by-hop response headers that must not be forwarded to the client.
fn is_hop_by_hop(name: &str) -> bool {
    matches!(
        name,
        "connection"
            | "content-encoding"
            | "content-length"
            | "keep-alive"
            | "proxy-authenticate"
            | "proxy-authorization"
            | "te"
            | "trailer"
            | "transfer-encoding"
            | "upgrade"
    )
}

/// Normalizes a gateway base URL the way `new URL(base).toString()` does:
/// a bare origin gains a trailing `/`, which makes absolute-path joins replace
/// the path exactly like the JS reference implementation.
fn normalize_base_url(base: &str) -> Result<Url, String> {
    Url::parse(base).map_err(|err| format!("invalid gateway base URL: {err}"))
}

/// Builds the axum router for a sidecar instance using the default
/// (in-memory) secret provider.
pub fn build_app(config: &SidecarConfig) -> Result<Router, String> {
    build_app_with_secrets(config, Arc::new(EnvSecretProvider::new(config.app_secret.clone())))
}

/// Builds the axum router with a caller-supplied [`SecretProvider`].
pub fn build_app_with_secrets(
    config: &SidecarConfig,
    secrets: Arc<dyn SecretProvider>,
) -> Result<Router, String> {
    let base_url = normalize_base_url(&config.gateway_base_url)?;
    let client = reqwest::Client::builder()
        .build()
        .map_err(|err| format!("failed to build HTTP client: {err}"))?;

    let state = SidecarState {
        gateway_display: base_url.to_string(),
        base_url,
        loopback_only: is_loopback_host(&config.host),
        client,
        secrets,
        offline_queue: None,
    };

    Ok(Router::new().fallback(handle).with_state(state))
}

/// Builds the axum router with a caller-supplied [`SecretProvider`] **and**
/// an optional in-memory offline queue.
///
/// When `queue` is `Some`, gateway transport failures are silently enqueued and
/// the caller receives a `202 Accepted` response instead of `502 Bad Gateway`.
pub fn build_app_with_offline_queue(
    config: &SidecarConfig,
    secrets: Arc<dyn SecretProvider>,
    queue: Option<Arc<BoxedOfflineQueue>>,
) -> Result<Router, String> {
    let base_url = normalize_base_url(&config.gateway_base_url)?;
    let client = reqwest::Client::builder()
        .build()
        .map_err(|err| format!("failed to build HTTP client: {err}"))?;

    let state = SidecarState {
        gateway_display: base_url.to_string(),
        base_url,
        loopback_only: is_loopback_host(&config.host),
        client,
        secrets,
        offline_queue: queue,
    };

    Ok(Router::new().fallback(handle).with_state(state))
}

/// Builds the app, binds the configured socket (enforcing the loopback guard),
/// and serves until shutdown.
pub async fn run(config: SidecarConfig) -> Result<(), Box<dyn std::error::Error>> {
    assert_loopback_host(&config.host, config.allow_non_loopback)?;
    let app = build_app(&config)?;

    let listener = tokio::net::TcpListener::bind((config.host.as_str(), config.port)).await?;
    let local: SocketAddr = listener.local_addr()?;
    eprintln!(
        "ubag-sidecar listening on http://{local} -> {}",
        normalize_base_url(&config.gateway_base_url)?
    );

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    Ok(())
}

async fn shutdown_signal() {
    let _ = tokio::signal::ctrl_c().await;
}

async fn handle(State(state): State<SidecarState>, request: Request<Body>) -> Response {
    match dispatch(state, request).await {
        Ok(response) => response,
        Err(message) => {
            error_response(StatusCode::BAD_GATEWAY, "UBAG-SIDECAR-PROXY-001", &message)
        }
    }
}

async fn dispatch(state: SidecarState, request: Request<Body>) -> Result<Response, String> {
    let (parts, body) = request.into_parts();
    let uri = parts.uri.clone();

    // Absolute-form requests are only honoured when they target loopback, the
    // same guard the TS `parseLocalRoute` applies.
    if let Some(host) = uri.host() {
        if !is_loopback_host(host) {
            return Err(
                "sidecar proxy only accepts relative /v1 routes or loopback absolute-form requests"
                    .to_string(),
            );
        }
    }

    let path = uri.path();
    if parts.method == Method::GET && path == "/health" {
        return Ok(health_response(&state));
    }

    if path.starts_with("/v1/") {
        return proxy_gateway(state, parts.method, uri, parts.headers, body).await;
    }

    Ok(error_response(
        StatusCode::NOT_FOUND,
        "UBAG-VALIDATION-ROUTE-001",
        "sidecar route was not found",
    ))
}

async fn proxy_gateway(
    state: SidecarState,
    method: Method,
    uri: Uri,
    in_headers: HeaderMap,
    body: Body,
) -> Result<Response, String> {
    let body_bytes = if method == Method::GET || method == Method::HEAD {
        Bytes::new()
    } else {
        to_bytes(body, MAX_BODY_BYTES)
            .await
            .map_err(|err| format!("failed to read request body: {err}"))?
    };

    let relative = match uri.query() {
        Some(query) => format!("{}?{}", uri.path(), query),
        None => uri.path().to_string(),
    };
    let target = state
        .base_url
        .join(&relative)
        .map_err(|err| format!("failed to build gateway target URL: {err}"))?;

    let mut out_headers = HeaderMap::new();
    for (name, value) in in_headers.iter() {
        let lower = name.as_str().to_ascii_lowercase();
        if lower == "host" || lower == "content-length" {
            continue;
        }
        out_headers.append(name.clone(), value.clone());
    }

    let mut body_bytes = body_bytes;
    if requires_idempotency(&method, target.path()) && !out_headers.contains_key("idempotency-key")
    {
        let key = generate_idempotency_key();
        let header_value = HeaderValue::from_str(&key)
            .map_err(|err| format!("failed to set idempotency key: {err}"))?;
        out_headers.insert(HeaderName::from_static("idempotency-key"), header_value);
        body_bytes = inject_idempotency_key(body_bytes, &key, &out_headers);
    }

    out_headers.insert(
        HeaderName::from_static("x-ubag-sidecar"),
        HeaderValue::from_static("loopback"),
    );

    // Application secret passthrough: only fill in Authorization when the client
    // did not provide one. The secret is held in memory and never persisted.
    if !out_headers.contains_key("authorization") {
        if let Some(secret) = state.secrets.app_secret() {
            if let Ok(value) = HeaderValue::from_str(&format!("Bearer {secret}")) {
                out_headers.insert(HeaderName::from_static("authorization"), value);
            }
        }
    }

    let send_result = state
        .client
        .request(method, target)
        .headers(out_headers)
        .body(body_bytes.to_vec())
        .send()
        .await;

    // When a transport error occurs and an offline queue is configured, buffer
    // the request and return 202 Accepted instead of surfacing 502.
    let upstream = match send_result {
        Ok(resp) => resp,
        Err(err) => {
            if let Some(ref queue) = state.offline_queue {
                let request_value = serde_json::json!({
                    "path": uri.path(),
                    "query": uri.query(),
                });
                let entry = queue.enqueue(request_value);
                let payload = serde_json::json!({
                    "queued": true,
                    "entry_id": entry.id,
                    "message": "request queued for later delivery",
                });
                return Ok(json_response(StatusCode::ACCEPTED, &payload));
            }
            return Err(format!("gateway request failed: {err}"));
        }
    };

    let status = upstream.status();
    let mut builder = Response::builder().status(status);
    for (name, value) in upstream.headers().iter() {
        if !is_hop_by_hop(&name.as_str().to_ascii_lowercase()) {
            builder = builder.header(name, value);
        }
    }
    let response_body = upstream
        .bytes()
        .await
        .map_err(|err| format!("failed to read gateway response body: {err}"))?;

    builder
        .body(Body::from(response_body))
        .map_err(|err| format!("failed to build proxy response: {err}"))
}

fn health_response(state: &SidecarState) -> Response {
    let payload = serde_json::json!({
        "service": "ubag-sidecar",
        "status": "ok",
        "gateway_base_url": state.gateway_display,
        "loopback_only": state.loopback_only,
        "trace_id": trace_id(),
    });
    json_response(StatusCode::OK, &payload)
}

fn error_response(status: StatusCode, code: &str, message: &str) -> Response {
    let category = if code.contains("SIDECAR") {
        "sidecar"
    } else {
        "validation"
    };
    let payload = serde_json::json!({
        "error": {
            "code": code,
            "category": category,
            "message": message,
            "retryable": false,
            "doc_url": format!("https://docs.ubag.dev/errors/{code}"),
            "trace_id": trace_id(),
        }
    });
    json_response(status, &payload)
}

fn json_response(status: StatusCode, payload: &serde_json::Value) -> Response {
    let body = serde_json::to_vec(payload).unwrap_or_else(|_| b"{}".to_vec());
    Response::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Body::from(body))
        .expect("static JSON response is always valid")
}

fn trace_id() -> String {
    let millis = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);
    let mut random = [0u8; 6];
    let _ = getrandom::getrandom(&mut random);
    let mut suffix = String::with_capacity(12);
    for byte in random {
        suffix.push_str(&format!("{byte:02x}"));
    }
    format!("trace_{millis:x}{suffix}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn idempotency_key_is_ulid_shaped() {
        let key = generate_idempotency_key();
        assert_eq!(key.len(), 26);
        let allowed = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
        assert!(key.chars().all(|c| allowed.contains(c)), "key={key}");
        // Crockford alphabet excludes I, L, O, U.
        assert!(!key.contains(['I', 'L', 'O', 'U']));
    }

    #[test]
    fn requires_idempotency_matches_ts_rules() {
        assert!(requires_idempotency(&Method::POST, "/v1/jobs"));
        assert!(requires_idempotency(&Method::POST, "/v1/webhooks/replay"));
        assert!(requires_idempotency(&Method::POST, "/v1/jobs/job_123/cancel"));
        assert!(requires_idempotency(&Method::POST, "/v1/jobs/job_123/retry"));
        assert!(requires_idempotency(
            &Method::PUT,
            "/v1/jobs/job_123/artifacts/report.txt"
        ));
        assert!(requires_idempotency(
            &Method::DELETE,
            "/v1/jobs/job_123/artifacts/report.txt"
        ));

        // Non-mutating / non-matching routes must not auto-generate keys.
        assert!(!requires_idempotency(&Method::GET, "/v1/jobs"));
        assert!(!requires_idempotency(&Method::POST, "/v1/jobs/job_123"));
        assert!(!requires_idempotency(&Method::POST, "/v1/jobs/job_123/pause"));
        assert!(!requires_idempotency(
            &Method::PATCH,
            "/v1/jobs/job_123/artifacts/report.txt"
        ));
        assert!(!requires_idempotency(
            &Method::PUT,
            "/v1/jobs/job_123/artifacts"
        ));
    }

    #[test]
    fn loopback_guard_matches_ts() {
        assert!(is_loopback_host("127.0.0.1"));
        assert!(is_loopback_host("localhost"));
        assert!(is_loopback_host("::1"));
        assert!(!is_loopback_host("0.0.0.0"));

        assert!(assert_loopback_host("0.0.0.0", false).is_err());
        assert!(assert_loopback_host("0.0.0.0", true).is_ok());
        assert!(assert_loopback_host("127.0.0.1", false).is_ok());
    }

    #[test]
    fn inject_idempotency_only_for_json_objects() {
        let mut headers = HeaderMap::new();
        headers.insert("content-type", HeaderValue::from_static("application/json"));

        let injected = inject_idempotency_key(
            Bytes::from_static(b"{\"hello\":\"world\"}"),
            "01ABCDEFGHJKMNPQRSTVWXYZ12",
            &headers,
        );
        let parsed: serde_json::Value = serde_json::from_slice(&injected).unwrap();
        assert_eq!(parsed["idempotency_key"], "01ABCDEFGHJKMNPQRSTVWXYZ12");
        assert_eq!(parsed["hello"], "world");

        // Existing key is preserved untouched.
        let kept = inject_idempotency_key(
            Bytes::from_static(b"{\"idempotency_key\":\"keep\"}"),
            "01ABCDEFGHJKMNPQRSTVWXYZ12",
            &headers,
        );
        let parsed_kept: serde_json::Value = serde_json::from_slice(&kept).unwrap();
        assert_eq!(parsed_kept["idempotency_key"], "keep");

        // Non-JSON bodies are passed through verbatim.
        let mut text_headers = HeaderMap::new();
        text_headers.insert("content-type", HeaderValue::from_static("text/plain"));
        let raw = inject_idempotency_key(Bytes::from_static(b"artifact"), "k", &text_headers);
        assert_eq!(&raw[..], b"artifact");
    }
}
