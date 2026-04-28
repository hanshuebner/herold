<script lang="ts">
  /**
   * Snooze quick-pick overlay: four canned options + a custom
   * datetime input. Closes on Escape, on backdrop click, or after a
   * successful snooze dispatch.
   */
  import { mail } from './store.svelte';
  import { snoozePicker, snoozeQuickOptions } from './snooze-picker.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { localeTag } from '../i18n/i18n.svelte';

  let options = $derived(snoozeQuickOptions());
  let custom = $state('');

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
    snoozePicker.close();
    if (!eid) return;
    void mail.snoozeEmail(eid, at);
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
    if (dayDiff === 1) return `${time} tomorrow`;
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
      <h2 id="snooze-title">Snooze until…</h2>
      <button
        type="button"
        class="close"
        aria-label="Close"
        onclick={() => snoozePicker.close()}
      >
        ×
      </button>
    </header>
    <ul class="quick-list" role="listbox" aria-label="Quick options">
      {#each options as o (o.label)}
        <li>
          <button type="button" onclick={() => commit(o.at)}>
            <span class="label">{o.label}</span>
            <span class="when">{fmt(o.at)}</span>
          </button>
        </li>
      {/each}
    </ul>
    <div class="custom-row">
      <label>
        <span>Custom</span>
        <input type="datetime-local" bind:value={custom} />
      </label>
      <button type="button" class="commit" onclick={commitCustom} disabled={!custom}>
        Snooze
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
