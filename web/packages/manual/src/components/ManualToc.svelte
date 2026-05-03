<script lang="ts">
  /**
   * ManualToc renders the left-rail table of contents.
   *
   * It receives the full chapter list and the currently active slug, and
   * fires `onNavigate` when the user clicks a chapter link.
   */
  import type { ManualChapter } from '../markdoc/bundle.js';

  interface Props {
    chapters: ManualChapter[];
    currentSlug: string;
    searchQuery: string;
    onNavigate: (slug: string) => void;
    t: (key: string) => string;
  }

  const { chapters, currentSlug, searchQuery, onNavigate, t }: Props = $props();

  /**
   * Filter chapters by the search query against the chapter title only.
   * Body text search is out of scope for v1.
   */
  const visible = $derived(
    searchQuery.trim().length === 0
      ? chapters
      : chapters.filter((ch) =>
          ch.title.toLowerCase().includes(searchQuery.trim().toLowerCase()),
        ),
  );
</script>

<nav class="manual-toc" aria-label={t('manual.toc.label')}>
  <ul class="toc-list" role="list">
    {#each visible as chapter (chapter.slug)}
      <li class="toc-item">
        <button
          type="button"
          class="toc-link"
          class:toc-link--active={chapter.slug === currentSlug}
          aria-current={chapter.slug === currentSlug ? 'page' : undefined}
          onclick={() => onNavigate(chapter.slug)}
        >
          {chapter.title}
        </button>
      </li>
    {/each}
    {#if visible.length === 0}
      <li class="toc-empty" aria-live="polite">
        {t('manual.search.noResults')}
      </li>
    {/if}
  </ul>
</nav>

<style>
  .manual-toc {
    width: 100%;
    overflow-y: auto;
  }

  .toc-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .toc-item {
    display: block;
  }

  .toc-link {
    display: block;
    width: 100%;
    text-align: left;
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-01) var(--easing-productive-enter),
                color var(--duration-fast-01) var(--easing-productive-enter);
  }

  .toc-link:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .toc-link--active {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
    font-weight: 600;
  }

  .toc-link--active:hover {
    background: color-mix(in srgb, var(--interactive) 20%, transparent);
    color: var(--interactive);
  }

  .toc-empty {
    padding: var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    font-style: italic;
  }
</style>
