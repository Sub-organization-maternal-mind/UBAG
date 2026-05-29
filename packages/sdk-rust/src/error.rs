//! Error types for the UBAG SDK.

use std::collections::BTreeMap;
use std::fmt;

/// The stable error envelope returned by the gateway: `{ "error": { ... } }`.
#[derive(Debug, Clone, serde::Deserialize)]
pub struct ErrorEnvelope {
    pub error: ErrorDetails,
}

/// Details inside the stable error envelope.
#[derive(Debug, Clone, Default, serde::Deserialize)]
pub struct ErrorDetails {
    #[serde(default)]
    pub code: String,
    #[serde(default)]
    pub category: String,
    #[serde(default)]
    pub message: String,
    #[serde(default)]
    pub retryable: Option<bool>,
    #[serde(default)]
    pub retry_after_ms: Option<i64>,
    #[serde(default)]
    pub trace_id: String,
    #[serde(default)]
    pub doc_url: Option<String>,
    #[serde(default)]
    pub details: Option<serde_json::Value>,
}

impl ErrorEnvelope {
    /// Returns true when the payload looks like a genuine UBAG error envelope.
    pub fn is_valid(&self) -> bool {
        self.error.code.starts_with("UBAG-")
            && !self.error.category.is_empty()
            && !self.error.message.is_empty()
            && self.error.retryable.is_some()
            && !self.error.trace_id.is_empty()
    }
}

/// All error conditions surfaced by the SDK.
#[derive(Debug)]
pub enum Error {
    /// Invalid client configuration (e.g. malformed base URL).
    Config(String),
    /// The request could not be sent (network/transport failure).
    Transport { method: String, url: String, cause: String },
    /// The gateway returned a non-2xx response.
    Api(ApiError),
    /// Serialization / deserialization failure.
    Serde(String),
}

/// A non-2xx HTTP response from the gateway.
#[derive(Debug, Clone)]
pub struct ApiError {
    pub status: u16,
    pub method: String,
    pub url: String,
    pub headers: BTreeMap<String, String>,
    pub raw_body: Vec<u8>,
    pub envelope: Option<ErrorEnvelope>,
}

impl ApiError {
    pub fn code(&self) -> Option<&str> {
        self.envelope.as_ref().map(|e| e.error.code.as_str())
    }

    pub fn category(&self) -> Option<&str> {
        self.envelope.as_ref().map(|e| e.error.category.as_str())
    }

    pub fn retryable(&self) -> bool {
        self.envelope
            .as_ref()
            .and_then(|e| e.error.retryable)
            .unwrap_or(false)
    }

    /// Resolves the retry-after delay in milliseconds, preferring the envelope
    /// over the `Retry-After` header.
    pub fn retry_after_ms(&self) -> Option<i64> {
        if let Some(envelope) = &self.envelope {
            if let Some(ms) = envelope.error.retry_after_ms {
                return Some(ms);
            }
        }
        let header = self.headers.get("retry-after")?;
        header
            .trim()
            .parse::<f64>()
            .ok()
            .map(|seconds| (seconds.max(0.0) * 1000.0) as i64)
    }

    pub fn trace_id(&self) -> Option<&str> {
        if let Some(envelope) = &self.envelope {
            if !envelope.error.trace_id.is_empty() {
                return Some(envelope.error.trace_id.as_str());
            }
        }
        self.headers
            .get("ubag-trace-id")
            .or_else(|| self.headers.get("x-request-id"))
            .map(|s| s.as_str())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Config(msg) => write!(f, "ubag config error: {msg}"),
            Error::Transport { method, url, cause } => {
                write!(f, "ubag transport error: {method} {url}: {cause}")
            }
            Error::Api(api) => match &api.envelope {
                Some(env) if !env.error.message.is_empty() => write!(f, "{}", env.error.message),
                _ => write!(f, "UBAG API request failed with HTTP {}", api.status),
            },
            Error::Serde(msg) => write!(f, "ubag serialization error: {msg}"),
        }
    }
}

impl std::error::Error for Error {}

/// Parses a raw error body into an [`ApiError`], extracting the envelope when present.
pub(crate) fn build_api_error(
    status: u16,
    method: String,
    url: String,
    headers: BTreeMap<String, String>,
    raw_body: Vec<u8>,
) -> ApiError {
    let envelope = serde_json::from_slice::<ErrorEnvelope>(&raw_body)
        .ok()
        .filter(ErrorEnvelope::is_valid);
    ApiError {
        status,
        method,
        url,
        headers,
        raw_body,
        envelope,
    }
}
