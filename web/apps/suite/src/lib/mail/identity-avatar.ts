/**
 * Avatar URL helper for Identity objects.
 *
 * Centralises the `jmap.downloadUrl` call so every surface that renders an
 * identity avatar (settings editor, future thread-header, chat sidebar) builds
 * the same URL without duplicating the parameter conventions.
 */

import { jmap } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import type { Identity } from './types';

/**
 * Return the JMAP download URL for the identity's avatar blob, or null when
 * the identity has no `avatarBlobId` or the JMAP session has not bootstrapped
 * yet.
 *
 * The URL requests `disposition=inline` so the browser renders the image
 * in-page rather than triggering a download.
 */
export function identityAvatarUrl(identity: Identity): string | null {
  if (!identity.avatarBlobId) return null;
  const accountId =
    auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null;
  if (!accountId) return null;
  return jmap.downloadUrl({
    accountId,
    blobId: identity.avatarBlobId,
    type: 'image/*',
    name: 'avatar',
    disposition: 'inline',
  });
}
