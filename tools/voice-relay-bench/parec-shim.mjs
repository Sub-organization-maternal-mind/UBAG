#!/usr/bin/env node
// Stand-in for `parec` (perf-fleet P7.7). The relay under test starts this as its provider-sink monitor (UBAG_VOICE_PAREC); the
// harness generates a per-platform wrapper that execs it. It writes raw s16le 1920 B chunks (20 ms) on an absolute schedule,
// records the hand-off time of every chunk (process.hrtime.bigint(), one per line), and keeps the pipe open after the PCM ends
// until the gate file exists, so the relay sees EOF ("monitor_exited") at a point the harness controls. The relay's own parec
// arguments are ignored. Config: JSON file named by UBAG_BENCH_SHIM_CONFIG {pcm, pace_ms, loop, max_chunks, gate, log} (max_chunks > 0 stops emitting after that many chunks).
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { performance } from 'node:perf_hooks';

const CHUNK = 1920;
const cfg = JSON.parse(readFileSync(process.env.UBAG_BENCH_SHIM_CONFIG, 'utf8'));
const pcm = readFileSync(cfg.pcm);
const stamps = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const gateOpen = () => existsSync(cfg.gate);
const flush = () => { try { writeFileSync(cfg.log, stamps.map(String).join('\n') + (stamps.length ? '\n' : '')); } catch { /* best effort */ } };
const out = process.stdout;
out.on('error', () => { flush(); process.exit(0); });
process.on('SIGTERM', () => { flush(); process.exit(0); });

async function write(buf) {
  stamps.push(process.hrtime.bigint());
  if (!out.write(buf)) await new Promise((resolve) => out.once('drain', resolve));
}

const start = performance.now();
let off = 0;
for (let k = 0; !gateOpen() && !(cfg.max_chunks && k >= cfg.max_chunks); k++) {
  if (off >= pcm.length) {
    if (!cfg.loop || !pcm.length) break;
    off = 0;
  }
  const wait = start + k * cfg.pace_ms - performance.now();
  if (wait > 0) await sleep(wait);
  const chunk = pcm.subarray(off, off + CHUNK);
  off += chunk.length;
  await write(chunk);
}
while (!gateOpen()) await sleep(10);
flush();
out.end(() => process.exit(0));
