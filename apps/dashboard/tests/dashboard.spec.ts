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
  { path: '/webhooks', name: 'webhooks' },
  { path: '/templates', name: 'templates' },
  { path: '/workflows', name: 'workflows' },
  { path: '/cache', name: 'cache' },
  { path: '/audit', name: 'audit' },
  { path: '/users', name: 'users-roles' },
  { path: '/quotas', name: 'quotas-billing' },
  { path: '/settings', name: 'settings' },
  { path: '/metrics', name: 'metrics' },
];

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
    expect(count).toBe(17);

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

test.describe('Visual snapshots', () => {
  // Run at desktop only for snapshot baseline (reduce snapshot count)
  test.use({ viewport: { width: 1440, height: 900 } });

  for (const route of ALL_ROUTES) {
    test(`${route.name} desktop snapshot`, async ({ page }) => {
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
});

test.describe('§24.2 page set completeness', () => {
  test('all 17 §24.2 pages have nav entries', async ({ page }) => {
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

  test('§24.2 count is exactly 17', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const count = await page.locator('aside nav a[href]').count();
    expect(count).toBe(17);
  });
});
