//! Disk-backed offline queue for the UBAG sidecar.
//!
//! Mirrors the `OfflineQueue` API from `sdk-rust` but adds a [`SledOfflineStore`]
//! (compiled in only with the `offline` feature) that persists entries to a
//! `sled` embedded database so they survive process restarts.
//!
//! A [`MemoryOfflineStore`] is always available (no feature flag) and is used in
//! tests as well as when a persistent store is not needed.

use serde::{Deserialize, Serialize};
use std::sync::Mutex;

/// A single buffered request that has not yet been delivered to the gateway.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct OfflineEntry {
    /// Unique identifier for this entry (monotonically increasing `q_{n}`).
    pub id: String,
    /// The serialized request body to forward when connectivity is restored.
    pub request: serde_json::Value,
    /// Number of delivery attempts made so far.
    pub attempts: u32,
}

/// Persistence back-end for [`OfflineQueue`].
pub trait OfflineStore: Send + Sync {
    /// Returns all buffered entries in insertion order.
    fn read(&self) -> Vec<OfflineEntry>;
    /// Atomically replaces the stored entries with `entries`.
    fn write(&self, entries: Vec<OfflineEntry>);
}

/// Convenience type alias for a dynamically-dispatched queue (erases the store
/// type), which is what [`SidecarState`](crate::SidecarState) stores.
pub type BoxedOfflineQueue = OfflineQueue<Box<dyn OfflineStore>>;

// Blanket impl so `Box<dyn OfflineStore>` itself satisfies the `OfflineStore`
// bound, enabling `OfflineQueue<Box<dyn OfflineStore>>`.
impl OfflineStore for Box<dyn OfflineStore> {
    fn read(&self) -> Vec<OfflineEntry> {
        (**self).read()
    }
    fn write(&self, entries: Vec<OfflineEntry>) {
        (**self).write(entries);
    }
}

// ── In-memory store ──────────────────────────────────────────────────────────

/// Volatile in-memory [`OfflineStore`].  Used in tests and when persistence is
/// not required.
#[derive(Default)]
pub struct MemoryOfflineStore {
    entries: Mutex<Vec<OfflineEntry>>,
}

impl OfflineStore for MemoryOfflineStore {
    fn read(&self) -> Vec<OfflineEntry> {
        self.entries.lock().unwrap().clone()
    }

    fn write(&self, entries: Vec<OfflineEntry>) {
        *self.entries.lock().unwrap() = entries;
    }
}

// ── Sled-backed store (offline feature only) ──────────────────────────────────

/// Persistent [`OfflineStore`] backed by a `sled` embedded database.
///
/// Entries are stored as JSON blobs keyed by their [`OfflineEntry::id`].
/// Only compiled when the `offline` feature is enabled.
#[cfg(feature = "offline")]
pub struct SledOfflineStore {
    tree: sled::Tree,
}

#[cfg(feature = "offline")]
impl SledOfflineStore {
    /// Opens (or creates) a sled database at `dir` and returns a store backed
    /// by the `"offline_queue"` tree within it.
    pub fn open(dir: &str) -> Result<Self, sled::Error> {
        let db = sled::open(dir)?;
        let tree = db.open_tree("offline_queue")?;
        Ok(Self { tree })
    }
}

#[cfg(feature = "offline")]
impl OfflineStore for SledOfflineStore {
    fn read(&self) -> Vec<OfflineEntry> {
        self.tree
            .iter()
            .filter_map(|res| res.ok())
            .filter_map(|(_, v)| serde_json::from_slice(&v).ok())
            .collect()
    }

    fn write(&self, entries: Vec<OfflineEntry>) {
        // Clear existing entries then insert the new set.
        let _ = self.tree.clear();
        for entry in &entries {
            if let Ok(bytes) = serde_json::to_vec(entry) {
                let _ = self.tree.insert(entry.id.as_bytes(), bytes);
            }
        }
    }
}

// ── OfflineQueue ──────────────────────────────────────────────────────────────

/// FIFO queue that buffers requests when the upstream gateway is unreachable.
///
/// The queue is generic over any [`OfflineStore`] so the same logic works with
/// both the in-memory and sled-backed stores.
pub struct OfflineQueue<S: OfflineStore> {
    store: S,
    counter: Mutex<u64>,
}

impl<S: OfflineStore> OfflineQueue<S> {
    /// Creates a new queue backed by `store`.
    pub fn new(store: S) -> Self {
        OfflineQueue {
            store,
            counter: Mutex::new(0),
        }
    }

    /// Appends `request` to the queue and returns the new entry.
    pub fn enqueue(&self, request: serde_json::Value) -> OfflineEntry {
        let mut entries = self.store.read();
        let mut c = self.counter.lock().unwrap();
        *c += 1;
        // Zero-pad to 20 digits so lexicographic order == insertion order in sled.
        let entry = OfflineEntry {
            id: format!("q_{:020}", *c),
            request,
            attempts: 0,
        };
        entries.push(entry.clone());
        self.store.write(entries);
        entry
    }

    /// Attempts to deliver all buffered entries in FIFO order using `sender`.
    ///
    /// Delivery stops at the first failure: the failed entry has its attempt
    /// counter incremented and is retained at the head of the queue.
    pub fn flush<F>(&self, mut sender: F) -> Result<(), String>
    where
        F: FnMut(&serde_json::Value) -> Result<(), String>,
    {
        let mut entries = self.store.read();
        while !entries.is_empty() {
            match sender(&entries[0].request) {
                Ok(()) => {
                    entries.remove(0);
                    self.store.write(entries.clone());
                }
                Err(e) => {
                    entries[0].attempts += 1;
                    self.store.write(entries);
                    return Err(e);
                }
            }
        }
        Ok(())
    }

    /// Returns the number of entries currently in the queue.
    pub fn size(&self) -> usize {
        self.store.read().len()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_queue() -> OfflineQueue<MemoryOfflineStore> {
        OfflineQueue::new(MemoryOfflineStore::default())
    }

    /// Basic round-trip: enqueue two requests, flush both successfully.
    #[test]
    fn round_trip() {
        let q = make_queue();
        q.enqueue(serde_json::json!({"op": "a"}));
        q.enqueue(serde_json::json!({"op": "b"}));
        assert_eq!(q.size(), 2);

        let mut flushed = Vec::new();
        q.flush(|req| {
            flushed.push(req.clone());
            Ok(())
        })
        .unwrap();

        assert_eq!(q.size(), 0);
        assert_eq!(flushed.len(), 2);
        assert_eq!(flushed[0]["op"], "a");
        assert_eq!(flushed[1]["op"], "b");
    }

    /// When the sender returns an error the entry is retained and its attempt
    /// counter is incremented.
    #[test]
    fn retains_on_error() {
        let q = make_queue();
        q.enqueue(serde_json::json!({"op": "a"}));
        q.enqueue(serde_json::json!({"op": "b"}));

        let result = q.flush(|_req| Err("network down".to_string()));
        assert!(result.is_err());
        // Both entries retained (stopped at first failure).
        assert_eq!(q.size(), 2);

        // Confirm attempt counter was incremented on the head entry.
        let entries = q.store.read();
        assert_eq!(entries[0].attempts, 1);
        assert_eq!(entries[1].attempts, 0);
    }

    /// Partial flush: first entry succeeds, second fails.
    #[test]
    fn partial_flush() {
        let q = make_queue();
        q.enqueue(serde_json::json!({"op": "1"}));
        q.enqueue(serde_json::json!({"op": "2"}));

        let mut call = 0u32;
        let _ = q.flush(|_req| {
            call += 1;
            if call == 1 {
                Ok(())
            } else {
                Err("second failed".to_string())
            }
        });

        // Only one entry remains.
        assert_eq!(q.size(), 1);
        let entries = q.store.read();
        assert_eq!(entries[0].request["op"], "2");
    }

    /// Flushing an empty queue is a no-op that returns Ok.
    #[test]
    fn flush_empty_is_ok() {
        let q = make_queue();
        assert!(q.flush(|_| Ok(())).is_ok());
    }

    // ── Sled store tests (offline feature only) ───────────────────────────────

    #[cfg(feature = "offline")]
    #[test]
    fn sled_round_trip() {
        let dir = std::env::temp_dir().join(format!(
            "ubag_sled_test_{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let store = SledOfflineStore::open(dir.to_str().unwrap()).expect("open sled");
        let q = OfflineQueue::new(store);

        q.enqueue(serde_json::json!({"x": 1}));
        q.enqueue(serde_json::json!({"x": 2}));
        assert_eq!(q.size(), 2);

        let mut received = Vec::new();
        q.flush(|req| {
            received.push(req.clone());
            Ok(())
        })
        .unwrap();

        assert_eq!(q.size(), 0);
        assert_eq!(received.len(), 2);

        // Cleanup.
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[cfg(feature = "offline")]
    #[test]
    fn sled_retains_on_error() {
        let dir = std::env::temp_dir().join(format!(
            "ubag_sled_err_{}",
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let store = SledOfflineStore::open(dir.to_str().unwrap()).expect("open sled");
        let q = OfflineQueue::new(store);

        q.enqueue(serde_json::json!({"op": "a"}));
        let result = q.flush(|_| Err("offline".to_string()));
        assert!(result.is_err());
        assert_eq!(q.size(), 1);

        let _ = std::fs::remove_dir_all(&dir);
    }
}
