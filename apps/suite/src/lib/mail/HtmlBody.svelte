<script lang="ts">
  /**
   * Renders an HTML email body inside a sandboxed iframe per
   * docs/architecture/04-rendering.md.
   *
   * sandbox="allow-same-origin": no `allow-scripts`, so script tags in the
   * email body are inert. The `allow-same-origin` token lets the parent
   * (this Svelte component) read the iframe's contentDocument so we can
   * measure the body height and resize the iframe to fit.
   *
   * Sanitisation (DOMPurify, link rewriting, image-proxy rewriting) lands
   * in a follow-up commit. Sandbox is the primary defence; the rewriting
   * layer adds tracking-pixel suppression and outbound-link safety.
   */
  interface Props {
    html: string;
  }
  let { html }: Props = $props();

  let frameEl = $state<HTMLIFrameElement | null>(null);
  let height = $state(120);

  function onLoad(): void {
    const doc = frameEl?.contentDocument;
    if (!doc?.body) return;
    // One animation frame so layout has settled.
    requestAnimationFrame(() => {
      if (!doc.body) return;
      // scrollHeight measures the rendered body; clamp to a sane minimum.
      height = Math.max(doc.body.scrollHeight + 8, 120);
    });
  }
</script>

<iframe
  bind:this={frameEl}
  sandbox="allow-same-origin"
  srcdoc={html}
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
