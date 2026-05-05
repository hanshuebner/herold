<script lang="ts">
  /**
   * HelpView - admin manual viewer, mounted at #/help.
   *
   * Loads the admin.json bundle (produced by `pnpm --filter @herold/manual run bundle`)
   * and renders it via the shared @herold/manual Manual component.
   *
   * Route shape (mirrors the suite's HelpView):
   *   #/help                  -> home chapter
   *   #/help/{slug}           -> specific chapter
   *   #/help/{slug}/{heading} -> chapter + heading anchor (heading id is a
   *                              path segment, NOT a real URL fragment, so
   *                              there is no double-hash and the router parses
   *                              it cleanly).
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

  // parts: ['help'] | ['help', slug] | ['help', slug, headingId]
  const slug = $derived(router.parts[1] ?? null);
  const activeHash = $derived(router.parts[2]);

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
      // web/apps/admin/public/help/bundle.json and served verbatim by Vite
      // at /admin/help/bundle.json (same-origin, no CORS).
      // In production it is embedded in the herold binary under /admin/help/.
      //
      // NOTE: /admin/manual/ is reserved for the standalone SSR manual on the
      // admin listener, so the JSON bundle must live under a different path to
      // avoid the route collision that would yield a 404.
      const res = await fetch('/admin/help/bundle.json');
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
    const parts = ['help', targetSlug];
    if (targetHash) parts.push(targetHash);
    router.navigate('/' + parts.join('/'));
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
