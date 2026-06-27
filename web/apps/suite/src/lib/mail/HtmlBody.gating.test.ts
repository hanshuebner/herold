/**
 * Tests for the initial-render gating in HtmlBody.svelte (Forgejo #47
 * follow-up): the iframe is hidden (opacity 0, class body-hidden) until its
 * height is measured for the first time; a spinner overlays after ~100 ms.
 *
 * happy-dom does not fire real iframe onload events, so we can only assert
 * client-side state observable at render time. The visual flow (spinner ->
 * full-height reveal with no cut-off) is confirmed via the puppeteer pass.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';
import HtmlBody from './HtmlBody.svelte';

// ── module mocks ──────────────────────────────────────────────────────────────

vi.mock('../i18n/i18n.svelte', () => ({
  // Return the key unchanged so tests assert the key used, not a translated string.
  t: (key: string) => key,
  localeTag: () => 'en',
}));

vi.mock('./sanitize', () => ({
  sanitizeHtml: (html: string) =>
    `<!doctype html><html><body>${html}</body></html>`,
  htmlHasExternalImages: () => false,
}));

vi.mock('./scroll-parent', () => ({
  findScrollParent: () => null,
}));

// ── tests ─────────────────────────────────────────────────────────────────────

describe('HtmlBody: initial-render gating (Forgejo #47 follow-up)', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('iframe is present in the DOM immediately', () => {
    render(HtmlBody, { props: { html: '<p>Hello</p>' } });
    const frame = document.querySelector('iframe');
    expect(frame).toBeInTheDocument();
  });

  it('iframe starts hidden via body-hidden class before first measurement', () => {
    render(HtmlBody, { props: { html: '<p>Hello</p>' } });
    const frame = document.querySelector('iframe');
    expect(frame).toHaveClass('body-hidden');
  });

  it('spinner is not rendered before the 100ms threshold', async () => {
    render(HtmlBody, { props: { html: '<p>Hello</p>' } });
    // Flush Svelte effects so the setTimeout is registered.
    await Promise.resolve();
    await Promise.resolve();
    // Do not advance time yet.
    expect(document.querySelector('[role="status"]')).not.toBeInTheDocument();
  });

  it('spinner appears with the correct aria-label after the 100ms threshold', async () => {
    render(HtmlBody, { props: { html: '<p>Hello</p>' } });
    // Flush Svelte effects so the setTimeout is registered.
    await Promise.resolve();
    await Promise.resolve();
    // Advance past the 100ms delay.
    vi.advanceTimersByTime(110);
    // Flush the reactive update triggered by showSpinner = true.
    await Promise.resolve();
    await Promise.resolve();
    const spinner = document.querySelector('[role="status"]');
    expect(spinner).toBeInTheDocument();
    expect(spinner).toHaveAttribute('aria-label', 'msg.body.renderingAria');
  });
});
