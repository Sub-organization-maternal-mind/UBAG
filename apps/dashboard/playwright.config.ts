import { defineConfig, devices } from '@playwright/test';

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
    baseURL: 'http://localhost:58180',
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'npm run preview',
    url: 'http://localhost:58180',
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
  snapshotDir: 'tests/snapshots',
});
