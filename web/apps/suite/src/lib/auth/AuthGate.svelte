<script lang="ts">
  import { auth } from './auth.svelte';

  interface Props {
    children?: import('svelte').Snippet;
  }
  let { children }: Props = $props();

  // Kick off bootstrap once; the auth singleton is idempotent.
  $effect(() => {
    if (auth.status === 'idle') {
      void auth.bootstrap();
    }
    if (auth.status === 'unauthenticated') {
      // Slight delay so the user sees the "redirecting" state. Avoids a
      // disorienting instant flash on slow auth.
      const t = setTimeout(() => auth.redirectToLogin(), 150);
      return () => clearTimeout(t);
    }
  });
</script>

{#if auth.status === 'ready'}
  {@render children?.()}
{:else}
  <div class="splash" role="status" aria-live="polite">
    <div class="card">
      <h1 class="wordmark">Herold</h1>

      {#if auth.status === 'idle' || auth.status === 'bootstrapping'}
        <p class="message">Connecting…</p>
        <div class="spinner" aria-hidden="true"></div>
      {:else if auth.status === 'unauthenticated'}
        <p class="message">Redirecting to sign-in…</p>
      {:else if auth.status === 'error'}
        <p class="message error">Could not reach the server.</p>
        {#if auth.errorMessage}
          <p class="detail">{auth.errorMessage}</p>
        {/if}
        <button type="button" class="retry" onclick={() => auth.bootstrap()}>
          Retry
        </button>
      {/if}
    </div>
  </div>
{/if}

<style>
  .splash {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    min-height: 100dvh;
    background: var(--background);
    padding: var(--spacing-06);
  }
  .card {
    text-align: center;
    max-width: 28rem;
  }
  .wordmark {
    font-family: var(--font-sans);
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: 600;
    letter-spacing: -0.02em;
    margin: 0 0 var(--spacing-05);
    color: var(--text-primary);
  }
  .message {
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    margin: 0 0 var(--spacing-04);
    color: var(--text-secondary);
  }
  .message.error {
    color: var(--support-error);
  }
  .detail {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-helper);
    margin: 0 0 var(--spacing-05);
    word-break: break-word;
  }
  .retry {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .spinner {
    width: 24px;
    height: 24px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    margin: 0 auto;
    animation: spin 800ms linear infinite;
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
