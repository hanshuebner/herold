/**
 * Tests for the two-phase inline image upload flow (issue #74).
 *
 * startInlineImage (synchronous) creates the attachment record and blob URL
 * immediately so the caller can insert a thumbnail in the editor before the
 * JMAP round-trip begins.
 *
 * uploadInlineImage (async) completes the upload and returns null on success
 * or an error message on failure. On failure the caller is expected to remove
 * the placeholder image node from the ProseMirror document.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { compose } from './compose.svelte';

// Stub the JMAP client so uploadBlob never hits the network.
// NOTE: vi.mock is hoisted — the factory must not reference outer let/const.
// We use vi.fn() directly in the mock, then look up the mock from jmap after import.
vi.mock('../jmap/client', () => ({
  jmap: {
    maxUploadSize: null,
    uploadBlob: vi.fn(),
    downloadUrl: vi.fn().mockReturnValue(null),
  },
  strict: vi.fn(),
}));

// Stub mail store so compose paths that need an accountId work.
vi.mock('../mail/store.svelte', () => ({
  mail: {
    mailAccountId: 'acct1',
    primaryIdentity: null,
    drafts: null,
    identities: new Map(),
    mailboxes: new Map(),
    loadMailboxes: vi.fn(),
    loadIdentities: vi.fn(),
  },
}));

vi.mock('../settings/settings.svelte', () => ({
  settings: { undoWindowSec: 0 },
}));

vi.mock('../toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

vi.mock('../i18n/i18n.svelte', () => ({
  localeTag: () => 'en',
}));

// Provide a minimal URL.createObjectURL / revokeObjectURL stub so the
// happy-dom environment does not throw when called.
const blobUrlCounter = { n: 0 };
if (typeof globalThis.URL.createObjectURL !== 'function') {
  globalThis.URL.createObjectURL = () => `blob:test/${++blobUrlCounter.n}`;
}
if (typeof globalThis.URL.revokeObjectURL !== 'function') {
  globalThis.URL.revokeObjectURL = () => undefined;
}

function makeImageFile(name = 'photo.png'): File {
  return new File(['img-bytes'], name, { type: 'image/png' });
}

// Retrieve the mocked uploadBlob function after module resolution.
async function getUploadBlob() {
  const { jmap } = await import('../jmap/client');
  return jmap.uploadBlob as ReturnType<typeof vi.fn>;
}

describe('startInlineImage', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
  });

  it('returns a key, cid, and objectURL synchronously', () => {
    const file = makeImageFile();
    const result = compose.startInlineImage(file);

    expect(result).not.toBeNull();
    expect(result?.key).toMatch(/^att-/);
    expect(result?.cid).toBeTruthy();
    expect(result?.objectURL).toMatch(/^blob:/);
  });

  it('adds an uploading attachment record immediately', () => {
    const file = makeImageFile();
    compose.startInlineImage(file);

    expect(compose.attachments).toHaveLength(1);
    const att = compose.attachments[0]!;
    expect(att.status).toBe('uploading');
    expect(att.inline).toBe(true);
    expect(att.blobId).toBeNull();
    expect(att.objectURL).toMatch(/^blob:/);
    expect(att.cid).toBeTruthy();
  });

  it('returns null when no mail account is available', async () => {
    const { jmap } = await import('../jmap/client');
    const { mail } = await import('../mail/store.svelte');
    const origAccountId = (mail as { mailAccountId: string | null }).mailAccountId;
    // Temporarily clear the account id.
    (mail as { mailAccountId: string | null }).mailAccountId = null;

    const result = compose.startInlineImage(makeImageFile());
    expect(result).toBeNull();
    expect(compose.attachments).toHaveLength(0);

    (mail as { mailAccountId: string | null }).mailAccountId = origAccountId;
    void jmap;
  });

  it('returns null when file exceeds maxUploadSize', async () => {
    const { jmap } = await import('../jmap/client');
    (jmap as { maxUploadSize: number | null }).maxUploadSize = 10;

    const bigFile = new File([new Uint8Array(100)], 'big.png', { type: 'image/png' });
    const result = compose.startInlineImage(bigFile);
    expect(result).toBeNull();
    expect(compose.attachments).toHaveLength(0);

    (jmap as { maxUploadSize: number | null }).maxUploadSize = null;
  });
});

describe('uploadInlineImage', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
  });

  it('patches attachment to ready status on success and returns null', async () => {
    const uploadBlob = await getUploadBlob();
    const file = makeImageFile();
    uploadBlob.mockResolvedValueOnce({ blobId: 'blob-server-1' });

    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const key = started!.key;

    const errMsg = await compose.uploadInlineImage(key, file);

    expect(errMsg).toBeNull();
    const att = compose.attachments.find((a) => a.key === key);
    expect(att?.status).toBe('ready');
    expect(att?.blobId).toBe('blob-server-1');
    expect(att?.error).toBeNull();
  });

  it('patches attachment to failed status on upload error and returns error message', async () => {
    const uploadBlob = await getUploadBlob();
    const file = makeImageFile();
    uploadBlob.mockRejectedValueOnce(new Error('network timeout'));

    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const key = started!.key;

    const errMsg = await compose.uploadInlineImage(key, file);

    expect(errMsg).toBe('network timeout');
    const att = compose.attachments.find((a) => a.key === key);
    expect(att?.status).toBe('failed');
    expect(att?.error).toBe('network timeout');
  });

  it('returns an error message when the key is not found', async () => {
    const file = makeImageFile();
    const errMsg = await compose.uploadInlineImage('nonexistent-key', file);
    expect(errMsg).toBeTruthy();
  });
});

describe('two-phase upload lifecycle', () => {
  beforeEach(() => {
    compose.attachments = [];
    vi.clearAllMocks();
  });

  it('attachment is uploading during the upload and ready after', async () => {
    const uploadBlob = await getUploadBlob();
    const file = makeImageFile();
    let resolveUpload!: (v: { blobId: string }) => void;
    uploadBlob.mockImplementationOnce(
      () => new Promise((res) => { resolveUpload = res; }),
    );

    const started = compose.startInlineImage(file);
    expect(started).not.toBeNull();
    const key = started!.key;

    // At this point the attachment is still uploading (upload not yet resolved).
    expect(compose.attachments.find((a) => a.key === key)?.status).toBe('uploading');

    // Complete the upload.
    const uploadPromise = compose.uploadInlineImage(key, file);
    resolveUpload({ blobId: 'blob-done' });
    const err = await uploadPromise;

    expect(err).toBeNull();
    expect(compose.attachments.find((a) => a.key === key)?.status).toBe('ready');
  });
});
