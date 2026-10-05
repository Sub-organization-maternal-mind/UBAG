import { test, expect } from '@playwright/test';

// Drift detection canaries from Phase 3.
// These tests verify adapter selectors still exist on live provider pages.
// Gate: UBAG_E2E=1 must be set; tests are skipped otherwise.

const SKIP = process.env.UBAG_E2E !== '1';

test.describe('Adapter drift detection canaries', () => {
  test.skip(SKIP, 'Set UBAG_E2E=1 to run live adapter tests');

  const PROVIDERS = [
    {
      name: 'chatgpt',
      url: 'https://chat.openai.com',
      selectors: [
        { name: 'input-box', selector: 'textarea[placeholder], div[contenteditable="true"]' },
        { name: 'send-button', selector: 'button[data-testid="send-button"], button[aria-label*="send" i]' },
      ],
    },
    {
      name: 'gemini',
      url: 'https://gemini.google.com',
      selectors: [
        { name: 'input-box', selector: 'div[contenteditable="true"], rich-textarea' },
        { name: 'send-button', selector: 'button[aria-label*="send" i], mat-icon-button' },
      ],
    },
  ];

  for (const provider of PROVIDERS) {
    test(`${provider.name}: key selectors still present`, async ({ page }) => {
      await page.goto(provider.url, { waitUntil: 'domcontentloaded', timeout: 30_000 });

      for (const { name, selector } of provider.selectors) {
        const found = await page.locator(selector).first().isVisible({ timeout: 10_000 }).catch(() => false);
        // Fail closed: a missing selector IS the drift this canary exists to detect.
        expect(found, `[DRIFT] ${provider.name}: selector "${name}" (${selector}) not found`).toBe(true);
      }

      // Verify the page has a meaningful title (not a Cloudflare block or error page)
      const title = await page.title();
      expect(title.length).toBeGreaterThan(0);
      expect(title.toLowerCase()).not.toContain('error');
      expect(title.toLowerCase()).not.toContain('blocked');
    });
  }
});

test.describe('Gateway + automation path (live)', () => {
  test.skip(SKIP, 'Set UBAG_E2E=1 to run live automation tests');

  test('submits a job to the live gateway', async ({ request }) => {
    const gatewayUrl = process.env.UBAG_E2E_GATEWAY ?? 'http://localhost:8081';
    const appSecret = process.env.UBAG_E2E_APP_SECRET ?? '';

    // Fail closed: a missing secret must never read as a pass.
    expect(appSecret, 'UBAG_E2E_APP_SECRET must be set when UBAG_E2E=1').not.toBe('');

    const apiVersion = '2026-05-22';
    const marker = 'UBAG_E2E_OK';
    const key = `e2e-${Math.random().toString(36).slice(2)}`;
    const headers = {
      Authorization: `Bearer ${appSecret}`,
      'Ubag-Api-Version': apiVersion,
      'Content-Type': 'application/json',
    };

    const res = await request.post(`${gatewayUrl}/v1/jobs`, {
      headers: { ...headers, 'Idempotency-Key': key },
      data: {
        api_version: apiVersion,
        idempotency_key: key,
        client: { app_id: 'e2e-test', app_version: '1.0.0', sdk: { name: 'e2e', version: '1.0.0' } },
        job: { target: 'mock', command_type: 'chat.prompt', input: { prompt: `Return the exact text: ${marker}` } },
      },
    });
    expect(res.status()).toBe(202);
    const jobId = (await res.json()).job_id as string;
    expect(jobId).toMatch(/^job_[A-Za-z0-9]+$/);

    // Poll to a terminal state; only plain `completed` carrying the marker passes.
    let job: { status: string; result?: unknown } = { status: 'queued' };
    const terminal = ['completed', 'completed_with_warnings', 'failed_retryable', 'failed_terminal', 'dead_letter', 'cancelled', 'timed_out'];
    await expect
      .poll(async () => {
        const r = await request.get(`${gatewayUrl}/v1/jobs/${jobId}`, { headers });
        expect(r.status()).toBe(200);
        job = await r.json();
        return terminal.includes(job.status);
      }, { timeout: 30_000 })
      .toBe(true);
    expect(job.status).toBe('completed');
    expect(JSON.stringify(job.result ?? null)).toContain(marker);
  });
});
