#!/usr/bin/env node
// UBAG Postgres round-trip test runner.
//
// Exercises the env-gated Postgres store integration tests in the Go gateway
// against a real Postgres instance. These tests skip themselves automatically
// when UBAG_TEST_POSTGRES_DSN is unset, so running them through `go test ./...`
// silently passes without proving anything. This runner:
//
//   1. Requires UBAG_TEST_POSTGRES_DSN (fails loudly when missing).
//   2. Optionally applies migrations/postgres/*.sql with `--apply-migrations`
//      (idempotent CREATE TABLE IF NOT EXISTS migrations; needs `psql`).
//   3. Runs ONLY the packages that carry Postgres tests, verbosely.
//   4. Fails if every Postgres test was skipped — i.e. the DSN never connected.
//
// Usage:
//   UBAG_TEST_POSTGRES_DSN=postgres://ubag:ubag@localhost:5432/ubag?sslmode=disable \
//     node tools/run-postgres-roundtrip-tests.mjs [--apply-migrations]
//
// Nothing here is destructive: migrations only add objects, and the Go tests
// scope their writes to throwaway tenant/app identifiers.

import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const repoRoot = resolve(new URL('..', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'));
const gatewayDir = join(repoRoot, 'apps', 'gateway');
const migrationsDir = join(repoRoot, 'migrations', 'postgres');

const applyMigrations = process.argv.includes('--apply-migrations');

const dsn = process.env.UBAG_TEST_POSTGRES_DSN;
if (!dsn || dsn.trim() === '') {
  console.error('Postgres round-trip tests blocked: UBAG_TEST_POSTGRES_DSN is not set.');
  console.error('');
  console.error('Start a local Postgres (the small profile already ships one) and export a DSN, e.g.:');
  console.error('  docker compose -f docker-compose.small.yml up -d postgres');
  console.error('  $env:UBAG_TEST_POSTGRES_DSN = "postgres://ubag:ubag@localhost:5432/ubag?sslmode=disable"');
  console.error('  node tools/run-postgres-roundtrip-tests.mjs --apply-migrations');
  process.exit(1);
}

const goExe = findGo();
if (!goExe) {
  console.error('Postgres round-trip tests blocked: go is not available on PATH or local Codex toolchains.');
  process.exit(1);
}

if (applyMigrations) {
  applyPostgresMigrations(dsn);
}

const packages = discoverPostgresPackages(gatewayDir);
if (packages.length === 0) {
  console.error('No Postgres test packages found under apps/gateway.');
  process.exit(1);
}

console.log(`Running Postgres round-trip tests across ${packages.length} package(s):`);
for (const pkg of packages) {
  console.log(`  - ${pkg}`);
}

const result = spawnSync(goExe, ['test', '-v', '-count=1', ...packages], {
  cwd: gatewayDir,
  encoding: 'utf8',
  env: { ...process.env, GOTOOLCHAIN: 'local' }
});

if (result.error) {
  console.error(`Failed to run ${goExe}: ${result.error.message}`);
  process.exit(1);
}

const output = `${result.stdout ?? ''}${result.stderr ?? ''}`;
process.stdout.write(output);

if (result.status !== 0) {
  console.error('\nPostgres round-trip tests FAILED.');
  process.exit(result.status ?? 1);
}

// Guard against a false-green: if every Postgres test skipped, the DSN never
// connected and we proved nothing.
const ranPostgres = /=== RUN\s+Test\w*Postgres/i.test(output) || /--- PASS:\s+Test\w*Postgres/i.test(output);
const allSkipped = /UBAG_TEST_POSTGRES_DSN is not set/.test(output);
if (!ranPostgres || allSkipped) {
  console.error('\nPostgres round-trip tests did not execute against a live database.');
  console.error('Every Postgres test skipped — verify UBAG_TEST_POSTGRES_DSN points at a reachable instance.');
  process.exit(2);
}

console.log('\nPostgres round-trip tests passed against the live database.');
process.exit(0);

function discoverPostgresPackages(root) {
  const dirs = new Set();
  walk(join(root, 'internal'));
  walk(join(root, 'internal', 'httpapi'));
  return [...dirs].sort();

  function walk(dir) {
    if (!existsSync(dir)) return;
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(full);
      } else if (entry.isFile() && hasPostgresTest(full, entry.name)) {
        const rel = full.slice(root.length + 1).replace(/\\/g, '/');
        const pkgDir = rel.slice(0, rel.lastIndexOf('/'));
        dirs.add(`./${pkgDir}`);
      }
    }
  }

  function hasPostgresTest(full, name) {
    if (!name.endsWith('_test.go')) return false;
    if (name.includes('postgres')) return true;
    // artifacts_test.go gates its Postgres path on the same env var.
    try {
      return readFileSync(full, 'utf8').includes('UBAG_TEST_POSTGRES_DSN');
    } catch {
      return false;
    }
  }
}

function applyPostgresMigrations(connString) {
  const psql = resolveExecutable('psql');
  if (!psql) {
    console.error('--apply-migrations requires `psql` on PATH; skipping migration application.');
    console.error('Apply migrations/postgres/*.sql manually, then re-run without --apply-migrations.');
    process.exit(1);
  }
  const files = readdirSync(migrationsDir)
    .filter((name) => name.endsWith('.sql'))
    .sort();
  for (const file of files) {
    const path = join(migrationsDir, file);
    console.log(`Applying migration ${file} ...`);
    const run = spawnSync(psql, [connString, '-v', 'ON_ERROR_STOP=1', '-f', path], {
      stdio: 'inherit'
    });
    if (run.status !== 0) {
      console.error(`Migration ${file} failed with exit code ${run.status ?? 'unknown'}.`);
      process.exit(run.status ?? 1);
    }
  }
  console.log('All migrations applied.\n');
}

function resolveExecutable(bin) {
  const probe = spawnSync(bin, ['--version'], { encoding: 'utf8' });
  return !probe.error && probe.status === 0 ? bin : null;
}

function findGo() {
  const onPath = spawnSync('go', ['version'], { encoding: 'utf8' });
  if (!onPath.error && onPath.status === 0) {
    return 'go';
  }

  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData) return null;

  const root = join(localAppData, 'CodexToolchains');
  if (!existsSync(root)) return null;

  return readdirSync(root, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name.startsWith('go'))
    .map((entry) => join(root, entry.name, 'go', 'bin', process.platform === 'win32' ? 'go.exe' : 'go'))
    .filter((path) => existsSync(path))
    .sort()
    .reverse()[0] ?? null;
}
