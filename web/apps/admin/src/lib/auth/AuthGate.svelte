<script lang="ts">
  import { auth } from './auth.svelte';
  import LoginView from '../../views/LoginView.svelte';

  interface Props {
    children?: import('svelte').Snippet;
  }
  let { children }: Props = $props();

  // Kick off bootstrap once; the auth singleton is idempotent.
  $effect(() => {
    if (auth.status === 'idle') {
      void auth.bootstrap();
    }
  });
</script>

{#if auth.status === 'ready'}
  {@render children?.()}
{:else if auth.status === 'unauthenticated'}
  <LoginView />
{:else}
  <!-- bootstrapping: show centered spinner -->
  <div class="splash" role="status" aria-live="polite">
    <div class="card">
      <h1 class="wordmark">Herold admin</h1>
      <p class="message">Connecting...</p>
      <div class="spinner" aria-hidden="true"></div>
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
