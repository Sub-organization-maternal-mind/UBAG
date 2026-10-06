// Reader for the relay's mic "FIFO" (perf-fleet P7.7): the black-box FIFO sink. POSIX: a real FIFO opened non-blocking (the relay's
// own non-blocking open needs a reader, so the sink starts first). Windows (self-test only): a regular file that the relay appends
// to, tailed by position. Every read is stamped with process.hrtime.bigint() so mic transit time can be derived per byte offset.
import { closeSync, constants, openSync, readSync } from 'node:fs';

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

export class Sink {
  constructor(path, { fifo, keep = true } = {}) {
    this.path = path;
    this.fifo = fifo;
    this.keep = keep;
    this.chunks = [];
    this.stamps = []; // {t: bigint ns, end: cumulative bytes}
    this.total = 0;
    this.base = 0; // total at the last take(): timeOfByte offsets are relative to it
    this.lastRead = process.hrtime.bigint();
    this.running = false;
  }

  start() {
    this.fd = this.fifo ? openSync(this.path, constants.O_RDONLY | constants.O_NONBLOCK) : openSync(this.path, 'r');
    this.running = true;
    this.loop = this.#poll();
    return this;
  }

  async #poll() {
    const buf = Buffer.allocUnsafe(65536);
    while (this.running) {
      let n = 0;
      try {
        n = readSync(this.fd, buf, 0, buf.length, this.fifo ? null : this.total);
      } catch (error) {
        if (error.code !== 'EAGAIN') throw error;
      }
      if (n > 0) {
        this.total += n;
        this.lastRead = process.hrtime.bigint();
        this.stamps.push({ t: this.lastRead, end: this.total });
        if (this.keep) this.chunks.push(Buffer.from(buf.subarray(0, n)));
        continue;
      }
      await sleep(1);
    }
  }

  /** Bytes read since the last take(); also resets the stamps. */
  take() {
    const data = Buffer.concat(this.chunks);
    this.chunks = [];
    this.stamps = [];
    this.base = this.total;
    return data;
  }

  /** Time the bytes up to `end` (exclusive, counted from the last take()) had been read, or null if they never arrived. */
  timeOfByte(end) {
    const s = this.stamps.find((x) => x.end >= this.base + end);
    return s ? s.t : null;
  }

  /** Resolves once no byte has arrived for `quietMs` (or after `maxMs`). */
  async quiet(quietMs, maxMs = 15000) {
    const deadline = Date.now() + maxMs;
    while (Date.now() < deadline) {
      if (Number(process.hrtime.bigint() - this.lastRead) / 1e6 >= quietMs) return true;
      await sleep(Math.max(1, Math.min(quietMs / 4, 25)));
    }
    return false;
  }

  async stop() {
    this.running = false;
    await this.loop;
    try { closeSync(this.fd); } catch { /* already closed */ }
  }
}
