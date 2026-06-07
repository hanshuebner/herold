/**
 * SharedFilesForm.svelte: deep-link tests (REQ-ATT-70a).
 *
 * Covers:
 *   1. Subject rendered as a clickable button when sourceMessageId is present.
 *   2. Clicking resolves Email id -> threadId via JMAP Email/get and calls
 *      router.navigate with the resolved threadId.
 *   3. When Email/get returns an empty list (message deleted), shows a toast
 *      and does NOT navigate.
 *   4. Subject rendered as plain text (not a button) when sourceMessageId
 *      is absent.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';

// ── Mocks ─────────────────────────────────────────────────────────────────────

const mockNavigate = vi.fn();

vi.mock('../../lib/router/router.svelte', () => ({
  router: {
    navigate: (...args: unknown[]) => mockNavigate(...args),
    current: '/settings',
    parts: ['settings'],
  },
}));

const mockBatch = vi.fn();

vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    batch: (...args: unknown[]) => mockBatch(...args),
    hasCapability: vi.fn(() => false),
    session: null,
    maxUploadSize: null,
  },
  strict: vi.fn(),
  setJmapOnUnauthenticated: vi.fn(),
}));

vi.mock('../../lib/jmap/types', () => ({
  Capability: {
    Mail: 'urn:ietf:params:jmap:mail',
    HeroldFileShares: 'https://herold.dev/jmap/file-shares',
  },
}));

const mockToastShow = vi.fn();

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: {
    show: (...args: unknown[]) => mockToastShow(...args),
    dismiss: vi.fn(),
    current: null,
  },
}));

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: {
    ask: vi.fn(async () => true),
    pending: null,
    decide: vi.fn(),
  },
}));

const mockQueryFileShares = vi.fn();

vi.mock('../../lib/jmap/file-shares', () => ({
  queryFileShares: (...args: unknown[]) => mockQueryFileShares(...args),
  updateFileShare: vi.fn(async () => undefined),
  fileShareCapability: vi.fn(() => null),
  hasFileShares: vi.fn(() => false),
}));

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct-1',
    identities: new Map(),
    primaryIdentity: null,
    loadThread: vi.fn().mockResolvedValue(undefined),
  },
}));

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeShare(overrides: Partial<{
  id: string;
  sourceMessageId: string | null;
  sourceSubject: string | null;
  sourceRecipients: string[];
}> = {}) {
  return {
    id: 'share-1',
    blobId: 'blob-1',
    name: 'report.pdf',
    type: 'application/pdf',
    size: 1024 * 100,
    url: 'https://example.com/share/abc',
    state: 'active' as const,
    createdAt: new Date().toISOString(),
    expiresAt: new Date(Date.now() + 86400_000 * 7).toISOString(),
    maxDownloads: null,
    downloadCount: 0,
    hasPassword: false,
    lastDownloadedAt: null,
    sourceMessageId: null,
    sourceSubject: null,
    sourceRecipients: [] as string[],
    ...overrides,
  };
}

// ── Tests ─────────────────────────────────────────────────────────────────────

import SharedFilesForm from './SharedFilesForm.svelte';

describe('SharedFilesForm: subject deep-link (REQ-ATT-70a)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders subject as a button when sourceMessageId is present', async () => {
    const share = makeShare({
      sourceMessageId: 'email-123',
      sourceSubject: 'Meeting agenda',
    });
    mockQueryFileShares.mockResolvedValue([share]);

    const { findByRole } = render(SharedFilesForm);

    // The button's accessible name includes the subject text.
    const btn = await findByRole('button', { name: /Meeting agenda/ });
    expect(btn).toBeInTheDocument();
  });

  it('navigates to the resolved threadId on click', async () => {
    const share = makeShare({
      sourceMessageId: 'email-123',
      sourceSubject: 'Meeting agenda',
    });
    mockQueryFileShares.mockResolvedValue([share]);

    // Email/get returns a matching email with a threadId.
    mockBatch.mockImplementation(
      async (configure: (b: { call: (...args: unknown[]) => void }) => void) => {
        configure({ call: () => undefined });
        return {
          responses: [
            [
              'Email/get',
              { list: [{ id: 'email-123', threadId: 'thread-abc' }] },
              'c1',
            ],
          ],
        };
      },
    );

    const { findByRole } = render(SharedFilesForm);
    const btn = await findByRole('button', { name: /Meeting agenda/ });
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(mockBatch).toHaveBeenCalled();
      expect(mockNavigate).toHaveBeenCalledWith(
        '/mail/thread/' + encodeURIComponent('thread-abc'),
      );
    });
  });

  it('shows a toast and does NOT navigate when the message has been deleted', async () => {
    const share = makeShare({
      sourceMessageId: 'email-deleted',
      sourceSubject: 'Old message',
    });
    mockQueryFileShares.mockResolvedValue([share]);

    // Email/get returns an empty list (message deleted).
    mockBatch.mockImplementation(
      async (configure: (b: { call: (...args: unknown[]) => void }) => void) => {
        configure({ call: () => undefined });
        return {
          responses: [['Email/get', { list: [] }, 'c1']],
        };
      },
    );

    const { findByRole } = render(SharedFilesForm);
    const btn = await findByRole('button', { name: /Old message/ });
    await fireEvent.click(btn);

    await waitFor(() => {
      expect(mockToastShow).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'That message no longer exists' }),
      );
      expect(mockNavigate).not.toHaveBeenCalled();
    });
  });

  it('renders subject as plain text (not a button) when sourceMessageId is absent', async () => {
    const share = makeShare({
      sourceMessageId: null,
      sourceSubject: 'Static subject',
    });
    mockQueryFileShares.mockResolvedValue([share]);

    const { findByText, queryByRole } = render(SharedFilesForm);

    // The subject text must be visible.
    const text = await findByText(/Static subject/, { exact: false });
    expect(text).toBeInTheDocument();

    // There must be no button with that label.
    const btn = queryByRole('button', { name: /Static subject/ });
    expect(btn).toBeNull();
  });

  it('renders (no subject) fallback when sourceSubject is null', async () => {
    const share = makeShare({
      sourceMessageId: 'email-456',
      sourceSubject: null,
    });
    mockQueryFileShares.mockResolvedValue([share]);

    const { findByRole } = render(SharedFilesForm);

    const btn = await findByRole('button', { name: /no subject/ });
    expect(btn).toBeInTheDocument();
  });
});
