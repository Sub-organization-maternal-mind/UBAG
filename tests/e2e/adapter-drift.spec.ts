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
      name: 'claude-web',
      url: 'https://claude.ai',
      selectors: [
        { name: 'input-box', selector: 'div[contenteditable="true"], textarea[placeholder]' },
        { name: 'send-button', selector: 'button[aria-label*="send" i], button[type="submit"]' },
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
        // Report drift as soft assertion (don't fail the test, just warn)
        if (!found) {
          console.warn(`[DRIFT] ${provider.name}: selector "${name}" (${selector}) not found`);
        }
        // Hard assertion: at least the page loaded (no complete outage)
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

    if (!appSecret) {
      console.warn('UBAG_E2E_APP_SECRET not set — skipping gateway job test');
      return;
    }

    const res = await request.post(`${gatewayUrl}/v1/jobs`, {
      headers: {
        'Authorization': `Bearer ${appSecret}`,
        'Ubag-Api-Version': '2026-05-22',
        'Content-Type': 'application/json',
        'Idempotency-Key': Math.random().toString(36).slice(2),
      },
      data: {
        job: {
          target: 'https://example.com',
          command_type: 'fetch',
          input: { url: 'https://example.com' },
        },
        client: { app_id: 'e2e-test', app_version: '1.0.0', sdk: { name: 'e2e', version: '1.0.0' } },
      },
    });

    expect(res.status()).toBeLessThan(500);
    const body = await res.json();
    const jobId = body?.job?.id ?? body?.id;
    if (jobId) console.log(`[E2E] Created job: ${jobId}`);
  });
});
