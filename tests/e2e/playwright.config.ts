import { defineConfig, devices } from '@playwright/test';

const E2E_ENABLED = process.env.UBAG_E2E === '1';

export default defineConfig({
  testDir: '.',
  fullyParallel: false,
  workers: 1,
  timeout: 120_000,
  reporter: [['list'], ['html', { open: 'never', outputFolder: '../../playwright-report/e2e' }]],
  use: {
    baseURL: process.env.UBAG_E2E_GATEWAY ?? 'http://localhost:8081',
    trace: 'on-first-retry',
    // Stealth browser settings
    launchOptions: {
      args: ['--disable-blink-features=AutomationControlled'],
      executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH,
    },
  },
  projects: [
    {
      name: 'e2e-live',
      use: { ...devices['Desktop Chrome'] },
      testMatch: E2E_ENABLED ? '**/*.spec.ts' : '__never_match__',
    },
  ],
});
