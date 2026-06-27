/**
 * Component tests for the inline "new reply while reading" banner
 * (issue #118, issue #56). The banner appears whenever the mail store reports
 * pending arrivals for the open thread; it persists until explicitly
 * dismissed; "Neue Antwort anzeigen" invokes onAccept so ThreadReader can
 * call acceptPendingArrivals and scroll to the new messages; "Verstanden"
 * invokes onDismiss so ThreadReader can call dismissPendingArrivals without
 * inserting the arrivals into the rendered list.
 *
 * The banner does NOT call store methods itself; it delegates all
 * store mutations to the caller via the onAccept / onDismiss props.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ThreadNewReplyBanner from './ThreadNewReplyBanner.svelte';
import type { Email } from './types';

const { mailMock } = vi.hoisted(() => {
  return {
    mailMock: {
      arrivals: [] as Email[],
      pendingArrivalsForThread: vi.fn((_tid: string) => mailMock.arrivals),
    },
  };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>): string => {
    if (!params) return key;
    const suffix = Object.entries(params)
      .map(([k, v]) => `${k}=${v}`)
      .join(',');
    return `${key}(${suffix})`;
  },
}));

function makeArrival(id: string, sender: string, preview: string, ts: string): Email {
  return {
    id,
    threadId: 'tid-1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: sender, email: `${sender}@example.test` }],
    to: null,
    subject: 'Re: deploy schedule',
    preview,
    receivedAt: ts,
    hasAttachment: false,
    blobId: 'blob-stub',
  };
}

describe('ThreadNewReplyBanner', () => {
  beforeEach(() => {
    mailMock.arrivals = [];
    mailMock.pendingArrivalsForThread.mockReset();
    mailMock.pendingArrivalsForThread.mockImplementation(
      (_tid: string) => mailMock.arrivals,
    );
  });

  it('renders nothing when there are no pending arrivals', () => {
    const { container } = render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onAccept: vi.fn(), onDismiss: vi.fn() },
    });
    expect(container.querySelector('.new-reply-banner')).toBeNull();
  });

  it('renders single-arrival heading with sender label and preview', () => {
    mailMock.arrivals = [
      makeArrival('e-new', 'Carol', 'Quick correction: 16:00 UTC', '2026-05-09T12:00:00Z'),
    ];
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onAccept: vi.fn(), onDismiss: vi.fn() },
    });
    expect(
      screen.getByText('mail.threadReader.newReply.one.heading(sender=Carol)'),
    ).toBeInTheDocument();
    expect(
      screen.getByText('Quick correction: 16:00 UTC'),
    ).toBeInTheDocument();
  });

  it('collapses multiple arrivals with a +N more hint', () => {
    mailMock.arrivals = [
      makeArrival('e-1', 'Bob', 'first', '2026-05-09T10:00:00Z'),
      makeArrival('e-2', 'Carol', 'second', '2026-05-09T10:01:00Z'),
      makeArrival('e-3', 'Dan', 'latest', '2026-05-09T10:02:00Z'),
    ];
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onAccept: vi.fn(), onDismiss: vi.fn() },
    });
    // "3 new replies" heading.
    expect(
      screen.getByText('mail.threadReader.newReply.many.heading(count=3)'),
    ).toBeInTheDocument();
    // Preview shows the latest (Dan's) snippet.
    expect(screen.getByText('latest')).toBeInTheDocument();
    // "+2 more" hint.
    expect(
      screen.getByText('mail.threadReader.newReply.more(count=2)'),
    ).toBeInTheDocument();
  });

  it('"Verstanden" calls onDismiss without calling onAccept', async () => {
    mailMock.arrivals = [
      makeArrival('e-new', 'Carol', 'snippet', '2026-05-09T12:00:00Z'),
    ];
    const onAccept = vi.fn();
    const onDismiss = vi.fn();
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onAccept, onDismiss },
    });
    const dismissBtn = screen.getByText('mail.threadReader.newReply.dismiss');
    await fireEvent.click(dismissBtn);
    expect(onDismiss).toHaveBeenCalledOnce();
    expect(onAccept).not.toHaveBeenCalled();
  });

  it('"Neue Antwort anzeigen" calls onAccept without calling onDismiss', async () => {
    mailMock.arrivals = [
      makeArrival('e-old', 'Bob', 'older', '2026-05-09T10:00:00Z'),
      makeArrival('e-latest', 'Dan', 'newest', '2026-05-09T10:02:00Z'),
    ];
    const onAccept = vi.fn();
    const onDismiss = vi.fn();
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onAccept, onDismiss },
    });
    const showBtn = screen.getByText('mail.threadReader.newReply.show');
    await fireEvent.click(showBtn);
    expect(onAccept).toHaveBeenCalledOnce();
    expect(onDismiss).not.toHaveBeenCalled();
  });
});
