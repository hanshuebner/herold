/**
 * Component tests for ListChip (REQ-LIST-02, 10..12, 20..22).
 *
 * Covers: absence when no List-ID header, derived/generic label
 * rendering for a description-less List-ID with the raw token confined
 * to the popover (REQ-LIST-02, issue #415), click/keyboard popover
 * activation and focusability (issue #415), hide-action-when-header-
 * absent (REQ-LIST-20/21/22), and the click behaviours for each action
 * kind.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import ListChip from './ListChip.svelte';
import type { Email } from './types';

// Every key renders as itself (as before); a call site passing `params`
// (only `mailingList.rawId` does) appends them so tests can assert the
// interpolated value (e.g. the raw List-ID token) reached the DOM.
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>) =>
    params ? `${key}:${Object.values(params).join(',')}` : key,
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

  it('renders the chip with the description-part label when List-ID is present (described case is unchanged)', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '"Project X discuss" <projectx-discuss.example.com>',
        }),
      },
    });
    expect(screen.getByText('Project X discuss')).toBeInTheDocument();
  });
});

describe('ListChip: description-less label derivation (REQ-LIST-02, issue #415)', () => {
  it('never renders the raw List-ID token as the chip label', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<3IYSMFU7-4UI13WR.newsletterversand.example>',
        }),
      },
    });
    expect(
      screen.queryByText('3IYSMFU7-4UI13WR.newsletterversand.example'),
    ).not.toBeInTheDocument();
    expect(screen.queryByText('3IYSMFU7-4UI13WR')).not.toBeInTheDocument();
  });

  it('derives the label from the sender display name when present', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          from: [{ name: 'DIE ZEIT Newsletter', email: 'noreply@newsletterversand.example' }],
          'header:List-ID:asText': '<3IYSMFU7-4UI13WR.newsletterversand.example>',
        }),
      },
    });
    expect(screen.getByText('DIE ZEIT Newsletter')).toBeInTheDocument();
  });

  it('falls back to the sender email domain when there is no display name', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          from: [{ name: null, email: 'noreply@newsletterversand.example' }],
          'header:List-ID:asText': '<3IYSMFU7-4UI13WR.newsletterversand.example>',
        }),
      },
    });
    expect(screen.getByText('newsletterversand.example')).toBeInTheDocument();
  });

  it('falls back to the generic translated label when there is no usable sender info', () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          from: null,
          'header:List-ID:asText': '<3IYSMFU7-4UI13WR.newsletterversand.example>',
        }),
      },
    });
    expect(screen.getByText('mailingList.genericLabel')).toBeInTheDocument();
  });

  it('shows the raw List-ID identifier only inside the popover, not as the chip label', async () => {
    render(ListChip, {
      props: {
        email: makeEmail({
          from: null,
          'header:List-ID:asText': '<3IYSMFU7-4UI13WR.newsletterversand.example>',
        }),
      },
    });
    expect(screen.queryByTestId('list-chip-raw-id')).not.toBeInTheDocument();
    await fireEvent.mouseEnter(screen.getByTestId('list-chip-anchor'));
    expect(screen.getByTestId('list-chip-raw-id')).toHaveTextContent(
      '3IYSMFU7-4UI13WR.newsletterversand.example',
    );
  });
});

describe('ListChip: affordance and activation (issue #415)', () => {
  it('the chip is a focusable native button', () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    const button = screen.getByRole('button');
    button.focus();
    expect(button).toHaveFocus();
  });

  it('opens the popover on click, without requiring hover first', async () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button'));
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('opens the popover on Enter key activation, without requiring hover or click', async () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    const button = screen.getByRole('button');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await fireEvent.keyDown(button, { key: 'Enter' });
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('opens the popover on Space key activation', async () => {
    render(ListChip, {
      props: { email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }) },
    });
    const button = screen.getByRole('button');
    await fireEvent.keyDown(button, { key: ' ' });
    expect(screen.getByRole('menu')).toBeInTheDocument();
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

describe('ListChip: popover escapes a clipping ancestor (issue #457)', () => {
  // Mirrors MessageAccordion's `.from` sender row the chip actually
  // renders inside: one line tall with `overflow: hidden` so the chip's
  // own label can ellipsis (issue #415). A popover that opens below this
  // row and stays a normal descendant of it is clipped away entirely --
  // mounting the chip in isolation (as every other test in this file
  // does) never exercises that clip, which is why those tests kept
  // passing while the chip was unusable in the real thread view.
  let clippingRow: HTMLDivElement;

  beforeEach(() => {
    clippingRow = document.createElement('div');
    clippingRow.className = 'from';
    clippingRow.style.overflow = 'hidden';
    clippingRow.style.display = 'flex';
    clippingRow.style.height = '20px';
    clippingRow.style.lineHeight = '20px';
    document.body.appendChild(clippingRow);
  });

  afterEach(() => {
    clippingRow.remove();
  });

  it('renders the open popover outside the clipping row rather than as its descendant', async () => {
    render(ListChip, {
      target: clippingRow,
      props: {
        email: makeEmail({
          'header:List-ID:asText': '<a.example.com>',
          'header:List-Archive:asText': '<https://example.com/archive>',
        }),
      },
    });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    await fireEvent.click(screen.getByRole('button'));
    const menu = screen.getByRole('menu');
    // The defect: a popover that stays inside `clippingRow` is clipped
    // by its `overflow: hidden` and never seen, even though it is in
    // the DOM and `aria-expanded` is true.
    expect(clippingRow.contains(menu)).toBe(false);
    expect(document.body.contains(menu)).toBe(true);
    expect(screen.getByText('mailingList.action.viewArchive')).toBeInTheDocument();
  });

  it('still shows the identifier and no-actions note when escaped from the clipping row', async () => {
    render(ListChip, {
      target: clippingRow,
      props: {
        email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }),
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    const menu = screen.getByRole('menu');
    expect(clippingRow.contains(menu)).toBe(false);
    expect(screen.getByText('mailingList.noActions')).toBeInTheDocument();
    expect(screen.getByTestId('list-chip-raw-id')).toHaveTextContent('a.example.com');
  });

  it('removes the portalled popover from the document when the component unmounts', async () => {
    const { unmount } = render(ListChip, {
      target: clippingRow,
      props: {
        email: makeEmail({ 'header:List-ID:asText': '<a.example.com>' }),
      },
    });
    await fireEvent.click(screen.getByRole('button'));
    expect(screen.getByRole('menu')).toBeInTheDocument();
    unmount();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });
});
