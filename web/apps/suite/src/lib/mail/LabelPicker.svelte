<script lang="ts">
  import { mail } from './store.svelte';
  import { labelPicker } from './label-picker.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Mailbox } from './types';

  // The labelable set is the user-created mailboxes -- system roles
  // (Inbox, Sent, Drafts, Spam, Trash, Archive, ...) act as locations,
  // not labels, per REQ-LBL spec and the existing mail.customMailboxes
  // helper. The picker shows them sorted alphabetically with a
  // checkbox per row reflecting current membership.
  const candidates = $derived<Mailbox[]>(
    mail.customMailboxes.slice().sort((a, b) => a.name.localeCompare(b.name)),
  );

  let filter = $state('');
  let inputEl = $state<HTMLInputElement | null>(null);

  const visible = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    if (!f) return candidates;
    return candidates.filter((m) => m.name.toLowerCase().includes(f));
  });

  $effect(() => {
    if (labelPicker.isOpen) {
      filter = '';
      queueMicrotask(() => inputEl?.focus());
    }
  });

  $effect(() => {
    if (!labelPicker.isOpen) return;
    return keyboard.pushLayer([
      {
        key: 'Escape',
        description: 'Close label picker',
        action: () => labelPicker.close(),
      },
    ]);
  });

  /**
   * For a given mailbox, summarise its membership state across the
   * targeted set: 'all' = every targeted email already has it, 'none' =
   * none do, 'some' = mixed (we treat 'some' as "off, click to enable").
   */
  function membershipState(mailboxId: string): 'all' | 'none' | 'some' {
    const ids = labelPicker.isBulk
      ? labelPicker.bulkIds
      : labelPicker.emailId
        ? [labelPicker.emailId]
        : [];
    let on = 0;
    let off = 0;
    for (const id of ids) {
      const e = mail.emails.get(id);
      if (!e) continue;
      if (e.mailboxIds[mailboxId]) on++;
      else off++;
    }
    if (on > 0 && off === 0) return 'all';
    if (off > 0 && on === 0) return 'none';
    return 'some';
  }

  function toggle(m: Mailbox): void {
    const state = membershipState(m.id);
    const turnOn = state !== 'all';
    if (labelPicker.isBulk) {
      void mail.bulkSetLabel(labelPicker.bulkIds, m.id, turnOn);
    } else if (labelPicker.emailId) {
      void mail.setEmailLabel(labelPicker.emailId, m.id, turnOn);
    }
  }
</script>

{#if labelPicker.isOpen}
  <div class="backdrop" aria-hidden="true" onclick={() => labelPicker.close()}></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="label-title"
    tabindex="-1"
  >
    <header>
      <h2 id="label-title">
        {#if labelPicker.isBulk}
          {labelPicker.bulkIds.length === 1
            ? t('labelPicker.title.bulk', { count: labelPicker.bulkIds.length })
            : t('labelPicker.title.bulk.other', { count: labelPicker.bulkIds.length })}
        {:else}
          {t('labelPicker.title.single')}
        {/if}
      </h2>
      <button
        type="button"
        class="close"
        aria-label={t('picker.close')}
        onclick={() => labelPicker.close()}
      >
        ×
      </button>
    </header>

    <div class="filter-row">
      <input
        type="text"
        placeholder={t('labelPicker.filter')}
        bind:value={filter}
        bind:this={inputEl}
        aria-label={t('labelPicker.filter')}
        autocomplete="off"
      />
    </div>

    {#if candidates.length === 0}
      <p class="empty">
        {t('labelPicker.empty')}
      </p>
    {:else if visible.length === 0}
      <p class="empty">{t('labelPicker.empty.filter', { filter })}</p>
    {:else}
      <ul class="list">
        {#each visible as m (m.id)}
          {@const state = membershipState(m.id)}
          <li>
            <button
              type="button"
              class="row"
              class:checked={state === 'all'}
              class:partial={state === 'some'}
              aria-pressed={state === 'all'}
              onclick={() => toggle(m)}
            >
              <span class="check" aria-hidden="true">
                {#if state === 'all'}✓{:else if state === 'some'}–{:else}{' '}{/if}
              </span>
              <span class="name">{m.name}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}

    <footer>
      <button type="button" class="done" onclick={() => labelPicker.close()}>
        {t('labelPicker.done')}
      </button>
    </footer>
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
  .row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    width: 100%;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-primary);
    text-align: left;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .row:hover {
    background: var(--layer-01);
  }
  .check {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 18px;
    height: 18px;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-sm);
    font-weight: 700;
    color: var(--text-on-color);
    background: var(--layer-01);
    flex: 0 0 auto;
  }
  .row.checked .check {
    background: var(--interactive);
    border-color: var(--interactive);
  }
  .row.partial .check {
    background: var(--layer-03);
    border-color: var(--text-helper);
    color: var(--text-secondary);
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    padding: var(--spacing-03) var(--spacing-04);
    border-top: 1px solid var(--border-subtle-01);
  }
  .done {
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .done:hover {
    filter: brightness(1.1);
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
