//! UBAG mobile monitoring app — native command layer.
//!
//! Responsibilities kept on the Rust side:
//!   * Secure storage of the gateway app-secret in the OS credential store
//!     (never written to disk in plaintext, never logged).
//!   * A push-notification registration stub with a clear integration TODO.
//!
//! All gateway REST traffic is performed from the webview through the
//! `tauri-plugin-http` plugin (see `capabilities/default.json`), which issues
//! requests from Rust and therefore bypasses browser CORS for the
//! user-configured gateway URL.

use keyring::Entry;

/// Logical service namespace for credential-store entries.
const KEYRING_SERVICE: &str = "dev.ubag.mobile";

fn entry(key: &str) -> Result<Entry, String> {
    Entry::new(KEYRING_SERVICE, key).map_err(|e| format!("keyring init failed: {e}"))
}

/// Store a secret value in the OS credential store.
///
/// The value is treated as sensitive: it is never logged and never returned in
/// error messages.
#[tauri::command]
fn secure_store_set(key: String, value: String) -> Result<(), String> {
    entry(&key)?
        .set_password(&value)
        .map_err(|e| format!("failed to store secret: {e}"))
}

/// Read a secret value from the OS credential store. Returns `None` when no
/// entry exists for the key.
#[tauri::command]
fn secure_store_get(key: String) -> Result<Option<String>, String> {
    match entry(&key)?.get_password() {
        Ok(value) => Ok(Some(value)),
        Err(keyring::Error::NoEntry) => Ok(None),
        Err(e) => Err(format!("failed to read secret: {e}")),
    }
}

/// Delete a secret value from the OS credential store. Deleting a missing entry
/// is treated as success (idempotent).
#[tauri::command]
fn secure_store_delete(key: String) -> Result<(), String> {
    match entry(&key)?.delete_credential() {
        Ok(()) => Ok(()),
        Err(keyring::Error::NoEntry) => Ok(()),
        Err(e) => Err(format!("failed to delete secret: {e}")),
    }
}

/// Push-notification registration stub.
///
/// TODO(push): Wire this to a real push transport before shipping alerts:
///   * Android: integrate Firebase Cloud Messaging (FCM) via a Tauri mobile
///     plugin or a Kotlin plugin shim, obtain the device token here.
///   * iOS: integrate APNs via `UNUserNotificationCenter` in a Swift plugin
///     shim, obtain the device token here.
///   * Register the returned token with the gateway webhook/device surface so
///     the operator receives alert pushes for failed/blocked jobs.
///
/// For now this returns a sentinel so the UI can show that push is not yet
/// enabled without crashing.
#[tauri::command]
fn register_push_token() -> Result<Option<String>, String> {
    log::info!("push registration requested (stub — not yet implemented)");
    Ok(None)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_http::init())
        .invoke_handler(tauri::generate_handler![
            secure_store_set,
            secure_store_get,
            secure_store_delete,
            register_push_token,
        ])
        .run(tauri::generate_context!())
        .expect("error while running UBAG mobile application");
}
