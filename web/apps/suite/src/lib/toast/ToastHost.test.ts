/**
 * Issue #480: a toast message longer than the toast's fixed width must
 * wrap instead of being clipped to a single line with an ellipsis.
 *
 * happy-dom does not cascade scoped Svelte component styles into
 * `getComputedStyle` (see the established pattern in
 * MailView.date-alignment.test.ts), so the assertion is done in two steps:
 *
 *   1. Structural: render the toast with a long message and confirm the
 *      full text is present verbatim in the `.message` element (nothing
 *      truncated it before it reached the DOM).
 *   2. Source: read ToastHost.svelte as a raw string via Vite's `?raw`
 *      suffix and assert that the `.message` rule in the `<style>` block
 *      does not clip -- no `white-space: nowrap` / `text-overflow:
 *      ellipsis` -- so a future edit cannot silently reintroduce clipping.
 */

import { describe, it, expect, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';
import ToastHost from './ToastHost.svelte';
import toastHostSource from './ToastHost.svelte?raw';
import { toast } from './toast.svelte';

afterEach(() => {
  toast.dismiss();
  cleanup();
});

/**
 * Extract the declarations inside the first occurrence of `selector { ... }`
 * from a CSS string. Mirrors the minimal extractor in
 * MailView.date-alignment.test.ts; not a full CSS parser.
 */
function extractRuleBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.[\]]/g, (c) => `\\${c}`);
  const re = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`);
  const m = css.match(re);
  return m?.[1] ?? '';
}

function toastHostStyles(): string {
  const match = toastHostSource.match(/<style>([\s\S]*?)<\/style>/);
  if (!match?.[1]) throw new Error('Could not find <style> block in ToastHost.svelte');
  return match[1];
}

const LONG_MESSAGE =
  'Could not cancel send: external submission cannot be undone after ' +
  'the wire transfer to the outbound relay has already started and the ' +
  'receiving mail exchanger has acknowledged the message body in full.';

describe('ToastHost message wrapping (re #480)', () => {
  it('renders the full long message text with nothing truncated', () => {
    toast.show({ message: LONG_MESSAGE, kind: 'error', timeoutMs: 0 });
    render(ToastHost);

    const message = screen.getByText(LONG_MESSAGE);
    expect(message.textContent).toBe(LONG_MESSAGE);
  });

  it('renders short messages unchanged', () => {
    toast.show({ message: 'Saved', kind: 'info', timeoutMs: 0 });
    render(ToastHost);

    expect(screen.getByText('Saved')).toBeInTheDocument();
  });

  it('does not clip .message with nowrap/ellipsis in the component stylesheet', () => {
    const css = extractRuleBody(toastHostStyles(), '.message');
    expect(css).not.toMatch(/white-space\s*:\s*nowrap/);
    expect(css).not.toMatch(/text-overflow\s*:\s*ellipsis/);
  });
});
