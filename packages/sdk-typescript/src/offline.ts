import type { UbagJsonObject } from "./types.js";

export interface JobQueueEntry {
  id: string;
  request: UbagJsonObject;
  enqueuedAt: string;
  attempts: number;
}

export interface StorageAdapter {
  read(): JobQueueEntry[];
  write(entries: JobQueueEntry[]): void;
}

export type QueueSender = (request: UbagJsonObject) => Promise<void>;

let counter = 0;

export class OfflineQueue {
  constructor(private readonly storage: StorageAdapter) {}

  enqueue(request: UbagJsonObject): JobQueueEntry {
    const entries = this.storage.read();
    const entry: JobQueueEntry = {
      id: `q_${Date.now()}_${counter++}`,
      request,
      enqueuedAt: new Date().toISOString(),
      attempts: 0,
    };
    entries.push(entry);
    this.storage.write(entries);
    return entry;
  }

  // flush sends entries in FIFO order. On the first sender error it persists
  // the remaining entries (including the one that failed) and rethrows.
  async flush(sender: QueueSender): Promise<void> {
    let entries = this.storage.read();
    while (entries.length > 0) {
      const entry = entries[0] as JobQueueEntry;
      try {
        await sender(entry.request);
      } catch (err) {
        entry.attempts += 1;
        this.storage.write(entries);
        throw err;
      }
      entries = entries.slice(1);
      this.storage.write(entries);
    }
  }

  size(): number {
    return this.storage.read().length;
  }
}
