/**
 * Pure helper for the identity avatar upload pipeline (REQ-SET-03b).
 *
 * Extracted out of IdentityAvatarForm so the orchestration — Blob/upload,
 * Identity/set update of avatarBlobId, the apply-to-all confirm, the
 * X-Face confirm — can be unit-tested without rendering the picker or
 * the capture dialog. The component itself is now a thin wrapper that
 * gathers user input (existing-blob picker, capture+crop dialog, remove
 * button) and delegates to these helpers.
 */

import type { Identity } from '../../lib/mail/types';

/** 1 MiB cap on the uploaded avatar blob (REQ-SET-03b). */
export const AVATAR_MAX_BYTES = 1024 * 1024;

export interface UploadDeps {
  /** AccountId for the JMAP urn:ietf:params:jmap:mail capability. */
  accountId: string | null;
  /** Bound `jmap.uploadBlob` from `lib/jmap/client`. */
  uploadBlob: (args: {
    accountId: string;
    body: Blob;
    type: string;
  }) => Promise<{ blobId: string }>;
  /** `mail.updateIdentityAvatar` */
  updateIdentityAvatar: (
    identityId: string,
    blobId: string | null,
  ) => Promise<void>;
  /** `mail.updateIdentityXFaceEnabled` */
  updateIdentityXFaceEnabled: (
    identityId: string,
    enabled: boolean,
  ) => Promise<void>;
  /** `confirm.ask` from the dialog store. */
  confirmAsk: (req: {
    title: string;
    message: string;
    confirmLabel: string;
    cancelLabel: string;
  }) => Promise<boolean>;
  /** `t` from i18n. */
  t: (key: string, args?: Record<string, string | number>) => string;
  /** Live identity map (used to compute the apply-to-all set). */
  allIdentities: () => Identity[];
}

/**
 * Upload a pre-prepared Blob (already cropped + scaled to ≤ 1 MB).
 * Returns the new blobId, or null when the size guard rejects it.
 */
export async function uploadAvatarBlob(
  deps: UploadDeps,
  blob: Blob,
): Promise<string | null> {
  if (blob.size > AVATAR_MAX_BYTES) return null;
  if (!deps.accountId) return null;
  const { blobId } = await deps.uploadBlob({
    accountId: deps.accountId,
    body: blob,
    type: blob.type || 'image/jpeg',
  });
  return blobId;
}

/**
 * Run the post-upload prompts the spec mandates on first upload:
 *   - apply-to-all: when no other identity carries an avatar yet AND
 *     there is more than one identity in total.
 *   - X-Face: when the current identity has xFaceEnabled === false.
 *
 * Mutates the JMAP server through deps, never touches local state.
 */
export async function applyPostUploadPrompts(
  deps: UploadDeps,
  current: Identity,
  blobId: string,
): Promise<void> {
  const allIdentities = deps.allIdentities();
  const othersWithAvatar = allIdentities.filter(
    (id) =>
      id.id !== current.id &&
      typeof id.avatarBlobId === 'string' &&
      id.avatarBlobId.length > 0,
  );
  if (othersWithAvatar.length === 0 && allIdentities.length > 1) {
    const ok = await deps.confirmAsk({
      title: deps.t('settings.avatar.applyToAll.title'),
      message: deps.t('settings.avatar.applyToAll.message', {
        count: allIdentities.length,
      }),
      confirmLabel: deps.t('settings.avatar.applyToAll.confirm'),
      cancelLabel: deps.t('settings.avatar.applyToAll.cancel'),
    });
    if (ok) {
      for (const other of allIdentities) {
        if (other.id !== current.id) {
          await deps.updateIdentityAvatar(other.id, blobId);
        }
      }
    }
  }

  if (current.xFaceEnabled === false) {
    const ok = await deps.confirmAsk({
      title: deps.t('settings.avatar.xface.title'),
      message: deps.t('settings.avatar.xface.message'),
      confirmLabel: deps.t('settings.avatar.xface.confirm'),
      cancelLabel: deps.t('settings.avatar.xface.cancel'),
    });
    if (ok) {
      await deps.updateIdentityXFaceEnabled(current.id, true);
    }
  }
}

/**
 * Top-level orchestration: upload + Identity/set + prompts.
 * Returns the new blobId on success, null when the upload was rejected
 * by the size guard or no accountId is available.
 */
export async function uploadAndApplyAvatar(
  deps: UploadDeps,
  current: Identity,
  blob: Blob,
): Promise<string | null> {
  const blobId = await uploadAvatarBlob(deps, blob);
  if (!blobId) return null;
  await deps.updateIdentityAvatar(current.id, blobId);
  await applyPostUploadPrompts(deps, current, blobId);
  return blobId;
}
