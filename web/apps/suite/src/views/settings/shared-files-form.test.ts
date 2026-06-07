/**
 * Tests for SharedFilesForm.svelte (REQ-ATT-70..73, REQ-ATT-70a, REQ-SHARE-04).
 *
 * Coverage:
 *   1. Renders share name and basic metadata.
 *   2. Active share without source: no back-reference line.
 *   3. Active share with sourceSubject and sourceRecipients: back-reference
 *      line renders "Shared in "<subject>" -- to <recipients>".
 *   4. Share with sourceSubject but empty recipients: shows subject, no
 *      recipient portion.
 *   5. Pending share (sourceSubject null): no back-reference line.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, waitFor } from '@testing-library/svelte';
import SharedFilesForm from './SharedFilesForm.svelte';
import type { FileShare } from '../../lib/jmap/file-shares';

// ── JMAP client mock ──────────────────────────────────────────────────────────

vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    batch: vi.fn(async () => ({
      responses: [],
      sessionState: 's1',
    })),
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct-1' },
      capabilities: {
        'urn:ietf:params:jmap:mail': {},
      },
    },
    hasCapability: vi.fn(() => true),
  },
  strict: vi.fn((r: unknown) => r),
}));

// ── Dialog mock ───────────────────────────────────────────────────────────────

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => false) },
}));

// ── Toast mock ────────────────────────────────────────────────────────────────

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

// ── file-shares module mock ───────────────────────────────────────────────────

const mockShares: FileShare[] = [];

vi.mock('../../lib/jmap/file-shares', async (importOriginal) => {
  const mod = await importOriginal<typeof import('../../lib/jmap/file-shares')>();
  return {
    ...mod,
    queryFileShares: vi.fn(async () => mockShares),
    fileShareCapability: vi.fn(() => null),
  };
});

// ── Helpers ───────────────────────────────────────────────────────────────────

function makeShare(overrides: Partial<FileShare> = {}): FileShare {
  return {
    id: 'share-1',
    blobId: 'blob-1',
    name: 'report.zip',
    type: 'application/zip',
    size: 5 * 1024 * 1024,
    url: 'https://example.com/share/abc123',
    state: 'active',
    createdAt: new Date(Date.now() - 86400 * 1000).toISOString(),
    expiresAt: new Date(Date.now() + 30 * 86400 * 1000).toISOString(),
    maxDownloads: null,
    downloadCount: 0,
    hasPassword: false,
    lastDownloadedAt: null,
    sourceMessageId: null,
    sourceSubject: null,
    sourceRecipients: [],
    ...overrides,
  };
}

beforeEach(() => {
  mockShares.length = 0;
});

describe('SharedFilesForm', () => {
  it('renders the share name', async () => {
    mockShares.push(makeShare({ name: 'my-file.pdf' }));
    const { findByText } = render(SharedFilesForm);
    const name = await findByText('my-file.pdf');
    expect(name).toBeInTheDocument();
  });

  it('does not render a back-reference line when sourceSubject is null (active share without source)', async () => {
    mockShares.push(
      makeShare({
        state: 'active',
        sourceMessageId: null,
        sourceSubject: null,
        sourceRecipients: [],
      }),
    );
    const { container, findByText } = render(SharedFilesForm);
    await findByText('report.zip'); // wait for load
    const shareSource = container.querySelector('.share-source');
    expect(shareSource).not.toBeInTheDocument();
  });

  it('does not render a back-reference line for a pending share (sourceSubject null)', async () => {
    mockShares.push(
      makeShare({
        state: 'pending',
        sourceMessageId: null,
        sourceSubject: null,
        sourceRecipients: [],
      }),
    );
    const { container, findByText } = render(SharedFilesForm);
    await findByText('report.zip');
    const shareSource = container.querySelector('.share-source');
    expect(shareSource).not.toBeInTheDocument();
  });

  it('renders the back-reference line with subject and recipients when source is present (REQ-ATT-70a)', async () => {
    mockShares.push(
      makeShare({
        state: 'active',
        sourceMessageId: 'email-abc',
        sourceSubject: 'Q3 Report',
        sourceRecipients: ['alice@example.com', 'bob@example.com'],
      }),
    );
    const { findByText, container } = render(SharedFilesForm);
    await findByText('report.zip');
    const shareSource = container.querySelector('.share-source');
    expect(shareSource).toBeInTheDocument();
    expect(shareSource?.textContent).toContain('Shared in');
    expect(shareSource?.textContent).toContain('"Q3 Report"');
    expect(shareSource?.textContent).toContain('alice@example.com, bob@example.com');
  });

  it('renders the subject but omits recipients when sourceRecipients is empty', async () => {
    mockShares.push(
      makeShare({
        state: 'active',
        sourceMessageId: 'email-xyz',
        sourceSubject: 'Solo message',
        sourceRecipients: [],
      }),
    );
    const { findByText, container } = render(SharedFilesForm);
    await findByText('report.zip');
    const shareSource = container.querySelector('.share-source');
    expect(shareSource).toBeInTheDocument();
    expect(shareSource?.textContent).toContain('"Solo message"');
    // No " -- to " portion when recipients is empty.
    expect(shareSource?.textContent).not.toContain(' — to ');
  });

  it('renders the subject but omits recipients when sourceRecipients is undefined (omitted by server)', async () => {
    mockShares.push(
      makeShare({
        state: 'active',
        sourceMessageId: 'email-uvw',
        sourceSubject: 'Old share',
        sourceRecipients: undefined,
      }),
    );
    const { findByText, container } = render(SharedFilesForm);
    await findByText('report.zip');
    const shareSource = container.querySelector('.share-source');
    expect(shareSource).toBeInTheDocument();
    expect(shareSource?.textContent).toContain('"Old share"');
    expect(shareSource?.textContent).not.toContain(' — to ');
  });
});
