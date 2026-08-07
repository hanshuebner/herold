<script lang="ts">
  /**
   * Snooze quick-pick overlay: four canned options + a custom
   * datetime input. Closes on Escape, on backdrop click, or after a
   * successful snooze dispatch.
   */
  import { mail } from './store.svelte';
  import { snoozePicker, snoozeQuickOptions } from './snooze-picker.svelte';
  import { computeMoveCandidates } from './move-picker.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { localeTag, t } from '../i18n/i18n.svelte';
  import type { Mailbox } from './types';

  let options = $derived(snoozeQuickOptions());
  let custom = $state('');

  // Wake-destination affordance (issue #274): defaults to the account's
  // Inbox and applies to whichever option the user commits (quick pick
  // or custom datetime) so choosing a destination never becomes a second
  // required step. Every mailbox is a valid destination -- unlike the
  // move picker, "wake in place" (the message's current mailbox) is a
  // legitimate choice, so candidates are not filtered by current
  // membership.
  let wakeMailboxId = $state('');
  let wakeCandidates = $derived.by<Mailbox[]>(() => {
    if (!snoozePicker.isOpen) return [];
    return computeMoveCandidates(mail.mailboxes.values(), new Set());
  });

  $effect(() => {
    if (snoozePicker.isOpen) {
      wakeMailboxId = mail.inbox?.id ?? '';
    }
  });

  $effect(() => {
    if (!snoozePicker.isOpen) return;
    return keyboard.pushLayer([
      {
        key: 'Escape',
        description: 'Close snooze',
        action: () => snoozePicker.close(),
      },
    ]);
  });

  function commit(at: Date): void {
    const eid = snoozePicker.emailId;
    const wake = wakeMailboxId || undefined;
    snoozePicker.close();
    if (!eid) return;
    void mail.snoozeEmail(eid, at, wake);
  }

  function commitCustom(): void {
    if (!custom) return;
    const d = new Date(custom);
    if (Number.isNaN(d.getTime())) return;
    if (d.getTime() <= Date.now()) return;
    commit(d);
  }

  function fmt(d: Date): string {
    const tag = localeTag();
    const time = d.toLocaleTimeString(tag, {
      hour: 'numeric',
      minute: '2-digit',
    });
    const dayDiff = Math.round(
      (new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() -
        new Date().setHours(0, 0, 0, 0)) /
        86400000,
    );
    if (dayDiff === 0) return time;
    if (dayDiff === 1) return `${time} ${t('mail.snooze.tomorrow')}`;
    if (dayDiff > 0 && dayDiff < 7) {
      return `${d.toLocaleDateString(tag, { weekday: 'long' })}, ${time}`;
    }
    return `${d.toLocaleDateString(tag, {
      month: 'short',
      day: 'numeric',
    })}, ${time}`;
  }
</script>

{#if snoozePicker.isOpen}
  <div class="backdrop" aria-hidden="true" onclick={() => snoozePicker.close()}></div>
  <div
    class="modal"
    role="dialog"
    aria-modal="true"
    aria-labelledby="snooze-title"
    tabindex="-1"
  >
    <header>
      <h2 id="snooze-title">{t('mail.snooze.heading')}</h2>
      <button
        type="button"
        class="close"
        aria-label={t('mail.snooze.close')}
        onclick={() => snoozePicker.close()}
      >
        ×
      </button>
    </header>
    <ul class="quick-list" role="listbox" aria-label={t('mail.snooze.quickOptions')}>
      {#each options as o (o.label)}
        <li>
          <button type="button" onclick={() => commit(o.at)}>
            <span class="label">{t(o.key)}</span>
            <span class="when">{fmt(o.at)}</span>
          </button>
        </li>
      {/each}
    </ul>
    <div class="wake-row">
      <label>
        <span>{t('mail.snooze.wakeIn')}</span>
        <select bind:value={wakeMailboxId} aria-label={t('mail.snooze.wakeIn')}>
          {#each wakeCandidates as m (m.id)}
            <option value={m.id}>{m.name}</option>
          {/each}
        </select>
      </label>
    </div>
    <div class="custom-row">
      <label>
        <span>{t('mail.snooze.custom')}</span>
        <input type="datetime-local" bind:value={custom} />
      </label>
      <button type="button" class="commit" onclick={commitCustom} disabled={!custom}>
        {t('mail.snooze.button')}
      </button>
    </div>
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

  .quick-list {
    list-style: none;
    margin: 0;
    padding: var(--spacing-02) 0;
  }
  .quick-list button {
    display: flex;
    justify-content: space-between;
    align-items: center;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-primary);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .quick-list button:hover {
    background: var(--layer-01);
  }
  .quick-list .label {
    font-weight: 500;
  }
  .quick-list .when {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .wake-row {
    padding: var(--spacing-02) var(--spacing-05) var(--spacing-03);
    border-top: 1px solid var(--border-subtle-01);
  }
  .wake-row label {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
  }
  .wake-row label span {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }
  .wake-row select {
    flex: 1;
    min-width: 0;
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-size: var(--type-body-01-size);
  }

  .custom-row {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
    padding: var(--spacing-03) var(--spacing-05);
    border-top: 1px solid var(--border-subtle-01);
  }
  .custom-row label {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .custom-row label span {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }
  .custom-row input {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
  }
  .commit {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .commit:disabled {
    opacity: 0.4;
    cursor: not-allowed;
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
