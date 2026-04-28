/**
 * Vitest global setup for the Suite SPA. Runs before every test file
 * (per vitest.config.ts setupFiles). Registers @testing-library/jest-
 * dom matchers (toBeInTheDocument, toHaveAttribute, ...) on Vitest's
 * expect, and resets DOM cleanup between tests.
 */
import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/svelte';

afterEach(() => {
  cleanup();
});
