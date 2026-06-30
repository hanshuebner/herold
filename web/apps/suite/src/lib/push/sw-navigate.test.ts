/**
 * Unit tests for the SW → SPA navigate-message helper.
 *
 * Covers the message-type guard, path normalisation (stripping the `/#`
 * openWindow prefix), and edge cases.  The App.svelte event listener wires
 * this helper directly to `navigator.serviceWorker`, so these tests provide
 * the definitive automated gate for the SPA-side of the fallback path.
 *
 * REQ-PUSH-70..73.
 */

import { describe, expect, it, vi } from 'vitest';
import { handleSwNavigateMessage, normalizeSwPath } from './sw-navigate';

// ── normalizeSwPath ───────────────────────────────────────────────────────────

describe('normalizeSwPath', () => {
  it('strips /#/ prefix from hash-router paths', () => {
    expect(normalizeSwPath('/#/mail/thread/T1234')).toBe('/mail/thread/T1234');
  });

  it('strips /#/ prefix for chat overlay paths', () => {
    expect(normalizeSwPath('/#/mail?openChat=conv-1')).toBe('/mail?openChat=conv-1');
  });

  it('strips bare /# prefix (no trailing slash)', () => {
    expect(normalizeSwPath('/#')).toBe('');
  });

  it('leaves plain paths unchanged', () => {
    expect(normalizeSwPath('/mail/thread/T1234')).toBe('/mail/thread/T1234');
    expect(normalizeSwPath('/mail')).toBe('/mail');
    expect(normalizeSwPath('/')).toBe('/');
  });

  it('leaves the root path unchanged', () => {
    expect(normalizeSwPath('/')).toBe('/');
  });
});

// ── handleSwNavigateMessage ───────────────────────────────────────────────────

describe('handleSwNavigateMessage — routing', () => {
  it('calls navigate with a plain mail path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'navigate', path: '/mail/thread/T1234' }, nav);
    expect(nav).toHaveBeenCalledWith('/mail/thread/T1234');
  });

  it('calls navigate with a compose path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage(
      { type: 'navigate', path: '/#/mail/compose?inReplyTo=e42&quick=1' },
      nav,
    );
    expect(nav).toHaveBeenCalledWith('/mail/compose?inReplyTo=e42&quick=1');
  });

  it('calls navigate after stripping /#/ from a thread path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'navigate', path: '/#/mail/thread/T1234' }, nav);
    expect(nav).toHaveBeenCalledWith('/mail/thread/T1234');
  });

  it('calls navigate after stripping /#/ from a chat overlay path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage(
      { type: 'navigate', path: '/#/mail?openChat=conv-1' },
      nav,
    );
    expect(nav).toHaveBeenCalledWith('/mail?openChat=conv-1');
  });

  it('calls navigate with / for the root fallback path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'navigate', path: '/' }, nav);
    expect(nav).toHaveBeenCalledWith('/');
  });
});

describe('handleSwNavigateMessage — guard clauses', () => {
  it('ignores messages with wrong type', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'other', path: '/mail' }, nav);
    handleSwNavigateMessage({ type: 'SKIP_WAITING' }, nav);
    expect(nav).not.toHaveBeenCalled();
  });

  it('ignores messages with empty path', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'navigate', path: '' }, nav);
    expect(nav).not.toHaveBeenCalled();
  });

  it('ignores messages with missing path property', () => {
    const nav = vi.fn();
    handleSwNavigateMessage({ type: 'navigate' }, nav);
    expect(nav).not.toHaveBeenCalled();
  });

  it('ignores non-object data', () => {
    const nav = vi.fn();
    handleSwNavigateMessage('invalid string', nav);
    handleSwNavigateMessage(null, nav);
    handleSwNavigateMessage(undefined, nav);
    handleSwNavigateMessage(42, nav);
    expect(nav).not.toHaveBeenCalled();
  });
});
