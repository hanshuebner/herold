<script lang="ts">
  /**
   * ManualPage renders a single chapter's Markdoc AST.
   *
   * The renderer switches on node.name for each Markdoc tag and maps it
   * to the matching Svelte component.  Structural nodes (headings, lists,
   * paragraphs, etc.) are rendered via plain HTML elements -- never via
   * {@html} for markup injection.  Only text-leaf content is rendered as
   * text nodes, which Svelte auto-escapes.
   *
   * Markdoc (without a custom heading schema) produces h1..h6 tag nodes
   * directly, not a generic "heading" node with a level attribute.
   */
  import type { RenderableTreeNode } from '@markdoc/markdoc';
  import type { ManualChapter } from '../markdoc/bundle.js';
  import { isTag, isText, children, attr, textContent, slugify } from '../markdoc/render.js';
  import Callout from './tags/Callout.svelte';
  import CodeGroup from './tags/CodeGroup.svelte';
  import IncludedCode from './tags/IncludedCode.svelte';
  import Req from './tags/Req.svelte';
  import Kbd from './tags/Kbd.svelte';

  interface Props {
    chapter: ManualChapter;
    t: (key: string) => string;
  }

  const { chapter, t }: Props = $props();
</script>

<!--
  Render the chapter AST.  The recursive snippet keeps ManualPage as a single
  file (no dynamic component import required).
-->
{#snippet node(n: RenderableTreeNode)}
  {#if isText(n)}
    {n}
  {:else if isTag(n)}
    {#if n.name === 'article' || n.name === 'document'}
      {#each children(n) as child}
        {@render node(child)}
      {/each}
    {:else if n.name === 'h1'}
      {@const id = slugify(textContent(n))}
      <h1 {id}>{#each children(n) as child}{@render node(child)}{/each}</h1>
    {:else if n.name === 'h2'}
      {@const id = slugify(textContent(n))}
      <h2 {id}>{#each children(n) as child}{@render node(child)}{/each}</h2>
    {:else if n.name === 'h3'}
      {@const id = slugify(textContent(n))}
      <h3 {id}>{#each children(n) as child}{@render node(child)}{/each}</h3>
    {:else if n.name === 'h4'}
      {@const id = slugify(textContent(n))}
      <h4 {id}>{#each children(n) as child}{@render node(child)}{/each}</h4>
    {:else if n.name === 'h5'}
      {@const id = slugify(textContent(n))}
      <h5 {id}>{#each children(n) as child}{@render node(child)}{/each}</h5>
    {:else if n.name === 'h6'}
      {@const id = slugify(textContent(n))}
      <h6 {id}>{#each children(n) as child}{@render node(child)}{/each}</h6>
    {:else if n.name === 'p'}
      <p>{#each children(n) as child}{@render node(child)}{/each}</p>
    {:else if n.name === 'strong'}
      <strong>{#each children(n) as child}{@render node(child)}{/each}</strong>
    {:else if n.name === 'em'}
      <em>{#each children(n) as child}{@render node(child)}{/each}</em>
    {:else if n.name === 'code'}
      <code class="inline-code">{#each children(n) as child}{@render node(child)}{/each}</code>
    {:else if n.name === 'fence'}
      {@const lang = attr<string>(n, 'language') ?? 'text'}
      {@const content = attr<string>(n, 'content') ?? children(n).filter(isText).join('')}
      <IncludedCode file="" {lang} {content} {t} />
    {:else if n.name === 'blockquote'}
      <blockquote>{#each children(n) as child}{@render node(child)}{/each}</blockquote>
    {:else if n.name === 'ul'}
      <ul>{#each children(n) as child}{@render node(child)}{/each}</ul>
    {:else if n.name === 'ol'}
      <ol>{#each children(n) as child}{@render node(child)}{/each}</ol>
    {:else if n.name === 'li'}
      <li>{#each children(n) as child}{@render node(child)}{/each}</li>
    {:else if n.name === 'a'}
      {@const href = attr<string>(n, 'href') ?? '#'}
      <a {href}>{#each children(n) as child}{@render node(child)}{/each}</a>
    {:else if n.name === 'img'}
      {@const src = attr<string>(n, 'src') ?? ''}
      {@const alt = attr<string>(n, 'alt') ?? ''}
      <img {src} {alt} loading="lazy" />
    {:else if n.name === 'hr'}
      <hr />
    {:else if n.name === 'br'}
      <br />
    {:else if n.name === 'table'}
      <table>{#each children(n) as child}{@render node(child)}{/each}</table>
    {:else if n.name === 'thead'}
      <thead>{#each children(n) as child}{@render node(child)}{/each}</thead>
    {:else if n.name === 'tbody'}
      <tbody>{#each children(n) as child}{@render node(child)}{/each}</tbody>
    {:else if n.name === 'tr'}
      <tr>{#each children(n) as child}{@render node(child)}{/each}</tr>
    {:else if n.name === 'th'}
      <th>{#each children(n) as child}{@render node(child)}{/each}</th>
    {:else if n.name === 'td'}
      <td>{#each children(n) as child}{@render node(child)}{/each}</td>
    {:else if n.name === 'Callout'}
      {@const calloutKids = children(n)}
      <Callout
        type={attr<'info' | 'warning' | 'caution'>(n, 'type')}
        title={attr<string>(n, 'title')}
        {t}
      >
        {#snippet children()}
          {#each calloutKids as child}{@render node(child)}{/each}
        {/snippet}
      </Callout>
    {:else if n.name === 'CodeGroup'}
      {@const groupKids = children(n)}
      <CodeGroup>
        {#snippet children()}
          {#each groupKids as child}{@render node(child)}{/each}
        {/snippet}
      </CodeGroup>
    {:else if n.name === 'IncludedCode'}
      <IncludedCode
        file={attr<string>(n, 'file') ?? ''}
        lang={attr<string>(n, 'lang')}
        content={attr<string>(n, 'content')}
        {t}
      />
    {:else if n.name === 'Req'}
      <Req id={attr<string>(n, 'id') ?? ''} {t} />
    {:else if n.name === 'Kbd'}
      <Kbd keys={attr<string>(n, 'keys') ?? ''} />
    {:else}
      <!-- Unknown tag: render children so content is not silently dropped -->
      {#each children(n) as child}{@render node(child)}{/each}
    {/if}
  {/if}
{/snippet}

<article class="manual-page" data-slug={chapter.slug}>
  {@render node(chapter.ast)}
</article>

<style>
  .manual-page {
    max-width: 72ch;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
  }

  .manual-page :global(h1),
  .manual-page :global(h2),
  .manual-page :global(h3),
  .manual-page :global(h4) {
    color: var(--text-primary);
    font-weight: 600;
    margin: var(--spacing-07) 0 var(--spacing-04);
    scroll-margin-top: var(--spacing-07);
  }

  .manual-page :global(h1) {
    font-size: 28px;
    line-height: 36px;
    margin-top: 0;
  }

  .manual-page :global(h2) {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    border-bottom: 1px solid var(--border-subtle-01);
    padding-bottom: var(--spacing-03);
  }

  .manual-page :global(h3) {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
  }

  .manual-page :global(p) {
    margin: 0 0 var(--spacing-05);
    color: var(--text-secondary);
  }

  .manual-page :global(ul),
  .manual-page :global(ol) {
    margin: 0 0 var(--spacing-05);
    padding-left: var(--spacing-06);
    color: var(--text-secondary);
  }

  .manual-page :global(li) {
    margin-bottom: var(--spacing-02);
  }

  .manual-page :global(a) {
    color: var(--interactive);
    text-decoration: underline;
  }

  .manual-page :global(a:hover) {
    opacity: 0.8;
  }

  .manual-page :global(.inline-code) {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    padding: 0 var(--spacing-02);
    color: var(--text-primary);
  }

  .manual-page :global(blockquote) {
    border-left: 4px solid var(--border-strong-01);
    margin: var(--spacing-05) 0;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-secondary);
    font-style: italic;
  }

  .manual-page :global(hr) {
    border: none;
    border-top: 1px solid var(--border-subtle-01);
    margin: var(--spacing-07) 0;
  }

  .manual-page :global(table) {
    width: 100%;
    border-collapse: collapse;
    margin: var(--spacing-05) 0;
    font-size: var(--type-body-compact-01-size);
  }

  .manual-page :global(th),
  .manual-page :global(td) {
    border: 1px solid var(--border-subtle-01);
    padding: var(--spacing-03) var(--spacing-04);
    text-align: left;
  }

  .manual-page :global(th) {
    background: var(--layer-02);
    font-weight: 600;
    color: var(--text-primary);
  }

  .manual-page :global(td) {
    color: var(--text-secondary);
  }

  .manual-page :global(img) {
    max-width: 100%;
    height: auto;
    border-radius: var(--radius-md);
    border: 1px solid var(--border-subtle-01);
  }
</style>
