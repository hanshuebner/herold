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

  interface Props {
    identity: Identity;
  }
  let { identity }: Props = $props();

  // ── State ──────────────────────────────────────────────────────────────

  let pickerOpen = $state(false);
  let uploading = $state(false);
  let fileInputEl = $state<HTMLInputElement | null>(null);

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

  // ── Upload helpers ──────────────────────────────────────────────────────

  const MAX_BYTES = 1024 * 1024; // 1 MiB
  const MAX_DIM = 512;

  /**
   * Resize `file` to at most MAX_DIM x MAX_DIM, then compress as JPEG (or
   * preserve as PNG/GIF for lossless types). Returns a Blob <= MAX_BYTES, or
   * null when compression cannot achieve the target size.
   */
  async function resizeAndCompress(file: File): Promise<Blob | null> {
    const bitmap = await createImageBitmap(file);
    const { width: sw, height: sh } = bitmap;

    const scale = Math.min(1, MAX_DIM / Math.max(sw, sh));
    const dw = Math.round(sw * scale);
    const dh = Math.round(sh * scale);

    const canvas = document.createElement('canvas');
    canvas.width = dw;
    canvas.height = dh;
    const ctx = canvas.getContext('2d')!;
    ctx.drawImage(bitmap, 0, 0, dw, dh);
    bitmap.close();

    // PNG and GIF: lossless round-trip; use PNG output.
    const lossless = file.type === 'image/png' || file.type === 'image/gif';
    if (lossless) {
      return new Promise<Blob | null>((res) =>
        canvas.toBlob((b) => res(b && b.size <= MAX_BYTES ? b : null), 'image/png'),
      );
    }

    // JPEG / WebP: try quality 0.85, then 0.7, then 0.5.
    for (const q of [0.85, 0.7, 0.5]) {
      const blob = await new Promise<Blob | null>((res) =>
        canvas.toBlob((b) => res(b), 'image/jpeg', q),
      );
      if (blob && blob.size <= MAX_BYTES) return blob;
    }
    return null;
  }

  async function uploadFile(file: File): Promise<string | null> {
    const blob = await resizeAndCompress(file);
    if (!blob) {
      toast.show({
        message: t('settings.avatar.upload.tooLarge'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return null;
    }

    const accountId =
      auth.session?.primaryAccounts['urn:ietf:params:jmap:mail'] ?? null;
    if (!accountId) return null;

    const result = await jmap.uploadBlob({
      accountId,
      body: blob,
      type: blob.type || 'image/jpeg',
    });
    return result.blobId;
  }

  // ── Actions ─────────────────────────────────────────────────────────────

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

  async function handleFileSelected(file: File): Promise<void> {
    pickerOpen = false;
    uploading = true;
    try {
      const blobId = await uploadFile(file);
      if (!blobId) return;

      await mail.updateIdentityAvatar(identity.id, blobId);

      // Apply-to-all prompt: fires when NO other identity currently has an
      // avatar AND there is more than one identity in total.
      const allIdentities = Array.from(mail.identities.values());
      const othersWithAvatar = allIdentities.filter(
        (id) => id.id !== identity.id && typeof id.avatarBlobId === 'string' && id.avatarBlobId.length > 0,
      );
      if (othersWithAvatar.length === 0 && allIdentities.length > 1) {
        const ok = await confirm.ask({
          title: t('settings.avatar.applyToAll.title'),
          message: t('settings.avatar.applyToAll.message', {
            count: allIdentities.length,
          }),
          confirmLabel: t('settings.avatar.applyToAll.confirm'),
          cancelLabel: t('settings.avatar.applyToAll.cancel'),
        });
        if (ok) {
          for (const other of allIdentities) {
            if (other.id !== identity.id) {
              await mail.updateIdentityAvatar(other.id, blobId);
            }
          }
        }
      }

      // X-Face prompt: fires only when xFaceEnabled is explicitly false for
      // this identity (not when it is undefined/absent, because the server
      // may default to true).
      if (identity.xFaceEnabled === false) {
        const ok = await confirm.ask({
          title: t('settings.avatar.xface.title'),
          message: t('settings.avatar.xface.message'),
          confirmLabel: t('settings.avatar.xface.confirm'),
          cancelLabel: t('settings.avatar.xface.cancel'),
        });
        if (ok) {
          await mail.updateIdentityXFaceEnabled(identity.id, true);
        }
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

  function openFilePicker(): void {
    fileInputEl?.click();
  }

  function onFileChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (file) void handleFileSelected(file);
    // Reset so re-selecting the same file fires the event again.
    input.value = '';
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

              <!-- Pick new file tile -->
              <button
                type="button"
                class="tile-btn tile-new"
                onclick={openFilePicker}
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

  <!-- Hidden file input -->
  <input
    bind:this={fileInputEl}
    type="file"
    accept="image/png,image/jpeg,image/webp,image/gif"
    class="sr-only"
    onchange={onFileChange}
    tabindex="-1"
    aria-hidden="true"
  />
</div>

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

  /* ── Accessibility ── */
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
