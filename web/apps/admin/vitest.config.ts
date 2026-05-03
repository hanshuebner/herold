import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';

// vitest configuration for the Admin SPA.
// Kept separate from vite.config.ts so the dev / build pipeline does not
// need to import vitest types.
//
// svelteTesting re-routes Svelte's exports for the testing-library entry
// points so $state, $derived, etc. work outside a browser mount path.
export default defineConfig({
  plugins: [svelte({ hot: !process.env.VITEST }), svelteTesting()],
  test: {
    globals: true,
    environment: 'happy-dom',
    setupFiles: ['src/vitest.setup.ts'],
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/**/*.ts', 'src/**/*.svelte'],
      exclude: [
        'src/**/*.test.ts',
        'src/main.ts',
        'src/vitest.setup.ts',
      ],
    },
  },
});
