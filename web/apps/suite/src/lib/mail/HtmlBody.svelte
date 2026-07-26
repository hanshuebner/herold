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
   *   - sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
   *     with NO allow-scripts: scripts in mail are inert; the parent
   *     can still read contentDocument to auto-size the iframe. The
   *     two allow-popups tokens are required for <a target="_blank">
   *     clicks to open a real top-level browser tab — without them
   *     the browser silently blocks the navigation, which the user
   *     sees as "links don't open" (issue #103). allow-popups-to-
   *     escape-sandbox ensures the new tab is NOT itself sandboxed,
   *     so the destination page works normally.
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
   *
   * Undecodable inline images (issue #269, REQ-ATT-26 / REQ-MAIL-21):
   *   Resolved cid: images the browser cannot actually decode (a TIFF pasted
   *   into an Apple Mail signature is the reproduction case; browsers do not
   *   render TIFF in <img>) fire an `error` event or settle with zero
   *   natural dimensions. `wireInlineImageDecodeChecks()` reports that
   *   outcome to the parent (`onImageDecodeFailed`/`onImageDecodeSucceeded`),
   *   which folds it into the attachment chip strip (AttachmentList.svelte)
   *   so the original bytes stay reachable via a download chip even though
   *   the image itself never displays.
   */
  import { sanitizeHtml } from './sanitize';
  import { findScrollParent } from './scroll-parent';
  import { t } from '../i18n/i18n.svelte';
  import { inlineImageDecodeStatus } from './image-decode';

  interface Props {
    html: string;
    loadImages?: boolean;
    /** cid -> URL map for inline images. */
    cidMap?: Record<string, string>;
    /**
     * Server-supplied intrinsic pixel dimensions keyed by cid (no angle
     * brackets, no `cid:` prefix). Forwarded to `sanitizeHtml` so it can
     * inject `aspect-ratio: W / H` on resolved cid images, enabling the
     * browser to reserve layout space before image bytes arrive (issue #47).
     */
    cidDimensions?: Record<string, { width: number; height: number }>;
    /**
     * Metadata for the inline images (name + download URL) keyed by the
     * resolved URL that appears in the rendered img src. Used to wire the
     * overlay download buttons to the correct blob URL.
     */
    inlineImageMeta?: Record<string, { name: string; downloadUrl: string }>;
    /**
     * True while the server's background internalize worker has not yet
     * finished this message (REQ-EXTIMG-BG-INTERNAL-40). Forwarded to
     * `sanitizeHtml` so its iframe stylesheet only sizes the "images
     * pending" placeholder box for messages that are actually still
     * pending -- otherwise a genuine already-internalized tracking pixel
     * whose bytes coincidentally share the placeholder's prefix gets
     * forced into an oversized gray block (issue #160).
     */
    internalizePending?: boolean;
    /**
     * Called (issue #269) when an inline image resolved via `inlineImageMeta`
     * fires an `error` event, or finishes loading with zero natural
     * dimensions -- i.e. the browser could not decode it. Keyed by the same
     * resolved URL used as `inlineImageMeta`'s key. The caller
     * (MessageAccordion.svelte) folds this into the set of inline images
     * surfaced as download chips (REQ-ATT-26 / REQ-MAIL-21).
     */
    onImageDecodeFailed?: (url: string) => void;
    /**
     * Called (issue #269) when an inline image resolved via `inlineImageMeta`
     * finishes loading with non-zero natural dimensions -- i.e. the browser
     * decoded it successfully. This overrides any earlier decode-failed
     * signal (including the static content-type hint computed by the
     * caller), so an image type that is statically flagged but actually
     * renders in this browser (e.g. Safari's native TIFF support) does not
     * keep a stale chip.
     */
    onImageDecodeSucceeded?: (url: string) => void;
  }
  let {
    html,
    loadImages = false,
    cidMap,
    cidDimensions,
    inlineImageMeta,
    internalizePending = false,
    onImageDecodeFailed,
    onImageDecodeSucceeded,
  }: Props = $props();

  let frameEl = $state<HTMLIFrameElement | null>(null);
  let wrapperEl = $state<HTMLDivElement | null>(null);
  let height = $state(120);

  /**
   * Latches to true the first time recomputeHeight() produces a real height
   * inside onLoad's rAF path. Never resets on subsequent srcdoc changes (e.g.
   * "Load images") so the already-visible body is not re-hidden behind a
   * spinner on user actions.
   */
  let measured = $state(false);

  /**
   * Flips to true ~100 ms after mount if the first height measurement has
   * not yet arrived, so fast loads show nothing and slow ones show a spinner.
   * Cleared immediately when the measurement lands.
   */
  let showSpinner = $state(false);

  /**
   * setTimeout handle, stored so recomputeHeight() can cancel it before it
   * fires when measurement arrives quickly (avoiding a brief spinner flash).
   * Not reactive — it is a plain timer ID.
   */
  let spinnerTimer: ReturnType<typeof setTimeout> | null = null;

  // Start the 100ms spinner delay once on component mount. This $effect has no
  // synchronous reactive reads so Svelte only runs it once. The setTimeout
  // callback executes outside the reactive tracking context, so reading
  // `measured` inside it does not register a dependency and cannot loop.
  $effect(() => {
    spinnerTimer = setTimeout(() => {
      spinnerTimer = null;
      if (!measured) showSpinner = true;
    }, 100);
    return () => {
      if (spinnerTimer !== null) {
        clearTimeout(spinnerTimer);
        spinnerTimer = null;
      }
    };
  });

  // Re-sanitise whenever inputs change (e.g. user clicks "Load images").
  let srcdoc = $derived(
    sanitizeHtml(html, { loadImages, cidMap, cidDimensions, internalizePending }),
  );

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

  /**
   * Inline images already checked this srcdoc load, so the load/error
   * listeners attached below are not re-attached on every recomputeHeight()
   * call (the body ResizeObserver can fire many times per load).
   */
  let decodeCheckedImages = new WeakSet<HTMLImageElement>();

  /**
   * Wire the runtime undecodable-image signal (issue #269): for every
   * resolved inline `<img>` the overlay also tracks (via `inlineImageMeta`),
   * report its decode outcome once it settles. Called once per iframe load
   * from onLoad() — the images themselves are fresh elements on every
   * srcdoc replacement, so there is nothing to unwire.
   */
  function wireInlineImageDecodeChecks(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    if (!inlineImageMeta || Object.keys(inlineImageMeta).length === 0) return;
    for (const img of doc.querySelectorAll<HTMLImageElement>('img[src]')) {
      const src = img.getAttribute('src');
      if (!src || !inlineImageMeta[src]) continue;
      if (decodeCheckedImages.has(img)) continue;
      decodeCheckedImages.add(img);

      const report = (): void => {
        const status = inlineImageDecodeStatus(img);
        if (status === 'success') onImageDecodeSucceeded?.(src);
        else if (status === 'failure') onImageDecodeFailed?.(src);
        // 'pending' — a later load/error event will report the real outcome.
      };
      if (img.complete) {
        report();
      } else {
        img.addEventListener('load', report, { once: true });
      }
      // A decode failure that the browser surfaces as a load error (rather
      // than a "loaded but zero-dimension" image) still needs an explicit
      // failure report — `complete` can be true with naturalWidth 0 in that
      // case too, but not every browser guarantees it, so the event is the
      // authoritative failure signal on top of the naturalWidth check.
      img.addEventListener('error', () => onImageDecodeFailed?.(src), { once: true });
    }
  }

  function recomputeHeight(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    // scrollHeight is an integer and can round down by up to a pixel
    // relative to the true (fractional) layout height, shaving a sliver
    // off the last line's descenders. getBoundingClientRect().height
    // reports the fractional value; take whichever of the two is taller,
    // then round up so the +8 buffer is never eaten by the rounding
    // itself (issue #158).
    const measuredHeight = Math.max(
      doc.body.scrollHeight,
      doc.body.getBoundingClientRect().height,
    );
    height = Math.max(Math.ceil(measuredHeight) + 8, 120);
    // Latch on the first real measurement: cancel the spinner timer, hide the
    // spinner, and reveal the iframe. Subsequent calls (from the body
    // ResizeObserver, image `load` events, or details `toggle` events) still
    // update `height` so the iframe tracks content growth/shrinkage — the
    // `measured` branch simply does not run again.
    if (!measured) {
      measured = true;
      showSpinner = false;
      if (spinnerTimer !== null) {
        clearTimeout(spinnerTimer);
        spinnerTimer = null;
      }
    }
    computeOverlay();
  }

  function onLoad(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    // Fresh document -> fresh <img> elements; nothing from the previous
    // srcdoc load can still be tracked.
    decodeCheckedImages = new WeakSet<HTMLImageElement>();
    wireInlineImageDecodeChecks();
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

    // Wheel-forwarding fix (issue #51): the iframe is always full-height
    // (its CSS height equals its content scrollHeight) so it has no
    // internal scroll. On macOS with a Magic Mouse, the first scroll
    // gesture nevertheless targets the iframe's browsing context and the
    // browser applies an elastic rubber-band animation there, absorbing
    // the gesture without scrolling the outer thread container. The second
    // gesture then scrolls the outer container normally.
    //
    // Fix: intercept wheel events inside the iframe document, cancel the
    // default action (suppressing the rubber-band), and forward the delta
    // directly to the nearest scrollable ancestor of the wrapper element.
    // { passive: false } is required to be able to call preventDefault().
    // The listener is attached to the iframe's document so it is
    // automatically released when the document is replaced (srcdoc change).
    const scrollParent = wrapperEl ? findScrollParent(wrapperEl) : null;
    if (scrollParent) {
      const sp = scrollParent;
      doc.addEventListener(
        'wheel',
        (e: WheelEvent) => {
          e.preventDefault();
          // Normalise delta to pixels so scrollBy is correct for all
          // pointer devices. Magic Mouse always emits DOM_DELTA_PIXEL;
          // traditional scroll wheels typically emit DOM_DELTA_LINE.
          let dy = e.deltaY;
          let dx = e.deltaX;
          if (e.deltaMode === WheelEvent.DOM_DELTA_LINE) {
            // 16 px per line matches the Chrome/Firefox default line height
            // used for line-delta wheel events.
            dy *= 16;
            dx *= 16;
          } else if (e.deltaMode === WheelEvent.DOM_DELTA_PAGE) {
            dy *= sp.clientHeight;
            dx *= sp.clientWidth;
          }
          sp.scrollBy({ top: dy, left: dx, behavior: 'instant' });
        },
        { passive: false },
      );
    }
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

<div class="frame-wrapper" bind:this={wrapperEl} style:min-height={measured ? undefined : '120px'}>
  {#if !measured && showSpinner}
    <!--
      Spinner overlay: shown only when the first height measurement has not
      arrived within ~100 ms. The iframe stays in the layout (opacity 0) so
      contentDocument / scrollHeight remain measurable and onload fires
      normally. The overlay is pointer-events: none so it does not interfere.
    -->
    <div class="body-spinner-wrap">
      <div
        class="body-spinner"
        role="status"
        aria-label={t('msg.body.renderingAria')}
      ></div>
    </div>
  {/if}
  <iframe
    bind:this={frameEl}
    sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
    {srcdoc}
    referrerpolicy="no-referrer"
    loading="lazy"
    title="Message body"
    style:height="{height}px"
    class:body-hidden={!measured}
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

  /*
   * While !measured the iframe is invisible but still in the layout so the
   * browser can render its content and we can read contentDocument.scrollHeight.
   * pointer-events: none prevents any stray interactions before reveal.
   */
  iframe.body-hidden {
    opacity: 0;
    pointer-events: none;
  }

  /* Spinner overlay: centred over the invisible iframe while it loads. */
  .body-spinner-wrap {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    pointer-events: none;
  }

  .body-spinner {
    width: 24px;
    height: 24px;
    border: 2px solid var(--layer-02, #e0e0e0);
    border-top-color: var(--interactive, #0f62fe);
    border-radius: 50%;
    animation: body-spin 800ms linear infinite;
  }

  @keyframes body-spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .body-spinner {
      animation: none;
    }
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
