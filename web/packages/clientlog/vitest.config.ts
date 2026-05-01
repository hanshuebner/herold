import { defineConfig } from 'vitest/config';

// Vitest configuration for the clientlog package. Pure TypeScript tests,
// no Svelte components, no DOM framework. Uses happy-dom for the minimal
// browser-API surface (window.onerror, navigator.sendBeacon, etc.).
export default defineConfig({
  test: {
    globals: true,
    environment: 'happy-dom',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/**/*.ts'],
      exclude: ['src/**/*.test.ts', 'src/test/**'],
    },
  },
});
