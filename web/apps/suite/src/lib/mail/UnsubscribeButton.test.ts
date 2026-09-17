/**
 * Component tests for UnsubscribeButton (REQ-UNS-01..43).
 *
 * Covers: absence when no message advertises a mechanism (REQ-UNS-03),
 * thread-scoped (not per-message) sourcing (REQ-UNS-11), the one-click
 * flow with no confirmation dialog (REQ-UNS-20/30) calling
 * `Email/unsubscribe` through the JMAP client (issue #412) and its
 * success/failure toasts (REQ-UNS-40/41) -- the failure toast offering
 * both the HTTPS link (REQ-UNS-21) and the mailto fallback (REQ-UNS-22)
 * -- capability-gated fallback to the plain-link behaviour, plain-https
 * (REQ-UNS-21), mailto (REQ-UNS-22), and the cleartext refusal
 * (REQ-UNS-04).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import UnsubscribeButton from './UnsubscribeButton.svelte';
import type { Email } from './types';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>) =>
    params ? `${key}:${JSON.stringify(params)}` : key,
}));

const {
  composeMock,
  toastMock,
  postOneClickMock,
  recordUnsubscribedMock,
  hasCapabilityMock,
  mailMock,
} = vi.hoisted(() => {
  const composeMock = { openWith: vi.fn() };
  const toastMock = { show: vi.fn() };
  const postOneClickMock = vi.fn();
  const recordUnsubscribedMock = vi.fn();
  const hasCapabilityMock = vi.fn(() => true);
  const mailMock = {
    emailAccountId: new Map<string, string>(),
    mailAccountId: 'acct-1',
  };
  return {
    composeMock,
    toastMock,
    postOneClickMock,
    recordUnsubscribedMock,
    hasCapabilityMock,
    mailMock,
  };
});

vi.mock('../compose/compose.svelte', () => ({ compose: composeMock }));
vi.mock('../toast/toast.svelte', () => ({ toast: toastMock }));
vi.mock('./unsubscribe', () => ({ postOneClickUnsubscribe: postOneClickMock }));
vi.mock('./unsubscribed-from', () => ({ recordUnsubscribed: recordUnsubscribedMock }));
vi.mock('../jmap/client', () => ({ jmap: { hasCapability: hasCapabilityMock } }));
vi.mock('./store.svelte', () => ({ mail: mailMock }));

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
  hasCapabilityMock.mockReset();
  hasCapabilityMock.mockReturnValue(true);
  mailMock.emailAccountId = new Map();
  mailMock.mailAccountId = 'acct-1';
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

describe('UnsubscribeButton: one-click (REQ-UNS-20/30/40/41), issue #412', () => {
  function oneClickEmail(overrides: Partial<Email> = {}): Email {
    return makeEmail({
      'header:List-Unsubscribe:asText': '<https://example.com/unsub?id=1>',
      'header:List-Unsubscribe-Post:asText': 'List-Unsubscribe=One-Click',
      ...overrides,
    });
  }

  it('calls Email/unsubscribe via the JMAP client with no confirmation dialog and toasts success', async () => {
    postOneClickMock.mockResolvedValue({ emailId: 'e1', status: 'ok', httpStatus: 200 });
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    expect(postOneClickMock).toHaveBeenCalledWith('acct-1', 'e1');
    // No dialog/confirm affordance of any kind appears.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(toastMock.show).toHaveBeenCalled());
    expect(recordUnsubscribedMock).toHaveBeenCalledWith('sender@list.example.com');
    expect(toastMock.show).toHaveBeenCalledWith(
      expect.objectContaining({ message: expect.stringContaining('unsubscribe.toast.success') }),
    );
  });

  it('uses the per-email account tag when the row was folded in from a sub-account view', async () => {
    postOneClickMock.mockResolvedValue({ emailId: 'e1', status: 'ok' });
    mailMock.emailAccountId = new Map([['e1', 'acct-sub']]);
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    await waitFor(() => expect(postOneClickMock).toHaveBeenCalledWith('acct-sub', 'e1'));
  });

  it('toasts failure with link + mailto fallback actions on status "failed"', async () => {
    postOneClickMock.mockResolvedValue({
      emailId: 'e1',
      status: 'failed',
      httpStatus: 500,
      error: 'upstream returned 500 Internal Server Error',
    });
    render(UnsubscribeButton, {
      props: {
        emails: [
          oneClickEmail({
            'header:List-Unsubscribe:asText':
              '<https://example.com/unsub?id=1>, <mailto:unsub@example.com>',
          }),
        ],
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    await waitFor(() =>
      expect(toastMock.show).toHaveBeenCalledWith(
        expect.objectContaining({
          message: 'unsubscribe.toast.failed',
          kind: 'error',
          detail: 'https://example.com/unsub?id=1',
          actionLabel: 'unsubscribe.toast.openLink',
          secondaryActionLabel: 'unsubscribe.toast.sendEmail',
        }),
      ),
    );
    expect(recordUnsubscribedMock).not.toHaveBeenCalled();

    // The HTTPS link action opens the URL in a new tab.
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    const spec = toastMock.show.mock.calls.at(-1)?.[0];
    spec.undo();
    expect(openSpy).toHaveBeenCalledWith(
      'https://example.com/unsub?id=1',
      '_blank',
      'noopener,noreferrer',
    );

    // The mailto fallback opens a prefilled compose window.
    spec.secondaryAction();
    expect(composeMock.openWith).toHaveBeenCalledWith(
      expect.objectContaining({ to: 'unsub@example.com' }),
    );
  });

  it('toasts failure on status "unsupported" too', async () => {
    postOneClickMock.mockResolvedValue({
      emailId: 'e1',
      status: 'unsupported',
      error: 'no HTTPS List-Unsubscribe URL',
    });
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    await waitFor(() =>
      expect(toastMock.show).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'unsubscribe.toast.failed', kind: 'error' }),
      ),
    );
  });

  it('toasts failure when the JMAP call throws', async () => {
    postOneClickMock.mockRejectedValue(new Error('network down'));
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    await waitFor(() =>
      expect(toastMock.show).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'unsubscribe.toast.failed', kind: 'error' }),
      ),
    );
  });

  it('falls back to opening the link directly when the server lacks the capability', async () => {
    hasCapabilityMock.mockReturnValue(false);
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(UnsubscribeButton, { props: { emails: [oneClickEmail()] } });
    await fireEvent.click(screen.getByRole('button'));
    expect(openSpy).toHaveBeenCalledWith(
      'https://example.com/unsub?id=1',
      '_blank',
      'noopener,noreferrer',
    );
    expect(postOneClickMock).not.toHaveBeenCalled();
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
    postOneClickMock.mockResolvedValue({ emailId: 'e1', status: 'ok' });
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
    expect(postOneClickMock).toHaveBeenCalledWith('acct-1', 'e1');
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
