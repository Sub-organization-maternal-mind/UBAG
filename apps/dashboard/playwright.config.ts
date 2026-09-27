import { defineConfig, devices } from '@playwright/test';

const previewPort = process.env.UBAG_PLAYWRIGHT_PREVIEW_PORT;
if (previewPort && !/^\d{2,5}$/.test(previewPort)) throw new Error('Invalid Playwright preview port');
const dashboardURL = previewPort ? `http://127.0.0.1:${previewPort}` : 'http://localhost:58180';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  expect: {
    // Linux baselines are generated in the Playwright noble image; allow a small
    // pixel-diff ratio to absorb font-antialiasing differences vs the CI runner.
    toHaveScreenshot: { maxDiffPixelRatio: 0.02 },
  },
  reporter: [['html', { open: 'never' }], ['list']],
  use: {
    baseURL: dashboardURL,
    trace: 'on-first-retry',
  },
  webServer: {
    command: previewPort ? 'node ../../serve-dashboard.mjs' : 'npm run preview',
    url: dashboardURL,
    env: previewPort ? { PORT: previewPort, UBAG_DASHBOARD_GATEWAY_URL: 'http://127.0.0.1:9' } : undefined,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  snapshotDir: 'tests/snapshots',
});
