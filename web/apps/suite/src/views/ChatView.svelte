<script lang="ts">
  import { router } from '../lib/router/router.svelte';

  let conversationId = $derived(
    router.parts[1] === 'conversation' ? router.parts[2] : undefined,
  );
</script>

<div class="chat">
  <h1>
    {#if conversationId}
      Conversation {conversationId}
    {:else}
      Chat
    {/if}
  </h1>
  <p class="lead">Chat view placeholder. DMs, Spaces, presence, and the
    ephemeral-channel WebSocket arrive after mail's first end-to-end feature.</p>
</div>

<style>
  .chat {
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
