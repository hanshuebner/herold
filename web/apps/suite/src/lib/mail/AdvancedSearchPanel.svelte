<script lang="ts">
  import { mail } from './store.svelte';
  import { emptyFields, fieldsToQuery, queryToFields } from './advanced-search';
  import type { AdvancedSearchFields } from './advanced-search';

  interface Props {
    /** The current raw query string from the URL (used to pre-populate fields). */
    currentQuery?: string;
    onSearch: (query: string) => void;
    onClose: () => void;
  }
  let { currentQuery = '', onSearch, onClose }: Props = $props();

  // Build name->id and id->name maps from the live mailbox list.
  let mailboxIdByName = $derived.by(() => {
    const m = new Map<string, string>();
    for (const mb of mail.mailboxes.values()) {
      m.set(mb.name, mb.id);
    }
    return m;
  });

  let mailboxNameById = $derived.by(() => {
    const m = new Map<string, string>();
    for (const mb of mail.mailboxes.values()) {
      m.set(mb.id, mb.name);
    }
    return m;
  });

  // Sorted mailbox list for the dropdown (all mailboxes).
  let sortedMailboxes = $derived(
    [...mail.mailboxes.values()].sort((a, b) => a.sortOrder - b.sortOrder || a.name.localeCompare(b.name)),
  );

  // Start empty; populate once mailboxIdByName is derived and currentQuery is known.
  let fields = $state<AdvancedSearchFields>(emptyFields());

  // Populate fields whenever the query or the mailbox map changes (e.g. on first
  // open when mailboxes may not yet have loaded, or when navigating via history).
  $effect(() => {
    fields = queryToFields(currentQuery, mailboxIdByName);
  });

  function setSentMailbox(): void {
    const sent = mail.sent;
    if (sent) {
      fields = { ...fields, mailboxId: sent.id };
    }
  }

  function handleSubmit(e: Event): void {
    e.preventDefault();
    const q = fieldsToQuery(fields, mailboxNameById);
    onSearch(q);
  }

  function handleClear(): void {
    fields = emptyFields();
  }

  // Close the panel when Escape is pressed anywhere inside it.
  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onClose();
    }
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  class="panel"
  role="region"
  aria-label="Advanced search"
  onkeydown={handleKeydown}
>
  <form class="panel-form" onsubmit={handleSubmit}>
    <div class="row">
      <label class="field">
        <span class="label">From</span>
        <input
          type="text"
          class="input"
          placeholder="Sender name or address"
          bind:value={fields.from}
          autocomplete="off"
          spellcheck="false"
        />
      </label>

      <label class="field">
        <span class="label">To</span>
        <input
          type="text"
          class="input"
          placeholder="Recipient name or address"
          bind:value={fields.to}
          autocomplete="off"
          spellcheck="false"
        />
      </label>
    </div>

    <div class="row">
      <label class="field">
        <span class="label">Subject</span>
        <input
          type="text"
          class="input"
          placeholder="Subject contains"
          bind:value={fields.subject}
          autocomplete="off"
          spellcheck="false"
        />
      </label>

      <label class="field">
        <span class="label">Body</span>
        <input
          type="text"
          class="input"
          placeholder="Body contains"
          bind:value={fields.body}
          autocomplete="off"
          spellcheck="false"
        />
      </label>
    </div>

    <div class="row">
      <label class="field date-field">
        <span class="label">After</span>
        <input
          type="date"
          class="input"
          bind:value={fields.after}
        />
      </label>

      <label class="field date-field">
        <span class="label">Before</span>
        <input
          type="date"
          class="input"
          bind:value={fields.before}
        />
      </label>
    </div>

    <div class="row">
      <label class="field mailbox-field">
        <span class="label">Mailbox</span>
        <select class="input select" bind:value={fields.mailboxId}>
          <option value="">Any mailbox</option>
          {#each sortedMailboxes as mb (mb.id)}
            <option value={mb.id}>{mb.name}</option>
          {/each}
        </select>
      </label>

      <div class="field shortcuts-field">
        <span class="label">Shortcuts</span>
        <div class="shortcuts">
          <button
            type="button"
            class="shortcut-btn"
            onclick={setSentMailbox}
            title="Set mailbox to Sent"
          >
            in:sent
          </button>
        </div>
      </div>
    </div>

    <div class="row row-bottom">
      <label class="attachment-toggle">
        <input
          type="checkbox"
          bind:checked={fields.hasAttachment}
        />
        <span>Has attachment</span>
      </label>

      <div class="actions">
        <button
          type="button"
          class="btn-clear"
          onclick={handleClear}
        >
          Clear
        </button>
        <button type="submit" class="btn-search">
          Search
        </button>
      </div>
    </div>
  </form>
</div>

<style>
  .panel {
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
    border-top: 1px solid var(--border-subtle-01);
    padding: var(--spacing-04) var(--spacing-05);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.18);
  }

  .panel-form {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .row {
    display: flex;
    gap: var(--spacing-04);
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    flex: 1;
    min-width: 160px;
  }

  .date-field {
    flex: 0 1 200px;
    min-width: 140px;
  }

  .mailbox-field {
    flex: 0 1 220px;
    min-width: 140px;
  }

  .shortcuts-field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    flex: 0 0 auto;
  }

  .label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 500;
    white-space: nowrap;
  }

  .input {
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    min-height: var(--touch-min);
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
    width: 100%;
    box-sizing: border-box;
  }

  .input::placeholder {
    color: var(--text-helper);
  }

  .input:focus {
    outline: none;
    border-color: var(--interactive);
    box-shadow: 0 0 0 1px var(--interactive);
  }

  .select {
    appearance: none;
    -webkit-appearance: none;
    cursor: pointer;
    /* Show a subtle down-arrow using an inline SVG background. */
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%236f6f6f' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M6 9l6 6 6-6'/%3E%3C/svg%3E");
    background-repeat: no-repeat;
    background-position: right var(--spacing-03) center;
    background-size: 16px;
    padding-right: calc(var(--spacing-03) + 16px + var(--spacing-02));
  }

  .select option {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .shortcuts {
    display: flex;
    gap: var(--spacing-02);
    flex-wrap: wrap;
    padding-top: var(--spacing-01);
  }

  .shortcut-btn {
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    color: var(--interactive);
    border-radius: var(--radius-pill);
    font-size: var(--type-code-01-size);
    font-family: var(--font-mono);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    border: 1px solid var(--border-subtle-01);
  }

  .shortcut-btn:hover {
    background: var(--layer-03);
  }

  .row-bottom {
    align-items: center;
    justify-content: space-between;
  }

  .attachment-toggle {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    cursor: pointer;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
  }

  .attachment-toggle input[type='checkbox'] {
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
    flex-shrink: 0;
  }

  .actions {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
  }

  .btn-clear {
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-clear:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .btn-search {
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-search:hover {
    filter: brightness(1.1);
  }
</style>
