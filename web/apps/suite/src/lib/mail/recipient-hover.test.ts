/**
 * Tests for the recipient-hover singleton (REQ-MAIL-46).
 *
 * Coverage:
 *   - 400 ms hover-intent before opening
 *   - keyboard focus opens immediately
 *   - moving to a new trigger cancels the previous open without
 *     re-paying the open delay
 *   - 150 ms close grace; cancelClose keeps the card open
 *   - closeNow tears the card down synchronously
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { recipientHover, _internals_forTest } from './recipient-hover.svelte';

const { OPEN_DELAY_MS, CLOSE_DELAY_MS } = _internals_forTest;

function makeAnchor(): HTMLElement {
  return document.createElement('button');
}

describe('recipient-hover singleton', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    recipientHover.closeNow();
  });
  afterEach(() => {
    vi.useRealTimers();
    recipientHover.closeNow();
  });

  it('opens after the 400 ms hover-intent delay', () => {
    const anchor = makeAnchor();
    recipientHover.requestOpen({ anchor, email: 'x@y.com', capturedName: 'X' });
    expect(recipientHover.open).toBeNull();
    vi.advanceTimersByTime(OPEN_DELAY_MS - 1);
    expect(recipientHover.open).toBeNull();
    vi.advanceTimersByTime(2);
    expect(recipientHover.open?.email).toBe('x@y.com');
  });

  it('keyboard focus opens immediately', () => {
    const anchor = makeAnchor();
    recipientHover.requestOpen(
      { anchor, email: 'x@y.com', capturedName: null },
      { immediate: true },
    );
    expect(recipientHover.open?.email).toBe('x@y.com');
  });

  it('moving to a new trigger swaps the card without paying the open delay', () => {
    const a = makeAnchor();
    const b = makeAnchor();
    recipientHover.requestOpen({ anchor: a, email: 'a@x.com', capturedName: null });
    vi.advanceTimersByTime(OPEN_DELAY_MS + 10);
    expect(recipientHover.open?.email).toBe('a@x.com');
    // Pointer leaves a, enters b — close grace started by leaving.
    recipientHover.requestClose();
    recipientHover.requestOpen({ anchor: b, email: 'b@x.com', capturedName: null });
    // No timer needed: the swap happens synchronously.
    expect(recipientHover.open?.email).toBe('b@x.com');
  });

  it('close grace is 150 ms and cancellable', () => {
    const a = makeAnchor();
    recipientHover.requestOpen(
      { anchor: a, email: 'a@x.com', capturedName: null },
      { immediate: true },
    );
    expect(recipientHover.open?.email).toBe('a@x.com');
    recipientHover.requestClose();
    vi.advanceTimersByTime(CLOSE_DELAY_MS - 1);
    expect(recipientHover.open?.email).toBe('a@x.com');
    recipientHover.cancelClose();
    vi.advanceTimersByTime(CLOSE_DELAY_MS + 10);
    expect(recipientHover.open?.email).toBe('a@x.com');
  });

  it('closeNow tears the card down without waiting', () => {
    const a = makeAnchor();
    recipientHover.requestOpen(
      { anchor: a, email: 'a@x.com', capturedName: null },
      { immediate: true },
    );
    expect(recipientHover.open?.email).toBe('a@x.com');
    recipientHover.closeNow();
    expect(recipientHover.open).toBeNull();
  });

  it('cancels a pending open when leaving before the delay elapses', () => {
    const a = makeAnchor();
    recipientHover.requestOpen({ anchor: a, email: 'a@x.com', capturedName: null });
    vi.advanceTimersByTime(100);
    recipientHover.requestClose();
    vi.advanceTimersByTime(OPEN_DELAY_MS + CLOSE_DELAY_MS + 50);
    expect(recipientHover.open).toBeNull();
  });
});
