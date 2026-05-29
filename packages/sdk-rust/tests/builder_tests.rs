//! Request-construction conformance tests.
//!
//! These tests inject a capturing [`Transport`] so no network stack is
//! required. They validate method, path, headers, and body against the shared
//! expectations encoded in the Go SDK and the conformance fixtures.

use std::collections::BTreeMap;
use std::sync::Mutex;

use serde_json::{json, Value};
use ubag::{
    Client, HttpRequest, HttpResponse, ListJobsParams, RequestOptions, Transport, API_VERSION,
    SDK_NAME, SDK_VERSION,
};

#[derive(Default)]
struct CapturingTransport {
    last: Mutex<Option<HttpRequest>>,
    status: u16,
    body: Vec<u8>,
    headers: BTreeMap<String, String>,
}

impl CapturingTransport {
    fn json(status: u16, body: Value) -> Self {
        Self {
            last: Mutex::new(None),
            status,
            body: serde_json::to_vec(&body).unwrap(),
            headers: BTreeMap::new(),
        }
    }

    fn raw(status: u16, body: &str, headers: BTreeMap<String, String>) -> Self {
        Self {
            last: Mutex::new(None),
            status,
            body: body.as_bytes().to_vec(),
            headers,
        }
    }
}

impl Transport for CapturingTransport {
    fn execute(&self, request: HttpRequest) -> Result<HttpResponse, String> {
        *self.last.lock().unwrap() = Some(request);
        Ok(HttpResponse {
            status: self.status,
            headers: self.headers.clone(),
            body: self.body.clone(),
        })
    }
}

fn captured(transport: &std::sync::Arc<CapturingTransport>) -> HttpRequest {
    transport.last.lock().unwrap().clone().expect("request captured")
}

fn client_with(transport: std::sync::Arc<CapturingTransport>) -> Client {
    struct Shared(std::sync::Arc<CapturingTransport>);
    impl Transport for Shared {
        fn execute(&self, request: HttpRequest) -> Result<HttpResponse, String> {
            self.0.execute(request)
        }
    }
    Client::with_transport("http://127.0.0.1:7878", Box::new(Shared(transport)))
        .unwrap()
        .with_app_secret("app_secret_fixture")
}

#[test]
fn health_sends_api_version_header() {
    let transport = std::sync::Arc::new(CapturingTransport::json(200, json!({"status": "ok"})));
    let client = client_with(transport.clone());

    let result = client.health(RequestOptions::new()).unwrap();
    assert_eq!(result["status"], "ok");

    let request = captured(&transport);
    assert_eq!(request.method, "GET");
    assert_eq!(request.url, "http://127.0.0.1:7878/v1/health");
    assert_eq!(request.headers.get("Ubag-Api-Version").unwrap(), API_VERSION);
    assert_eq!(request.headers.get("Ubag-Sdk-Name").unwrap(), SDK_NAME);
    assert_eq!(request.headers.get("Ubag-Sdk-Version").unwrap(), SDK_VERSION);
    assert_eq!(
        request.headers.get("Authorization").unwrap(),
        "Bearer app_secret_fixture"
    );
    assert!(request.body.is_none());
}

#[test]
fn version_omits_idempotency_key() {
    let transport = std::sync::Arc::new(CapturingTransport::json(200, json!({"version": "0.0.0"})));
    let client = client_with(transport.clone());

    client.version(RequestOptions::new().with_idempotency_key("ignored")).unwrap();

    let request = captured(&transport);
    assert_eq!(request.url, "http://127.0.0.1:7878/v1/version");
    assert!(request.headers.get("Idempotency-Key").is_none());
}

#[test]
fn create_job_injects_version_idempotency_and_sdk_metadata() {
    let transport = std::sync::Arc::new(CapturingTransport::json(
        202,
        json!({"job_id": "job_fixture", "status": "queued"}),
    ));
    let client = client_with(transport.clone());

    let mut body = serde_json::Map::new();
    body.insert(
        "client".into(),
        json!({"app_id": "fixture-app", "app_version": "0.0.0"}),
    );
    body.insert(
        "job".into(),
        json!({"target": "mock_target", "command_type": "echo", "input": {"prompt": "Hello UBAG"}}),
    );

    client
        .create_job(body, RequestOptions::new().with_idempotency_key("idem_rust_sdk"))
        .unwrap();

    let request = captured(&transport);
    assert_eq!(request.method, "POST");
    assert_eq!(request.url, "http://127.0.0.1:7878/v1/jobs");
    assert_eq!(
        request.headers.get("Idempotency-Key").unwrap(),
        "idem_rust_sdk"
    );
    assert_eq!(
        request.headers.get("Content-Type").unwrap(),
        "application/json"
    );

    let sent: Value = serde_json::from_slice(request.body.as_ref().unwrap()).unwrap();
    assert_eq!(sent["api_version"], API_VERSION);
    assert_eq!(sent["idempotency_key"], "idem_rust_sdk");
    assert_eq!(sent["client"]["sdk"]["name"], SDK_NAME);
    assert_eq!(sent["client"]["sdk"]["version"], SDK_VERSION);
}

#[test]
fn create_job_generates_idempotency_key_when_missing() {
    let transport = std::sync::Arc::new(CapturingTransport::json(202, json!({"status": "queued"})));
    let client = client_with(transport.clone());

    let mut body = serde_json::Map::new();
    body.insert("job".into(), json!({"target": "mock_target"}));
    client.create_job(body, RequestOptions::new()).unwrap();

    let request = captured(&transport);
    let key = request.headers.get("Idempotency-Key").unwrap();
    assert_eq!(key.len(), 26);
    let sent: Value = serde_json::from_slice(request.body.as_ref().unwrap()).unwrap();
    assert_eq!(sent["idempotency_key"], Value::String(key.clone()));
}

#[test]
fn list_jobs_builds_filter_query() {
    let transport = std::sync::Arc::new(CapturingTransport::json(200, json!({"jobs": []})));
    let client = client_with(transport.clone());

    let params = ListJobsParams {
        cursor: Some("cursor_1".into()),
        limit: Some(1),
        status: Some("completed".into()),
        ..Default::default()
    };
    client.list_jobs(params, RequestOptions::new()).unwrap();

    let request = captured(&transport);
    assert_eq!(
        request.url,
        "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed"
    );
}

#[test]
fn cancel_job_is_idempotent_post() {
    let transport = std::sync::Arc::new(CapturingTransport::json(202, json!({"status": "cancelled"})));
    let client = client_with(transport.clone());

    let mut body = serde_json::Map::new();
    body.insert("reason".into(), json!("caller_cancelled"));
    client
        .cancel_job("job_1", body, RequestOptions::new().with_idempotency_key("idem_cancel"))
        .unwrap();

    let request = captured(&transport);
    assert_eq!(request.method, "POST");
    assert_eq!(request.url, "http://127.0.0.1:7878/v1/jobs/job_1/cancel");
    assert_eq!(request.headers.get("Idempotency-Key").unwrap(), "idem_cancel");
    let sent: Value = serde_json::from_slice(request.body.as_ref().unwrap()).unwrap();
    assert_eq!(sent["idempotency_key"], "idem_cancel");
    assert_eq!(sent["reason"], "caller_cancelled");
}

#[test]
fn put_artifact_sends_bytes_and_generates_key() {
    let transport = std::sync::Arc::new(CapturingTransport::json(201, json!({"idempotent_replay": false})));
    let client = client_with(transport.clone());

    client
        .put_job_artifact("job_1", "report.txt", b"hello artifact".to_vec(), "text/plain", RequestOptions::new())
        .unwrap();

    let request = captured(&transport);
    assert_eq!(request.method, "PUT");
    assert_eq!(
        request.url,
        "http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt"
    );
    assert_eq!(request.headers.get("Content-Type").unwrap(), "text/plain");
    assert_eq!(request.headers.get("Idempotency-Key").unwrap().len(), 26);
    assert_eq!(request.body.as_ref().unwrap(), b"hello artifact");
}

#[test]
fn get_artifact_returns_bytes_and_checksum() {
    let mut headers = BTreeMap::new();
    headers.insert("content-type".into(), "text/plain".into());
    headers.insert("ubag-artifact-checksum".into(), "sha256_fixture".into());
    let transport = std::sync::Arc::new(CapturingTransport::raw(200, "hello artifact", headers));
    let client = client_with(transport.clone());

    let download = client
        .get_job_artifact("job_1", "report.txt", RequestOptions::new())
        .unwrap();
    assert_eq!(download.body, b"hello artifact");
    assert_eq!(download.content_type, "text/plain");
    assert_eq!(download.checksum, "sha256_fixture");
}

#[test]
fn metrics_request_sets_text_accept() {
    let transport = std::sync::Arc::new(CapturingTransport::raw(
        200,
        "ubag_gateway_requests_total 1\n",
        BTreeMap::new(),
    ));
    let client = client_with(transport.clone());

    let text = client.metrics(RequestOptions::new()).unwrap();
    assert_eq!(text, "ubag_gateway_requests_total 1\n");

    let request = captured(&transport);
    assert_eq!(request.url, "http://127.0.0.1:7878/v1/metrics");
    assert_eq!(request.headers.get("Accept").unwrap(), "text/plain");
}

#[test]
fn api_error_envelope_is_parsed() {
    let transport = std::sync::Arc::new(CapturingTransport::json(
        401,
        json!({
            "error": {
                "code": "UBAG-AUTH-MISSING-001",
                "category": "auth",
                "message": "No supported credential was provided",
                "retryable": false,
                "trace_id": "trace_auth_missing"
            }
        }),
    ));
    let client = client_with(transport.clone());

    let error = client.list_workflows(RequestOptions::new()).unwrap_err();
    match error {
        ubag::Error::Api(api) => {
            assert_eq!(api.status, 401);
            assert_eq!(api.code(), Some("UBAG-AUTH-MISSING-001"));
            assert_eq!(api.category(), Some("auth"));
            assert!(!api.retryable());
            assert_eq!(api.trace_id(), Some("trace_auth_missing"));
        }
        other => panic!("expected API error, got {other:?}"),
    }
}
