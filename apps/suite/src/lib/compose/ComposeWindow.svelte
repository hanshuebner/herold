<script lang="ts">
  import { compose } from './compose.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { mail } from '../mail/store.svelte';

  // Per-compose keyboard layer: Mod+Enter sends, Escape closes.
  // Both pass through input-focus carve-outs (see keyboard engine
  // shouldSkipForFocus: Escape and Mod+Enter always pass through).
  $effect(() => {
    if (!compose.isOpen) return;
    const pop = keyboard.pushLayer([
      {
        key: 'Mod+Enter',
        description: 'Send',
        action: () => {
          void compose.send();
        },
      },
      {
        key: 'Escape',
        description: 'Close compose',
        action: () => compose.close(),
      },
    ]);
    return pop;
  });

  // Focus the right field when compose opens — for reply / forward, the
  // body (cursor at top, above the quoted block); otherwise the To field.
  let toInput = $state<HTMLInputElement | null>(null);
  let bodyTextarea = $state<HTMLTextAreaElement | null>(null);
  $effect(() => {
    if (compose.status !== 'editing') return;
    requestAnimationFrame(() => {
      if (compose.replyContext.parentId && bodyTextarea) {
        bodyTextarea.focus();
        bodyTextarea.setSelectionRange(0, 0);
      } else if (toInput) {
        toInput.focus();
      }
    });
  });

  let identity = $derived(mail.primaryIdentity);
</script>

{#if compose.isOpen}
  <div class="backdrop" onclick={() => compose.close()} aria-hidden="true"></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="compose-title"
    tabindex="-1"
  >
    <header class="modal-header">
      <h2 id="compose-title">
        {#if compose.replyContext.parentKeyword === '$answered'}
          Reply
        {:else if compose.replyContext.parentKeyword === '$forwarded'}
          Forward
        {:else}
          New message
        {/if}
      </h2>
      <button
        type="button"
        class="close"
        onclick={() => compose.close()}
        aria-label="Close compose"
      >
        ×
      </button>
    </header>

    <div class="fields">
      <div class="row">
        <span class="label">From</span>
        <span class="from-display">
          {#if identity}
            {identity.name ? `${identity.name} <${identity.email}>` : identity.email}
          {:else}
            <span class="muted">Loading identity…</span>
          {/if}
        </span>
      </div>

      <label class="row">
        <span class="label">To</span>
        <input
          bind:this={toInput}
          bind:value={compose.to}
          type="text"
          spellcheck="false"
          autocomplete="off"
          placeholder="recipient@example.com"
          disabled={compose.status === 'sending'}
        />
      </label>

      <label class="row">
        <span class="label">Subject</span>
        <input
          bind:value={compose.subject}
          type="text"
          spellcheck="true"
          disabled={compose.status === 'sending'}
        />
      </label>

      <label class="row body-row">
        <span class="label">Body</span>
        <textarea
          bind:this={bodyTextarea}
          bind:value={compose.body}
          rows="14"
          placeholder="Write your message…"
          spellcheck="true"
          disabled={compose.status === 'sending'}
        ></textarea>
      </label>
    </div>

    {#if compose.errorMessage}
      <p class="error" role="alert">{compose.errorMessage}</p>
    {/if}

    <footer class="modal-footer">
      <button
        type="button"
        class="discard"
        onclick={() => compose.close()}
        disabled={compose.status === 'sending'}
      >
        Discard
      </button>
      <button
        type="button"
        class="send"
        onclick={() => void compose.send()}
        disabled={compose.status === 'sending'}
      >
        {compose.status === 'sending' ? 'Sending…' : 'Send'}
      </button>
    </footer>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 900;
    animation: fade-in var(--duration-fast-02) var(--easing-productive-enter);
  }
  .modal {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(720px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-05));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 901;
    overflow: hidden;
    animation: rise var(--duration-moderate-01) var(--easing-productive-enter);
  }

  .modal-header {
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

  .fields {
    padding: var(--spacing-04) var(--spacing-05);
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    overflow: auto;
    flex: 1;
  }
  .row {
    display: flex;
    align-items: baseline;
    gap: var(--spacing-04);
    padding: var(--spacing-02) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .label {
    width: 6em;
    flex: 0 0 auto;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .from-display {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
  }
  input[type='text'] {
    flex: 1;
    background: none;
    border: none;
    outline: none;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: 0;
  }
  input[type='text']::placeholder {
    color: var(--text-helper);
  }

  .body-row {
    align-items: flex-start;
    border-bottom: none;
  }
  textarea {
    flex: 1;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    outline: none;
    color: var(--text-primary);
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: var(--spacing-03);
    resize: vertical;
  }
  textarea:focus,
  input[type='text']:focus {
    box-shadow: 0 0 0 2px var(--focus);
    border-radius: var(--radius-sm);
  }

  .error {
    margin: 0 var(--spacing-05);
    padding: var(--spacing-03) var(--spacing-04);
    background: rgba(250, 77, 86, 0.12);
    border-left: 3px solid var(--support-error);
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  .modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    padding: var(--spacing-04) var(--spacing-05);
    border-top: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .send,
  .discard {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .send {
    background: var(--interactive);
    color: var(--text-on-color);
  }
  .send:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .send:disabled,
  .discard:disabled {
    opacity: 0.5;
    cursor: progress;
  }
  .discard {
    color: var(--text-secondary);
  }
  .discard:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
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
