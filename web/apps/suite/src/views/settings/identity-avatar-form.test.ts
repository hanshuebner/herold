/**
 * IdentityAvatarForm tests (REQ-SET-03b).
 *
 * Covers:
 *   - First upload with no other identity having an avatar AND > 1 identity
 *     triggers the apply-to-all prompt; confirm sets all; cancel sets only current.
 *   - Upload when at least one other identity has an avatar does NOT trigger
 *     apply-to-all.
 *   - Picker tile-click on an existing avatar reuses that blobId without
 *     re-uploading.
 *   - Picker deduplication: two identities with the same blobId render as one tile.
 *   - First upload with xFaceEnabled=false triggers X-Face prompt.
 *   - Subsequent upload (xFaceEnabled already true) does NOT re-prompt.
 *   - Remove clears the current identity's avatarBlobId to null.
 *   - File-size guard: a 5 MB picture is downscaled to <= 1 MB before upload.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mocks ──────────────────────────────────────────────────────────────────

// Two test identities; start with neither having an avatar.
const IDENTITY_A = {
  id: 'id-a',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  avatarBlobId: null as string | null | undefined,
  xFaceEnabled: false,
};
const IDENTITY_B = {
  id: 'id-b',
  name: 'Bob',
  email: 'bob@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  avatarBlobId: null as string | null | undefined,
  xFaceEnabled: false,
};

// Use a reactive Map so tests can mutate identity state between assertions.
const identitiesMap = new Map([
  [IDENTITY_A.id, { ...IDENTITY_A }],
  [IDENTITY_B.id, { ...IDENTITY_B }],
]);

vi.mock('../../lib/mail/store.svelte', () => ({
  mail: {
    get identities() {
      return identitiesMap;
    },
    mailAccountId: 'acct1',
    updateIdentityAvatar: vi.fn(async () => undefined),
    updateIdentityXFaceEnabled: vi.fn(async () => undefined),
  },
}));

vi.mock('../../lib/jmap/client', () => ({
  jmap: {
    uploadBlob: vi.fn(async () => ({ blobId: 'blob-new', type: 'image/jpeg', size: 50_000, accountId: 'acct1' })),
    downloadUrl: vi.fn(({ blobId }: { blobId: string }) => `/jmap/dl/${blobId}`),
  },
}));

vi.mock('../../lib/auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'acct1' },
    },
  },
}));

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn() },
}));

// confirm.ask resolves to the value pushed into confirmAnswers.
const confirmAnswers: boolean[] = [];
vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: {
    ask: vi.fn(async () => {
      const answer = confirmAnswers.shift();
      return answer ?? false;
    }),
  },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>): string => {
    if (args) {
      return `${key}:${JSON.stringify(args)}`;
    }
    return key;
  },
}));

vi.mock('../../lib/mail/identity-avatar', () => ({
  identityAvatarUrl: (identity: { avatarBlobId?: string | null }) =>
    identity.avatarBlobId ? `/jmap/dl/${identity.avatarBlobId}` : null,
}));

// ── Canvas / Blob stub for resize path ───────────────────────────────────

// happy-dom does not implement canvas; provide a minimal stub.
const MOCK_BLOB_SIZE = 800_000; // 800 KB — under the 1 MB limit

vi.stubGlobal('createImageBitmap', async () => ({
  width: 800,
  height: 600,
  close: vi.fn(),
}));

const toBlob = vi.fn((cb: (b: Blob | null) => void) => {
  const blob = new Blob(['x'.repeat(MOCK_BLOB_SIZE)], { type: 'image/jpeg' });
  cb(blob);
});

vi.stubGlobal('HTMLCanvasElement', class {
  width = 0;
  height = 0;
  getContext() {
    return { drawImage: vi.fn() };
  }
  toBlob = toBlob;
});

// Stub document.createElement so canvas uses the stub class above.
const _originalCreateElement = document.createElement.bind(document);
vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
  if (tag === 'canvas') {
    const canvas = new (vi.mocked(HTMLCanvasElement as unknown as typeof HTMLCanvasElement))();
    return canvas as unknown as HTMLElement;
  }
  return _originalCreateElement(tag);
});

// ── Imported mocks ──────────────────────────────────────────────────────

const { mail } = await import('../../lib/mail/store.svelte');
const { jmap } = await import('../../lib/jmap/client');
const { confirm } = await import('../../lib/dialog/confirm.svelte');
const { toast } = await import('../../lib/toast/toast.svelte');

import IdentityAvatarForm from './IdentityAvatarForm.svelte';

// ── Helpers ────────────────────────────────────────────────────────────────

function resetIdentities(aAvatar?: string | null, bAvatar?: string | null, aXFace = false) {
  identitiesMap.set(IDENTITY_A.id, { ...IDENTITY_A, avatarBlobId: aAvatar ?? null, xFaceEnabled: aXFace });
  identitiesMap.set(IDENTITY_B.id, { ...IDENTITY_B, avatarBlobId: bAvatar ?? null });
}

function makeIdentity(overrides?: Partial<typeof IDENTITY_A>) {
  return { ...IDENTITY_A, ...overrides };
}

function makeFile(sizeBytes = 500_000): File {
  const data = 'x'.repeat(sizeBytes);
  return new File([data], 'photo.jpg', { type: 'image/jpeg' });
}

// ── Tests ───────────────────────────────────────────────────────────────────

describe('IdentityAvatarForm: apply-to-all prompt', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    confirmAnswers.length = 0;
    resetIdentities();
  });

  it('triggers apply-to-all when no other identity has an avatar and > 1 identities', async () => {
    confirmAnswers.push(true, false); // apply-to-all: confirm; xface: skip
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: undefined });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    // Simulate file selection via the hidden input.
    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = makeFile();
    Object.defineProperty(fileInput, 'files', { value: [file], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(confirm.ask)).toHaveBeenCalledWith(
        expect.objectContaining({ title: 'settings.avatar.applyToAll.title' }),
      );
    });

    // After confirm, updateIdentityAvatar called for both identities.
    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_A.id, 'blob-new');
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_B.id, 'blob-new');
    });
  });

  it('cancel on apply-to-all prompt sets only the current identity', async () => {
    confirmAnswers.push(false); // apply-to-all: cancel
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: undefined });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = makeFile();
    Object.defineProperty(fileInput, 'files', { value: [file], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_A.id, 'blob-new');
    });

    // IDENTITY_B must NOT have been updated.
    const calls = vi.mocked(mail.updateIdentityAvatar).mock.calls;
    const calledForB = calls.some(([id]) => id === IDENTITY_B.id);
    expect(calledForB).toBe(false);
  });

  it('does NOT trigger apply-to-all when another identity already has an avatar', async () => {
    // IDENTITY_B already has an avatar.
    resetIdentities(null, 'blob-existing');
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: undefined });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    const file = makeFile();
    Object.defineProperty(fileInput, 'files', { value: [file], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_A.id, 'blob-new');
    });

    // confirm.ask should NOT have been called for apply-to-all.
    const applyToAllCalls = vi.mocked(confirm.ask).mock.calls.filter(
      ([req]) => (req as { title?: string }).title === 'settings.avatar.applyToAll.title',
    );
    expect(applyToAllCalls.length).toBe(0);
  });
});

describe('IdentityAvatarForm: X-Face prompt', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    confirmAnswers.length = 0;
    resetIdentities();
  });

  it('prompts for X-Face when xFaceEnabled is false after first upload', async () => {
    confirmAnswers.push(false, true); // apply-to-all: cancel; xface: confirm
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: false });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', { value: [makeFile()], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      const xfaceCalls = vi.mocked(confirm.ask).mock.calls.filter(
        ([req]) => (req as { title?: string }).title === 'settings.avatar.xface.title',
      );
      expect(xfaceCalls.length).toBe(1);
    });

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityXFaceEnabled)).toHaveBeenCalledWith(IDENTITY_A.id, true);
    });
  });

  it('does NOT prompt for X-Face when xFaceEnabled is already true', async () => {
    confirmAnswers.push(false); // apply-to-all: cancel
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: true });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', { value: [makeFile()], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalled();
    });

    const xfaceCalls = vi.mocked(confirm.ask).mock.calls.filter(
      ([req]) => (req as { title?: string }).title === 'settings.avatar.xface.title',
    );
    expect(xfaceCalls.length).toBe(0);
  });
});

describe('IdentityAvatarForm: picker tile behaviour', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    confirmAnswers.length = 0;
  });

  it('clicking an existing tile calls updateIdentityAvatar with that blobId without uploading', async () => {
    // Only IDENTITY_A has the blob; IDENTITY_B has none.
    resetIdentities('blob-alice', null);
    const identity = makeIdentity({ avatarBlobId: 'blob-alice' });
    render(IdentityAvatarForm, { props: { identity } });

    // Open the picker.
    const changeBtn = screen.getByRole('button', { name: 'settings.avatar.change' });
    await fireEvent.click(changeBtn);

    // There should be exactly one existing-avatar tile (for Alice).
    const tiles = screen.getAllByRole('button', { name: 'Alice' });
    expect(tiles.length).toBe(1);

    await fireEvent.click(tiles[0]!);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_A.id, 'blob-alice');
    });

    // uploadBlob must not have been called.
    expect(vi.mocked(jmap.uploadBlob)).not.toHaveBeenCalled();
  });

  it('deduplicates tiles: two identities with the same blobId render as one tile', async () => {
    resetIdentities('blob-shared', 'blob-shared');
    const identity = makeIdentity({ avatarBlobId: 'blob-shared' });
    render(IdentityAvatarForm, { props: { identity } });

    const changeBtn = screen.getByRole('button', { name: 'settings.avatar.change' });
    await fireEvent.click(changeBtn);

    // With the same blobId on both identities, only one image tile is shown
    // (the last map entry wins, which is Bob given Map insertion order).
    // The "Pick new file" tile is also present, so total button count is 2
    // (1 existing + 1 "pick new"). Assert only 1 existing-avatar tile.
    const pickerDialog = screen.getByRole('dialog', { name: 'Choose avatar' });
    const tileBtns = pickerDialog.querySelectorAll('button.tile-btn:not(.tile-new)');
    expect(tileBtns.length).toBe(1);
  });
});

describe('IdentityAvatarForm: Remove button', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetIdentities();
  });

  it('Remove button is absent when identity has no avatar', () => {
    const identity = makeIdentity({ avatarBlobId: null });
    render(IdentityAvatarForm, { props: { identity } });
    expect(screen.queryByRole('button', { name: 'settings.avatar.remove' })).toBeNull();
  });

  it('Remove button appears when identity has an avatar and calls updateIdentityAvatar(null)', async () => {
    const identity = makeIdentity({ avatarBlobId: 'blob-abc' });
    render(IdentityAvatarForm, { props: { identity } });

    const removeBtn = screen.getByRole('button', { name: 'settings.avatar.remove' });
    expect(removeBtn).toBeInTheDocument();

    await fireEvent.click(removeBtn);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(IDENTITY_A.id, null);
    });
  });
});

describe('IdentityAvatarForm: file-size guard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    confirmAnswers.length = 0;
    resetIdentities();
  });

  it('a 5 MB file is processed through resize; the upload payload is within 1 MB', async () => {
    // toBlob stub always returns MOCK_BLOB_SIZE (800 KB) regardless of quality.
    confirmAnswers.push(false); // apply-to-all: cancel
    const bigFile = makeFile(5 * 1024 * 1024);
    const identity = makeIdentity({ avatarBlobId: null, xFaceEnabled: undefined });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', { value: [bigFile], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(jmap.uploadBlob)).toHaveBeenCalled();
    });

    const uploadCall = vi.mocked(jmap.uploadBlob).mock.calls[0]!;
    const uploadedBlob = uploadCall[0].body as Blob;
    expect(uploadedBlob.size).toBeLessThanOrEqual(1024 * 1024);
  });

  it('shows a tooLarge toast when compressed blob still exceeds 1 MB', async () => {
    // Override toBlob to always return an oversized blob.
    toBlob.mockImplementationOnce((cb: (b: Blob | null) => void) => {
      const big = new Blob(['x'.repeat(1100_000)], { type: 'image/jpeg' });
      cb(big);
    });
    toBlob.mockImplementationOnce((cb: (b: Blob | null) => void) => {
      const big = new Blob(['x'.repeat(1100_000)], { type: 'image/jpeg' });
      cb(big);
    });
    toBlob.mockImplementationOnce((cb: (b: Blob | null) => void) => {
      const big = new Blob(['x'.repeat(1100_000)], { type: 'image/jpeg' });
      cb(big);
    });

    const identity = makeIdentity({ avatarBlobId: null });
    const { container } = render(IdentityAvatarForm, { props: { identity } });

    const fileInput = container.querySelector('input[type="file"]') as HTMLInputElement;
    Object.defineProperty(fileInput, 'files', { value: [makeFile(5_000_000)], configurable: true });
    await fireEvent.change(fileInput);

    await vi.waitFor(() => {
      expect(vi.mocked(toast.show)).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'settings.avatar.upload.tooLarge', kind: 'error' }),
      );
    });

    expect(vi.mocked(jmap.uploadBlob)).not.toHaveBeenCalled();
  });
});
