import type { JsonObject, JsonValue } from './store.js';

export type QueueStatus =
  | 'queued'
  | 'leased'
  | 'retry_scheduled'
  | 'completed'
  | 'dead_lettered'
  | 'cancelled';

export interface QueueEnqueueOptions {
  queueName?: string;
  payloadVersion?: number;
  priority?: number;
  delayMs?: number;
  runAt?: Date | string;
  maxAttempts?: number;
  dedupeKey?: string;
  idempotencyScope?: string;
  idempotencyKey?: string;
  metadata?: JsonObject;
}

export interface QueuedJob<TPayload = JsonValue> {
  id: string;
  queueName: string;
  jobName: string;
  payload: TPayload;
  payloadVersion: number;
  status: QueueStatus;
  priority: number;
  runAt: string;
  attemptCount: number;
  maxAttempts: number;
  createdAt: string;
  updatedAt: string;
  dedupeKey?: string;
  idempotencyScope?: string;
  idempotencyKey?: string;
  metadata?: JsonObject;
}

export interface QueueLeaseOptions {
  queueName?: string;
  workerId: string;
  leaseMs?: number;
}

export interface QueueLease<TPayload = JsonValue> {
  job: QueuedJob<TPayload> & { status: 'leased' };
  leaseId: string;
  workerId: string;
  attemptNumber: number;
  leaseExpiresAt: string;
}

export interface QueueAcknowledgeOptions {
  result?: JsonValue;
}

export interface QueueRejectOptions {
  retry?: boolean;
  delayMs?: number;
  reason?: string;
  error?: JsonValue;
}

export interface QueueDeadLetterOptions {
  reason: string;
  error?: JsonValue;
}

export interface QueueStats {
  queueName: string;
  queued: number;
  leased: number;
  retryScheduled: number;
  completed: number;
  deadLettered: number;
  cancelled: number;
  oldestReadyAt?: string;
}

export interface Queue {
  /**
   * Enqueue a named, JSON-serializable job. Active dedupe keys return the
   * existing active job. Idempotency keys must return the same job for the same
   * request body and reject conflicting request hashes.
   */
  enqueue<TPayload extends JsonValue>(
    jobName: string,
    payload: TPayload,
    options?: QueueEnqueueOptions,
  ): Promise<QueuedJob<TPayload>>;

  /**
   * Atomically claim one due job ordered by priority desc, runAt asc, createdAt
   * asc. The adapter must set a lease token and visibility timeout before
   * returning. Expired leases are eligible to be reclaimed.
   */
  leaseNext<TPayload extends JsonValue>(
    options: QueueLeaseOptions,
  ): Promise<QueueLease<TPayload> | null>;

  /**
   * Complete a leased job. Acknowledge must fail when the lease token does not
   * match the active lease.
   */
  acknowledge(
    jobId: string,
    leaseId: string,
    options?: QueueAcknowledgeOptions,
  ): Promise<QueuedJob>;

  /**
   * Reject a leased job. Retryable rejection reschedules until maxAttempts is
   * reached; the final rejection moves the job to the dead-letter table.
   */
  reject(
    jobId: string,
    leaseId: string,
    options?: QueueRejectOptions,
  ): Promise<QueuedJob>;

  /**
   * Extend the visibility timeout for the active lease. The call must fail for
   * stale lease tokens.
   */
  extendLease(
    jobId: string,
    leaseId: string,
    leaseMs: number,
  ): Promise<QueueLease>;

  /**
   * Cancel non-terminal jobs. Workers may still need cooperative cancellation
   * checks between browser or adapter steps.
   */
  cancel(jobId: string, reason?: string): Promise<QueuedJob>;

  /**
   * Force a job into the dead-letter state for operator or adapter terminal
   * failures.
   */
  deadLetter(jobId: string, options: QueueDeadLetterOptions): Promise<QueuedJob>;

  getStatus(jobId: string): Promise<QueuedJob | null>;

  getStats(queueName?: string): Promise<QueueStats>;
}
