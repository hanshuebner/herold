/**
 * Pure-function tests for the identity-avatar upload pipeline
 * (REQ-SET-03b). Exercises the spec's invariants without rendering the
 * Svelte form or the capture dialog:
 *
 *   - Apply-to-all prompt fires only when no other identity carries an
 *     avatar AND there is more than one identity total.
 *   - X-Face prompt fires only when the current identity's xFaceEnabled
 *     is explicitly false.
 *   - Existing-blob pick (handled in the form, not here) is intentionally
 *     out of scope; the picker tile + remove button are tested in the
 *     remaining form-level test.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  uploadAvatarBlob,
  applyPostUploadPrompts,
  uploadAndApplyAvatar,
  AVATAR_MAX_BYTES,
  type UploadDeps,
} from './identity-avatar-upload';
import type { Identity } from '../../lib/mail/types';

const IDENTITY_A: Identity = {
  id: 'id-a',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  avatarBlobId: null,
  xFaceEnabled: false,
} as unknown as Identity;

const IDENTITY_B: Identity = {
  id: 'id-b',
  name: 'Bob',
  email: 'bob@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  avatarBlobId: null,
  xFaceEnabled: false,
} as unknown as Identity;

function makeDeps(opts: {
  identities: Identity[];
  uploaded?: { blobId: string };
  confirmAnswers?: boolean[];
  accountId?: string | null;
}): UploadDeps & {
  uploadBlob: ReturnType<typeof vi.fn>;
  updateIdentityAvatar: ReturnType<typeof vi.fn>;
  updateIdentityXFaceEnabled: ReturnType<typeof vi.fn>;
  confirmAsk: ReturnType<typeof vi.fn>;
} {
  const answers = [...(opts.confirmAnswers ?? [])];
  return {
    accountId: 'accountId' in opts ? opts.accountId ?? null : 'acct1',
    uploadBlob: vi.fn(async () => opts.uploaded ?? { blobId: 'blob-new' }),
    updateIdentityAvatar: vi.fn(async () => undefined),
    updateIdentityXFaceEnabled: vi.fn(async () => undefined),
    confirmAsk: vi.fn(async () => answers.shift() ?? false),
    t: (key: string, args?: Record<string, string | number>): string =>
      args ? `${key}:${JSON.stringify(args)}` : key,
    allIdentities: () => opts.identities,
  };
}

describe('uploadAvatarBlob', () => {
  it('rejects blobs over the size cap', async () => {
    const deps = makeDeps({ identities: [IDENTITY_A] });
    const blob = new Blob([new Uint8Array(AVATAR_MAX_BYTES + 1)], {
      type: 'image/jpeg',
    });
    expect(await uploadAvatarBlob(deps, blob)).toBeNull();
    expect(deps.uploadBlob).not.toHaveBeenCalled();
  });

  it('returns null when no accountId is available', async () => {
    const deps = makeDeps({ identities: [IDENTITY_A], accountId: null });
    const blob = new Blob(['x'], { type: 'image/jpeg' });
    expect(await uploadAvatarBlob(deps, blob)).toBeNull();
    expect(deps.uploadBlob).not.toHaveBeenCalled();
  });

  it('uploads and returns the blobId on success', async () => {
    const deps = makeDeps({
      identities: [IDENTITY_A],
      uploaded: { blobId: 'blob-xyz' },
    });
    const blob = new Blob(['x'], { type: 'image/jpeg' });
    expect(await uploadAvatarBlob(deps, blob)).toBe('blob-xyz');
    expect(deps.uploadBlob).toHaveBeenCalledWith({
      accountId: 'acct1',
      body: blob,
      type: 'image/jpeg',
    });
  });
});

describe('applyPostUploadPrompts: apply-to-all', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('fires when no other identity has an avatar and >1 identities; confirm sets all', async () => {
    const deps = makeDeps({
      identities: [IDENTITY_A, IDENTITY_B],
      confirmAnswers: [true, false], // apply-to-all: yes; xface: no
    });
    await applyPostUploadPrompts(deps, IDENTITY_A, 'blob-new');
    expect(deps.confirmAsk).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'settings.avatar.applyToAll.title' }),
    );
    expect(deps.updateIdentityAvatar).toHaveBeenCalledWith('id-b', 'blob-new');
  });

  it('cancel skips updating other identities', async () => {
    const deps = makeDeps({
      identities: [IDENTITY_A, IDENTITY_B],
      confirmAnswers: [false, false],
    });
    await applyPostUploadPrompts(deps, IDENTITY_A, 'blob-new');
    expect(deps.updateIdentityAvatar).not.toHaveBeenCalled();
  });

  it('does NOT fire when another identity already has an avatar', async () => {
    const others = [
      IDENTITY_A,
      { ...IDENTITY_B, avatarBlobId: 'blob-existing' } as Identity,
    ];
    const deps = makeDeps({
      identities: others,
      confirmAnswers: [false], // only the xface prompt fires
    });
    await applyPostUploadPrompts(deps, IDENTITY_A, 'blob-new');
    const applyCalls = (deps.confirmAsk.mock.calls as Array<[{ title?: string }]>).filter(
      (call) => call[0]?.title === 'settings.avatar.applyToAll.title',
    );
    expect(applyCalls.length).toBe(0);
  });

  it('does NOT fire with only one identity', async () => {
    const deps = makeDeps({ identities: [IDENTITY_A], confirmAnswers: [false] });
    await applyPostUploadPrompts(deps, IDENTITY_A, 'blob-new');
    const applyCalls = (deps.confirmAsk.mock.calls as Array<[{ title?: string }]>).filter(
      (call) => call[0]?.title === 'settings.avatar.applyToAll.title',
    );
    expect(applyCalls.length).toBe(0);
  });
});

describe('applyPostUploadPrompts: X-Face prompt', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('fires when xFaceEnabled is explicitly false; confirm enables xface', async () => {
    const deps = makeDeps({
      identities: [IDENTITY_A],
      confirmAnswers: [true],
    });
    await applyPostUploadPrompts(deps, IDENTITY_A, 'blob-new');
    expect(deps.updateIdentityXFaceEnabled).toHaveBeenCalledWith('id-a', true);
  });

  it('does NOT fire when xFaceEnabled is true', async () => {
    const idAlready = { ...IDENTITY_A, xFaceEnabled: true } as Identity;
    const deps = makeDeps({ identities: [idAlready] });
    await applyPostUploadPrompts(deps, idAlready, 'blob-new');
    expect(deps.confirmAsk).not.toHaveBeenCalled();
  });

  it('does NOT fire when xFaceEnabled is undefined (server default)', async () => {
    const idAbsent = { ...IDENTITY_A, xFaceEnabled: undefined } as unknown as Identity;
    const deps = makeDeps({ identities: [idAbsent] });
    await applyPostUploadPrompts(deps, idAbsent, 'blob-new');
    expect(deps.confirmAsk).not.toHaveBeenCalled();
  });
});

describe('uploadAndApplyAvatar', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('runs upload + Identity/set + prompts in order', async () => {
    const deps = makeDeps({
      identities: [IDENTITY_A, IDENTITY_B],
      uploaded: { blobId: 'blob-final' },
      confirmAnswers: [true, true],
    });
    const blob = new Blob(['x'], { type: 'image/jpeg' });
    const out = await uploadAndApplyAvatar(deps, IDENTITY_A, blob);
    expect(out).toBe('blob-final');
    expect(deps.uploadBlob).toHaveBeenCalled();
    expect(deps.updateIdentityAvatar).toHaveBeenCalledWith('id-a', 'blob-final');
    expect(deps.updateIdentityAvatar).toHaveBeenCalledWith('id-b', 'blob-final');
    expect(deps.updateIdentityXFaceEnabled).toHaveBeenCalledWith('id-a', true);
  });

  it('short-circuits to null when the size guard rejects', async () => {
    const deps = makeDeps({ identities: [IDENTITY_A] });
    const blob = new Blob([new Uint8Array(AVATAR_MAX_BYTES + 1)], {
      type: 'image/jpeg',
    });
    const out = await uploadAndApplyAvatar(deps, IDENTITY_A, blob);
    expect(out).toBeNull();
    expect(deps.updateIdentityAvatar).not.toHaveBeenCalled();
  });
});
