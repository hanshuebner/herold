/**
 * Component tests for ListChip (REQ-LIST-10..12, 20..22).
 *
 * Covers: absence when no List-ID header, label rendering (REQ-LIST-02),
 * hover-reveals-popover (REQ-LIST-11), hide-action-when-header-absent
 * (REQ-LIST-20/21/22), and the click behaviours for each action kind.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ListChip from './ListChip.svelte';
import type { Email } from './types';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

const { composeMock, toastMock } = vi.hoisted(() => {
  const composeMock = {
    isOpen: false,
    inlineMode: false,
    openWith: vi.fn(),
    openReplyToList: vi.fn().mockResolvedValue(undefined),
  };
  const toastMock = { show: vi.fn() };
  return { composeMock, toastMock };
});

vi.mock('../compose/compose.svelte', () => ({ compose: composeMock }));
vi.mock('../toast/toast.svelte', () => ({ toast: toastMock }));

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
  composeMock.isOpen = false;
  composeMock.inlineMode = false;
  composeMock.openWith.mockClear();
  composeMock.openReplyToList.mockClear();
  toastMock.show.mockClear();
  vi.restoreAllMocks();
});

describe('ListChip: presence (REQ-LIST-12)', () => {
  it('renders nothing when the message has no List-ID header', () => {
    render(ListChip, { props: { email: makeEmail() } });
    expect(screen.queryByTestId('list-chip-anchor')).not.toBeInTheDocument();
  });

  it('renders the chip with the description-part label when List-ID is present', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '"Project X discuss" <projectx-discuss.example.com>',
        }),
      },
    });
    expect(screen.getByText('Project X discuss')).toBeInTheDocument();
  });

  it('falls back to the local part of the identifier when no description', () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<projectx-discuss.example.com>' }) },
    });
    expect(screen.getByText('projectx-discuss')).toBeInTheDocument();
  });
});

describe('ListChip: popover actions (REQ-LIST-11, 20..22)', () => {
  it('reveals the popover on hover', async () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('hides View archive / Get help / Reply to list when their headers are absent', async () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    expect(screen.queryByText('mailingList.action.viewArchive')).not.toBeInTheDocument();
    expect(screen.queryByText('mailingList.action.getHelp')).not.toBeInTheDocument();
    expect(screen.queryByText('mailingList.action.replyToList')).not.toBeInTheDocument();
    expect(screen.getByText('mailingList.noActions')).toBeInTheDocument();
  });

  it('shows only the actions whose headers are present', async () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<a.example.com>',
          'header:List-Archive:asText': '<https://example.com/archive>',
          'header:List-Post:asText': 'NO',
        }),
      },
    });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    expect(screen.getByText('mailingList.action.viewArchive')).toBeInTheDocument();
    expect(screen.queryByText('mailingList.action.getHelp')).not.toBeInTheDocument();
    // List-Post: NO means "no posting" (RFC 2369 SS3.4) -- hidden.
    expect(screen.queryByText('mailingList.action.replyToList')).not.toBeInTheDocument();
  });

  it('View archive opens the https URL in a new tab', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<a.example.com>',
          'header:List-Archive:asText': '<https://example.com/archive>',
        }),
      },
    });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    await fireEvent.click(screen.getByText('mailingList.action.viewArchive'));
    expect(openSpy).toHaveBeenCalledWith(
      'https://example.com/archive',
      '_blank',
      'noopener,noreferrer',
    );
  });

  it('a cleartext-only archive URL warns instead of opening (REQ-LIST-20 cleartext logic)', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<a.example.com>',
          'header:List-Archive:asText': '<http://example.com/archive>',
        }),
      },
    });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    await fireEvent.click(screen.getByText('mailingList.action.viewArchive'));
    expect(openSpy).not.toHaveBeenCalled();
    expect(toastMock.show).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'mailingList.cleartextWarning' }),
    );
  });

  it('Get help with a mailto: URL opens a prefilled compose, not a tab', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<a.example.com>',
          'header:List-Help:asText': '<mailto:help@example.com?subject=Help>',
        }),
      },
    });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    await fireEvent.click(screen.getByText('mailingList.action.getHelp'));
    expect(openSpy).not.toHaveBeenCalled();
    expect(composeMock.openWith).toHaveBeenCalledWith(
      expect.objectContaining({ to: 'help@example.com', subject: 'Help' }),
    );
  });

  it('Reply to list opens a reply targeting the List-Post address', async () => {
    const email = makeEmail({
      'header:List-ID:asText': '<a.example.com>',
      'header:List-Post:asText': '<mailto:list@example.com>',
    });
    render(ListChip, { props: { email } });
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    await fireEvent.click(screen.getByText('mailingList.action.replyToList'));
    expect(composeMock.openReplyToList).toHaveBeenCalledWith(email, 'list@example.com');
  });
});
