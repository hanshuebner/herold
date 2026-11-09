<script lang="ts">
  // Renders a plain-text body chunk with URLs as anchors. Segments are
  // emitted as Svelte text and anchor nodes, so message text can never
  // inject markup. Whitespace is preserved by the <pre> below.
  import { splitLinks } from './linkify';

  interface Props {
    text: string;
    quoted?: boolean;
  }
  let { text, quoted = false }: Props = $props();
  let segments = $derived(splitLinks(text));
</script>

<pre class="text-body" class:quoted>{#each segments as seg}{#if seg.kind === 'link'}<a href={seg.href} target="_blank" rel="noopener noreferrer">{seg.text}</a>{:else}{seg.text}{/if}{/each}</pre>

<style>
  /* Per re #233: plain-text and HTML messages must read as one coherent
   * conversation, so the plain-text branch shares the HTML branch's
   * proportional font and un-tinted background instead of presenting its
   * own shaded monospace panel. `white-space: pre-wrap` is kept so
   * authored hard line breaks and indentation (ASCII art, signature
   * blocks, ordinary non-flowed text/plain) still render as authored;
   * only the mono face and grey panel are dropped. `format=flowed`
   * (RFC 3676) bodies arrive from the server already reflowed -- soft
   * breaks joined, quote structure preserved as `> ` prefixes -- so
   * `pre-wrap` on those simply lets the resulting prose rewrap to the
   * pane width like ordinary flowing text (re #261). */
  .text-body {
    margin: 0;
    white-space: pre-wrap;
    word-break: break-word;
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-primary);
    overflow: auto;
  }
  .text-body.quoted {
    color: var(--text-helper);
    margin-top: var(--spacing-03);
  }
</style>
