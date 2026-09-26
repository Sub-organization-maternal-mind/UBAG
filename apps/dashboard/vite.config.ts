import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  // Local-dev-only defaults for src/lib/stores/settings.ts, set by
  // tools/local-launcher and apps/dashboard/scripts/run-local.sh before
  // building. Unset (the default for any other build, including a real
  // production build) bakes in an empty string, which is a no-op — settings.ts
  // then falls back to its normal production behavior unchanged.
  define: {
    __UBAG_DEFAULT_GATEWAY_URL__: JSON.stringify(process.env.UBAG_DEV_DEFAULT_GATEWAY_URL || ''),
    __UBAG_DEFAULT_APP_SECRET__: JSON.stringify(process.env.UBAG_DEV_DEFAULT_APP_SECRET || ''),
    __UBAG_DEFAULT_LIVE_BROWSER_WS__: JSON.stringify(process.env.UBAG_DEV_DEFAULT_LIVE_BROWSER_WS || ''),
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2020',
    // Dynamic imports (xterm, chart.js) split into separate chunks
    // automatically; warn early if a route chunk grows past budget.
    chunkSizeWarningLimit: 150,
  },
  server: {
    // Deliberately uncommon (not 3000/5173/8080/etc.) to avoid colliding with
    // other local dev servers on this machine.
    port: 58179,
    strictPort: false,
    proxy: {
      '/v1': {
        target: process.env.UBAG_DEV_DEFAULT_GATEWAY_URL || 'http://127.0.0.1:58080',
        changeOrigin: true,
      },
    },
  },
  preview: {
    port: 58180,
    strictPort: true,
  },
  test: {
    environment: 'jsdom',
    // Exclude Playwright specs — they are run via `npx playwright test`, not Vitest
    exclude: ['tests/**', '**/node_modules/**', '**/dist/**'],
  },
});
