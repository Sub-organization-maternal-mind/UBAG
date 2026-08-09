#!/usr/bin/env node

import { mkdir, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';

import { buildConfig, formatHuman, runBenchmark } from './benchmark.mjs';

const HELP = `Usage: node tools/benchmark/run.mjs [options]

Deterministic UBAG mock-target benchmark. A local gateway must be running.

Options:
  --scenario <name>          acceptance (default) or mock-e2e
  --base-url <url>           Gateway base URL (default: http://127.0.0.1:8080)
  --allow-remote             Explicitly permit a non-loopback base URL
  --app-secret <secret>      Compatibility override (default: UBAG_APP_SECRET)
  --warmups <count>          Warmup runs, 0-1000 (default: 1)
  --samples <count>          Measured runs, 1-10000 (default: 10)
  --poll-interval-ms <ms>    E2E polling interval, 1-60000 (default: 50)
  --timeout-ms <ms>          Per-sample timeout, 1-3600000 (default: 30000)
  --json                     Print the sanitized result as JSON
  --output <path>            Write the sanitized result JSON to a file
  --help, -h                 Show this help

Prefer UBAG_APP_SECRET over --app-secret because CLI arguments may be visible
in process listings.

Remote targets are rejected unless --allow-remote is supplied. The runner only
submits target "mock" jobs and never enables live providers.
`;

async function main() {
  const args = process.argv.slice(2);
  if (args.includes('--help') || args.includes('-h')) {
    process.stdout.write(HELP);
    return;
  }
  const config = buildConfig(args);
  const result = await runBenchmark(config);
  const json = `${JSON.stringify(result, null, 2)}\n`;
  if (config.output) {
    await mkdir(dirname(config.output), { recursive: true });
    await writeFile(config.output, json, 'utf8');
  }
  process.stdout.write(config.json ? json : `${formatHuman(result)}\n`);
}

main().catch((error) => {
  process.stderr.write(`benchmark failed: ${error instanceof Error ? error.message : 'unknown error'}\n`);
  process.exitCode = 1;
});
