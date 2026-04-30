<script lang="ts">
  /**
   * Chip-based recipient input field (REQ-MAIL-11a..d).
   *
   * Renders confirmed recipients as inline chips with a free-text input
   * at the end of the chip row. Supports:
   *   - Commit on comma, semicolon, Enter, Tab, or trailing space after
   *     a structurally-complete address (REQ-MAIL-11b).
   *   - Paste: parses the whole clipboard text; recognized fragments
   *     become chips, unrecognized tail stays in the buffer (REQ-MAIL-11c).
   *   - Remove: clicking x on a chip removes that recipient.
   *   - Autocomplete dropdown from the contacts store (REQ-MAIL-11).
   *   - Blur warning when the buffer holds non-empty, unrecognized text
   *     (REQ-MAIL-11d).
   */
  import { contacts, type ContactSuggestion } from '../contacts/store.svelte';
  import { seenAddresses } from '../contacts/seen-addresses.svelte';
  import { jmap, strict } from '../jmap/client';
  import { Capability } from '../jmap/types';
  import { auth } from '../auth/auth.svelte';
  import {
    tryCommit,
    parsePaste,
    isStructurallyComplete,
    recipientToString,
    type Recipient,
  } from './recipient-parse';

  interface Props {
    label: string;
    chips: Recipient[];
    onChipsChange: (chips: Recipient[]) => void;
    onWarning: (text: string | null) => void;
    placeholder?: string;
    disabled?: boolean;
    autofocus?: boolean;
  }

  let {
    label,
    chips,
    onChipsChange,
    onWarning,
    placeholder,
    disabled = false,
    autofocus = false,
  }: Props = $props();

  let inputEl = $state<HTMLInputElement | null>(null);
  let buffer = $state('');
  let isOpen = $state(false);
  let focusIdx = $state(0);

  // Focus the input when autofocus is requested. Using an $effect rather
  // than the native `autofocus` attribute suppresses the Svelte a11y warning
  // while still giving programmatic focus on mount.
  $effect(() => {
    if (autofocus && inputEl) {
      requestAnimationFrame(() => inputEl?.focus());
    }
  });

  // The buffer drives the autocomplete filter.
  let suggestions = $derived<ContactSuggestion[]>(
    isOpen && buffer.trim().length >= 2 ? contacts.filter(buffer.trim()) : [],
  );

  // Reset dropdown focus when suggestion list changes.
  $effect(() => {
    void suggestions;
    focusIdx = 0;
  });

  function onFocus(): void {
    if (contacts.status === 'idle') void contacts.load();
    if (seenAddresses.status === 'idle') void seenAddresses.load();
    isOpen = true;
  }

  function onBlur(): void {
    // Defer so a click on a suggestion registers first.
    setTimeout(() => {
      isOpen = false;
      // REQ-MAIL-11d (clarified): commit the buffer to a chip when it
      // parses as a structurally complete address. The user moving
      // focus elsewhere is treated as confirmation that the typed
      // address is final, the same way Tab / Enter / comma already do.
      // Without this, a user who types alice@x.test and clicks into
      // the Subject field would see "At least one recipient is
      // required" on Send because the buffer never reaches the chip
      // array (issue addressed in 2026-04-30).
      if (buffer.trim() && isStructurallyComplete(buffer)) {
        if (commitBuffer()) {
          return;
        }
      }
      checkWarning();
    }, 120);
  }

  /** Emit a warning when the buffer has non-empty unrecognized text. */
  function checkWarning(): void {
    const trimmed = buffer.trim();
    if (!trimmed) {
      onWarning(null);
      return;
    }
    // Try to commit the buffer; if it fully parses, no warning.
    const { chips: parsed, rest } = tryCommit(trimmed);
    if (parsed.length > 0 && !rest.trim()) {
      onWarning(null);
    } else {
      onWarning(`Couldn't recognize: ${trimmed}`);
    }
  }

  /**
   * Attempt to commit the current buffer contents as one or more chips.
   * Recognized tokens become chips; the unrecognized tail stays in the buffer.
   * Returns true when at least one chip was committed.
   */
  function commitBuffer(): boolean {
    const trimmed = buffer.trim();
    if (!trimmed) return false;
    const { chips: newChips, rest } = tryCommit(trimmed);
    if (newChips.length === 0) return false;
    onChipsChange([...chips, ...newChips]);
    buffer = rest;
    onWarning(null);
    return true;
  }

  /** Add a chip from the autocomplete dropdown. */
  function commitSuggestion(s: ContactSuggestion): void {
    const r: Recipient = s.name ? { name: s.name, email: s.email } : { email: s.email };
    onChipsChange([...chips, r]);
    buffer = '';
    isOpen = false;
    focusIdx = 0;
    onWarning(null);
    requestAnimationFrame(() => inputEl?.focus());
  }

  /** Remove a chip by index. */
  function removeChip(idx: number): void {
    const next = chips.filter((_, i) => i !== idx);
    onChipsChange(next);
    requestAnimationFrame(() => inputEl?.focus());
  }

  function onInput(e: Event): void {
    const val = (e.currentTarget as HTMLInputElement).value;
    buffer = val;
    isOpen = true;
    // Clear warning as the user types.
    onWarning(null);
  }

  function onKeyDown(e: KeyboardEvent): void {
    // Dropdown navigation takes priority.
    if (isOpen && suggestions.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        focusIdx = Math.min(focusIdx + 1, suggestions.length - 1);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        focusIdx = Math.max(focusIdx - 1, 0);
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        const target = suggestions[focusIdx];
        if (target) {
          e.preventDefault();
          commitSuggestion(target);
          return;
        }
      }
      if (e.key === 'Escape') {
        isOpen = false;
        return;
      }
    }

    // Separator keys commit the buffer when structurally complete.
    if (e.key === ',' || e.key === ';') {
      if (isStructurallyComplete(buffer)) {
        e.preventDefault();
        commitBuffer();
      }
      // If not complete (e.g. mid-angle-bracket) the character is inserted normally.
      return;
    }

    if (e.key === 'Enter') {
      if (isStructurallyComplete(buffer)) {
        e.preventDefault();
        commitBuffer();
      }
      return;
    }

    if (e.key === 'Tab') {
      // Tab: commit if buffer is structurally complete and non-empty, then let
      // the browser move focus normally (don't preventDefault unless we consumed it).
      if (buffer.trim() && isStructurallyComplete(buffer)) {
        if (commitBuffer()) {
          e.preventDefault();
        }
      }
      return;
    }

    // Space after a complete address commits. Avoid committing mid-name or
    // mid-angle-bracket.
    if (e.key === ' ') {
      if (isStructurallyComplete(buffer) && buffer.trim()) {
        e.preventDefault();
        // Only commit if the buffer looks like a complete email (no unfinished
        // tokens). tryCommit will leave the rest in the buffer if needed.
        const { chips: parsed } = tryCommit(buffer.trim());
        if (parsed.length > 0) {
          commitBuffer();
        } else {
          // Not a complete address yet — insert the space normally.
          buffer = buffer + ' ';
        }
      }
      return;
    }

    // Backspace with an empty buffer removes the last chip.
    if (e.key === 'Backspace' && buffer === '' && chips.length > 0) {
      removeChip(chips.length - 1);
    }
  }

  function onPaste(e: ClipboardEvent): void {
    e.preventDefault();
    const text = e.clipboardData?.getData('text') ?? '';
    if (!text) return;

    const { chips: newChips, rest } = parsePaste(text);
    if (newChips.length > 0) {
      onChipsChange([...chips, ...newChips]);
    }
    buffer = rest;
    if (rest.trim()) {
      onWarning(`Couldn't recognize: ${rest.trim()}`);
    } else {
      onWarning(null);
    }
  }

  /** Focus the input when clicking anywhere in the field row. */
  function onRowClick(): void {
    inputEl?.focus();
  }

  /**
   * Return true when the chip's email is in the seen-addresses history but
   * NOT already a saved JMAP Contact. Used to show the "Save to contacts"
   * affordance (REQ-MAIL-11 secondary).
   * TODO REQ-MAIL-11 secondary: wire "Save to contacts" into the chip menu
   * when the Contact/set round-trip is tested end-to-end.
   */
  function isSeenOnly(chip: Recipient): boolean {
    const email = chip.email.toLowerCase();
    const inContacts = contacts.suggestions.some((c) => c.email.toLowerCase() === email);
    if (inContacts) return false;
    return seenAddresses.entries.some((sa) => sa.email.toLowerCase() === email);
  }

  /** Create a JMAP Contact for a chip whose address is seen-only. */
  async function saveToContacts(chip: Recipient): Promise<void> {
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/set',
          {
            accountId,
            create: {
              new1: {
                name: chip.name
                  ? {
                      full: chip.name,
                      components: [{ type: 'personal', value: chip.name }],
                    }
                  : undefined,
                emails: {
                  primary: { address: chip.email },
                },
              },
            },
          },
          [Capability.Contacts],
        );
      });
      strict(responses);
      // The server removes the matching SeenAddress row on the next state
      // advance (REQ-MAIL-11l). No local cleanup needed here.
    } catch (err) {
      console.error('saveToContacts failed', err);
    }
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="recipient-field" onclick={onRowClick}>
  <span class="label" aria-hidden="true">{label}</span>
  <div class="chip-row" role="group" aria-label="{label} recipients">
    {#each chips as chip, i (recipientToString(chip) + i)}
      <span class="chip">
        <span class="chip-label" title={chip.name ? chip.email : undefined}>
          {chip.name ?? chip.email}
        </span>
        {#if isSeenOnly(chip)}
          <button
            type="button"
            class="chip-save"
            title="Save to contacts"
            aria-label="Save {chip.email} to contacts"
            {disabled}
            onclick={(e) => { e.stopPropagation(); void saveToContacts(chip); }}
          >
            +
          </button>
        {/if}
        <button
          type="button"
          class="chip-remove"
          aria-label="Remove {chip.name ?? chip.email}"
          {disabled}
          onclick={(e) => { e.stopPropagation(); removeChip(i); }}
        >
          x
        </button>
      </span>
    {/each}
    <div class="input-wrap">
      <input
        bind:this={inputEl}
        type="text"
        value={buffer}
        {placeholder}
        {disabled}
        spellcheck="false"
        autocomplete="off"
        oninput={onInput}
        onkeydown={onKeyDown}
        onpaste={onPaste}
        onfocus={onFocus}
        onblur={onBlur}
      />
      {#if isOpen && suggestions.length > 0}
        <ul class="dropdown" role="listbox" aria-label="Address suggestions">
          {#each suggestions as s, i (s.id + s.email)}
            <li class:focused={focusIdx === i}>
              <button
                type="button"
                role="option"
                aria-selected={focusIdx === i}
                onmouseenter={() => (focusIdx = i)}
                onmousedown={(e) => {
                  e.preventDefault();
                  commitSuggestion(s);
                }}
              >
                <span class="name">{s.name || s.email}</span>
                {#if s.name}
                  <span class="email">{s.email}</span>
                {/if}
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>
</div>

<style>
  .recipient-field {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-04);
    padding: var(--spacing-02) 0;
    border-bottom: 1px solid var(--border-subtle-01);
    cursor: text;
    flex: 1;
    min-width: 0;
  }

  .label {
    width: 6em;
    flex: 0 0 auto;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    padding-top: 3px;
  }

  .chip-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--spacing-02);
    flex: 1;
    min-width: 0;
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-01);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    padding: 1px var(--spacing-02) 1px var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    line-height: 1.4;
    max-width: 24em;
    overflow: hidden;
  }

  .chip-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-primary);
  }

  .chip-remove,
  .chip-save {
    flex: 0 0 auto;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    font-size: 11px;
    line-height: 1;
    color: var(--text-helper);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .chip-remove:hover:not(:disabled),
  .chip-save:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .chip-remove:disabled,
  .chip-save:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .input-wrap {
    position: relative;
    flex: 1;
    min-width: 120px;
  }

  input {
    width: 100%;
    background: transparent;
    border: none;
    outline: none;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    padding: 2px 0;
  }
  input::placeholder {
    color: var(--text-helper);
  }
  input:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .dropdown {
    position: absolute;
    z-index: 30;
    top: calc(100% + 2px);
    left: 0;
    right: 0;
    list-style: none;
    margin: 0;
    padding: var(--spacing-01) 0;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.4);
    max-height: 240px;
    overflow: auto;
  }
  .dropdown li.focused {
    background: var(--layer-01);
  }
  .dropdown button {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--spacing-01);
    width: 100%;
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-primary);
    text-align: left;
  }
  .name {
    font-weight: 500;
  }
  .email {
    color: var(--text-helper);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
</style>
