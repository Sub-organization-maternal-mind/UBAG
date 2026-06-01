//! Offline queue with a pluggable store. The in-memory store is always
//! available; a sled-backed store is provided behind the `offline-sled` feature.

use serde::{Deserialize, Serialize};
use std::sync::Mutex;

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct OfflineEntry {
    pub id: String,
    pub request: serde_json::Value,
    pub attempts: u32,
}

pub trait OfflineStore {
    fn read(&self) -> Vec<OfflineEntry>;
    fn write(&self, entries: Vec<OfflineEntry>);
}

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

pub struct OfflineQueue<S: OfflineStore> {
    store: S,
    counter: Mutex<u64>,
}

impl<S: OfflineStore> OfflineQueue<S> {
    pub fn new(store: S) -> Self {
        OfflineQueue { store, counter: Mutex::new(0) }
    }

    pub fn enqueue(&self, request: serde_json::Value) -> OfflineEntry {
        let mut entries = self.store.read();
        let mut c = self.counter.lock().unwrap();
        *c += 1;
        let entry = OfflineEntry { id: format!("q_{}", *c), request, attempts: 0 };
        entries.push(entry.clone());
        self.store.write(entries);
        entry
    }

    /// Sends entries FIFO; on first sender error persists remaining and returns Err.
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

    pub fn size(&self) -> usize {
        self.store.read().len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trip() {
        let q = OfflineQueue::new(MemoryOfflineStore::default());
        q.enqueue(serde_json::json!({"target": "mock"}));
        q.enqueue(serde_json::json!({"target": "mock"}));
        assert_eq!(q.size(), 2);
        let mut sent = 0;
        q.flush(|_| { sent += 1; Ok(()) }).unwrap();
        assert_eq!(sent, 2);
        assert_eq!(q.size(), 0);
    }

    #[test]
    fn retains_on_error() {
        let q = OfflineQueue::new(MemoryOfflineStore::default());
        q.enqueue(serde_json::json!({"target": "mock"}));
        let r = q.flush(|_| Err("offline".to_string()));
        assert!(r.is_err());
        assert_eq!(q.size(), 1);
    }
}
