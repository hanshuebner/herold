<script lang="ts">
  /**
   * One-time confirmation modal for mailing-list reactions per REQ-MAIL-191.
   *
   * Shown when the user reacts to a message that has a List-ID header and
   * more than 5 recipients, warning that the reaction will propagate to N
   * people via outbound reaction email.
   */
  import { reactionConfirm } from './reaction-confirm.svelte';

  let dontAskAgain = $state(false);
  let ctx = $derived(reactionConfirm.pending);
</script>

{#if ctx}
  <div class="backdrop" aria-hidden="true" onclick={() => ctx?.onCancel()}></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="dialog"
      aria-modal="true"
      aria-labelledby="rcm-title"
      tabindex="-1"
      onkeydown={(e) => { if (e.key === 'Escape') ctx?.onCancel(); }}
    >
      <h2 id="rcm-title" class="title">Send reaction to {ctx.totalRecipients} people?</h2>
      <p class="body">
        This message was sent to a mailing list. Reacting will send a reaction
        email to all {ctx.totalRecipients} recipients.
      </p>
      <label class="check-label">
        <input type="checkbox" bind:checked={dontAskAgain} />
        Don't ask again for this list
      </label>
      <div class="actions">
        <button type="button" class="btn-secondary" onclick={() => ctx?.onCancel()}>
          Cancel
        </button>
        <button type="button" class="btn-primary" onclick={() => ctx?.onConfirm(dontAskAgain)}>
          Send reaction
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 299;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 300;
    pointer-events: none;
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 400px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-01-size);
    font-weight: var(--type-heading-01-weight);
    line-height: var(--type-heading-01-line);
    color: var(--text-primary);
  }

  .body {
    margin: 0;
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
  }

  .check-label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    cursor: pointer;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .btn-secondary,
  .btn-primary {
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: 32px;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-secondary {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }

  .btn-secondary:hover {
    background: var(--layer-03);
  }

  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }

  .btn-primary:hover {
    opacity: 0.9;
  }
</style>
