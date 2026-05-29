import { createHash, randomUUID } from 'node:crypto';

const activeDedupeStatuses = new Set(['queued', 'leased', 'retry_scheduled']);
const terminalStatuses = new Set(['completed', 'dead_lettered', 'cancelled']);

export function createReferenceMemoryQueue({ nowMs = Date.parse('2026-01-01T00:00:00.000Z') } = {}) {
  return new ReferenceMemoryQueue(nowMs);
}

class ReferenceMemoryQueue {
  #jobs = new Map();
  #deadLetters = new Map();
  #nowMs;

  constructor(nowMs) {
    this.#nowMs = nowMs;
  }

  advanceTimeBy(ms) {
    this.#nowMs += ms;
  }

  async enqueue(jobName, payload, options = {}) {
    const queueName = options.queueName ?? 'default';
    const requestHash = hashRequest(jobName, payload);
    const idempotency = normalizeIdempotency(options);

    if (idempotency) {
      const existing = [...this.#jobs.values()].find((job) => {
        return job.idempotencyScope === idempotency.scope && job.idempotencyKey === idempotency.key;
      });

      if (existing) {
        if (existing.idempotencyRequestHash !== requestHash) {
          throw new Error('idempotency conflict: request hash does not match existing job');
        }

        return cloneJob(existing);
      }
    }

    if (options.dedupeKey) {
      const duplicate = [...this.#jobs.values()].find((job) => {
        return (
          job.queueName === queueName
          && job.dedupeKey === options.dedupeKey
          && activeDedupeStatuses.has(job.status)
        );
      });

      if (duplicate) {
        return cloneJob(duplicate);
      }
    }

    const now = this.#now();
    const runAt = options.runAt
      ? toIso(options.runAt)
      : new Date(this.#nowMs + (options.delayMs ?? 0)).toISOString();
    const job = {
      id: randomUUID(),
      queueName,
      jobName,
      payload: cloneJson(payload),
      payloadVersion: options.payloadVersion ?? 1,
      status: 'queued',
      priority: options.priority ?? 0,
      runAt,
      attemptCount: 0,
      maxAttempts: options.maxAttempts ?? 3,
      createdAt: now,
      updatedAt: now,
      dedupeKey: options.dedupeKey,
      idempotencyScope: idempotency?.scope,
      idempotencyKey: idempotency?.key,
      idempotencyRequestHash: idempotency ? requestHash : undefined,
      metadata: options.metadata ? cloneJson(options.metadata) : undefined,
      leaseId: undefined,
      leaseExpiresAt: undefined,
      leasedBy: undefined,
    };

    this.#jobs.set(job.id, job);
    return cloneJob(job);
  }

  async leaseNext(options) {
    const queueName = options.queueName ?? 'default';
    const now = this.#now();
    const due = [...this.#jobs.values()]
      .filter((job) => {
        if (job.queueName !== queueName || Date.parse(job.runAt) > this.#nowMs) {
          return false;
        }

        if (job.status === 'queued' || job.status === 'retry_scheduled') {
          return true;
        }

        return job.status === 'leased' && job.leaseExpiresAt && Date.parse(job.leaseExpiresAt) <= this.#nowMs;
      })
      .sort((left, right) => {
        return (
          right.priority - left.priority
          || Date.parse(left.runAt) - Date.parse(right.runAt)
          || Date.parse(left.createdAt) - Date.parse(right.createdAt)
        );
      })[0];

    if (!due) {
      return null;
    }

    due.status = 'leased';
    due.attemptCount += 1;
    due.leaseId = randomUUID();
    due.leasedBy = options.workerId;
    due.leaseExpiresAt = new Date(this.#nowMs + (options.leaseMs ?? 30_000)).toISOString();
    due.updatedAt = now;

    return {
      job: cloneJob(due),
      leaseId: due.leaseId,
      workerId: options.workerId,
      attemptNumber: due.attemptCount,
      leaseExpiresAt: due.leaseExpiresAt,
    };
  }

  async acknowledge(jobId, leaseId, options = {}) {
    const job = this.#requireLeasedJob(jobId, leaseId);
    job.status = 'completed';
    job.result = options.result ? cloneJson(options.result) : undefined;
    job.leaseId = undefined;
    job.leasedBy = undefined;
    job.leaseExpiresAt = undefined;
    job.updatedAt = this.#now();
    job.completedAt = job.updatedAt;
    return cloneJob(job);
  }

  async reject(jobId, leaseId, options = {}) {
    const job = this.#requireLeasedJob(jobId, leaseId);
    job.lastError = options.error ? cloneJson(options.error) : { reason: options.reason ?? 'rejected' };
    job.leaseId = undefined;
    job.leasedBy = undefined;
    job.leaseExpiresAt = undefined;

    if (options.retry !== false && job.attemptCount < job.maxAttempts) {
      job.status = 'retry_scheduled';
      job.runAt = new Date(this.#nowMs + (options.delayMs ?? 0)).toISOString();
      job.updatedAt = this.#now();
      return cloneJob(job);
    }

    job.status = 'dead_lettered';
    job.updatedAt = this.#now();
    job.completedAt = job.updatedAt;
    this.#deadLetters.set(job.id, {
      jobId: job.id,
      reason: options.reason ?? 'max attempts reached',
      movedAt: job.updatedAt,
      lastError: job.lastError,
    });
    return cloneJob(job);
  }

  async extendLease(jobId, leaseId, leaseMs) {
    const job = this.#requireLeasedJob(jobId, leaseId);
    job.leaseExpiresAt = new Date(this.#nowMs + leaseMs).toISOString();
    job.updatedAt = this.#now();
    return {
      job: cloneJob(job),
      leaseId,
      workerId: job.leasedBy,
      attemptNumber: job.attemptCount,
      leaseExpiresAt: job.leaseExpiresAt,
    };
  }

  async cancel(jobId, reason) {
    const job = this.#requireJob(jobId);
    if (terminalStatuses.has(job.status)) {
      return cloneJob(job);
    }

    job.status = 'cancelled';
    job.lastError = reason ? { reason } : undefined;
    job.leaseId = undefined;
    job.leasedBy = undefined;
    job.leaseExpiresAt = undefined;
    job.updatedAt = this.#now();
    job.cancelledAt = job.updatedAt;
    return cloneJob(job);
  }

  async deadLetter(jobId, options) {
    const job = this.#requireJob(jobId);
    if (job.status !== 'completed' && job.status !== 'cancelled') {
      job.status = 'dead_lettered';
      job.lastError = options.error ? cloneJson(options.error) : { reason: options.reason };
      job.leaseId = undefined;
      job.leasedBy = undefined;
      job.leaseExpiresAt = undefined;
      job.updatedAt = this.#now();
      job.completedAt = job.updatedAt;
      this.#deadLetters.set(job.id, {
        jobId: job.id,
        reason: options.reason,
        movedAt: job.updatedAt,
        lastError: job.lastError,
      });
    }

    return cloneJob(job);
  }

  async getStatus(jobId) {
    const job = this.#jobs.get(jobId);
    return job ? cloneJob(job) : null;
  }

  async getStats(queueName = 'default') {
    const jobs = [...this.#jobs.values()].filter((job) => job.queueName === queueName);
    const ready = jobs.filter((job) => job.status === 'queued' || job.status === 'retry_scheduled');

    return {
      queueName,
      queued: jobs.filter((job) => job.status === 'queued').length,
      leased: jobs.filter((job) => job.status === 'leased').length,
      retryScheduled: jobs.filter((job) => job.status === 'retry_scheduled').length,
      completed: jobs.filter((job) => job.status === 'completed').length,
      deadLettered: jobs.filter((job) => job.status === 'dead_lettered').length,
      cancelled: jobs.filter((job) => job.status === 'cancelled').length,
      oldestReadyAt: ready.length
        ? ready.map((job) => job.runAt).sort()[0]
        : undefined,
    };
  }

  #requireJob(jobId) {
    const job = this.#jobs.get(jobId);
    if (!job) {
      throw new Error(`job not found: ${jobId}`);
    }

    return job;
  }

  #requireLeasedJob(jobId, leaseId) {
    const job = this.#requireJob(jobId);
    if (job.status !== 'leased' || job.leaseId !== leaseId) {
      throw new Error('active lease token required');
    }

    return job;
  }

  #now() {
    return new Date(this.#nowMs).toISOString();
  }
}

function normalizeIdempotency(options) {
  const hasScope = typeof options.idempotencyScope === 'string';
  const hasKey = typeof options.idempotencyKey === 'string';

  if (hasScope !== hasKey) {
    throw new Error('idempotencyScope and idempotencyKey must be provided together');
  }

  return hasScope
    ? { scope: options.idempotencyScope, key: options.idempotencyKey }
    : null;
}

function hashRequest(jobName, payload) {
  return createHash('sha256')
    .update(jobName)
    .update('\0')
    .update(JSON.stringify(payload))
    .digest('hex');
}

function cloneJob(job) {
  const publicJob = {
    id: job.id,
    queueName: job.queueName,
    jobName: job.jobName,
    payload: cloneJson(job.payload),
    payloadVersion: job.payloadVersion,
    status: job.status,
    priority: job.priority,
    runAt: job.runAt,
    attemptCount: job.attemptCount,
    maxAttempts: job.maxAttempts,
    createdAt: job.createdAt,
    updatedAt: job.updatedAt,
  };

  copyDefined(publicJob, 'dedupeKey', job.dedupeKey);
  copyDefined(publicJob, 'idempotencyScope', job.idempotencyScope);
  copyDefined(publicJob, 'idempotencyKey', job.idempotencyKey);
  copyDefined(publicJob, 'metadata', job.metadata ? cloneJson(job.metadata) : undefined);

  return publicJob;
}

function copyDefined(target, key, value) {
  if (value !== undefined) {
    target[key] = value;
  }
}

function cloneJson(value) {
  return JSON.parse(JSON.stringify(value));
}

function toIso(value) {
  return value instanceof Date ? value.toISOString() : new Date(value).toISOString();
}
