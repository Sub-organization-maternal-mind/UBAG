import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  kit: {
    // Base path: empty for local dev / preview / Playwright (routes at root);
    // set UBAG_BASE_PATH=/dashboard for the production build served under /dashboard/.
    paths: {
      base: process.env.UBAG_BASE_PATH ?? '',
    },
    adapter: adapter({
      pages: 'dist',
      assets: 'dist',
      fallback: 'index.html',
      precompress: false,
      strict: false,
    }),
    prerender: { handleHttpError: 'warn' },
  },
};
