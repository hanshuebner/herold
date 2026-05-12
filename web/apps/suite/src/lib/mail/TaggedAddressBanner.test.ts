/**
 * Component test for the per-message tagged-address banner (REQ-MAIL-12c).
 *
 * Renders the banner against fully-mocked singletons (mail.identities,
 * taggedAddressStore, labelDialog, toast) and asserts:
 *   - The banner is suppressed when X-Herold-Recipient is absent / has
 *     no suffix / matches an existing filter / matches an existing
 *     dismissal / the capability is not advertised.
 *   - All four actions are reachable.
 *   - The Dismiss button triggers taggedAddressStore.dismiss with the
 *     resolved (baseIdentityId, suffix) pair.
 *   - Each label-creating action opens the labelDialog and (on confirm)
 *     calls taggedAddressStore.createFilter with the right action.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import TaggedAddressBanner from './TaggedAddressBanner.svelte';
import type { Email, Identity } from './types';

// ── hoisted mocks ────────────────────────────────────────────────────────────

const {
  identityById,
  identitiesMap,
  storeMock,
  labelDialogMock,
  toastMock,
} = vi.hoisted(() => {
  const identityById = new Map<string, Identity>();
  const identitiesMap = {
    values: () => identityById.values(),
  };
  const storeMock = {
    capabilityAdvertised: true,
    filterRecords: [] as Array<{ id: string; baseIdentityId: string; suffix: string }>,
    dismissalRecords: [] as Array<{ baseIdentityId: string; suffix: string }>,
    load: vi.fn().mockResolvedValue(undefined),
    createFilter: vi.fn().mockResolvedValue({
      id: 'f-new',
      baseIdentityId: 'id-1',
      suffix: 'amazon',
      action: 'label',
      labelName: 'Amazon',
      createdAt: '2026-05-12T00:00:00Z',
      updatedAt: '2026-05-12T00:00:00Z',
    }),
    dismiss: vi.fn().mockResolvedValue(undefined),
  };
  const labelDialogMock = {
    open: vi.fn().mockResolvedValue({ name: 'Amazon', color: '#5d6d7e' }),
  };
  const toastMock = {
    show: vi.fn(),
  };
  return { identityById, identitiesMap, storeMock, labelDialogMock, toastMock };
});

vi.mock('./store.svelte', () => ({
  mail: {
    get identities() {
      return identitiesMap;
    },
  },
}));
vi.mock('./tagged-address-store.svelte', () => ({
  taggedAddressStore: storeMock,
}));
vi.mock('../dialog/label-dialog.svelte', () => ({
  labelDialog: labelDialogMock,
}));
vi.mock('../toast/toast.svelte', () => ({
  toast: toastMock,
}));
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string, params?: Record<string, string | number>) =>
    params ? `${key}|${JSON.stringify(params)}` : key,
  localeTag: () => 'en',
}));

// ── helpers ──────────────────────────────────────────────────────────────────

function makeIdentity(id: string, email: string): Identity {
  return {
    id,
    name: email,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

function makeEmail(headerValue: string | null): Email {
  return {
    id: 'e-1',
    threadId: 't-1',
    mailboxIds: {},
    keywords: {},
    from: [{ name: 'Sender', email: 'sender@external.example' }],
    to: null,
    subject: 'Hello',
    preview: '',
    receivedAt: '2026-05-12T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-1',
    'header:X-Herold-Recipient:asText': headerValue,
  } as Email;
}

function resetMocks(): void {
  identityById.clear();
  identityById.set('id-1', makeIdentity('id-1', 'alice@example.local'));
  storeMock.capabilityAdvertised = true;
  storeMock.filterRecords = [];
  storeMock.dismissalRecords = [];
  storeMock.load.mockClear();
  storeMock.createFilter.mockClear();
  storeMock.dismiss.mockClear();
  labelDialogMock.open.mockClear();
  labelDialogMock.open.mockResolvedValue({ name: 'Amazon', color: '#5d6d7e' });
  toastMock.show.mockClear();
}

// ── tests ────────────────────────────────────────────────────────────────────

describe('TaggedAddressBanner', () => {
  beforeEach(() => {
    resetMocks();
  });

  it('renders the banner when the header has a fresh +suffix and the gates pass', () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    expect(screen.queryByTestId('tagged-address-banner')).toBeInTheDocument();
    expect(screen.queryByTestId('tagged-banner-label')).toBeInTheDocument();
    expect(screen.queryByTestId('tagged-banner-label-archive')).toBeInTheDocument();
    expect(screen.queryByTestId('tagged-banner-label-archive-read')).toBeInTheDocument();
    expect(screen.queryByTestId('tagged-banner-dismiss')).toBeInTheDocument();
  });

  it('hides the banner when X-Herold-Recipient is absent (REQ-FLOW-35: outbound mail)', () => {
    render(TaggedAddressBanner, { props: { email: makeEmail(null) } });
    expect(screen.queryByTestId('tagged-address-banner')).not.toBeInTheDocument();
  });

  it('hides the banner when X-Herold-Recipient carries no +suffix', () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice@example.local') },
    });
    expect(screen.queryByTestId('tagged-address-banner')).not.toBeInTheDocument();
  });

  it('hides the banner when an existing filter matches (REQ-MAIL-12c gate c)', () => {
    storeMock.filterRecords = [
      { id: 'f-1', baseIdentityId: 'id-1', suffix: 'amazon' },
    ];
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    expect(screen.queryByTestId('tagged-address-banner')).not.toBeInTheDocument();
  });

  it('hides the banner when a dismissal matches (REQ-MAIL-12c gate c)', () => {
    storeMock.dismissalRecords = [{ baseIdentityId: 'id-1', suffix: 'amazon' }];
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    expect(screen.queryByTestId('tagged-address-banner')).not.toBeInTheDocument();
  });

  it('hides the banner when the capability is not advertised (REQ-MAIL-12c gate b)', () => {
    storeMock.capabilityAdvertised = false;
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    expect(screen.queryByTestId('tagged-address-banner')).not.toBeInTheDocument();
  });

  it('calls taggedAddressStore.dismiss with the resolved pair on Dismiss', async () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    await fireEvent.click(screen.getByTestId('tagged-banner-dismiss'));
    // Allow the async handler to resolve.
    await Promise.resolve();
    expect(storeMock.dismiss).toHaveBeenCalledWith('id-1', 'amazon');
    expect(toastMock.show).toHaveBeenCalled();
  });

  it('opens the label dialog and creates a filter with action=label on the first button', async () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    await fireEvent.click(screen.getByTestId('tagged-banner-label'));
    // The label dialog opens with the suffix capitalised as the default name.
    expect(labelDialogMock.open).toHaveBeenCalledTimes(1);
    const openArgs = labelDialogMock.open.mock.calls[0]![0]!;
    expect(openArgs.defaultName).toBe('Amazon');
    // Let the dialog promise resolve.
    await Promise.resolve();
    await Promise.resolve();
    expect(storeMock.createFilter).toHaveBeenCalledWith({
      baseIdentityId: 'id-1',
      suffix: 'amazon',
      action: 'label',
      labelName: 'Amazon',
    });
  });

  it('passes action=label_archive to the store on the second button', async () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    await fireEvent.click(screen.getByTestId('tagged-banner-label-archive'));
    await Promise.resolve();
    await Promise.resolve();
    expect(storeMock.createFilter).toHaveBeenCalledWith(
      expect.objectContaining({ action: 'label_archive' }),
    );
  });

  it('passes action=label_archive_read to the store on the third button', async () => {
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    await fireEvent.click(screen.getByTestId('tagged-banner-label-archive-read'));
    await Promise.resolve();
    await Promise.resolve();
    expect(storeMock.createFilter).toHaveBeenCalledWith(
      expect.objectContaining({ action: 'label_archive_read' }),
    );
  });

  it('does not call createFilter when the user cancels the label dialog', async () => {
    labelDialogMock.open.mockResolvedValueOnce(null);
    render(TaggedAddressBanner, {
      props: { email: makeEmail('alice+amazon@example.local') },
    });
    await fireEvent.click(screen.getByTestId('tagged-banner-label'));
    await Promise.resolve();
    await Promise.resolve();
    expect(storeMock.createFilter).not.toHaveBeenCalled();
  });
});
