<script lang="ts">
  import { router } from '../lib/router/router.svelte';

  let threadId = $derived(router.parts[1] === 'thread' ? router.parts[2] : undefined);
  let label = $derived(router.parts[1] === 'label' ? router.parts[2] : undefined);
</script>

<div class="mail">
  <h1>
    {#if threadId}
      Thread {threadId}
    {:else if label}
      Label: {label}
    {:else}
      Inbox
    {/if}
  </h1>
  <p class="lead">Mail view placeholder. Inbox list, thread reading pane, and
    actions arrive once the JMAP client substrate lands.</p>
</div>

<style>
  .mail {
    padding: var(--spacing-07);
    max-width: 60ch;
  }
  h1 {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    margin: 0 0 var(--spacing-03);
  }
  .lead {
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
    margin: 0;
  }
</style>
