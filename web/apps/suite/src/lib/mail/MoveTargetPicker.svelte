<script lang="ts">
  import { mail } from './store.svelte';
  import {
    movePicker,
    computeMoveCandidates,
    filterMailboxesByName,
  } from './move-picker.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import type { Mailbox } from './types';

  let candidates = $derived.by<Mailbox[]>(() => {
    if (!movePicker.isOpen) return [];
    if (movePicker.isBulk) {
      // In bulk mode, every mailbox is a valid target — we don't try to
      // exclude "already there" because that would shrink the set
      // arbitrarily depending on which selected emails happen to live
      // where.
      return computeMoveCandidates(mail.mailboxes.values(), new Set());
    }
    const eid = movePicker.emailId;
    if (!eid) return [];
    const email = mail.emails.get(eid);
    if (!email) return [];
    return computeMoveCandidates(
      mail.mailboxes.values(),
      new Set(Object.keys(email.mailboxIds)),
    );
  });

  let focusIdx = $state(0);
  let filter = $state('');
  let inputEl = $state<HTMLInputElement | null>(null);

  let visible = $derived(filterMailboxesByName(candidates, filter));

  // Reset focus when the filtered list changes shape so the index stays valid.
  $effect(() => {
    void visible;
    focusIdx = 0;
  });

  // Autofocus the filter input when the picker opens.
  $effect(() => {
    if (movePicker.isOpen) {
      queueMicrotask(() => inputEl?.focus());
    }
  });

  // Push a layer for Escape; arrow keys + Enter are handled at the input
  // since the input has focus and otherwise the engine's focus carve-out
  // would suppress single-key shortcuts.
  $effect(() => {
    if (!movePicker.isOpen) return;
    return keyboard.pushLayer([
      {
        key: 'Escape',
        description: 'Close move dialog',
        action: () => movePicker.close(),
      },
    ]);
  });

  function commit(target: Mailbox): void {
    if (movePicker.isBulk) {
      const ids = [...movePicker.bulkIds];
      movePicker.close();
      void mail.bulkMoveToMailbox(ids, target.id);
      return;
    }
    const eid = movePicker.emailId;
    movePicker.close();
    if (!eid) return;
    void mail.moveEmailToMailbox(eid, target.id);
  }

  function onKey(e: KeyboardEvent): void {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      focusIdx = Math.min(focusIdx + 1, visible.length - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      focusIdx = Math.max(focusIdx - 1, 0);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const target = visible[focusIdx];
      if (target) commit(target);
    }
  }
</script>

{#if movePicker.isOpen}
  <div class="backdrop" aria-hidden="true" onclick={() => movePicker.close()}></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="move-title"
    tabindex="-1"
  >
    <header>
      <h2 id="move-title">
        {#if movePicker.isBulk}
          Move {movePicker.bulkIds.length} message{movePicker.bulkIds.length === 1 ? '' : 's'} to
        {:else}
          Move to mailbox
        {/if}
      </h2>
      <button
        type="button"
        class="close"
        aria-label="Close move dialog"
        onclick={() => movePicker.close()}
      >
        ×
      </button>
    </header>

    <div class="filter-row">
      <input
        type="text"
        placeholder="Filter mailboxes…"
        bind:value={filter}
        bind:this={inputEl}
        onkeydown={onKey}
        aria-label="Filter mailboxes"
        autocomplete="off"
      />
    </div>

    {#if visible.length === 0}
      <p class="empty">
        {filter ? `No mailboxes match “${filter}”.` : 'No other mailboxes available.'}
      </p>
    {:else}
      <ul class="list" role="listbox" aria-label="Move target">
        {#each visible as m, i (m.id)}
          <li class:focused={focusIdx === i}>
            <button
              type="button"
              role="option"
              aria-selected={focusIdx === i}
              onclick={() => commit(m)}
              onmouseenter={() => (focusIdx = i)}
            >
              <span class="name">{m.name}</span>
              {#if m.role}
                <span class="role">{m.role}</span>
              {/if}
            </button>
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
    width: min(420px, calc(100vw - 2 * var(--spacing-05)));
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

  .filter-row {
    padding: var(--spacing-03) var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  input {
    width: 100%;
    background: var(--layer-01);
    color: var(--text-primary);
    padding: var(--spacing-02) var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-size: var(--type-body-01-size);
  }
  input:focus {
    outline: 2px solid var(--interactive);
    outline-offset: -1px;
  }

  .empty {
    margin: 0;
    padding: var(--spacing-06) var(--spacing-05);
    text-align: center;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .list {
    list-style: none;
    margin: 0;
    padding: var(--spacing-02) 0;
    overflow: auto;
    flex: 1;
    max-height: 320px;
  }
  .list li.focused {
    background: var(--layer-01);
  }
  .list button {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    width: 100%;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-primary);
    text-align: left;
    min-height: var(--touch-min);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .role {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    text-transform: capitalize;
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
