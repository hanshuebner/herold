<script lang="ts">
  /**
   * Contact photo editor — pick/drop image, crop to square, preview,
   * set, and remove. REQ-CONT-60..61, REQ-CONT-113.
   *
   * Bindable state (read by the parent form to upload and patch on save):
   *   action            'keep' | 'replace' | 'remove'
   *   pendingFile       Blob of the cropped JPEG (set when action === 'replace')
   *   pendingMediaType  MIME type of pendingFile ('image/jpeg')
   *   pendingPreviewUrl object URL for the preview image (revoked on destroy)
   *
   * The component does not upload; the parent does so on form save.
   */

  import { onDestroy } from 'svelte';
  import ContactAvatar from './ContactAvatar.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { validatePhotoFile } from './jscontact';

  interface Props {
    /** Current photo blobId from the server; null when no photo. */
    currentBlobId?: string | null;
    /** Initial letter for monogram fallback display. */
    fallbackInitial?: string;
    /** Whether the parent form is saving (disables controls). */
    disabled?: boolean;
    /** Bound: what to do on save. */
    action?: 'keep' | 'replace' | 'remove';
    /** Bound: cropped image blob ready for upload (when action === 'replace'). */
    pendingFile?: Blob | null;
    /** Bound: MIME type of pendingFile. */
    pendingMediaType?: string;
    /** Bound: object URL for preview rendering; revoked on component destroy. */
    pendingPreviewUrl?: string | null;
  }

  let {
    currentBlobId = null,
    fallbackInitial = '?',
    disabled = false,
    action = $bindable('keep' as 'keep' | 'replace' | 'remove'),
    pendingFile = $bindable(null as Blob | null),
    pendingMediaType = $bindable(''),
    pendingPreviewUrl = $bindable(null as string | null),
  }: Props = $props();

  let errorMsg = $state('');
  let dragOver = $state(false);
  let fileInput = $state<HTMLInputElement | null>(null);

  /** blobId to show in ContactAvatar (null when action changes the display). */
  let displayBlobId = $derived(
    action === 'keep' ? currentBlobId : null,
  );

  /** Show pending preview when replacing, current blob when keeping, nothing when removing. */
  let hasDisplay = $derived(
    action === 'replace' ? pendingPreviewUrl !== null : action === 'keep' ? currentBlobId !== null : false,
  );

  onDestroy(() => {
    if (pendingPreviewUrl) {
      URL.revokeObjectURL(pendingPreviewUrl);
    }
  });

  function openPicker(): void {
    fileInput?.click();
  }

  function handleFileInput(e: Event): void {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) void processFile(file);
    // Reset so the same file can be re-picked.
    input.value = '';
  }

  function handleDragOver(e: DragEvent): void {
    e.preventDefault();
    dragOver = true;
  }

  function handleDragLeave(): void {
    dragOver = false;
  }

  function handleDrop(e: DragEvent): void {
    e.preventDefault();
    dragOver = false;
    if (disabled) return;
    const file = e.dataTransfer?.files?.[0];
    if (file) void processFile(file);
  }

  async function processFile(file: File): Promise<void> {
    errorMsg = '';
    const err = validatePhotoFile(file);
    if (err) {
      errorMsg = err;
      return;
    }

    try {
      const cropped = await cropToSquare(file);
      const preview = URL.createObjectURL(cropped);

      if (pendingPreviewUrl) URL.revokeObjectURL(pendingPreviewUrl);

      pendingFile = cropped;
      pendingMediaType = 'image/jpeg';
      pendingPreviewUrl = preview;
      action = 'replace';
    } catch {
      errorMsg = t('contacts.edit.photo.cropError');
    }
  }

  function handleRemove(): void {
    revokePreview();
    action = 'remove';
    errorMsg = '';
  }

  function handleRestore(): void {
    revokePreview();
    action = 'keep';
    errorMsg = '';
  }

  function revokePreview(): void {
    if (pendingPreviewUrl) {
      URL.revokeObjectURL(pendingPreviewUrl);
    }
    pendingFile = null;
    pendingMediaType = '';
    pendingPreviewUrl = null;
  }

  /**
   * Crop an image file to a square (centred) and return a JPEG Blob.
   * The canvas always outputs JPEG so the server receives a known type.
   */
  async function cropToSquare(file: File): Promise<Blob> {
    return new Promise((resolve, reject) => {
      const img = new Image();
      let objUrl = '';
      img.onload = () => {
        const side = Math.min(img.naturalWidth, img.naturalHeight);
        const canvas = document.createElement('canvas');
        canvas.width = side;
        canvas.height = side;
        const ctx = canvas.getContext('2d');
        if (!ctx) {
          URL.revokeObjectURL(objUrl);
          reject(new Error('No 2D context'));
          return;
        }
        const dx = (img.naturalWidth - side) / 2;
        const dy = (img.naturalHeight - side) / 2;
        ctx.drawImage(img, dx, dy, side, side, 0, 0, side, side);
        canvas.toBlob(
          (blob) => {
            URL.revokeObjectURL(objUrl);
            if (blob) resolve(blob);
            else reject(new Error('Canvas toBlob returned null'));
          },
          'image/jpeg',
          0.92,
        );
      };
      img.onerror = () => {
        URL.revokeObjectURL(objUrl);
        reject(new Error('Image load failed'));
      };
      objUrl = URL.createObjectURL(file);
      img.src = objUrl;
    });
  }
</script>

<div class="photo-editor">
  <!-- Drop target wraps the avatar so the full circle accepts drops. -->
  <div
    class="drop-zone"
    class:drag-over={dragOver}
    role="button"
    tabindex={disabled ? -1 : 0}
    aria-label={t('contacts.edit.photo.changeLabel')}
    ondragover={handleDragOver}
    ondragleave={handleDragLeave}
    ondrop={handleDrop}
    onclick={disabled ? undefined : openPicker}
    onkeydown={(e) => {
      if (!disabled && (e.key === 'Enter' || e.key === ' ')) {
        e.preventDefault();
        openPicker();
      }
    }}
  >
    {#if action === 'replace' && pendingPreviewUrl}
      <!-- Pending replacement preview. -->
      <img
        src={pendingPreviewUrl}
        alt={t('contacts.edit.photo.previewAlt')}
        class="avatar-preview"
        aria-hidden="true"
      />
    {:else if action === 'keep' && displayBlobId}
      <!-- Current server photo. -->
      <ContactAvatar
        blobId={displayBlobId}
        {fallbackInitial}
        size={80}
      />
    {:else}
      <!-- Monogram placeholder (no photo or photo removed). -->
      <span class="avatar-monogram" aria-hidden="true">
        {fallbackInitial.slice(0, 1).toUpperCase() || '?'}
      </span>
    {/if}

    <span class="change-hint" aria-hidden="true">
      {t('contacts.edit.photo.changeHint')}
    </span>
  </div>

  <!-- Hidden file input (click triggered by openPicker). -->
  <input
    bind:this={fileInput}
    type="file"
    accept="image/jpeg,image/png,image/gif,image/webp,image/avif"
    class="sr-only"
    oninput={handleFileInput}
    {disabled}
    aria-hidden="true"
    tabindex="-1"
  />

  <!-- Action buttons. -->
  <div class="photo-actions">
    {#if action === 'replace' || (action === 'keep' && currentBlobId)}
      <button
        type="button"
        class="action-btn remove"
        onclick={handleRemove}
        {disabled}
        aria-label={t('contacts.edit.photo.removeLabel')}
      >
        {t('contacts.edit.photo.remove')}
      </button>
    {/if}
    {#if action !== 'keep'}
      <button
        type="button"
        class="action-btn restore"
        onclick={handleRestore}
        {disabled}
      >
        {t('contacts.edit.photo.restore')}
      </button>
    {/if}
  </div>

  {#if errorMsg}
    <p class="photo-error" role="alert">{errorMsg}</p>
  {/if}
</div>

<style>
  .photo-editor {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--spacing-02);
  }

  /* Drop zone wraps the avatar circle. */
  .drop-zone {
    position: relative;
    width: 80px;
    height: 80px;
    border-radius: var(--radius-pill);
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: box-shadow var(--duration-fast-01) var(--easing-productive-enter);
  }

  .drop-zone:focus {
    outline: 2px solid var(--focus);
    outline-offset: 3px;
    border-radius: var(--radius-pill);
  }

  .drop-zone:hover .change-hint,
  .drop-zone:focus .change-hint {
    opacity: 1;
  }

  .drop-zone.drag-over {
    box-shadow: 0 0 0 3px var(--interactive);
  }

  /* Monogram placeholder circle. */
  .avatar-monogram {
    width: 80px;
    height: 80px;
    border-radius: var(--radius-pill);
    background: var(--interactive);
    color: var(--text-on-color);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 2rem;
    font-weight: 600;
    flex-shrink: 0;
    overflow: hidden;
  }

  /* Preview image (pending replacement). */
  .avatar-preview {
    width: 80px;
    height: 80px;
    border-radius: var(--radius-pill);
    object-fit: cover;
    display: block;
    flex-shrink: 0;
  }

  /* Overlay hint label on hover/focus. */
  .change-hint {
    position: absolute;
    bottom: -20px;
    left: 50%;
    transform: translateX(-50%);
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    white-space: nowrap;
    pointer-events: none;
    opacity: 0;
    transition: opacity var(--duration-fast-01) var(--easing-productive-enter);
  }

  /* Action buttons row. */
  .photo-actions {
    display: flex;
    gap: var(--spacing-02);
    margin-top: var(--spacing-04);
    flex-wrap: wrap;
  }

  .action-btn {
    padding: 2px var(--spacing-03);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    line-height: 1.4;
    border: 1px solid;
    background: transparent;
  }

  .action-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .action-btn.remove {
    color: var(--support-error);
    border-color: var(--support-error);
  }

  .action-btn.remove:hover:not(:disabled) {
    background: var(--support-error-bg, rgba(218, 30, 40, 0.1));
  }

  .action-btn.restore {
    color: var(--text-secondary);
    border-color: var(--border-subtle-01);
  }

  .action-btn.restore:hover:not(:disabled) {
    background: var(--layer-02);
  }

  /* Validation error message. */
  .photo-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    margin: 0;
    max-width: 320px;
    line-height: 1.4;
  }

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
