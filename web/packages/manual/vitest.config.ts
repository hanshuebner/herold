import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';

// Vitest configuration for @herold/manual.
// Kept separate from any potential vite.config.ts so the dev / build
// pipeline does not pull in vitest types.
//
// svelteTesting re-routes Svelte's exports for the testing-library entry
// points so $state, $derived, etc. work outside a browser mount path.
export default defineConfig({
  plugins: [svelte({ hot: !process.env.VITEST }), svelteTesting()],
  test: {
    globals: true,
    environment: 'happy-dom',
    setupFiles: ['./tests/setup.ts'],
    include: ['tests/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/**/*.{ts,svelte}'],
      exclude: ['src/index.ts'],
    },
  },
});
