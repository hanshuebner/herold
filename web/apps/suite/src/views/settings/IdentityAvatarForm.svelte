<script lang="ts">
  /**
   * Per-identity avatar editor (REQ-SET-03b).
   *
   * Shows a round 64x64 preview of the current avatar (or an initial-letter
   * placeholder), a "Change..." button that opens a picker popover, and a
   * "Remove" button when an avatar is currently set.
   *
   * The picker lists every distinct avatar blob already set across the user's
   * identities (deduplicated by blobId) plus a "Pick new file" tile.
   *
   * File upload path:
   *   - Client-side resize to at most 512x512 via <canvas> + toBlob.
   *   - Validates result <= 1 MB; retries at lower JPEG quality before giving up.
   *   - Uploads via jmap.uploadBlob.
   *   - On first upload with no other identity having an avatar (and > 1
   *     identities present), prompts "Apply to all?".
   *   - On first upload when xFaceEnabled is currently false, prompts
   *     "Include in outbound mail?".
   */
  import { mail } from '../../lib/mail/store.svelte';
  import { jmap } from '../../lib/jmap/client';
  import { auth } from '../../lib/auth/auth.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';
  import { identityAvatarUrl } from '../../lib/mail/identity-avatar';
  import type { Identity } from '../../lib/mail/types';
  import AvatarCaptureDialog from './AvatarCaptureDialog.svelte';
  import {
    AVATAR_MAX_BYTES,
    uploadAndApplyAvatar,
    type UploadDeps,
  } from './identity-avatar-upload';

  interface Props {
    identity: Identity;
  }
  let { identity }: Props = $props();

  // ── State ──────────────────────────────────────────────────────────────

  let pickerOpen = $state(false);
  let uploading = $state(false);
  let captureOpen = $state(false);

  // Derive the set of distinct blobIds already used across all identities,
  // excluding null/undefined values.
  let existingBlobs = $derived(
    Array.from(
      new Map(
        Array.from(mail.identities.values())
          .filter((id): id is Identity & { avatarBlobId: string } =>
            typeof id.avatarBlobId === 'string' && id.avatarBlobId.length > 0,
          )
          .map((id) => [id.avatarBlobId, id]),
      ).values(),
    ),
  );

  let currentAvatarUrl = $derived(identityAvatarUrl(identity));

  // Initial letter for the placeholder avatar (uppercase first letter of
  // display name, falling back to the first character of the email address).
  let initial = $derived(
    (identity.name?.trim() || identity.email)
      .charAt(0)
      .toUpperCase(),
  );

  // ── Actions ─────────────────────────────────────────────────────────────

  function uploadDeps(): UploadDeps {
    return {
      accountId:
        auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null,
      uploadBlob: jmap.uploadBlob.bind(jmap),
      updateIdentityAvatar: mail.updateIdentityAvatar.bind(mail),
      updateIdentityXFaceEnabled: mail.updateIdentityXFaceEnabled.bind(mail),
      confirmAsk: confirm.ask.bind(confirm),
      t,
      allIdentities: () => Array.from(mail.identities.values()),
    };
  }

  async function pickExisting(blobId: string): Promise<void> {
    pickerOpen = false;
    try {
      await mail.updateIdentityAvatar(identity.id, blobId);
    } catch (err) {
      toast.show({
        message: t('settings.avatar.upload.failed', {
          reason: err instanceof Error ? err.message : String(err),
        }),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  async function handleCaptureConfirm(blob: Blob): Promise<void> {
    captureOpen = false;
    pickerOpen = false;
    uploading = true;
    try {
      const blobId = await uploadAndApplyAvatar(uploadDeps(), identity, blob);
      if (!blobId) {
        if (blob.size > AVATAR_MAX_BYTES) {
          toast.show({
            message: t('settings.avatar.upload.tooLarge'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
        return;
      }
    } catch (err) {
      toast.show({
        message: t('settings.avatar.upload.failed', {
          reason: err instanceof Error ? err.message : String(err),
        }),
        kind: 'error',
        timeoutMs: 6000,
      });
    } finally {
      uploading = false;
    }
  }

  function openCaptureDialog(): void {
    pickerOpen = false;
    captureOpen = true;
  }

  async function removeAvatar(): Promise<void> {
    try {
      await mail.updateIdentityAvatar(identity.id, null);
    } catch (err) {
      toast.show({
        message: t('settings.avatar.upload.failed', {
          reason: err instanceof Error ? err.message : String(err),
        }),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

</script>

<div class="avatar-form">
  <!-- LEFT: round avatar preview -->
  <div class="preview-wrap">
    {#if currentAvatarUrl}
      <img
        class="avatar-img"
        src={currentAvatarUrl}
        alt={identity.name || identity.email}
        aria-label={t('settings.avatar.title')}
      />
    {:else}
      <div class="avatar-placeholder" aria-label={t('settings.avatar.title')}>
        {initial}
      </div>
    {/if}
    {#if uploading}
      <div class="upload-overlay" aria-busy="true" aria-label="Uploading..."></div>
    {/if}
  </div>

  <!-- RIGHT: controls -->
  <div class="controls">
    <span class="control-title">{t('settings.avatar.title')}</span>
    <div class="buttons">
      <div class="picker-wrap">
        <button
          type="button"
          class="ghost"
          onclick={() => { pickerOpen = !pickerOpen; }}
          disabled={uploading}
          aria-expanded={pickerOpen}
          aria-haspopup="true"
        >
          {t('settings.avatar.change')}
        </button>

        {#if pickerOpen}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="popover"
            role="dialog"
            aria-label="Choose avatar"
            tabindex="-1"
            onkeydown={(e) => { if (e.key === 'Escape') pickerOpen = false; }}
          >
            <div class="tile-grid">
              <!-- Existing avatar tiles (deduplicated by blobId) -->
              {#each existingBlobs as src (src.avatarBlobId)}
                {@const url = jmap.downloadUrl({
                  accountId: auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? '',
                  blobId: src.avatarBlobId,
                  type: 'image/*',
                  name: 'avatar',
                  disposition: 'inline',
                })}
                <button
                  type="button"
                  class="tile-btn"
                  onclick={() => void pickExisting(src.avatarBlobId)}
                  title={src.name || src.email}
                  aria-label={src.name || src.email}
                >
                  {#if url}
                    <img class="tile-img" src={url} alt="" />
                  {:else}
                    <div class="tile-placeholder">
                      {(src.name?.trim() || src.email).charAt(0).toUpperCase()}
                    </div>
                  {/if}
                </button>
              {/each}

              <!-- Pick new picture: opens the capture dialog (file /
                   camera / drag-and-drop + interactive crop). -->
              <button
                type="button"
                class="tile-btn tile-new"
                onclick={openCaptureDialog}
                aria-label={t('settings.avatar.pickNew')}
                title={t('settings.avatar.pickNew')}
              >
                <span class="tile-plus">+</span>
              </button>
            </div>
          </div>
          <!-- Click-outside to close -->
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div
            class="backdrop"
            onclick={() => { pickerOpen = false; }}
            onkeydown={(e) => { if (e.key === 'Escape') pickerOpen = false; }}
            aria-hidden="true"
          ></div>
        {/if}
      </div>

      {#if identity.avatarBlobId}
        <button
          type="button"
          class="ghost danger"
          onclick={() => void removeAvatar()}
          disabled={uploading}
        >
          {t('settings.avatar.remove')}
        </button>
      {/if}
    </div>
  </div>

</div>

<AvatarCaptureDialog
  open={captureOpen}
  onCancel={() => { captureOpen = false; }}
  onConfirm={(blob) => { void handleCaptureConfirm(blob); }}
/>

<style>
  .avatar-form {
    display: flex;
    align-items: center;
    gap: var(--spacing-05);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  /* ── Round preview ── */
  .preview-wrap {
    position: relative;
    flex-shrink: 0;
    width: 64px;
    height: 64px;
  }

  .avatar-img,
  .avatar-placeholder {
    width: 64px;
    height: 64px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    object-fit: cover;
  }

  .avatar-placeholder {
    background: var(--interactive);
    color: var(--text-on-color);
    font-size: var(--type-heading-03-size);
    font-weight: 600;
    user-select: none;
  }

  .upload-overlay {
    position: absolute;
    inset: 0;
    border-radius: 50%;
    background: rgba(0, 0, 0, 0.4);
  }

  .upload-overlay::after {
    content: '';
    position: absolute;
    top: 50%;
    left: 50%;
    width: 20px;
    height: 20px;
    margin: -10px 0 0 -10px;
    border: 2px solid rgba(255, 255, 255, 0.3);
    border-top-color: #fff;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  @media (prefers-reduced-motion: reduce) {
    .upload-overlay::after {
      animation: none;
      border: 2px solid #fff;
    }
  }

  /* ── Controls ── */
  .controls {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .control-title {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 500;
  }

  .buttons {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .ghost {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .ghost:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .ghost:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .ghost.danger:hover:not(:disabled) {
    background: rgba(250, 77, 86, 0.12);
    color: var(--support-error);
  }

  /* ── Picker popover ── */
  .picker-wrap {
    position: relative;
  }

  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 9;
  }

  .popover {
    position: absolute;
    top: calc(100% + var(--spacing-02));
    left: 0;
    z-index: 10;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.25);
    min-width: 200px;
  }

  .tile-grid {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
  }

  .tile-btn {
    width: 48px;
    height: 48px;
    border-radius: 50%;
    overflow: hidden;
    border: 2px solid transparent;
    padding: 0;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .tile-btn:hover {
    border-color: var(--interactive);
  }

  .tile-img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  .tile-placeholder {
    width: 100%;
    height: 100%;
    background: var(--interactive);
    color: var(--text-on-color);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: var(--type-body-01-size);
    font-weight: 600;
  }

  .tile-new {
    background: var(--layer-01);
    border: 2px dashed var(--border-subtle-01);
    color: var(--text-secondary);
  }

  .tile-new:hover {
    border-color: var(--interactive);
    color: var(--interactive);
  }

  .tile-plus {
    font-size: 1.5rem;
    line-height: 1;
  }

</style>
