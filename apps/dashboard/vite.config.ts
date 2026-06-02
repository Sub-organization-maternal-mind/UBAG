import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2020',
  },
  server: {
    port: 4177,
    strictPort: false,
  },
  preview: {
    port: 4178,
    strictPort: true,
  },
  test: {
    environment: 'jsdom',
  },
});
