<script lang="ts">
  /**
   * HelpView - admin manual viewer, mounted at #/help.
   *
   * Loads the admin.json bundle (produced by `pnpm --filter @herold/manual run bundle`)
   * and renders it via the shared @herold/manual Manual component.
   *
   * Route shape:
   *   #/help              -> home chapter
   *   #/help/{slug}       -> specific chapter
   *   #/help/{slug}#{id}  -> chapter + heading anchor (hash from router.current)
   *
   * i18n: The admin SPA does not yet have an i18n system. The identity-fallback
   * `t = (k) => k` is passed so the Manual component uses its built-in English
   * strings via defaultT. This is acceptable per docs/design/web/notes/adr-0001.
   * When i18n is added to the admin SPA, wire the real t() function here.
   *
   * Fallback: if admin.json fails to load (e.g. the bundle step was not run),
   * an inline error is displayed instead of crashing the SPA.
   */
  import Manual from '@herold/manual';
  import type { ManualBundle } from '@herold/manual';
  import { router } from '../lib/router/router.svelte';

  // Identity-fallback translator: passes keys straight through so the Manual
  // component resolves them via defaultT (its built-in English strings).
  // See i18n note in the header comment above.
  const t = (key: string): string => key;

  // Derive the chapter slug from the second path segment: /help/{slug}.
  const slug = $derived(router.parts[1] ?? null);

  // Track the browser hash for sub-fragment anchors within a chapter.
  // For deep-link URLs like #/help/install#section, the router holds the
  // path (/help/install) and the sub-anchor lives in location.hash.
  let locationHash: string = $state(window.location.hash);
  $effect(() => {
    function onHashChange() {
      locationHash = window.location.hash;
    }
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  });

  // The sub-anchor is the part after the second # in the full hash string.
  // e.g. "#/help/install#configuration" -> "configuration"
  const activeHash = $derived((() => {
    const idx = locationHash.indexOf('#', 1);
    return idx >= 0 ? locationHash.slice(idx + 1) : undefined;
  })());

  // Bundle load state.
  let bundleStatus: 'loading' | 'ready' | 'error' = $state('loading');
  let bundle: ManualBundle | null = $state(null);
  let errorMessage: string = $state('');

  // Load the bundle once on mount.
  $effect(() => {
    void loadBundle();
  });

  async function loadBundle(): Promise<void> {
    try {
      // The bundle is produced by `scripts/build-web.sh` into
      // web/apps/admin/public/manual/admin.json and served verbatim by Vite
      // at /admin/manual/admin.json (same-origin, no CORS).
      // In production it is embedded in the herold binary under /admin/manual/.
      const res = await fetch('/admin/manual/admin.json');
      if (!res.ok) {
        // Not crash-worthy: bundle may be absent in dev builds. Show a
        // friendly message and let the rest of the SPA keep working.
        errorMessage = `Manual bundle not available (HTTP ${res.status}). Run the bundle step to generate it.`;
        bundleStatus = 'error';
        return;
      }
      bundle = (await res.json()) as ManualBundle;
      bundleStatus = 'ready';
    } catch (err) {
      errorMessage = `Failed to load manual: ${err instanceof Error ? err.message : String(err)}`;
      bundleStatus = 'error';
    }
  }

  function handleNavigate(targetSlug: string, targetHash?: string): void {
    const path = `/help/${targetSlug}`;
    if (targetHash) {
      // Navigate to the chapter, then set the sub-anchor.
      router.navigate(path);
      window.location.hash = '#' + path + '#' + targetHash;
    } else {
      router.navigate(path);
    }
  }
</script>

<div class="help-view">
  {#if bundleStatus === 'loading'}
    <div class="help-loading" role="status" aria-live="polite">
      <div class="spinner" aria-hidden="true"></div>
      <span>Loading manual...</span>
    </div>
  {:else if bundleStatus === 'error'}
    <div class="help-error" role="alert">
      <h1 class="help-error-title">Manual unavailable</h1>
      <p class="help-error-message">{errorMessage}</p>
    </div>
  {:else if bundle !== null}
    <Manual
      {bundle}
      {slug}
      hash={activeHash}
      onNavigate={handleNavigate}
      {t}
    />
  {/if}
</div>

<style>
  .help-view {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;
  }

  .help-loading {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-08);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .help-error {
    padding: var(--spacing-08);
    max-width: 600px;
  }

  .help-error-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-04);
  }

  .help-error-message {
    font-size: var(--type-body-01-size);
    color: var(--support-error);
    margin: 0;
    padding: var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }
</style>
