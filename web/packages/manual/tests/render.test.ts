/**
 * Unit tests for render.ts helpers.
 *
 * resolveManualHref is the link-rewriting helper used by ManualPage to map
 * Markdoc-emitted hrefs onto SPA-compatible hash routes.
 */
import { describe, it, expect } from 'vitest';
import { resolveManualHref, slugify } from '../src/markdoc/render.js';

describe('resolveManualHref', () => {
  it('rewrites a bare slug to a hash route', () => {
    expect(resolveManualHref('quickstart', 'index')).toBe('#/help/quickstart');
  });

  it('rewrites a slug with an in-chapter anchor', () => {
    expect(resolveManualHref('install#configuration', 'index')).toBe('#/help/install/configuration');
  });

  it('rewrites a same-page anchor against the current chapter slug', () => {
    expect(resolveManualHref('#build', 'install')).toBe('#/help/install/build');
  });

  it('preserves https URLs unchanged', () => {
    expect(resolveManualHref('https://example.com/foo', 'index')).toBe('https://example.com/foo');
  });

  it('preserves mailto: links unchanged', () => {
    expect(resolveManualHref('mailto:hi@example.com', 'index')).toBe('mailto:hi@example.com');
  });

  it('preserves path-absolute hrefs unchanged (escape hatch)', () => {
    expect(resolveManualHref('/api/v1/health', 'index')).toBe('/api/v1/health');
  });

  it('returns "#" for missing href', () => {
    expect(resolveManualHref(undefined, 'index')).toBe('#');
    expect(resolveManualHref('', 'index')).toBe('#');
  });

  it('returns "#" for a bare hash with no anchor', () => {
    expect(resolveManualHref('#', 'index')).toBe('#');
  });
});

describe('slugify', () => {
  // Sanity guard against regressions; the helper is also exercised through
  // the heading-id round-trip in Manual.test.ts but the contract is small
  // enough that direct cases are worth keeping near the helper.
  it('lowercases and replaces spaces with hyphens', () => {
    expect(slugify('Volume and port layout')).toBe('volume-and-port-layout');
  });

  it('strips punctuation', () => {
    expect(slugify('"Reverse DNS doesn\'t match HELO"')).toBe('reverse-dns-doesnt-match-helo');
  });
});
