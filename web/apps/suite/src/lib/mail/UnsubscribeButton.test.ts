/**
 * Component tests for UnsubscribeButton (REQ-UNS-01..43).
 *
 * Covers: absence when no message advertises a mechanism (REQ-UNS-03),
 * thread-scoped (not per-message) sourcing (REQ-UNS-11), the one-click
 * flow with no confirmation dialog (REQ-UNS-20/30) and its
 * success/failure toasts (REQ-UNS-40/41), plain-https (REQ-UNS-21),
 * mailto (REQ-UNS-22), and the cleartext refusal (REQ-UNS-04).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import UnsubscribeButton from './UnsubscribeButton.svelte';
import type { Email } from './types';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>) =>
    params ? `${key}:${JSON.stringify(params)}` : key,
}));

const { composeMock, toastMock, postOneClickMock, recordUnsubscribedMock } = vi.hoisted(() => {
  const composeMock = { openWith: vi.fn() };
  const toastMock = { show: vi.fn() };
  const postOneClickMock = vi.fn();
  const recordUnsubscribedMock = vi.fn();
  return { composeMock, toastMock, postOneClickMock, recordUnsubscribedMock };
});

vi.mock('../compose/compose.svelte', () => ({ compose: composeMock }));
vi.mock('../toast/toast.svelte', () => ({ toast: toastMock }));
vi.mock('./unsubscribe', () => ({ postOneClickUnsubscribe: postOneClickMock }));
vi.mock('./unsubscribed-from', () => ({ recordUnsubscribed: recordUnsubscribedMock }));

function makeEmail(overrides: Partial<Email> = {}): Email {
  return {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'List Sender', email: 'sender@list.example.com' }],
    to: null,
    subject: 'subject',
    preview: '',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob',
    'header:List-ID:asText': null,
    ...overrides,
  } as unknown as Email;
}

beforeEach(() => {
  composeMock.openWith.mockClear();
  toastMock.show.mockClear();
  postOneClickMock.mockReset();
  recordUnsubscribedMock.mockClear();
  vi.restoreAllMocks();
});

describe('UnsubscribeButton: presence (REQ-UNS-03/11)', () => {
  it('renders nothing when no message in the thread has List-Unsubscribe', () => {
    render(UnsubscribeButton, { props: { emails: [makeEmail(), makeEmail()] } });
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('renders once for the whole thread even though the header is per-message', () => {
    const emails = [
      makeEmail({ id: 'e1' }),
      makeEmail({ id: 'e2', 'header:List-Unsubscribe:asText': '<https://example.com/unsub>' }),
    ];
    render(UnsubscribeButton, { props: { emails } });
    expect(screen.getAllByRole('button')).toHaveLength(1);
  });
});

describe('UnsubscribeButton: one-click (REQ-UNS-20/30/40/41)', () => {
  function oneClickEmail(): Email {
    return makeEmail({
      'header:List-Unsubscribe:asText': '<https://example.com/unsub?id=1>',
      'header:List-Unsubscribe-Post:asText': 'List-Unsubscribe=One-Click',
    });
  }

  it('POSTs immediately with no confirmation dialog and toasts success', async () => {
    postOneClickMock.mockResolvedValue({ ok: true });
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    expect(postOneClickMock).toHaveBeenCalledWith('https://example.com/unsub?id=1');
    // No dialog/confirm affordance of any kind appears.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(toastMock.show).toHaveBeenCalled());
    expect(recordUnsubscribedMock).toHaveBeenCalledWith('sender@list.example.com');
    expect(toastMock.show).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining('unsubscribe.toast.success') }),
    );
  });

  it('toasts failure with the fallback detail on a non-2xx / network error', async () => {
    postOneClickMock.mockResolvedValue({ ok: false });
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    await waitFor(() =>
      expect(toastMock.show).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'unsubscribe.toast.failed',
          kind: 'error',
          detail: 'https://example.com/unsub?id=1',
        }),
      ),
    );
    expect(recordUnsubscribedMock).not.toHaveBeenCalled();
  });
});

describe('UnsubscribeButton: plain https (REQ-UNS-21)', () => {
  it('opens the URL in a new tab with noopener/noreferrer, no toast', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(UnsubscribeButton, {
      props: {
        emails: [makeEmail({ 'header:List-Unsubscribe:asText': '<https://example.com/unsub>' })],
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    expect(openSpy).toHaveBeenCalledWith('https://example.com/unsub', '_blank', 'noopener,noreferrer');
    expect(toastMock.show).not.toHaveBeenCalled();
    expect(postOneClickMock).not.toHaveBeenCalled();
  });
});

describe('UnsubscribeButton: mailto (REQ-UNS-22/23)', () => {
  it('opens a prefilled compose window, does not auto-send', async () => {
    render(UnsubscribeButton, {
      props: {
        emails: [
          makeEmail({
            'header:List-Unsubscribe:asText':
              '<mailto:unsub@example.com?subject=Unsubscribe&body=please+remove+me>',
          }),
        ],
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    expect(composeMock.openWith).toHaveBeenCalledWith(
      expect.objectContaining({ to: 'unsub@example.com', subject: 'Unsubscribe' }),
    );
    expect(postOneClickMock).not.toHaveBeenCalled();
  });

  it('prefers one-click silently when both one-click and mailto are present', async () => {
    postOneClickMock.mockResolvedValue({ ok: true });
    render(UnsubscribeButton, {
      props: {
        emails: [
          makeEmail({
            'header:List-Unsubscribe:asText':
              '<https://example.com/unsub>, <mailto:unsub@example.com>',
            'header:List-Unsubscribe-Post:asText': 'List-Unsubscribe=One-Click',
          }),
        ],
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    expect(postOneClickMock).toHaveBeenCalledWith('https://example.com/unsub');
    expect(composeMock.openWith).not.toHaveBeenCalled();
  });
});

describe('UnsubscribeButton: cleartext refusal (REQ-UNS-04)', () => {
  it('never auto-clicks a cleartext-only URL; shows the warning instead', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(UnsubscribeButton, {
      props: {
        emails: [makeEmail({ 'header:List-Unsubscribe:asText': '<http://example.com/unsub>' })],
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    expect(openSpy).not.toHaveBeenCalled();
    expect(postOneClickMock).not.toHaveBeenCalled();
    expect(toastMock.show).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'unsubscribe.cleartextWarning' }),
    );
  });
});
