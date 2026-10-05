// Single import point for the examples. In your own app install the package
// and import from "@ubag/sdk" instead. From this repo build it first:
//   pnpm --filter @ubag/sdk build
export * from "../../packages/sdk-typescript/dist/index.js";

import { UbagClient } from "../../packages/sdk-typescript/dist/index.js";
import { readFileSync } from "node:fs";

/** Server-side client. UBAG_TOKEN is an app secret / PAT: never ship it to a browser. */
export function clientFromEnv() {
  const appSecret = process.env.UBAG_TOKEN;
  if (!appSecret) {
    console.error("Set UBAG_TOKEN (and optionally UBAG_BASE_URL, default http://127.0.0.1:8080).");
    process.exit(2);
  }
  return new UbagClient({
    baseUrl: process.env.UBAG_BASE_URL ?? "http://127.0.0.1:8080",
    appSecret,
    sidecarDiscovery: false,
  });
}

/** Reads a file argument, or falls back to a generated placeholder so examples run with no assets. */
export function bytesOrFallback(path, fallback) {
  return path ? new Uint8Array(readFileSync(path)) : fallback();
}

// 1x1 transparent PNG.
export const tinyPng = () =>
  Uint8Array.from(
    Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64"),
  );

// Half a second of 16 kHz mono 16-bit silence as a WAV file.
export function silentWav(seconds = 0.5, rate = 16000) {
  const samples = Math.floor(seconds * rate);
  const buffer = Buffer.alloc(44 + samples * 2);
  buffer.write("RIFF", 0);
  buffer.writeUInt32LE(36 + samples * 2, 4);
  buffer.write("WAVEfmt ", 8);
  buffer.writeUInt32LE(16, 16);
  buffer.writeUInt16LE(1, 20);
  buffer.writeUInt16LE(1, 22);
  buffer.writeUInt32LE(rate, 24);
  buffer.writeUInt32LE(rate * 2, 28);
  buffer.writeUInt16LE(2, 32);
  buffer.writeUInt16LE(16, 34);
  buffer.write("data", 36);
  buffer.writeUInt32LE(samples * 2, 40);
  return new Uint8Array(buffer);
}
