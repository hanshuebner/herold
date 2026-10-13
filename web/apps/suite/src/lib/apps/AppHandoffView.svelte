<script lang="ts">
  /**
   * Placeholder page of the tab an app action opens (REQ-APPS-10). The
   * opening tab navigates or closes this tab once `AppAction/invoke`
   * returns; until then it shows "Handing over to <appName>…" with a
   * spinner. The app name and language arrive as hash parameters, so
   * the page renders without a session or any backend request.
   */
  import { i18n, t, type Locale } from '../i18n/i18n.svelte';
  import { parseHandoffHash } from './app-actions';

  interface Props {
    hash: string;
  }
  let { hash }: Props = $props();

  let parsed = $derived(parseHandoffHash(hash));

  $effect(() => {
    const lang = parsed.lang.split('-')[0];
    i18n.locale = (lang === 'de' ? 'de' : 'en') satisfies Locale;
    document.title = t('apps.handoff.title', { app: parsed.appName });
  });
</script>

<main class="handoff" aria-busy="true">
  <div class="card" role="status" aria-live="polite">
    <div class="spinner" aria-hidden="true"></div>
    {#if parsed.appName}
      <p class="app-name">{parsed.appName}</p>
    {/if}
    <p class="message">{t('apps.handoff.message', { app: parsed.appName })}</p>
  </div>
</main>

<style>
  .handoff {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    min-height: 100dvh;
    background: var(--background);
    color: var(--text-primary);
    padding: var(--spacing-06);
  }

  .card {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-04);
    text-align: center;
    max-width: 28rem;
  }

  .app-name {
    margin: 0;
    font-size: var(--type-heading-03-size);
    font-weight: var(--type-heading-03-weight);
  }

  .message {
    margin: 0;
    color: var(--text-secondary);
  }

  .spinner {
    width: 32px;
    height: 32px;
    border: 3px solid var(--border-subtle-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }

  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner {
      animation: none;
    }
  }
</style>
