<script lang="ts">
  import { router } from '../lib/router/router.svelte';
</script>

<div class="not-found">
  <h1>Not found</h1>
  <p class="lead">No view registered for <code>{router.current}</code>.</p>
  <button type="button" onclick={() => router.navigate('/mail')}>Go to inbox</button>
</div>

<style>
  .not-found {
    padding: var(--spacing-07);
    max-width: 60ch;
  }
  h1 {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    margin: 0 0 var(--spacing-03);
  }
  .lead {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-05);
  }
  code {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    background: var(--layer-02);
    color: var(--text-primary);
    padding: 0 var(--spacing-02);
    border-radius: var(--radius-sm);
  }
  button {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
  }
</style>
