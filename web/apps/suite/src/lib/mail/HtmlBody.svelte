<script lang="ts">
  /**
   * Renders an HTML email body inside a sandboxed iframe per
   * docs/architecture/04-rendering.md and 13-nonfunctional.md
   * REQ-SEC-04..07.
   *
   * Layered defence:
   *   - DOMPurify strips dangerous tags / attrs / URL schemes (sanitize.ts).
   *   - <a> rewritten to target="_blank" rel="noopener noreferrer".
   *   - <img>: cid: blocked; http(s) blocked unless loadImages=true; when
   *     loaded, src rewritten through /proxy/image so the recipient's
   *     IP / cookies don't reach the sender.
   *   - srcdoc carries an inline CSP (default-src 'none'; img-src 'self'
   *     data:; style-src 'unsafe-inline').
   *   - sandbox="allow-same-origin" with NO allow-scripts: scripts in
   *     mail are inert; the parent can still read contentDocument to
   *     auto-size the iframe.
   *
   * G16 inline-image overlay (REQ-ATT-26):
   *   After the iframe loads we scan its contentDocument for <img> elements
   *   that have a resolved src (i.e. cid: images that were successfully
   *   mapped to blob URLs). For each we track the image's bounding box
   *   relative to the wrapper and render a download button in an absolutely-
   *   positioned overlay layer. A ResizeObserver keeps the overlay in sync
   *   when the iframe reflowsoverlay.
   *
   *   This approach avoids injecting DOM into the sandboxed document while
   *   still giving the user a single-action download per inline image.
   */
  import { sanitizeHtml } from './sanitize';

  interface Props {
    html: string;
    loadImages?: boolean;
    /** cid -> URL map for inline images. */
    cidMap?: Record<string, string>;
    /**
     * Metadata for the inline images (name + download URL) keyed by the
     * resolved URL that appears in the rendered img src. Used to wire the
     * overlay download buttons to the correct blob URL.
     */
    inlineImageMeta?: Record<string, { name: string; downloadUrl: string }>;
  }
  let { html, loadImages = false, cidMap, inlineImageMeta }: Props = $props();

  let frameEl = $state<HTMLIFrameElement | null>(null);
  let wrapperEl = $state<HTMLDivElement | null>(null);
  let height = $state(120);

  // Re-sanitise whenever inputs change (e.g. user clicks "Load images").
  let srcdoc = $derived(sanitizeHtml(html, { loadImages, cidMap }));

  /**
   * One overlay entry: position relative to the wrapper element, plus
   * the download URL and filename.
   */
  interface OverlayButton {
    top: number;
    left: number;
    width: number;
    height: number;
    downloadUrl: string;
    name: string;
  }
  let overlayButtons = $state<OverlayButton[]>([]);

  function computeOverlay(): void {
    const frame = frameEl;
    const wrapper = wrapperEl;
    if (!frame || !wrapper) {
      overlayButtons = [];
      return;
    }
    const doc = frame.contentDocument;
    if (!doc?.body) {
      overlayButtons = [];
      return;
    }
    if (!inlineImageMeta || Object.keys(inlineImageMeta).length === 0) {
      overlayButtons = [];
      return;
    }
    const wrapperRect = wrapper.getBoundingClientRect();
    const frameRect = frame.getBoundingClientRect();
    const frameOffsetTop = frameRect.top - wrapperRect.top;
    const frameOffsetLeft = frameRect.left - wrapperRect.left;

    const buttons: OverlayButton[] = [];
    for (const img of doc.querySelectorAll<HTMLImageElement>('img[src]')) {
      const src = img.getAttribute('src');
      if (!src) continue;
      const meta = inlineImageMeta[src];
      if (!meta) continue;
      const imgRect = img.getBoundingClientRect();
      if (imgRect.width === 0 || imgRect.height === 0) continue;
      buttons.push({
        top: frameOffsetTop + imgRect.top - frameRect.top + (frame.contentWindow?.scrollY ?? 0),
        left: frameOffsetLeft + imgRect.left - frameRect.left,
        width: imgRect.width,
        height: imgRect.height,
        downloadUrl: meta.downloadUrl,
        name: meta.name,
      });
    }
    overlayButtons = buttons;
  }

  function recomputeHeight(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    height = Math.max(doc.body.scrollHeight + 8, 120);
    computeOverlay();
  }

  function onLoad(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    requestAnimationFrame(() => {
      recomputeHeight();
    });
    // Listen for `<details>` toggles + image loads as a coarse hook to
    // re-measure when content sizes change. The authoritative signal is
    // the inner-body ResizeObserver below — these listeners just
    // shorten the perceived latency on the user's first toggle by
    // queueing an immediate re-measure on the next frame.
    doc.addEventListener(
      'toggle',
      () => requestAnimationFrame(recomputeHeight),
      true,
    );
    doc.addEventListener(
      'load',
      () => requestAnimationFrame(recomputeHeight),
      true,
    );
    // Authoritative source of "the body grew/shrank": observe doc.body
    // directly. The previous approach watched the iframe's outer element,
    // which never changes size on inner reflow — so opening a trimmed
    // <details> grew scrollHeight without ever firing the observer, and
    // the iframe stayed pinned to its old height (the trimmed quote
    // hung off the bottom and the action toolbar overlapped it).
    bodyResizeObserver?.disconnect();
    bodyResizeObserver = new ResizeObserver(() => {
      recomputeHeight();
    });
    bodyResizeObserver.observe(doc.body);
  }

  // Outer-iframe observer: keeps the inline-image overlay aligned when
  // the iframe element's own bounding rect changes (parent layout shift).
  // The body size observer above owns the iframe height re-measurement.
  let resizeObserver: ResizeObserver | null = null;
  let bodyResizeObserver: ResizeObserver | null = null;
  $effect(() => {
    const frame = frameEl;
    if (!frame) return;
    resizeObserver?.disconnect();
    resizeObserver = new ResizeObserver(() => {
      computeOverlay();
    });
    resizeObserver.observe(frame);
    return () => {
      resizeObserver?.disconnect();
      resizeObserver = null;
      bodyResizeObserver?.disconnect();
      bodyResizeObserver = null;
    };
  });
</script>

<div class="frame-wrapper" bind:this={wrapperEl}>
  <iframe
    bind:this={frameEl}
    sandbox="allow-same-origin"
    {srcdoc}
    referrerpolicy="no-referrer"
    loading="lazy"
    title="Message body"
    style:height="{height}px"
    onload={onLoad}
  ></iframe>

  <!-- Overlay layer: positioned absolutely over the iframe. Each button
       sits over the matching <img> in the iframe body (G16). The overlay
       pointer-events are 'none' by default; individual buttons opt in. -->
  {#if overlayButtons.length > 0}
    <div class="overlay" aria-hidden="true" style:height="{height}px">
      {#each overlayButtons as btn, i (i)}
        <!-- svelte-ignore a11y_missing_attribute -->
        <a
          class="img-download"
          href={btn.downloadUrl}
          download={btn.name}
          aria-label="Download {btn.name}"
          title="Download {btn.name}"
          style:top="{btn.top}px"
          style:left="{btn.left}px"
          style:width="{btn.width}px"
          style:height="{btn.height}px"
        >
          <span class="img-download-icon" aria-hidden="true">&#11015;</span>
        </a>
      {/each}
    </div>
  {/if}
</div>

<style>
  .frame-wrapper {
    position: relative;
  }
  iframe {
    width: 100%;
    border: none;
    background: var(--background);
    display: block;
  }

  /* Overlay layer covering the iframe; pointer-events are none so the
     iframe remains interactive, but the child .img-download buttons opt in. */
  .overlay {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    pointer-events: none;
    overflow: hidden;
  }

  /* Individual download button: covers the matching image in the iframe.
     Transparent at rest; shows a download icon badge on hover/focus.
     The top-right badge approach: the icon is positioned top-right
     inside the anchor. */
  .img-download {
    position: absolute;
    pointer-events: all;
    display: block;
    /* The button itself is transparent to preserve the image view */
    background: transparent;
    border: none;
    text-decoration: none;
  }
  .img-download-icon {
    position: absolute;
    top: var(--spacing-02, 8px);
    right: var(--spacing-02, 8px);
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.55);
    color: #fff;
    border-radius: 50%;
    font-size: 14px;
    opacity: 0;
    transition: opacity 0.15s ease;
    pointer-events: none;
  }
  .img-download:hover .img-download-icon,
  .img-download:focus-visible .img-download-icon {
    opacity: 1;
  }
  .img-download:focus-visible {
    outline: 2px solid var(--focus, #0f62fe);
    outline-offset: 2px;
    border-radius: 2px;
  }
</style>
