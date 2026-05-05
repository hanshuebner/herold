<script lang="ts">
  /**
   * Manual is the top-level three-pane manual viewer component.
   *
   * Layout:
   *   - Left rail: search input + chapter TOC (ManualSearch + ManualToc)
   *   - Center: chapter content (ManualPage)
   *   - Right rail: "On this page" heading outline (ManualOnThisPage)
   *
   * The component is content-agnostic: it receives a ManualBundle (JSON)
   * and renders it.  All navigation state is lifted to the `slug` / `hash`
   * props so parent components (route or standalone shell) own history.
   */
  import type { ManualBundle } from '../markdoc/bundle.js';
  import { defaultT } from '../chrome/strings.js';
  import ManualToc from './ManualToc.svelte';
  import ManualPage from './ManualPage.svelte';
  import ManualOnThisPage from './ManualOnThisPage.svelte';
  import ManualSearch from './ManualSearch.svelte';

  interface Props {
    bundle: ManualBundle;
    /** Currently active chapter slug.  null means show the home chapter. */
    slug?: string | null;
    /** Currently active heading id (for right-rail highlight). */
    hash?: string;
    onNavigate: (slug: string, hash?: string) => void;
    t?: (key: string) => string;
  }

  const { bundle, slug = null, hash, onNavigate, t = defaultT }: Props = $props();

  let searchQuery = $state('');

  const activeSlug = $derived(slug ?? bundle.home);

  const currentChapter = $derived(
    bundle.chapters.find((ch) => ch.slug === activeSlug) ??
      bundle.chapters.find((ch) => ch.slug === bundle.home) ??
      bundle.chapters[0],
  );

  function handleTocNavigate(targetSlug: string): void {
    searchQuery = '';
    onNavigate(targetSlug);
  }

  function handleOutlineNavigate(headingId: string): void {
    if (currentChapter) {
      onNavigate(currentChapter.slug, headingId);
    }
  }

  function handleQuery(q: string): void {
    searchQuery = q;
  }

  /**
   * Scroll the heading with id === hash into view inside the center content
   * pane whenever the hash prop changes.  The SPA router encodes the heading
   * id as a path segment (not the real URL fragment), so the browser's native
   * anchor-scroll mechanism is bypassed; we must scroll explicitly.
   */
  $effect(() => {
    const targetId = hash;
    if (!targetId) return;
    // Use a microtask so the DOM has had one render cycle to render the
    // chapter before we try to locate the heading element.
    Promise.resolve().then(() => {
      const el = document.getElementById(targetId);
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    });
  });
</script>

<div class="manual-layout" data-audience={bundle.audience}>
  <!-- Left rail: search + TOC -->
  <aside class="manual-rail manual-rail--left" aria-label={t('manual.toc.label')}>
    <ManualSearch query={searchQuery} onQuery={handleQuery} {t} />
    {#if currentChapter !== undefined}
      <ManualToc
        chapters={bundle.chapters}
        currentSlug={currentChapter.slug}
        {searchQuery}
        onNavigate={handleTocNavigate}
        {t}
      />
    {/if}
  </aside>

  <!-- Center: chapter content -->
  <main class="manual-content" id="manual-main">
    {#if currentChapter !== undefined}
      <ManualPage chapter={currentChapter} {t} />
    {/if}
  </main>

  <!-- Right rail: "On this page" heading outline -->
  <aside class="manual-rail manual-rail--right" aria-label={t('manual.onthispage.label')}>
    {#if currentChapter !== undefined}
      <ManualOnThisPage
        outline={currentChapter.outline}
        activeId={hash}
        onNavigate={handleOutlineNavigate}
        {t}
      />
    {/if}
  </aside>
</div>

<style>
  .manual-layout {
    display: grid;
    grid-template-columns: 240px 1fr 200px;
    grid-template-rows: 1fr;
    min-height: 0;
    height: 100%;
    background: var(--background);
    color: var(--text-primary);
    font-family: var(--font-sans);
  }

  .manual-rail {
    overflow-y: auto;
    position: sticky;
    top: 0;
    height: 100vh;
    padding: var(--spacing-05) 0;
    background: var(--layer-01);
  }

  .manual-rail--left {
    border-right: 1px solid var(--border-subtle-01);
  }

  .manual-rail--right {
    border-left: 1px solid var(--border-subtle-01);
    padding: var(--spacing-05) var(--spacing-04);
  }

  .manual-content {
    overflow-y: auto;
    padding: var(--spacing-07) var(--spacing-08);
    min-width: 0;
  }

  /* Narrow screens: collapse to single column */
  @media (max-width: 900px) {
    .manual-layout {
      grid-template-columns: 1fr;
      grid-template-rows: auto 1fr auto;
    }

    .manual-rail {
      position: static;
      height: auto;
    }

    .manual-rail--left {
      border-right: none;
      border-bottom: 1px solid var(--border-subtle-01);
    }

    .manual-rail--right {
      border-left: none;
      border-top: 1px solid var(--border-subtle-01);
      order: 1;
    }

    .manual-content {
      padding: var(--spacing-05);
    }
  }

  @media (max-width: 640px) {
    .manual-content {
      padding: var(--spacing-04) var(--spacing-05);
    }
  }
</style>
