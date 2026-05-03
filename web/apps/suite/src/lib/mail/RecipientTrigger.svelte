<script lang="ts">
  /**
   * REQ-MAIL-46 hover trigger.
   *
   * Wraps a person reference (avatar, sender name, recipient row entry)
   * and reports pointer / focus interactions to the recipientHover
   * singleton. The wrapper is a focusable button so the card opens via
   * keyboard focus too (REQ-MAIL-46 immediate-on-focus rule).
   */

  import type { Snippet } from 'svelte';
  import { recipientHover } from './recipient-hover.svelte';

  interface Props {
    email: string;
    capturedName?: string | null;
    messageHeaders?: { face?: string; xFace?: string };
    /** Visual element class hooks. */
    inline?: boolean;
    children: Snippet;
  }

  let { email, capturedName = null, messageHeaders, inline = false, children }: Props = $props();

  let el = $state<HTMLButtonElement | null>(null);

  function handlePointerEnter(): void {
    if (!el) return;
    recipientHover.requestOpen({
      anchor: el,
      email,
      capturedName,
      messageHeaders,
    });
  }

  function handlePointerLeave(): void {
    recipientHover.requestClose();
  }

  function handleFocus(): void {
    if (!el) return;
    recipientHover.requestOpen(
      { anchor: el, email, capturedName, messageHeaders },
      { immediate: true },
    );
  }

  function handleBlur(): void {
    // Only close on blur when focus did not land in the popover; the
    // popover itself owns its grace logic.
    recipientHover.requestClose();
  }
</script>

<button
  type="button"
  bind:this={el}
  class="trigger"
  class:inline
  onpointerenter={handlePointerEnter}
  onpointerleave={handlePointerLeave}
  onfocus={handleFocus}
  onblur={handleBlur}
>
  {@render children()}
</button>

<style>
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: 0;
    background: transparent;
    color: inherit;
    border-radius: var(--radius-md);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .trigger.inline {
    padding: 0 var(--spacing-01);
  }
  .trigger:hover,
  .trigger:focus-visible {
    background: var(--layer-02);
    outline: none;
  }
</style>
