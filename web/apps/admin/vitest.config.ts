import { defineConfig } from 'vitest/config';

// vitest configuration for the Admin SPA.
// Kept separate from vite.config.ts so the dev / build pipeline does not
// need to import vitest types.
export default defineConfig({
  test: {
    globals: true,
    environment: 'happy-dom',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      include: ['src/**/*.ts'],
      exclude: [
        'src/**/*.test.ts',
        'src/main.ts',
      ],
    },
  },
});
