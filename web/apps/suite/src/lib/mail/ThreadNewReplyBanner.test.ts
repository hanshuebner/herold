/**
 * Component tests for the inline "new reply while reading" banner
 * (issue #118). The banner appears whenever the mail store reports
 * pending arrivals for the open thread; it persists until explicitly
 * dismissed; "Show new reply" passes the latest arrival's id to the
 * onShow callback so the thread reader can scroll and expand it.
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
      dismissPendingArrivals: vi.fn(),
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
    mailMock.dismissPendingArrivals.mockReset();
    mailMock.pendingArrivalsForThread.mockReset();
    mailMock.pendingArrivalsForThread.mockImplementation(
      (_tid: string) => mailMock.arrivals,
    );
  });

  it('renders nothing when there are no pending arrivals', () => {
    const { container } = render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onShow: vi.fn() },
    });
    expect(container.querySelector('.new-reply-banner')).toBeNull();
  });

  it('renders single-arrival heading with sender label and preview', () => {
    mailMock.arrivals = [
      makeArrival('e-new', 'Carol', 'Quick correction: 16:00 UTC', '2026-05-09T12:00:00Z'),
    ];
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onShow: vi.fn() },
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
      props: { threadId: 'tid-1', onShow: vi.fn() },
    });
    // "3 new replies" — heading uses the many.heading key with count.
    expect(
      screen.getByText('mail.threadReader.newReply.many.heading(count=3)'),
    ).toBeInTheDocument();
    // Preview shows the latest (Dan's) snippet.
    expect(screen.getByText('latest')).toBeInTheDocument();
    // "+2 more" hint surfaces the older arrivals.
    expect(
      screen.getByText('mail.threadReader.newReply.more(count=2)'),
    ).toBeInTheDocument();
  });

  it('Got it dismisses without invoking onShow', async () => {
    mailMock.arrivals = [
      makeArrival('e-new', 'Carol', 'snippet', '2026-05-09T12:00:00Z'),
    ];
    const onShow = vi.fn();
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onShow },
    });
    const dismissBtn = screen.getByText('mail.threadReader.newReply.dismiss');
    await fireEvent.click(dismissBtn);
    expect(mailMock.dismissPendingArrivals).toHaveBeenCalledWith('tid-1');
    expect(onShow).not.toHaveBeenCalled();
  });

  it('Show new reply dismisses and invokes onShow with the latest id', async () => {
    mailMock.arrivals = [
      makeArrival('e-old', 'Bob', 'older', '2026-05-09T10:00:00Z'),
      makeArrival('e-latest', 'Dan', 'newest', '2026-05-09T10:02:00Z'),
    ];
    const onShow = vi.fn();
    render(ThreadNewReplyBanner, {
      props: { threadId: 'tid-1', onShow },
    });
    const showBtn = screen.getByText('mail.threadReader.newReply.show');
    await fireEvent.click(showBtn);
    expect(mailMock.dismissPendingArrivals).toHaveBeenCalledWith('tid-1');
    expect(onShow).toHaveBeenCalledWith('e-latest');
  });
});
