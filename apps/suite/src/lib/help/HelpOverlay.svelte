<script lang="ts">
  import { help } from './help.svelte';
  import { keyboard, type Binding } from '../keyboard/engine.svelte';

  // Push a layer to handle Escape while the overlay is open.
  $effect(() => {
    if (!help.isOpen) return;
    const pop = keyboard.pushLayer([
      {
        key: 'Escape',
        description: 'Close help',
        action: () => help.close(),
      },
    ]);
    return pop;
  });

  // Snapshot active bindings when the overlay opens. Filter out bindings
  // without a description (those are anonymous / internal).
  let bindings = $derived<Binding[]>(
    help.isOpen
      ? Array.from(keyboard.activeBindings()).filter((b) => b.description)
      : [],
  );

  /** Pretty-print a binding key with platform-correct modifier glyphs. */
  function prettyKey(key: string): string {
    const isMac = /mac/i.test(
      typeof navigator !== 'undefined' ? navigator.platform : '',
    );
    return key
      .replace(/\bMod\b/g, isMac ? '⌘' : 'Ctrl')
      .replace(/\bShift\b/g, '⇧')
      .replace(/\bAlt\b/g, isMac ? '⌥' : 'Alt')
      .replace(/\bEnter\b/g, '⏎')
      .replace(/\bEscape\b/g, 'Esc');
  }
</script>

{#if help.isOpen}
  <div class="backdrop" aria-hidden="true" onclick={() => help.close()}></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="help-title"
    tabindex="-1"
  >
    <header>
      <h2 id="help-title">Keyboard shortcuts</h2>
      <button
        type="button"
        class="close"
        aria-label="Close help"
        onclick={() => help.close()}
      >
        ×
      </button>
    </header>

    {#if bindings.length === 0}
      <p class="empty">No bindings active.</p>
    {:else}
      <ul class="bindings">
        {#each bindings as b (b.key)}
          <li>
            <kbd>{prettyKey(b.key)}</kbd>
            <span class="desc">{b.description}</span>
          </li>
        {/each}
      </ul>
    {/if}
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 950;
    animation: fade-in var(--duration-fast-02) var(--easing-productive-enter);
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(560px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-05));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 951;
    overflow: hidden;
    animation: rise var(--duration-moderate-01) var(--easing-productive-enter);
  }

  header {
    display: flex;
    align-items: center;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  h2 {
    margin: 0;
    flex: 1;
    font-size: var(--type-heading-01-size);
    line-height: var(--type-heading-01-line);
    font-weight: var(--type-heading-01-weight);
  }
  .close {
    color: var(--text-helper);
    font-size: 20px;
    line-height: 1;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
  }
  .close:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .empty {
    padding: var(--spacing-06) var(--spacing-05);
    text-align: center;
    color: var(--text-helper);
    margin: 0;
  }

  .bindings {
    list-style: none;
    margin: 0;
    padding: var(--spacing-03) 0;
    overflow: auto;
    flex: 1;
  }
  .bindings li {
    display: grid;
    grid-template-columns: 7em 1fr;
    align-items: baseline;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
  }
  kbd {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    background: var(--layer-03);
    color: var(--text-primary);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    border: 1px solid var(--border-subtle-01);
    text-align: center;
    min-width: 2em;
    box-shadow: 0 1px 0 rgba(0, 0, 0, 0.4);
  }
  .desc {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }
  @keyframes rise {
    from {
      transform: translate(-50%, -45%);
      opacity: 0;
    }
    to {
      transform: translate(-50%, -50%);
      opacity: 1;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .backdrop,
    .modal {
      animation: none;
    }
  }
</style>
