/**
 * ThreadReader: Space / Shift+Space pager-scroll bindings (re #89).
 *
 * Space scrolls the thread's scroll container forward by ~one page;
 * Shift+Space scrolls it back. Both bindings are suppressed by the
 * keyboard engine's existing focus carve-out when an input element
 * has focus — that suppression is tested at the engine level.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import ThreadReader from './ThreadReader.svelte';
import type { Binding } from '../keyboard/engine.svelte';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { keyboardMock, mailMock } = vi.hoisted(() => {
  const capturedBindings: Binding[][] = [];

  const keyboardMock = {
    pushLayer: vi.fn((bindings: Binding[]) => {
      capturedBindings.push(bindings);
      return () => undefined;
    }),
    _capturedBindings: capturedBindings,
  };

  const EMAIL = {
    id: 'e-scroll',
    threadId: 'tid-scroll',
    mailboxIds: { 'mbx-inbox': true } as Record<string, true>,
    keywords: { $seen: true } as Record<string, true | undefined>,
    from: [{ name: 'Alice', email: 'alice@example.test' }],
    to: null,
    cc: null,
    subject: 'Scroll test',
    preview: 'scroll body',
    receivedAt: '2026-05-01T10:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    'header:List-ID:asText': null,
  };

  const mailMock = {
    mailboxes: new Map<string, import('./types').Mailbox>(),
    emails: new Map([['e-scroll', EMAIL]]),
    threads: new Map([
      ['tid-scroll', { id: 'tid-scroll', emailIds: ['e-scroll'] }],
    ]),
    identities: new Map(),
    listFolder: 'inbox' as string,
    get customMailboxes() {
      return [] as import('./types').Mailbox[];
    },
    threadStatus: (tid: string) => (tid === 'tid-scroll' ? 'ready' : 'idle'),
    threadError: () => null,
    threadEmails: (tid: string) => (tid === 'tid-scroll' ? [EMAIL] : []),
    loadThread: vi.fn().mockResolvedValue(undefined),
    openThreadId: null as string | null,
    setOpenThread: vi.fn(),
    pendingArrivalsForThread: () => [],
    dismissPendingArrivals: vi.fn(),
  };

  return { keyboardMock, mailMock };
});

// ── module mocks ───────────────────────────────────────────────────────────────

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));
vi.mock('../router/router.svelte', () => ({
  router: { navigate: vi.fn() },
}));
vi.mock('../keyboard/engine.svelte', () => ({ keyboard: keyboardMock }));
vi.mock('./ThreadToolbar.svelte', () => ({ default: () => null }));
vi.mock('./ThreadReplyBar.svelte', () => ({ default: () => null }));
vi.mock('./MessageAccordion.svelte', () => ({ default: () => null }));

// ── helpers ────────────────────────────────────────────────────────────────────

/** Find a binding by key across all layers captured by pushLayer. */
function findBinding(key: string): Binding | undefined {
  for (const layer of keyboardMock._capturedBindings) {
    const b = layer.find((b) => b.key === key);
    if (b) return b;
  }
  return undefined;
}

// ── tests ──────────────────────────────────────────────────────────────────────

describe('ThreadReader: Space/Shift+Space pager scroll (re #89)', () => {
  beforeEach(() => {
    keyboardMock._capturedBindings.length = 0;
    keyboardMock.pushLayer.mockClear();
  });

  it('registers a Space binding via pushLayer', () => {
    render(ThreadReader, { props: { threadId: 'tid-scroll' } });
    expect(findBinding(' ')).toBeDefined();
  });

  it('registers a Shift+Space binding via pushLayer', () => {
    render(ThreadReader, { props: { threadId: 'tid-scroll' } });
    expect(findBinding('Shift+ ')).toBeDefined();
  });

  it('Space binding calls scrollBy with positive top when clientHeight is non-zero', () => {
    render(ThreadReader, { props: { threadId: 'tid-scroll' } });
    const binding = findBinding(' ');
    expect(binding).toBeDefined();

    // The scroll container is the .scroll div rendered by ThreadReader.
    const scrollEl = document.querySelector<HTMLDivElement>('.scroll')!;
    expect(scrollEl).not.toBeNull();

    // jsdom reports clientHeight as 0 (no layout engine); override it so
    // the action sees a non-zero height and the sign of `top` is meaningful.
    Object.defineProperty(scrollEl, 'clientHeight', { configurable: true, value: 600 });
    const scrollBy = vi.spyOn(scrollEl, 'scrollBy').mockImplementation(() => {});

    binding!.action(new KeyboardEvent('keydown', { key: ' ' }));

    expect(scrollBy).toHaveBeenCalledOnce();
    const opts = scrollBy.mock.calls[0]?.[0] as ScrollToOptions;
    // Forward scroll: top must be positive.
    expect(opts.top).toBe(600 * 0.9);
  });

  it('Shift+Space binding calls scrollBy with negative top when clientHeight is non-zero', () => {
    render(ThreadReader, { props: { threadId: 'tid-scroll' } });
    const binding = findBinding('Shift+ ');
    expect(binding).toBeDefined();

    const scrollEl = document.querySelector<HTMLDivElement>('.scroll')!;
    expect(scrollEl).not.toBeNull();

    Object.defineProperty(scrollEl, 'clientHeight', { configurable: true, value: 600 });
    const scrollBy = vi.spyOn(scrollEl, 'scrollBy').mockImplementation(() => {});

    binding!.action(new KeyboardEvent('keydown', { key: ' ', shiftKey: true }));

    expect(scrollBy).toHaveBeenCalledOnce();
    const opts = scrollBy.mock.calls[0]?.[0] as ScrollToOptions;
    // Backward scroll: top must be negative.
    expect(opts.top).toBe(-(600 * 0.9));
  });
});
