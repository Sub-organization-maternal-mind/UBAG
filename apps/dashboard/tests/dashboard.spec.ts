import { test, expect } from '@playwright/test';
import { injectAxe, getViolations } from 'axe-playwright';

const BREAKPOINTS = [
  { name: 'mobile-320', width: 320, height: 568 },
  { name: 'mobile-375', width: 375, height: 667 },
  { name: 'mobile-414', width: 414, height: 736 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'desktop-1440', width: 1440, height: 900 },
];

const ALL_ROUTES = [
  { path: '/', name: 'overview' },
  { path: '/jobs', name: 'jobs' },
  { path: '/targets', name: 'targets' },
  { path: '/adapters', name: 'adapters' },
  { path: '/apps', name: 'apps' },
  { path: '/devices', name: 'devices' },
  { path: '/failed', name: 'failed-dlq' },
  { path: '/browser', name: 'browser-sessions' },
  { path: '/conversations', name: 'conversations' },
  { path: '/webhooks', name: 'webhooks' },
  { path: '/templates', name: 'templates' },
  { path: '/workflows', name: 'workflows' },
  { path: '/cache', name: 'cache' },
  { path: '/audit', name: 'audit' },
  { path: '/users', name: 'users-roles' },
  { path: '/security', name: 'security' },
  { path: '/admin', name: 'administration' },
  { path: '/quotas', name: 'quotas-billing' },
  { path: '/settings', name: 'settings' },
  { path: '/metrics', name: 'metrics' },
  { path: '/antigravity', name: 'antigravity' },
];

/**
 * The expected number of sidebar nav links. Derived from ALL_ROUTES rather than
 * hardcoded, so adding a page to the nav without listing it here (or vice
 * versa) fails the completeness test instead of needing the magic number
 * bumped in two places.
 */
const EXPECTED_NAV_COUNT = ALL_ROUTES.length;

function navHrefSelector(path: string) {
  const staticHref = path === '/' ? './' : `.${path}`;
  return `aside nav a[href="${path}"], aside nav a[href="${staticHref}"]`;
}

test.describe('Shell navigation', () => {
  test('nav lists all §24.2 pages', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const navLinks = page.locator('aside nav a[href]');
    const count = await navLinks.count();
    expect(count).toBe(EXPECTED_NAV_COUNT);

    // Verify key hrefs are present
    for (const route of ALL_ROUTES) {
      const link = page.locator(navHrefSelector(route.path));
      await expect(link).toBeVisible();
    }
  });

  test('nav has no horizontal overflow at all breakpoints', async ({ page }) => {
    for (const bp of BREAKPOINTS) {
      await page.setViewportSize({ width: bp.width, height: bp.height });
      await page.goto('/');
      await page.waitForLoadState('domcontentloaded');

      const body = await page.evaluate(() => ({
        scrollWidth: document.body.scrollWidth,
        clientWidth: document.body.clientWidth,
      }));

      // On mobile the sidebar is off-canvas (transform: translateX(-100%)) so
      // its pixels are outside the viewport but don't cause body scroll.
      // Allow a 1px rounding tolerance.
      expect(body.scrollWidth, `Overflow at ${bp.name}`).toBeLessThanOrEqual(
        body.clientWidth + 1
      );
    }
  });
});

test.describe('Page routing', () => {
  for (const route of ALL_ROUTES) {
    test(`${route.name} page loads without error`, async ({ page }) => {
      await page.goto(route.path);
      await page.waitForLoadState('domcontentloaded');

      // The nav is always rendered by the layout — its presence confirms the
      // page rendered without a hard crash.
      await expect(page.locator('aside nav')).toBeVisible();
    });
  }
});

test('Antigravity 401 links to gateway credential settings', async ({ page }) => {
  await page.route('**/v1/antigravity/**', (route) => route.fulfill({
    status: 401,
    contentType: 'application/json',
    body: '{"error":"unauthorized"}',
  }));
  await page.goto('/antigravity');
  await expect(page.getByText('Sign in to manage Antigravity accounts.')).toBeVisible();
  const settingsLink = page.getByRole('main').getByRole('link', { name: 'Settings' });
  await expect(settingsLink).toBeVisible();
  await settingsLink.click();
  await expect(page.getByLabel('App Secret (Bearer token)')).toBeVisible();
});

test('Antigravity sign-in keeps the code private until an account-pinned canary completes', async ({ page }) => {
  const accountID = 'acct_1';
  const authorizationURL = 'https://accounts.google.com/o/oauth2/auth?state=synthetic';
  let loginState = 'not_started';
  let verificationState = 'unverified';
  let canaryCalls = 0;

  await page.route('**/v1/antigravity/accounts', (route) => route.fulfill({
    json: { accounts: [{
      account_id: accountID, label: 'Synthetic worker', tier: 'pro', enabled: true,
      last_used: '0001-01-01T00:00:00Z', cooldown_until: null,
      created_at: '2026-01-01T00:00:00Z', worker_socket_present: true,
      verification_state: verificationState,
      verification_job_id: verificationState === 'unverified' ? undefined : 'job_synthetic',
      verified_at: verificationState === 'verified' ? '2026-01-01T12:00:00Z' : undefined,
    }] },
  }));
  await page.route('**/v1/antigravity/config', (route) => route.fulfill({
    json: { default_model: 'test-model', default_effort: 'high', account_count: 1, oauth_enabled: true },
  }));
  await page.route(`**/v1/antigravity/accounts/${accountID}/login`, (route) => {
    const method = route.request().method();
    if (method === 'POST') {
      const input = route.request().postDataJSON();
      if (input.action === 'start') loginState = 'awaiting_code';
      if (input.action === 'input') {
        expect(input.input).toBe('synthetic-code');
        loginState = 'verifying';
      }
    } else if (method === 'DELETE') loginState = 'stopped';
    return route.fulfill({
      json: {
        state: loginState,
        ...(loginState === 'awaiting_code' ? { authorization_url: authorizationURL } : {}),
      },
    });
  });
  await page.route('**/v1/antigravity/test', (route) => {
    expect(route.request().postDataJSON().account_id).toBe(accountID);
    canaryCalls++;
    verificationState = 'pending';
    return route.fulfill({ status: 202, json: { job_id: 'job_synthetic', status: 'accepted', model: 'test-model' } });
  });

  await page.goto('/antigravity');
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Open Google sign-in' })).toHaveAttribute('href', authorizationURL);
  for (const bp of BREAKPOINTS.filter(({ width }) => width <= 768)) {
    await page.setViewportSize({ width: bp.width, height: bp.height });
    const layout = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
      overflowingControls: Array.from(document.querySelectorAll('main button, main input, main select, main a'))
        .filter((element) => {
          const bounds = element.getBoundingClientRect();
          return bounds.left < -1 || bounds.right > document.documentElement.clientWidth + 1;
        }).map((element) => element.textContent?.trim()),
    }));
    expect(layout.scrollWidth, `Login panel horizontal scroll at ${bp.name}`).toBeLessThanOrEqual(layout.clientWidth + 1);
    expect(layout.overflowingControls, `Clipped login controls at ${bp.name}`).toEqual([]);
  }
  expect(canaryCalls).toBe(0);
  await page.getByLabel('Authorization code').fill('synthetic-code');
  await page.getByRole('button', { name: 'Submit code' }).click();
  await expect(page.getByText('Code submitted')).toBeVisible();
  await expect(page.getByText('synthetic-code')).toHaveCount(0);
  expect(await page.evaluate(() => JSON.stringify(localStorage))).not.toContain('synthetic-code');
  await page.getByRole('button', { name: 'Verify with test job' }).click();
  await expect.poll(() => canaryCalls).toBe(1);
  await expect(page.getByText('Canary pending')).toBeVisible();
  verificationState = 'verified';
  await page.getByRole('button', { name: 'Refresh accounts' }).click();
  await expect(page.getByText(/Canary passed/)).toBeVisible();
});

test('Antigravity account details fit the Hallmark mobile breakpoints', async ({ page }) => {
  await page.route('**/v1/antigravity/accounts', (route) => route.fulfill({
    json: { accounts: [{
      account_id: 'synthetic-slot', label: 'Synthetic slot', tier: 'pro', enabled: true,
      last_used: '0001-01-01T00:00:00Z', cooldown_until: null,
      created_at: '2026-01-01T00:00:00Z', worker_socket_present: false,
      verification_state: 'unverified',
    }] },
  }));
  await page.route('**/v1/antigravity/config', (route) => route.fulfill({
    json: {
      default_model: 'test-model', default_effort: 'high', max_concurrent: 1,
      account_count: 1, oauth_enabled: false,
    },
  }));

  for (const bp of BREAKPOINTS.filter(({ width }) => width <= 768)) {
    await page.setViewportSize({ width: bp.width, height: bp.height });
    await page.goto('/antigravity');
    await expect(page.getByText('Synthetic slot')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeDisabled();
    await expect(page.getByRole('button', { name: 'Test account' })).toBeDisabled();
    const layout = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
      overflowingControls: Array.from(document.querySelectorAll('main button, main input, main select, main a'))
        .filter((element) => {
          const bounds = element.getBoundingClientRect();
          return bounds.left < -1 || bounds.right > document.documentElement.clientWidth + 1;
        }).map((element) => element.textContent?.trim()),
    }));
    expect(layout.scrollWidth, `Horizontal scroll at ${bp.name}`).toBeLessThanOrEqual(layout.clientWidth + 1);
    expect(layout.overflowingControls, `Clipped controls at ${bp.name}`).toEqual([]);
  }
});

test('Antigravity does not claim an unreported OAuth status is disabled', async ({ page }) => {
  await page.route('**/v1/antigravity/accounts', (route) => route.fulfill({
    json: { accounts: [] },
  }));
  await page.route('**/v1/antigravity/config', (route) => route.fulfill({
    json: { default_model: 'test-model', default_effort: 'high', max_concurrent: 1, account_count: 0 },
  }));
  await page.goto('/antigravity');
  await expect(page.getByText('OAuth status unavailable')).toBeVisible();
  await expect(page.getByText('Disabled on gateway')).toHaveCount(0);
});

test('jobs attachment picker fits the Hallmark mobile breakpoints', async ({ page }) => {
  for (const bp of BREAKPOINTS.filter(({ width }) => width <= 768)) {
    await page.setViewportSize({ width: bp.width, height: bp.height });
    await page.goto('/jobs');
    await page.waitForLoadState('domcontentloaded');

    await expect(page.getByText('Drop files here or browse')).toBeVisible();
    const picker = page.locator('.attachment-picker');
    const geometry = await picker.evaluate((element) => ({
      right: element.getBoundingClientRect().right,
      viewport: document.documentElement.clientWidth,
      bodyScroll: document.body.scrollWidth,
    }));
    expect(geometry.right, `Picker overflow at ${bp.name}`).toBeLessThanOrEqual(geometry.viewport + 1);
    expect(geometry.bodyScroll, `Body overflow at ${bp.name}`).toBeLessThanOrEqual(geometry.viewport + 1);
  }
});

test.describe('Visual snapshots', () => {
  // Run at desktop only for snapshot baseline (reduce snapshot count)
  test.use({ viewport: { width: 1440, height: 900 } });

  // Routes without a committed chromium-linux baseline. Generate with
  // `npx playwright test -u` inside mcr.microsoft.com/playwright:v1.61.0-noble
  // (matches CI Ubuntu + playwright version, per 1710f34), then commit the
  // linux file and drop the name from this set. The routing suite above
  // still covers these pages on Linux.
  //
  // `antigravity` joined this set on 2026-09-27 when the page was added: its
  // chromium-win32 baseline is committed, but the Linux one cannot be produced
  // from a Windows workstation. It is here for the same reason as the other
  // three - a missing baseline is a platform-coverage gap, not a passing test.
  const MISSING_LINUX_BASELINES = new Set([
    'conversations',
    'security',
    'administration',
    'antigravity'
  ]);

  for (const route of ALL_ROUTES) {
    test(`${route.name} desktop snapshot`, async ({ page }) => {
      test.skip(
        process.platform === 'linux' && MISSING_LINUX_BASELINES.has(route.name),
        'no chromium-linux baseline committed yet'
      );
      await page.goto(route.path);
      await page.waitForLoadState('domcontentloaded');
      // Wait for loading states to settle
      await page.waitForTimeout(500);
      await expect(page).toHaveScreenshot(`${route.name}-desktop.png`, {
        maxDiffPixelRatio: 0.05,
        fullPage: true,
      });
    });
  }
});

test.describe('Accessibility (axe-core)', () => {
  test('homepage passes axe a11y check', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    await injectAxe(page);
    const violations = await getViolations(page, undefined, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
    });

    // Filter to critical/serious only (skip minor/moderate cosmetic issues)
    const critical = violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious'
    );
    expect(
      critical,
      'Critical a11y violations: ' + JSON.stringify(critical.map((v) => v.description))
    ).toHaveLength(0);
  });

  test('settings page passes axe check', async ({ page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    await injectAxe(page);
    const violations = await getViolations(page, undefined, {
      runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
    });

    const critical = violations.filter(
      (v) => v.impact === 'critical' || v.impact === 'serious'
    );
    expect(
      critical,
      JSON.stringify(critical.map((v) => v.description))
    ).toHaveLength(0);
  });

  for (const route of ALL_ROUTES) {
    if (route.path === '/' || route.path === '/settings') continue; // covered above
    test(`${route.name} passes axe check`, async ({ page }) => {
      await page.goto(route.path);
      await page.waitForLoadState('domcontentloaded');
      await page.waitForTimeout(300); // let error/empty states settle

      await injectAxe(page);
      const violations = await getViolations(page, undefined, {
        runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
      });

      const critical = violations.filter(
        (v) => v.impact === 'critical' || v.impact === 'serious'
      );
      expect(
        critical,
        `${route.path}: ` + JSON.stringify(critical.map((v) => ({ id: v.id, description: v.description })))
      ).toHaveLength(0);
    });
  }
});

test.describe('§24.2 page set completeness', () => {
  test('every §24.2 page has a nav entry', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const expectedHrefs = ALL_ROUTES.map((r) => r.path);
    for (const href of expectedHrefs) {
      await expect(
        page.locator(navHrefSelector(href)),
        `Missing nav link: ${href}`
      ).toBeVisible();
    }
  });

  test('§24.2 count matches the route inventory', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const count = await page.locator('aside nav a[href]').count();
    expect(count).toBe(EXPECTED_NAV_COUNT);
  });
});
