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
   */
  import { sanitizeHtml } from './sanitize';

  interface Props {
    html: string;
    loadImages?: boolean;
  }
  let { html, loadImages = false }: Props = $props();

  let frameEl = $state<HTMLIFrameElement | null>(null);
  let height = $state(120);

  // Re-sanitise whenever inputs change (e.g. user clicks "Load images").
  let srcdoc = $derived(sanitizeHtml(html, { loadImages }));

  function onLoad(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    requestAnimationFrame(() => {
      if (!doc.body) return;
      height = Math.max(doc.body.scrollHeight + 8, 120);
    });
  }
</script>

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

<style>
  iframe {
    width: 100%;
    border: none;
    background: var(--background);
    display: block;
  }
</style>
