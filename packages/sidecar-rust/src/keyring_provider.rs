//! OS keychain-backed [`SecretProvider`].
//!
//! When compiled with the `keychain` feature, [`KeyringSecretProvider`] queries
//! the OS credential store (Windows Credential Manager, macOS Keychain,
//! libsecret on Linux) via the `keyring` crate.  If no entry is found, or the
//! feature is disabled, it falls back to an in-memory secret supplied at
//! construction time.

#[cfg(feature = "keychain")]
use keyring::Entry;

use crate::SecretProvider;

/// [`SecretProvider`] backed by the OS keychain.
///
/// On platforms where the `keychain` feature is active, the provider first
/// attempts to read the secret from the OS credential store identified by
/// `service` + `username`.  If that lookup fails (entry absent, access
/// denied, …) the provider silently falls back to the `fallback` value that
/// was supplied at construction time.
///
/// When the `keychain` feature is **not** active, the provider always returns
/// the fallback directly (zero-overhead: no keychain crate compiled in).
pub struct KeyringSecretProvider {
    service: String,
    username: String,
    /// Fallback secret from env / CLI — returned when the keychain lookup
    /// fails or is unavailable.
    fallback: Option<String>,
}

impl KeyringSecretProvider {
    /// Creates a new provider.
    ///
    /// `service` and `username` identify the keychain entry.
    /// `fallback` is returned when the keychain is unavailable or has no entry.
    pub fn new(service: &str, username: &str, fallback: Option<String>) -> Self {
        Self {
            service: service.to_string(),
            username: username.to_string(),
            fallback,
        }
    }
}

impl SecretProvider for KeyringSecretProvider {
    fn app_secret(&self) -> Option<String> {
        #[cfg(feature = "keychain")]
        {
            if let Ok(entry) = Entry::new(&self.service, &self.username) {
                match entry.get_password() {
                    Ok(secret) if !secret.is_empty() => return Some(secret),
                    _ => {} // fall through to env fallback
                }
            }
        }
        self.fallback.clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Without the keychain feature active the provider must return the
    /// supplied fallback secret unchanged.
    #[test]
    fn returns_fallback_when_no_keychain() {
        let p = KeyringSecretProvider::new("ubag", "test", Some("env-secret".to_string()));
        assert_eq!(p.app_secret(), Some("env-secret".to_string()));
    }

    /// When no fallback is supplied and the keychain feature is off, the
    /// provider must return `None` (rather than panicking).
    #[test]
    fn returns_none_when_no_fallback() {
        let p = KeyringSecretProvider::new("ubag", "test", None);
        // Without the keychain feature the fallback path is taken and returns None.
        #[cfg(not(feature = "keychain"))]
        assert_eq!(p.app_secret(), None);
        // With the keychain feature we can't guarantee no entry exists in CI,
        // so we just confirm the call doesn't panic.
        #[cfg(feature = "keychain")]
        {
            let _ = p.app_secret();
        }
    }

    /// A provider whose fallback is Some("x") should continue to return it
    /// after being called multiple times (no mutation of the fallback).
    #[test]
    fn fallback_is_idempotent() {
        let p = KeyringSecretProvider::new("ubag", "test2", Some("stable".to_string()));
        assert_eq!(p.app_secret(), p.app_secret());
    }
}
