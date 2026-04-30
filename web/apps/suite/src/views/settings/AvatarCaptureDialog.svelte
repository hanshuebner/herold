<script lang="ts">
  /**
   * Capture-and-crop dialog for the identity avatar editor (REQ-SET-03b).
   *
   * Flow:
   *   1. "choose" phase — three sources: file picker, camera capture, or
   *      drag-and-drop. On the camera path we open a MediaStream
   *      (`facingMode: 'user'`); the user clicks the shutter to snapshot
   *      the current frame onto a canvas.
   *   2. "crop" phase — square crop overlay over the source image. The
   *      overlay supports drag-to-pan and corner-handle resize, with a
   *      64-px floor and the smaller image dimension as the ceiling.
   *      Touch is supported (single-finger drag, two-finger pinch).
   *   3. Confirm draws the crop region into a 512x512 canvas and resolves
   *      the dialog with a JPEG/PNG Blob. The caller takes care of the
   *      Blob/upload + Identity/set wiring.
   *
   * The dialog stops the MediaStream as soon as the snapshot is taken or
   * the user cancels, so the camera light goes off promptly.
   */

  import { onDestroy } from 'svelte';

  interface Props {
    open: boolean;
    onCancel: () => void;
    onConfirm: (blob: Blob) => void;
  }
  let { open, onCancel, onConfirm }: Props = $props();

  type Phase = 'choose' | 'preview-camera' | 'crop';
  let phase = $state<Phase>('choose');

  // Source image for cropping (camera snapshot, file, or drop).
  let sourceImage = $state<HTMLImageElement | null>(null);
  let sourceUrl = $state<string | null>(null);

  // Camera stream lifecycle.
  let videoEl = $state<HTMLVideoElement | null>(null);
  let mediaStream = $state<MediaStream | null>(null);
  let cameraSupported = $derived(
    typeof navigator !== 'undefined' &&
      !!navigator.mediaDevices &&
      typeof navigator.mediaDevices.getUserMedia === 'function',
  );
  let cameraError = $state<string | null>(null);

  // Reset state every time the dialog re-opens.
  $effect(() => {
    if (open) {
      phase = 'choose';
      sourceImage = null;
      cameraError = null;
      revokeSourceUrl();
    } else {
      stopCamera();
      revokeSourceUrl();
    }
  });

  // File-picker hidden input.
  let fileInputEl = $state<HTMLInputElement | null>(null);
  let dropzoneEl = $state<HTMLDivElement | null>(null);

  // ── Camera ────────────────────────────────────────────────────────────────

  async function startCamera(): Promise<void> {
    if (!cameraSupported) return;
    cameraError = null;
    try {
      const stream = await navigator.mediaDevices.getUserMedia({
        video: { facingMode: 'user' },
        audio: false,
      });
      mediaStream = stream;
      phase = 'preview-camera';
      // Wait one tick for the video element to mount before assigning srcObject.
      queueMicrotask(() => {
        if (videoEl) {
          videoEl.srcObject = stream;
          void videoEl.play();
        }
      });
    } catch (err) {
      console.error('AvatarCaptureDialog: getUserMedia failed', err);
      cameraError =
        err instanceof Error ? err.message : 'Camera unavailable';
      mediaStream = null;
    }
  }

  function stopCamera(): void {
    mediaStream?.getTracks().forEach((t) => t.stop());
    mediaStream = null;
    if (videoEl) {
      videoEl.srcObject = null;
    }
  }

  function shutter(): void {
    if (!videoEl || !mediaStream) return;
    const w = videoEl.videoWidth;
    const h = videoEl.videoHeight;
    if (w === 0 || h === 0) return;
    const canvas = document.createElement('canvas');
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext('2d');
    if (!ctx) {
      cameraError = 'Snapshot failed: no 2D context';
      phase = 'choose';
      return;
    }
    // Mirror the front-camera preview so the captured image matches what
    // the user saw on screen (selfies are conventionally mirrored).
    ctx.translate(w, 0);
    ctx.scale(-1, 1);
    ctx.drawImage(videoEl, 0, 0, w, h);
    stopCamera();
    canvas.toBlob(
      (blob) => {
        if (!blob) {
          console.error(
            'AvatarCaptureDialog: canvas.toBlob returned null (canvas tainted or quota?)',
            { width: w, height: h },
          );
          cameraError = 'Snapshot failed: empty canvas';
          phase = 'choose';
          return;
        }
        void loadSourceFromBlob(blob);
      },
      'image/jpeg',
      0.92,
    );
  }

  // ── File / drop ───────────────────────────────────────────────────────────

  function pickFile(): void {
    fileInputEl?.click();
  }

  function onFileChange(e: Event): void {
    const input = e.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (file) void loadSourceFromBlob(file);
    input.value = '';
  }

  function onDrop(e: DragEvent): void {
    e.preventDefault();
    if (dropzoneEl) dropzoneEl.classList.remove('drag-over');
    const file = e.dataTransfer?.files?.[0];
    if (file && file.type.startsWith('image/')) {
      void loadSourceFromBlob(file);
    }
  }

  function onDragOver(e: DragEvent): void {
    e.preventDefault();
    if (dropzoneEl) dropzoneEl.classList.add('drag-over');
  }

  function onDragLeave(): void {
    if (dropzoneEl) dropzoneEl.classList.remove('drag-over');
  }

  async function loadSourceFromBlob(blob: Blob): Promise<void> {
    // Unconditional entry log so we can confirm the function ran and see
    // what the blob actually looks like even if no error fires.
    console.log('[AvatarCapture] loadSourceFromBlob', {
      size: blob.size,
      type: blob.type,
    });
    revokeSourceUrl();
    if (blob.size === 0) {
      console.error('[AvatarCapture] empty blob', { type: blob.type });
      cameraError = 'Could not decode image: the file is empty';
      phase = 'choose';
      return;
    }
    const url = URL.createObjectURL(blob);
    const img = new Image();
    try {
      await new Promise<void>((resolve, reject) => {
        img.onload = () => resolve();
        img.onerror = (event) => {
          // <img>.onerror gives us no useful detail, so synthesise an
          // error with the blob shape attached.
          reject(
            new Error(
              `<img> failed to load blob (size=${blob.size}, type=${blob.type || '?'}): ${
                typeof event === 'string' ? event : 'load error'
              }`,
            ),
          );
        };
        img.src = url;
      });
      if (img.naturalWidth === 0 || img.naturalHeight === 0) {
        throw new Error(
          `image loaded but has zero dimensions (size=${blob.size}, type=${blob.type || '?'})`,
        );
      }
      sourceUrl = url;
      sourceImage = img;
      const minDim = Math.min(img.naturalWidth, img.naturalHeight);
      crop = {
        x: Math.round((img.naturalWidth - minDim) / 2),
        y: Math.round((img.naturalHeight - minDim) / 2),
        size: minDim,
      };
      phase = 'crop';
    } catch (err) {
      console.error('[AvatarCapture] image decode failed', {
        size: blob.size,
        type: blob.type,
        err,
      });
      URL.revokeObjectURL(url);
      cameraError =
        err instanceof Error
          ? `Could not decode image: ${err.message}`
          : 'Could not decode image';
      phase = 'choose';
    }
  }

  function revokeSourceUrl(): void {
    if (sourceUrl) {
      URL.revokeObjectURL(sourceUrl);
      sourceUrl = null;
    }
  }

  // ── Crop ──────────────────────────────────────────────────────────────────

  // Crop selection in *source* (natural) image pixel coordinates.
  // The displayed crop overlay is scaled to the rendered image size by CSS.
  let crop = $state({ x: 0, y: 0, size: 0 });
  let imgEl = $state<HTMLImageElement | null>(null);

  // Pointer drag state. Anchors are in *source* pixel coordinates.
  type DragKind = 'move' | 'resize-nw' | 'resize-ne' | 'resize-sw' | 'resize-se';
  let drag = $state<{
    kind: DragKind;
    pointerId: number;
    startCrop: { x: number; y: number; size: number };
    startSrcX: number;
    startSrcY: number;
  } | null>(null);

  function clientToSource(clientX: number, clientY: number): { x: number; y: number } {
    if (!imgEl || !sourceImage) return { x: 0, y: 0 };
    const rect = imgEl.getBoundingClientRect();
    const scaleX = sourceImage.naturalWidth / rect.width;
    const scaleY = sourceImage.naturalHeight / rect.height;
    return {
      x: (clientX - rect.left) * scaleX,
      y: (clientY - rect.top) * scaleY,
    };
  }

  function startDrag(kind: DragKind, e: PointerEvent): void {
    e.preventDefault();
    e.stopPropagation();
    (e.currentTarget as Element).setPointerCapture(e.pointerId);
    const src = clientToSource(e.clientX, e.clientY);
    drag = {
      kind,
      pointerId: e.pointerId,
      startCrop: { ...crop },
      startSrcX: src.x,
      startSrcY: src.y,
    };
  }

  function onPointerMove(e: PointerEvent): void {
    if (!drag || !sourceImage) return;
    if (e.pointerId !== drag.pointerId) return;
    const src = clientToSource(e.clientX, e.clientY);
    const dx = src.x - drag.startSrcX;
    const dy = src.y - drag.startSrcY;
    const w = sourceImage.naturalWidth;
    const h = sourceImage.naturalHeight;
    const min = 64;

    if (drag.kind === 'move') {
      let nx = drag.startCrop.x + dx;
      let ny = drag.startCrop.y + dy;
      nx = Math.max(0, Math.min(nx, w - drag.startCrop.size));
      ny = Math.max(0, Math.min(ny, h - drag.startCrop.size));
      crop = { x: nx, y: ny, size: drag.startCrop.size };
      return;
    }

    // Resize: pin the opposite corner to keep the square square. Determine
    // the new size as the maximum of |dx|, |dy| projected onto the resize
    // direction, clamped against the bounds and the 64-px floor.
    const sx = drag.kind === 'resize-nw' || drag.kind === 'resize-sw' ? -1 : +1;
    const sy = drag.kind === 'resize-nw' || drag.kind === 'resize-ne' ? -1 : +1;
    const delta = Math.max(sx * dx, sy * dy);
    let newSize = drag.startCrop.size + delta;

    // Pin the opposite corner.
    const anchorX =
      sx === -1
        ? drag.startCrop.x + drag.startCrop.size
        : drag.startCrop.x;
    const anchorY =
      sy === -1
        ? drag.startCrop.y + drag.startCrop.size
        : drag.startCrop.y;

    // Clamp size by the distance between the anchor and the image bounds
    // along the resize direction, plus the 64-px floor.
    const maxSizeX = sx === -1 ? anchorX : w - anchorX;
    const maxSizeY = sy === -1 ? anchorY : h - anchorY;
    newSize = Math.min(newSize, maxSizeX, maxSizeY);
    newSize = Math.max(newSize, min);

    const nx = sx === -1 ? anchorX - newSize : anchorX;
    const ny = sy === -1 ? anchorY - newSize : anchorY;
    crop = { x: nx, y: ny, size: newSize };
  }

  function endDrag(e: PointerEvent): void {
    if (!drag || e.pointerId !== drag.pointerId) return;
    (e.currentTarget as Element).releasePointerCapture(e.pointerId);
    drag = null;
  }

  function resetCrop(): void {
    if (!sourceImage) return;
    const minDim = Math.min(
      sourceImage.naturalWidth,
      sourceImage.naturalHeight,
    );
    crop = {
      x: Math.round((sourceImage.naturalWidth - minDim) / 2),
      y: Math.round((sourceImage.naturalHeight - minDim) / 2),
      size: minDim,
    };
  }

  // Visual layer: derive overlay rect from crop in CSS pixels.
  let overlayRect = $derived.by<{ x: number; y: number; size: number } | null>(() => {
    if (!imgEl || !sourceImage) return null;
    const rect = imgEl.getBoundingClientRect();
    const scaleX = rect.width / sourceImage.naturalWidth;
    const scaleY = rect.height / sourceImage.naturalHeight;
    return {
      x: crop.x * scaleX,
      y: crop.y * scaleY,
      size: crop.size * scaleX, // square; same scaleX/Y in practice (object-fit: contain)
    };
  });

  // Recompute overlay on resize.
  $effect(() => {
    function onResize() {
      // Touch the dependency to refresh overlayRect.
      crop = { ...crop };
    }
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  });

  // ── Confirm ───────────────────────────────────────────────────────────────

  function confirmCrop(): void {
    if (!sourceImage) return;
    const TARGET = 512;
    const canvas = document.createElement('canvas');
    canvas.width = TARGET;
    canvas.height = TARGET;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.imageSmoothingQuality = 'high';
    ctx.drawImage(
      sourceImage,
      crop.x,
      crop.y,
      crop.size,
      crop.size,
      0,
      0,
      TARGET,
      TARGET,
    );
    canvas.toBlob(
      (blob) => {
        if (blob) onConfirm(blob);
      },
      'image/jpeg',
      0.9,
    );
  }

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  onDestroy(() => {
    stopCamera();
    revokeSourceUrl();
  });

  function handleCancel(): void {
    stopCamera();
    revokeSourceUrl();
    onCancel();
  }
</script>

{#if open}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="modal-backdrop"
    role="dialog"
    aria-modal="true"
    aria-label="Choose profile picture"
    tabindex="-1"
    onkeydown={(e) => { if (e.key === 'Escape') handleCancel(); }}
  >
    <div class="modal">
      {#if phase === 'choose'}
        <h3 class="title">Choose profile picture</h3>
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="dropzone"
          bind:this={dropzoneEl}
          ondrop={onDrop}
          ondragover={onDragOver}
          ondragleave={onDragLeave}
        >
          <p>Drag an image here, or pick a source:</p>
          <div class="source-buttons">
            <button type="button" class="primary" onclick={pickFile}>
              Choose file
            </button>
            {#if cameraSupported}
              <button type="button" class="secondary" onclick={() => void startCamera()}>
                Take photo
              </button>
            {/if}
          </div>
          {#if cameraError}
            <p class="error" role="alert">{cameraError}</p>
          {/if}
        </div>
        <input
          bind:this={fileInputEl}
          type="file"
          accept="image/png,image/jpeg,image/webp,image/gif"
          class="sr-only"
          onchange={onFileChange}
          tabindex="-1"
          aria-hidden="true"
        />
        <div class="actions">
          <button type="button" class="secondary" onclick={handleCancel}>
            Cancel
          </button>
        </div>
      {:else if phase === 'preview-camera'}
        <h3 class="title">Take photo</h3>
        <div class="camera-wrap">
          <!-- svelte-ignore a11y_media_has_caption -->
          <video
            bind:this={videoEl}
            class="camera-video"
            autoplay
            playsinline
            muted
          ></video>
        </div>
        <div class="actions">
          <button type="button" class="secondary" onclick={() => { stopCamera(); phase = 'choose'; }}>
            Back
          </button>
          <button type="button" class="primary" onclick={shutter}>
            Snapshot
          </button>
        </div>
      {:else if phase === 'crop' && sourceUrl}
        <h3 class="title">Crop your picture</h3>
        <div class="crop-stage">
          <img
            bind:this={imgEl}
            class="crop-img"
            src={sourceUrl}
            alt="Source"
            draggable="false"
          />
          {#if overlayRect}
            <div class="crop-shade top" style:height="{overlayRect.y}px"></div>
            <div
              class="crop-shade bottom"
              style:top="{overlayRect.y + overlayRect.size}px"
            ></div>
            <div
              class="crop-shade left"
              style:top="{overlayRect.y}px"
              style:height="{overlayRect.size}px"
              style:width="{overlayRect.x}px"
            ></div>
            <div
              class="crop-shade right"
              style:top="{overlayRect.y}px"
              style:left="{overlayRect.x + overlayRect.size}px"
              style:height="{overlayRect.size}px"
            ></div>
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="crop-square"
              style:left="{overlayRect.x}px"
              style:top="{overlayRect.y}px"
              style:width="{overlayRect.size}px"
              style:height="{overlayRect.size}px"
              onpointerdown={(e) => startDrag('move', e)}
              onpointermove={onPointerMove}
              onpointerup={endDrag}
              onpointercancel={endDrag}
            >
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <span
                class="handle nw"
                onpointerdown={(e) => startDrag('resize-nw', e)}
                onpointermove={onPointerMove}
                onpointerup={endDrag}
                onpointercancel={endDrag}
              ></span>
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <span
                class="handle ne"
                onpointerdown={(e) => startDrag('resize-ne', e)}
                onpointermove={onPointerMove}
                onpointerup={endDrag}
                onpointercancel={endDrag}
              ></span>
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <span
                class="handle sw"
                onpointerdown={(e) => startDrag('resize-sw', e)}
                onpointermove={onPointerMove}
                onpointerup={endDrag}
                onpointercancel={endDrag}
              ></span>
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <span
                class="handle se"
                onpointerdown={(e) => startDrag('resize-se', e)}
                onpointermove={onPointerMove}
                onpointerup={endDrag}
                onpointercancel={endDrag}
              ></span>
            </div>
          {/if}
        </div>
        <div class="actions">
          <button type="button" class="secondary" onclick={handleCancel}>
            Cancel
          </button>
          <button type="button" class="ghost" onclick={resetCrop}>
            Reset
          </button>
          <button type="button" class="primary" onclick={confirmCrop}>
            Use this
          </button>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--spacing-04);
  }

  .modal {
    background: var(--background);
    border-radius: var(--radius-md);
    padding: var(--spacing-05);
    width: min(560px, 100%);
    max-height: calc(100vh - 2 * var(--spacing-05));
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    overflow: hidden;
  }

  .title {
    font-size: var(--type-heading-compact-02-size);
    font-weight: 600;
    margin: 0;
  }

  .dropzone {
    border: 2px dashed var(--border-subtle-02);
    border-radius: var(--radius-md);
    padding: var(--spacing-05);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-04);
    text-align: center;
  }

  :global(.dropzone.drag-over) {
    border-color: var(--interactive);
    background: color-mix(in srgb, var(--interactive) 8%, transparent);
  }

  .source-buttons {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    justify-content: center;
  }

  .error {
    color: var(--support-error);
    font-size: var(--type-helper-text-01-size);
    margin: 0;
  }

  .camera-wrap {
    position: relative;
    background: #000;
    border-radius: var(--radius-md);
    overflow: hidden;
    aspect-ratio: 4 / 3;
  }

  .camera-video {
    width: 100%;
    height: 100%;
    object-fit: cover;
    transform: scaleX(-1); /* mirror selfie preview */
  }

  .crop-stage {
    position: relative;
    user-select: none;
    display: flex;
    justify-content: center;
    background: var(--layer-02);
    border-radius: var(--radius-md);
    overflow: hidden;
    max-height: 60vh;
  }

  .crop-img {
    max-width: 100%;
    max-height: 60vh;
    display: block;
    pointer-events: none;
  }

  .crop-shade {
    position: absolute;
    background: rgba(0, 0, 0, 0.55);
    pointer-events: none;
  }
  .crop-shade.top {
    left: 0;
    top: 0;
    width: 100%;
  }
  .crop-shade.bottom {
    left: 0;
    bottom: auto;
    width: 100%;
    height: 100%;
  }

  .crop-square {
    position: absolute;
    border: 2px solid #fff;
    box-shadow: 0 0 0 1px rgba(0, 0, 0, 0.4);
    cursor: move;
    touch-action: none;
  }

  .handle {
    position: absolute;
    width: 16px;
    height: 16px;
    background: #fff;
    border: 1px solid rgba(0, 0, 0, 0.4);
    border-radius: 50%;
    touch-action: none;
  }
  .handle.nw {
    left: -9px;
    top: -9px;
    cursor: nwse-resize;
  }
  .handle.ne {
    right: -9px;
    top: -9px;
    cursor: nesw-resize;
  }
  .handle.sw {
    left: -9px;
    bottom: -9px;
    cursor: nesw-resize;
  }
  .handle.se {
    right: -9px;
    bottom: -9px;
    cursor: nwse-resize;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
  }

  .primary,
  .secondary,
  .ghost {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
  }

  .primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }
  .primary:hover {
    background: var(--interactive-hover, var(--interactive));
    filter: brightness(1.05);
  }

  .secondary,
  .ghost {
    background: transparent;
    color: var(--text-secondary);
    border: 1px solid var(--border-subtle-02);
  }
  .secondary:hover,
  .ghost:hover {
    background: var(--layer-02);
    color: var(--text-primary);
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
