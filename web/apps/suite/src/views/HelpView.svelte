<script lang="ts">
  /**
   * HelpView renders the user manual at the /help route.
   *
   * The manual bundle (user.json) is fetched lazily at first mount so the
   * main SPA bundle stays lean.  A missing or empty bundle is handled
   * gracefully (shows a loading / error state) so the view never crashes.
   *
   * Route shape: /help, /help/:chapter, /help/:chapter/:section
   * The :chapter segment maps to a chapter slug; :section maps to a heading
   * id for the right-rail outline highlight.
   */
  import { onMount } from 'svelte';
  import Manual from '@herold/manual';
  import type { ManualBundle } from '@herold/manual';
  import { router } from '../lib/router/router.svelte';
  import { t } from '../lib/i18n/i18n.svelte';

  // Derive slug + section from the current route.
  // parts: ['help'] | ['help', chapter] | ['help', chapter, section]
  const slug = $derived(router.parts[1] ?? null);
  const section = $derived(router.parts[2]);

  // Bundle load state.
  type LoadState = 'idle' | 'loading' | 'ready' | 'error';
  let loadState = $state<LoadState>('idle');
  let bundle = $state<ManualBundle | null>(null);

  onMount(() => {
    loadState = 'loading';
    fetch('/manual/user.json')
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json() as Promise<ManualBundle>;
      })
      .then((data) => {
        bundle = data;
        loadState = 'ready';
      })
      .catch(() => {
        loadState = 'error';
      });
  });

  function handleNavigate(targetSlug: string, headingId?: string): void {
    const parts = ['help', targetSlug];
    if (headingId) parts.push(headingId);
    router.navigate('/' + parts.join('/'));
  }
</script>

<div class="help-view">
  {#if loadState === 'loading' || loadState === 'idle'}
    <div class="help-state" role="status" aria-live="polite">
      <p>{t('manual.loading')}</p>
    </div>
  {:else if loadState === 'error'}
    <div class="help-state help-state--error" role="alert">
      <p>{t('manual.loadError')}</p>
    </div>
  {:else if bundle !== null && bundle.chapters.length > 0}
    <Manual
      {bundle}
      {slug}
      hash={section}
      onNavigate={handleNavigate}
      t={t}
    />
  {:else}
    <div class="help-state" role="status">
      <p>{t('manual.empty')}</p>
    </div>
  {/if}
</div>

<style>
  .help-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--background);
  }

  .help-state {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
  }

  .help-state--error {
    color: var(--support-error);
  }

  .help-state p {
    margin: 0;
  }
</style>
