//! Pluggable HTTP transport abstraction for the UBAG SDK.

use std::collections::BTreeMap;

/// A fully constructed HTTP request ready to be dispatched by a [`Transport`].
#[derive(Debug, Clone)]
pub struct HttpRequest {
    pub method: String,
    pub url: String,
    pub headers: BTreeMap<String, String>,
    pub body: Option<Vec<u8>>,
}

/// The raw HTTP response returned by a [`Transport`].
#[derive(Debug, Clone)]
pub struct HttpResponse {
    pub status: u16,
    pub headers: BTreeMap<String, String>,
    pub body: Vec<u8>,
}

/// Sends fully constructed requests. Implementations must be `Send + Sync` so a
/// [`crate::Client`] can be shared across threads. Tests provide a capturing
/// implementation to assert request construction without a network stack.
pub trait Transport: Send + Sync {
    fn execute(&self, request: HttpRequest) -> Result<HttpResponse, String>;
}

#[cfg(feature = "transport")]
mod reqwest_transport {
    use super::{HttpRequest, HttpResponse, Transport};
    use std::collections::BTreeMap;

    /// The default [`reqwest`]-backed blocking transport.
    pub struct ReqwestTransport {
        client: reqwest::blocking::Client,
    }

    impl ReqwestTransport {
        pub fn new() -> Self {
            Self {
                client: reqwest::blocking::Client::new(),
            }
        }

        pub fn with_client(client: reqwest::blocking::Client) -> Self {
            Self { client }
        }
    }

    impl Default for ReqwestTransport {
        fn default() -> Self {
            Self::new()
        }
    }

    impl Transport for ReqwestTransport {
        fn execute(&self, request: HttpRequest) -> Result<HttpResponse, String> {
            let method = reqwest::Method::from_bytes(request.method.as_bytes())
                .map_err(|e| e.to_string())?;
            let mut builder = self.client.request(method, &request.url);
            for (key, value) in &request.headers {
                builder = builder.header(key, value);
            }
            if let Some(body) = request.body {
                builder = builder.body(body);
            }
            let response = builder.send().map_err(|e| e.to_string())?;
            let status = response.status().as_u16();
            let mut headers = BTreeMap::new();
            for (name, value) in response.headers().iter() {
                if let Ok(value) = value.to_str() {
                    headers.insert(name.as_str().to_lowercase(), value.to_string());
                }
            }
            let body = response.bytes().map_err(|e| e.to_string())?.to_vec();
            Ok(HttpResponse {
                status,
                headers,
                body,
            })
        }
    }
}

#[cfg(feature = "transport")]
pub use reqwest_transport::ReqwestTransport;
