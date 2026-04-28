<script lang="ts">
  /**
   * Reactions strip displayed beneath each expanded message body.
   * Renders emoji-count chips per REQ-MAIL-172. The user's own chip is
   * visually distinct; clicking it toggles the reaction off (REQ-MAIL-171).
   * Hover/tap shows the reactor list (resolved from principalId to
   * display name where possible).
   */
  import { mail } from './store.svelte';

  interface Props {
    emailId: string;
    reactions: Record<string, string[]> | null | undefined;
    principalId: string | null;
    /** Called when the user clicks the "react" chip action (add). */
    onAddReaction: (emoji: string) => void;
  }
  let { emailId, reactions, principalId, onAddReaction }: Props = $props();

  // Derive a sorted list of [emoji, reactors] pairs from the sparse map.
  let chips = $derived.by(() => {
    if (!reactions) return [];
    return Object.entries(reactions)
      .filter(([, rs]) => rs.length > 0)
      .sort(([a], [b]) => a.localeCompare(b));
  });

  function isMine(reactors: string[]): boolean {
    return principalId !== null && reactors.includes(principalId);
  }

  function handleChipClick(emoji: string, reactors: string[]): void {
    if (!principalId) return;
    if (isMine(reactors)) {
      // Toggle off.
      void mail.toggleReaction(emailId, emoji, principalId);
    } else {
      onAddReaction(emoji);
    }
  }

  function reactorLabel(reactors: string[]): string {
    if (reactors.length === 0) return '';
    const names = reactors
      .slice(0, 5)
      .map((id) => {
        const contact = mail.emails;
        // Fall back to principalId when we have no name resolution yet.
        void contact;
        return id;
      });
    const extra = reactors.length - names.length;
    if (extra > 0) return names.join(', ') + ` and ${extra} more`;
    return names.join(', ');
  }
</script>

{#if chips.length > 0}
  <div class="reactions" aria-label="Reactions">
    {#each chips as [emoji, reactors]}
      <button
        type="button"
        class="chip"
        class:mine={isMine(reactors)}
        title={reactorLabel(reactors)}
        aria-label="{emoji} {reactors.length} reaction{reactors.length === 1 ? '' : 's'}{isMine(reactors) ? ' (click to remove)' : ''}"
        onclick={() => handleChipClick(emoji, reactors)}
      >
        <span class="chip-emoji" aria-hidden="true">{emoji}</span>
        <span class="chip-count">{reactors.length}</span>
      </button>
    {/each}
  </div>
{/if}

<style>
  .reactions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    padding: var(--spacing-03) 0 0;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-01);
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    line-height: 1;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    cursor: pointer;
    min-height: 26px;
  }

  .chip:hover {
    background: var(--layer-02);
    border-color: var(--border-strong-01);
  }

  /* The user's own reaction chip has an inset / filled look to
     distinguish it from others'. Uses the interactive token so it
     inherits both light and dark theme values. */
  .chip.mine {
    background: color-mix(in srgb, var(--interactive) 18%, transparent);
    border-color: var(--interactive);
    color: var(--interactive);
  }

  .chip.mine:hover {
    background: color-mix(in srgb, var(--interactive) 28%, transparent);
  }

  .chip-emoji {
    font-size: 15px;
    line-height: 1;
  }

  .chip-count {
    font-variant-numeric: tabular-nums;
  }
</style>
