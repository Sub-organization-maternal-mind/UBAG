//! Official Rust SDK for the UBAG (Universal Browser-Automation Gateway) v0 REST API.
//!
//! The crate is split into a toolchain-free request-construction core (always
//! compiled) and an optional [`reqwest`]-backed transport behind the
//! `transport` feature. Tests inject their own [`Transport`] so request
//! construction can be validated without a live gateway or a network stack.

mod error;
mod idempotency;
mod transport;

pub mod retry;
pub mod webhooks;
pub mod streaming;
pub mod telemetry;

pub use error::{ApiError, Error, ErrorDetails, ErrorEnvelope};
pub use idempotency::generate_idempotency_key;
pub use transport::{HttpRequest, HttpResponse, Transport};

#[cfg(feature = "transport")]
pub use transport::ReqwestTransport;

use std::collections::BTreeMap;

use serde_json::{Map, Value};

/// The API version pinned by this SDK release, sent as `Ubag-Api-Version`.
pub const API_VERSION: &str = "2026-05-22";
/// SDK name advertised via `Ubag-Sdk-Name` and `client.sdk.name`.
pub const SDK_NAME: &str = "ubag-rust";
/// SDK version advertised via `Ubag-Sdk-Version` and `client.sdk.version`.
pub const SDK_VERSION: &str = "0.0.0";

const JSON_CONTENT_TYPE: &str = "application/json";

/// A JSON object alias used for request and response payloads.
pub type Json = Map<String, Value>;

/// Cursor-based pagination parameters shared by operator collections.
#[derive(Debug, Clone, Default)]
pub struct ListParams {
    pub cursor: Option<String>,
    pub limit: Option<i64>,
}

/// Filtering and pagination parameters for `list_jobs`.
#[derive(Debug, Clone, Default)]
pub struct ListJobsParams {
    pub cursor: Option<String>,
    pub limit: Option<i64>,
    pub status: Option<String>,
    pub target: Option<String>,
    pub sort: Option<String>,
    pub fields: Vec<String>,
    pub include: Vec<String>,
}

/// Pagination parameters for per-job event listing.
#[derive(Debug, Clone, Default)]
pub struct ListJobEventsParams {
    pub cursor: Option<String>,
    pub after_sequence: Option<i64>,
    pub limit: Option<i64>,
}

/// A downloaded artifact: raw bytes plus content metadata.
#[derive(Debug, Clone)]
pub struct ArtifactDownload {
    pub body: Vec<u8>,
    pub content_type: String,
    pub checksum: String,
}

/// Per-request overrides applied on top of the client defaults.
#[derive(Debug, Clone, Default)]
pub struct RequestOptions {
    pub api_version: Option<String>,
    pub idempotency_key: Option<String>,
    pub headers: BTreeMap<String, String>,
}

impl RequestOptions {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn with_idempotency_key(mut self, key: impl Into<String>) -> Self {
        self.idempotency_key = Some(key.into());
        self
    }

    pub fn with_api_version(mut self, version: impl Into<String>) -> Self {
        self.api_version = Some(version.into());
        self
    }

    pub fn with_header(mut self, key: impl Into<String>, value: impl Into<String>) -> Self {
        self.headers.insert(key.into(), value.into());
        self
    }
}

/// The UBAG gateway client.
pub struct Client {
    base_url: String,
    api_version: String,
    app_secret: Option<String>,
    default_headers: BTreeMap<String, String>,
    transport: Box<dyn Transport>,
}

impl Client {
    /// Builds a client with the default [`reqwest`] transport.
    #[cfg(feature = "transport")]
    pub fn new(base_url: impl Into<String>) -> Result<Self, Error> {
        Self::with_transport(base_url, Box::new(ReqwestTransport::new()))
    }

    /// Builds a client with a caller-supplied [`Transport`]. This is the entry
    /// point used by unit tests to capture and assert request construction.
    pub fn with_transport(
        base_url: impl Into<String>,
        transport: Box<dyn Transport>,
    ) -> Result<Self, Error> {
        let mut base_url = base_url.into();
        if base_url.trim().is_empty() {
            return Err(Error::Config("baseURL is required".into()));
        }
        if !base_url.contains("://") {
            return Err(Error::Config("baseURL must include scheme and host".into()));
        }
        while base_url.ends_with('/') {
            base_url.pop();
        }
        Ok(Self {
            base_url,
            api_version: API_VERSION.to_string(),
            app_secret: None,
            default_headers: BTreeMap::new(),
            transport,
        })
    }

    pub fn with_app_secret(mut self, secret: impl Into<String>) -> Self {
        self.app_secret = Some(secret.into());
        self
    }

    pub fn with_api_version(mut self, version: impl Into<String>) -> Self {
        self.api_version = version.into();
        self
    }

    pub fn with_default_header(mut self, key: impl Into<String>, value: impl Into<String>) -> Self {
        self.default_headers.insert(key.into(), value.into());
        self
    }

    // --- System -----------------------------------------------------------

    pub fn health(&self, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", "/v1/health", None, options)
    }

    pub fn ready(&self, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", "/v1/ready", None, options)
    }

    pub fn version(&self, mut options: RequestOptions) -> Result<Value, Error> {
        options.idempotency_key = None;
        self.request("GET", "/v1/version", None, options)
    }

    /// Fetches the Prometheus metrics text exposition.
    pub fn metrics(&self, mut options: RequestOptions) -> Result<String, Error> {
        options
            .headers
            .insert("Accept".into(), "text/plain".into());
        let response = self.send("GET", "/v1/metrics", None, None, options)?;
        String::from_utf8(response.body).map_err(|e| Error::Serde(e.to_string()))
    }

    // --- Jobs --------------------------------------------------------------

    pub fn create_job(&self, request: Json, options: RequestOptions) -> Result<Value, Error> {
        let mut body = request;
        let api_version = string_field(&body, "api_version")
            .or_else(|| options.api_version.clone())
            .unwrap_or_else(|| self.api_version.clone());
        let idempotency_key = string_field(&body, "idempotency_key")
            .or_else(|| options.idempotency_key.clone())
            .unwrap_or_else(generate_idempotency_key);

        body.insert("api_version".into(), Value::String(api_version.clone()));
        body.insert(
            "idempotency_key".into(),
            Value::String(idempotency_key.clone()),
        );
        ensure_sdk_metadata(&mut body);

        let mut options = options;
        options.api_version = Some(api_version);
        options.idempotency_key = Some(idempotency_key);
        self.request("POST", "/v1/jobs", Some(Value::Object(body)), options)
    }

    pub fn get_job(&self, job_id: &str, options: RequestOptions) -> Result<Value, Error> {
        self.request(
            "GET",
            &format!("/v1/jobs/{}", encode_segment(job_id)),
            None,
            options,
        )
    }

    pub fn list_jobs(&self, params: ListJobsParams, options: RequestOptions) -> Result<Value, Error> {
        let path = format!("/v1/jobs{}", build_list_jobs_query(&params));
        self.request("GET", &path, None, options)
    }

    pub fn cancel_job(
        &self,
        job_id: &str,
        request: Json,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        self.mutate_job(job_id, "cancel", request, options)
    }

    pub fn retry_job(
        &self,
        job_id: &str,
        request: Json,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        self.mutate_job(job_id, "retry", request, options)
    }

    // --- Job events --------------------------------------------------------

    pub fn list_job_events(
        &self,
        job_id: &str,
        params: ListJobEventsParams,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        let mut pairs: Vec<(String, String)> = Vec::new();
        if let Some(cursor) = &params.cursor {
            pairs.push(("cursor".into(), cursor.clone()));
        }
        if let Some(after) = params.after_sequence {
            if after > 0 {
                pairs.push(("after_sequence".into(), after.to_string()));
            }
        }
        if let Some(limit) = params.limit {
            if limit > 0 {
                pairs.push(("limit".into(), limit.to_string()));
            }
        }
        let path = format!(
            "/v1/jobs/{}/events{}",
            encode_segment(job_id),
            encode_query(&pairs)
        );
        self.request("GET", &path, None, options)
    }

    /// Reads the per-job Server-Sent Events stream as raw bytes.
    pub fn stream_job_events_sse(
        &self,
        job_id: &str,
        mut options: RequestOptions,
    ) -> Result<String, Error> {
        options
            .headers
            .insert("Accept".into(), "text/event-stream".into());
        let path = format!("/v1/sse/jobs/{}", encode_segment(job_id));
        let response = self.send("GET", &path, None, None, options)?;
        String::from_utf8(response.body).map_err(|e| Error::Serde(e.to_string()))
    }

    // --- Artifacts ---------------------------------------------------------

    pub fn list_job_artifacts(&self, job_id: &str, options: RequestOptions) -> Result<Value, Error> {
        self.request(
            "GET",
            &format!("/v1/jobs/{}/artifacts", encode_segment(job_id)),
            None,
            options,
        )
    }

    pub fn get_job_artifact(
        &self,
        job_id: &str,
        key: &str,
        options: RequestOptions,
    ) -> Result<ArtifactDownload, Error> {
        let path = format!(
            "/v1/jobs/{}/artifacts/{}",
            encode_segment(job_id),
            encode_segment(key)
        );
        let response = self.send("GET", &path, None, None, options)?;
        Ok(ArtifactDownload {
            content_type: header_value(&response.headers, "content-type"),
            checksum: header_value(&response.headers, "ubag-artifact-checksum"),
            body: response.body,
        })
    }

    pub fn put_job_artifact(
        &self,
        job_id: &str,
        key: &str,
        body: Vec<u8>,
        content_type: &str,
        mut options: RequestOptions,
    ) -> Result<Value, Error> {
        if options.idempotency_key.is_none() {
            options.idempotency_key = Some(generate_idempotency_key());
        }
        let content_type = if content_type.is_empty() {
            "application/octet-stream"
        } else {
            content_type
        };
        let path = format!(
            "/v1/jobs/{}/artifacts/{}",
            encode_segment(job_id),
            encode_segment(key)
        );
        let response = self.send("PUT", &path, Some(body), Some(content_type), options)?;
        decode_json(&response.body)
    }

    pub fn delete_job_artifact(
        &self,
        job_id: &str,
        key: &str,
        mut options: RequestOptions,
    ) -> Result<(), Error> {
        if options.idempotency_key.is_none() {
            options.idempotency_key = Some(generate_idempotency_key());
        }
        let path = format!(
            "/v1/jobs/{}/artifacts/{}",
            encode_segment(job_id),
            encode_segment(key)
        );
        self.request("DELETE", &path, None, options)?;
        Ok(())
    }

    // --- Operator collections ---------------------------------------------

    pub fn list_workflows(&self, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", "/v1/workflows", None, options)
    }

    pub fn list_templates(&self, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", "/v1/templates", None, options)
    }

    pub fn list_targets(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/targets{}", build_list_query(&params)), None, options)
    }

    pub fn list_adapters(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/adapters{}", build_list_query(&params)), None, options)
    }

    pub fn list_apps(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/apps{}", build_list_query(&params)), None, options)
    }

    pub fn list_devices(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/devices{}", build_list_query(&params)), None, options)
    }

    pub fn list_webhooks(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/webhooks{}", build_list_query(&params)), None, options)
    }

    pub fn list_audit_events(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/audit{}", build_list_query(&params)), None, options)
    }

    pub fn list_events(&self, params: ListParams, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", &format!("/v1/events{}", build_list_query(&params)), None, options)
    }

    // --- Webhook replay & cache -------------------------------------------

    pub fn replay_webhook_delivery(&self, request: Json, options: RequestOptions) -> Result<Value, Error> {
        self.mutate_generic("/v1/webhooks/replay", request, options)
    }

    pub fn cache_status(&self, options: RequestOptions) -> Result<Value, Error> {
        self.request("GET", "/v1/cache", None, options)
    }

    // --- Internal helpers --------------------------------------------------

    fn mutate_job(
        &self,
        job_id: &str,
        operation: &str,
        request: Json,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        let path = format!("/v1/jobs/{}/{}", encode_segment(job_id), operation);
        self.mutate_generic_path(&path, request, options)
    }

    fn mutate_generic(&self, path: &str, request: Json, options: RequestOptions) -> Result<Value, Error> {
        self.mutate_generic_path(path, request, options)
    }

    fn mutate_generic_path(
        &self,
        path: &str,
        request: Json,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        let mut body = request;
        let api_version = string_field(&body, "api_version")
            .or_else(|| options.api_version.clone())
            .unwrap_or_else(|| self.api_version.clone());
        let idempotency_key = string_field(&body, "idempotency_key")
            .or_else(|| options.idempotency_key.clone())
            .unwrap_or_else(generate_idempotency_key);

        body.insert("api_version".into(), Value::String(api_version.clone()));
        body.insert(
            "idempotency_key".into(),
            Value::String(idempotency_key.clone()),
        );

        let mut options = options;
        options.api_version = Some(api_version);
        options.idempotency_key = Some(idempotency_key);
        self.request("POST", path, Some(Value::Object(body)), options)
    }

    fn request(
        &self,
        method: &str,
        path: &str,
        body: Option<Value>,
        options: RequestOptions,
    ) -> Result<Value, Error> {
        let serialized = match &body {
            Some(value) => Some(
                serde_json::to_vec(value).map_err(|e| Error::Serde(e.to_string()))?,
            ),
            None => None,
        };
        let content_type = serialized.as_ref().map(|_| JSON_CONTENT_TYPE);
        let response = self.send(method, path, serialized, content_type, options)?;
        if response.body.is_empty() || response.status == 204 {
            return Ok(Value::Null);
        }
        decode_json(&response.body)
    }

    fn send(
        &self,
        method: &str,
        path: &str,
        body: Option<Vec<u8>>,
        content_type: Option<&str>,
        options: RequestOptions,
    ) -> Result<HttpResponse, Error> {
        let url = format!("{}{}", self.base_url, path);
        let api_version = options
            .api_version
            .clone()
            .unwrap_or_else(|| self.api_version.clone());

        let mut headers: BTreeMap<String, String> = BTreeMap::new();
        headers.insert("Accept".into(), JSON_CONTENT_TYPE.into());
        headers.insert("Ubag-Api-Version".into(), api_version);
        headers.insert("Ubag-Sdk-Name".into(), SDK_NAME.into());
        headers.insert("Ubag-Sdk-Version".into(), SDK_VERSION.into());
        for (key, value) in &self.default_headers {
            headers.insert(key.clone(), value.clone());
        }
        for (key, value) in &options.headers {
            headers.insert(key.clone(), value.clone());
        }
        if let Some(secret) = &self.app_secret {
            headers
                .entry("Authorization".into())
                .or_insert_with(|| format!("Bearer {secret}"));
        }
        if let Some(key) = &options.idempotency_key {
            headers.insert("Idempotency-Key".into(), key.clone());
        }
        if body.is_some() {
            headers.insert(
                "Content-Type".into(),
                content_type.unwrap_or(JSON_CONTENT_TYPE).into(),
            );
        }

        let http_request = HttpRequest {
            method: method.to_string(),
            url: url.clone(),
            headers,
            body,
        };

        let response = self
            .transport
            .execute(http_request)
            .map_err(|cause| Error::Transport {
                method: method.to_string(),
                url: url.clone(),
                cause,
            })?;

        if response.status < 200 || response.status >= 300 {
            return Err(Error::Api(error::build_api_error(
                response.status,
                method.to_string(),
                url,
                response.headers,
                response.body,
            )));
        }
        Ok(response)
    }
}

fn ensure_sdk_metadata(body: &mut Json) {
    let client = body
        .entry("client".to_string())
        .or_insert_with(|| Value::Object(Map::new()));
    if let Value::Object(map) = client {
        if !map.contains_key("sdk") {
            let mut sdk = Map::new();
            sdk.insert("name".into(), Value::String(SDK_NAME.into()));
            sdk.insert("version".into(), Value::String(SDK_VERSION.into()));
            map.insert("sdk".into(), Value::Object(sdk));
        }
    }
}

fn string_field(body: &Json, key: &str) -> Option<String> {
    match body.get(key) {
        Some(Value::String(value)) if !value.is_empty() => Some(value.clone()),
        _ => None,
    }
}

fn decode_json(body: &[u8]) -> Result<Value, Error> {
    serde_json::from_slice(body).map_err(|e| Error::Serde(e.to_string()))
}

fn header_value(headers: &BTreeMap<String, String>, key: &str) -> String {
    headers
        .iter()
        .find(|(name, _)| name.eq_ignore_ascii_case(key))
        .map(|(_, value)| value.clone())
        .unwrap_or_default()
}

fn build_list_query(params: &ListParams) -> String {
    let mut pairs: Vec<(String, String)> = Vec::new();
    if let Some(cursor) = &params.cursor {
        pairs.push(("cursor".into(), cursor.clone()));
    }
    if let Some(limit) = params.limit {
        if limit > 0 {
            pairs.push(("limit".into(), limit.to_string()));
        }
    }
    encode_query(&pairs)
}

fn build_list_jobs_query(params: &ListJobsParams) -> String {
    let mut pairs: Vec<(String, String)> = Vec::new();
    if let Some(cursor) = &params.cursor {
        pairs.push(("cursor".into(), cursor.clone()));
    }
    if let Some(limit) = params.limit {
        if limit > 0 {
            pairs.push(("limit".into(), limit.to_string()));
        }
    }
    if let Some(status) = &params.status {
        pairs.push(("filter[status]".into(), status.clone()));
    }
    if let Some(target) = &params.target {
        pairs.push(("filter[target]".into(), target.clone()));
    }
    if let Some(sort) = &params.sort {
        pairs.push(("sort".into(), sort.clone()));
    }
    if !params.fields.is_empty() {
        pairs.push(("fields".into(), params.fields.join(",")));
    }
    if !params.include.is_empty() {
        pairs.push(("include".into(), params.include.join(",")));
    }
    encode_query(&pairs)
}

fn encode_query(pairs: &[(String, String)]) -> String {
    if pairs.is_empty() {
        return String::new();
    }
    let encoded: Vec<String> = pairs
        .iter()
        .map(|(key, value)| format!("{}={}", percent_encode(key), percent_encode(value)))
        .collect();
    format!("?{}", encoded.join("&"))
}

/// Percent-encodes a path segment, leaving only unreserved characters.
fn encode_segment(segment: &str) -> String {
    percent_encode(segment)
}

fn percent_encode(input: &str) -> String {
    let mut output = String::with_capacity(input.len());
    for byte in input.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                output.push(byte as char);
            }
            _ => output.push_str(&format!("%{byte:02X}")),
        }
    }
    output
}
