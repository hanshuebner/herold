import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { svelteTesting } from '@testing-library/svelte/vite';

// vitest configuration for the Suite SPA. Kept separate from
// vite.config.ts so the dev / build pipeline does not need to import
// vitest types. The svelteTesting plugin re-rewrites Svelte's exports
// for the testing-library entry points so $state etc. work outside the
// component-mount path.
export default defineConfig({
  plugins: [svelte({ hot: !process.env.VITEST }), svelteTesting()],
  test: {
    globals: true,
    environment: 'happy-dom',
    setupFiles: ['./src/testing/setup.ts'],
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/**/*.{ts,svelte}'],
      exclude: [
        'src/**/*.test.ts',
        'src/testing/**',
        'src/main.ts',
      ],
    },
  },
});
