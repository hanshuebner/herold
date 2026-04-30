/**
 * IdentityAvatarForm form-level tests (REQ-SET-03b).
 *
 * Focused on what the *form* owns: picker tile rendering, deduplication,
 * existing-blob pick, Remove button. The pure upload+prompt pipeline is
 * exercised in `identity-avatar-upload.test.ts`; the capture+crop dialog
 * has its own component and is not driven from this test.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';

// ── Mocks ──────────────────────────────────────────────────────────────────

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
    uploadBlob: vi.fn(async () => ({
      blobId: 'blob-new',
      type: 'image/jpeg',
      size: 50_000,
      accountId: 'acct1',
    })),
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

vi.mock('../../lib/dialog/confirm.svelte', () => ({
  confirm: { ask: vi.fn(async () => false) },
}));

vi.mock('../../lib/i18n/i18n.svelte', () => ({
  t: (key: string, args?: Record<string, unknown>): string =>
    args ? `${key}:${JSON.stringify(args)}` : key,
}));

vi.mock('../../lib/mail/identity-avatar', () => ({
  identityAvatarUrl: (identity: { avatarBlobId?: string | null }) =>
    identity.avatarBlobId ? `/jmap/dl/${identity.avatarBlobId}` : null,
}));

const { mail } = await import('../../lib/mail/store.svelte');
const { jmap } = await import('../../lib/jmap/client');

import IdentityAvatarForm from './IdentityAvatarForm.svelte';

function resetIdentities(aAvatar?: string | null, bAvatar?: string | null) {
  identitiesMap.set(IDENTITY_A.id, { ...IDENTITY_A, avatarBlobId: aAvatar ?? null });
  identitiesMap.set(IDENTITY_B.id, { ...IDENTITY_B, avatarBlobId: bAvatar ?? null });
}

function makeIdentity(overrides?: Partial<typeof IDENTITY_A>) {
  return { ...IDENTITY_A, ...overrides };
}

describe('IdentityAvatarForm: picker tile behaviour', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetIdentities();
  });

  it('clicking an existing tile calls updateIdentityAvatar with that blobId without uploading', async () => {
    resetIdentities('blob-alice', null);
    const identity = makeIdentity({ avatarBlobId: 'blob-alice' });
    render(IdentityAvatarForm, { props: { identity } });

    const changeBtn = screen.getByRole('button', { name: 'settings.avatar.change' });
    await fireEvent.click(changeBtn);

    const tiles = screen.getAllByRole('button', { name: 'Alice' });
    expect(tiles.length).toBe(1);

    await fireEvent.click(tiles[0]!);

    await vi.waitFor(() => {
      expect(vi.mocked(mail.updateIdentityAvatar)).toHaveBeenCalledWith(
        IDENTITY_A.id,
        'blob-alice',
      );
    });

    expect(vi.mocked(jmap.uploadBlob)).not.toHaveBeenCalled();
  });

  it('deduplicates tiles: two identities with the same blobId render as one tile', async () => {
    resetIdentities('blob-shared', 'blob-shared');
    const identity = makeIdentity({ avatarBlobId: 'blob-shared' });
    render(IdentityAvatarForm, { props: { identity } });

    const changeBtn = screen.getByRole('button', { name: 'settings.avatar.change' });
    await fireEvent.click(changeBtn);

    const pickerDialog = screen.getByRole('dialog', { name: 'Choose avatar' });
    const tileBtns = pickerDialog.querySelectorAll('button.tile-btn:not(.tile-new)');
    expect(tileBtns.length).toBe(1);
  });

  it('shows the "pick new" tile that opens the capture dialog', async () => {
    resetIdentities();
    const identity = makeIdentity();
    render(IdentityAvatarForm, { props: { identity } });

    const changeBtn = screen.getByRole('button', { name: 'settings.avatar.change' });
    await fireEvent.click(changeBtn);

    const newTile = screen.getByRole('button', { name: 'settings.avatar.pickNew' });
    expect(newTile).toBeInTheDocument();
  });
});

describe('IdentityAvatarForm: Remove button', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetIdentities();
  });

  it('is absent when identity has no avatar', () => {
    const identity = makeIdentity({ avatarBlobId: null });
    render(IdentityAvatarForm, { props: { identity } });
    expect(screen.queryByRole('button', { name: 'settings.avatar.remove' })).toBeNull();
  });

  it('appears when identity has an avatar and calls updateIdentityAvatar(null)', async () => {
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
