import assert from 'node:assert/strict';

const terminalStatuses = new Set(['completed', 'dead_lettered', 'cancelled']);

export async function runQueueConformanceSuite({
  name = 'queue',
  createQueue,
  advanceTimeBy,
} = {}) {
  if (typeof createQueue !== 'function') {
    throw new TypeError('runQueueConformanceSuite requires createQueue.');
  }

  const results = [];

  async function buildQueue(testName) {
    const created = await createQueue({ testName });
    if (created && typeof created.enqueue === 'function') {
      return { queue: created, advance: advanceTimeBy };
    }

    if (created?.queue && typeof created.queue.enqueue === 'function') {
      return {
        queue: created.queue,
        advance: created.advanceTimeBy ?? advanceTimeBy,
      };
    }

    throw new TypeError(`createQueue did not return a Queue for ${testName}.`);
  }

  async function run(testName, testFn) {
    const { queue, advance } = await buildQueue(testName);
    await testFn(queue, advance);
    results.push({ name: testName, status: 'passed' });
  }

  await run('enqueue returns durable status', async (queue) => {
    const job = await queue.enqueue(
      'browser.open',
      { url: 'https://example.test' },
      { queueName: 'edge', payloadVersion: 1 },
    );

    assert.equal(job.queueName, 'edge');
    assert.equal(job.jobName, 'browser.open');
    assert.equal(job.status, 'queued');
    assert.equal(job.payloadVersion, 1);
    assert.equal(job.attemptCount, 0);

    const status = await queue.getStatus(job.id);
    assert.equal(status?.id, job.id);
    assert.equal(status?.status, 'queued');
  });

  await run('idempotency returns same job and rejects conflicts', async (queue) => {
    const options = {
      queueName: 'edge',
      idempotencyScope: 'tenant:alpha',
      idempotencyKey: 'create-job-1',
    };

    const first = await queue.enqueue('target.capture', { targetId: 'one' }, options);
    const second = await queue.enqueue('target.capture', { targetId: 'one' }, options);
    assert.equal(second.id, first.id);

    await assert.rejects(
      () => queue.enqueue('target.capture', { targetId: 'two' }, options),
      /idempotency|conflict|request hash/i,
    );
  });

  await run('active dedupe key collapses duplicate queued work', async (queue) => {
    const first = await queue.enqueue(
      'target.capture',
      { targetId: 'one' },
      { queueName: 'edge', dedupeKey: 'target:one' },
    );
    const second = await queue.enqueue(
      'target.capture',
      { targetId: 'one' },
      { queueName: 'edge', dedupeKey: 'target:one' },
    );
    assert.equal(second.id, first.id);

    const lease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-1' });
    assert.equal(lease?.job.id, first.id);
    await queue.acknowledge(first.id, lease.leaseId);

    const third = await queue.enqueue(
      'target.capture',
      { targetId: 'one' },
      { queueName: 'edge', dedupeKey: 'target:one' },
    );
    assert.notEqual(third.id, first.id);
  });

  await run('lease order honors priority and queue name', async (queue) => {
    await queue.enqueue('low', { rank: 1 }, { queueName: 'edge', priority: 1 });
    const high = await queue.enqueue('high', { rank: 2 }, { queueName: 'edge', priority: 10 });
    await queue.enqueue('other-queue', { rank: 3 }, { queueName: 'server', priority: 100 });

    const lease = await queue.leaseNext({
      queueName: 'edge',
      workerId: 'worker-priority',
      leaseMs: 30_000,
    });

    assert.equal(lease?.job.id, high.id);
    assert.equal(lease.job.status, 'leased');
    assert.equal(lease.attemptNumber, 1);
  });

  await run('delayed jobs are not leased before runAt', async (queue, advance) => {
    await queue.enqueue('delayed', { after: true }, { queueName: 'edge', delayMs: 60_000 });

    const earlyLease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-delay' });
    assert.equal(earlyLease, null);

    if (typeof advance === 'function') {
      await advance(60_001);
      const dueLease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-delay' });
      assert.equal(dueLease?.job.jobName, 'delayed');
    }
  });

  await run('acknowledge requires active lease token', async (queue) => {
    const job = await queue.enqueue('complete-me', { ok: true }, { queueName: 'edge' });
    const lease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-ack' });
    assert.equal(lease?.job.id, job.id);

    await assert.rejects(
      () => queue.acknowledge(job.id, 'stale-lease'),
      /lease/i,
    );

    const completed = await queue.acknowledge(job.id, lease.leaseId, {
      result: { ok: true },
    });
    assert.equal(completed.status, 'completed');
  });

  await run('reject retries then moves to dead letter', async (queue) => {
    const job = await queue.enqueue(
      'retry-me',
      { fail: true },
      { queueName: 'edge', maxAttempts: 2 },
    );

    const firstLease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-retry' });
    assert.equal(firstLease?.job.id, job.id);
    const retry = await queue.reject(job.id, firstLease.leaseId, {
      retry: true,
      delayMs: 0,
      reason: 'transient',
      error: { code: 'TARGET_BUSY' },
    });
    assert.equal(retry.status, 'retry_scheduled');

    const secondLease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-retry' });
    assert.equal(secondLease?.job.id, job.id);
    const dead = await queue.reject(job.id, secondLease.leaseId, {
      retry: true,
      delayMs: 0,
      reason: 'still failing',
      error: { code: 'TARGET_BUSY' },
    });
    assert.equal(dead.status, 'dead_lettered');

    const stats = await queue.getStats('edge');
    assert.equal(stats.deadLettered, 1);
  });

  await run('cancel prevents future leasing', async (queue) => {
    const job = await queue.enqueue('cancel-me', { ok: false }, { queueName: 'edge' });
    const cancelled = await queue.cancel(job.id, 'operator request');
    assert.equal(cancelled.status, 'cancelled');

    const lease = await queue.leaseNext({ queueName: 'edge', workerId: 'worker-cancel' });
    assert.equal(lease, null);
  });

  await run('terminal jobs stay queryable for status visibility', async (queue) => {
    const job = await queue.enqueue('visible', { value: 1 }, { queueName: 'edge' });
    const forced = await queue.deadLetter(job.id, {
      reason: 'operator terminal failure',
      error: { code: 'OPERATOR_TERMINAL' },
    });
    assert.equal(forced.status, 'dead_lettered');
    assert.ok(terminalStatuses.has(forced.status));

    const status = await queue.getStatus(job.id);
    assert.equal(status?.status, 'dead_lettered');
  });

  return {
    name,
    passed: results.length,
    results,
  };
}
