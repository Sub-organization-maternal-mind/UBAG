import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { runQueueConformanceSuite } from '../conformance/queue-conformance.mjs';
import { createReferenceMemoryQueue } from './reference-memory-queue.mjs';

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = resolve(packageRoot, '..', '..');
const migrationRoot = join(repoRoot, 'migrations', 'sqlite');

const migrationCheck = await validateSqliteMigrations();

const report = await runQueueConformanceSuite({
  name: 'reference-memory-queue',
  createQueue: () => {
    const queue = createReferenceMemoryQueue();
    return { queue, advanceTimeBy: (ms) => queue.advanceTimeBy(ms) };
  },
});

console.log(`queue conformance: ${report.name} passed ${report.passed} checks`);
console.log(`sqlite migration checks: passed (${migrationCheck.mode})`);

async function validateSqliteMigrations() {
  const coreSql = readMigration('0001_edge_store_core.sql');
  const queueSql = readMigration('0002_edge_queue.sql');
  const webhookSql = readMigration('0003_webhook_outbox.sql');
  const combined = `${coreSql}\n${queueSql}\n${webhookSql}`;

  for (const forbidden of [
    /\bJSONB\b/i,
    /\bTIMESTAMPTZ\b/i,
    /\bSERIAL\b/i,
    /\bBIGSERIAL\b/i,
    /\bCREATE\s+TYPE\b/i,
    /\bALTER\s+TYPE\b/i,
  ]) {
    assert.doesNotMatch(combined, forbidden, `SQLite migrations must not use ${forbidden}`);
  }

  for (const token of [
    'PRAGMA foreign_keys = ON',
    'CREATE TABLE IF NOT EXISTS edge_schema_migrations',
    'CREATE TABLE IF NOT EXISTS edge_idempotency_keys',
    'CREATE TABLE IF NOT EXISTS edge_blob_objects',
    'CREATE TABLE IF NOT EXISTS edge_outbox_events',
  ]) {
    assert.match(coreSql, literalPattern(token), `core migration missing ${token}`);
  }

  for (const token of [
    'CREATE TABLE IF NOT EXISTS edge_queue_jobs',
    'CREATE TABLE IF NOT EXISTS edge_queue_attempts',
    'CREATE TABLE IF NOT EXISTS edge_queue_events',
    'CREATE TABLE IF NOT EXISTS edge_queue_dead_letters',
    'payload_json TEXT NOT NULL CHECK (json_valid(payload_json))',
    'lease_expires_at TEXT',
    'CREATE UNIQUE INDEX IF NOT EXISTS ux_edge_queue_jobs_active_dedupe',
    'CREATE UNIQUE INDEX IF NOT EXISTS ux_edge_queue_jobs_idempotency',
    "'retry_scheduled'",
    "'dead_lettered'",
  ]) {
    assert.match(queueSql, literalPattern(token), `queue migration missing ${token}`);
  }

  for (const token of [
    'CREATE TABLE IF NOT EXISTS webhook_deliveries',
    'CREATE TABLE IF NOT EXISTS webhook_delivery_attempts',
    "'0003'",
    "'webhook_outbox'",
  ]) {
    assert.match(webhookSql, literalPattern(token), `webhook migration missing ${token}`);
  }

  const executed = await tryExecuteSqliteMigrations(coreSql, queueSql, webhookSql);
  return { mode: executed ? 'executed in node:sqlite' : 'text invariants only' };
}

async function tryExecuteSqliteMigrations(coreSql, queueSql, webhookSql) {
  let sqlite;
  try {
    sqlite = await import('node:sqlite');
  } catch (error) {
    if (error?.code === 'ERR_UNKNOWN_BUILTIN_MODULE' || error?.code === 'ERR_MODULE_NOT_FOUND') {
      return false;
    }

    throw error;
  }

  if (typeof sqlite.DatabaseSync !== 'function') {
    return false;
  }

  const db = new sqlite.DatabaseSync(':memory:');
  try {
    db.exec(coreSql);
    db.exec(queueSql);
    db.exec(webhookSql);

    const tableCount = db.prepare(`
SELECT COUNT(*) AS count
FROM sqlite_master
WHERE type = 'table'
  AND name IN (
    'edge_schema_migrations',
    'edge_idempotency_keys',
    'edge_blob_objects',
    'edge_outbox_events',
    'edge_queue_jobs',
    'edge_queue_attempts',
     'edge_queue_events',
     'edge_queue_dead_letters',
     'webhook_deliveries',
     'webhook_delivery_attempts'
  );
`).get().count;
    assert.equal(tableCount, 10);

    db.exec(`
INSERT INTO edge_queue_jobs (
  id,
  queue_name,
  job_name,
  payload_json,
  run_at
) VALUES (
  'job-smoke-1',
  'edge',
  'smoke',
  '{"ok":true}',
  '2026-01-01T00:00:00.000Z'
);
`);

    const row = db.prepare(`
SELECT status, payload_version, max_attempts, attempt_count
FROM edge_queue_jobs
WHERE id = 'job-smoke-1';
`).get();
    assert.deepEqual({ ...row }, {
      status: 'queued',
      payload_version: 1,
      max_attempts: 3,
      attempt_count: 0,
    });
  } finally {
    db.close();
  }

  return true;
}

function readMigration(fileName) {
  return readFileSync(join(migrationRoot, fileName), 'utf8');
}

function literalPattern(value) {
  return new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
}
