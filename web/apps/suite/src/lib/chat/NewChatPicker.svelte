<script lang="ts">
  /**
   * New-chat picker modal — DM and Space creation.
   *
   * Mounted once in Shell.svelte (same pattern as ConfirmDialog / PromptDialog).
   * Open via newChatPicker.open({ mode: 'dm' | 'space' }).
   *
   * REQ-CHAT-01a..d: single entry point, Herold principal directory typeahead,
   * free-email validation, dedup routing to existing DM.
   * REQ-CHAT-02a: Space requires name + at least one member chip.
   * REQ-CHAT-15: principal ids are never rendered.
   */

  import { untrack } from 'svelte';
  import { newChatPicker, type PickerMode } from './new-chat-picker.svelte';
  import { chat } from './store.svelte';
  import { chatOverlay } from './overlay-store.svelte';
  import { toast } from '../toast/toast.svelte';
  import type { Principal } from './types';

  /** A confirmed recipient chip. */
  interface Chip {
    principal: Principal;
  }

  let ctx = $derived(newChatPicker.pending);

  let mode = $state<PickerMode>('dm');
  let inputValue = $state('');
  let suggestions = $state<Principal[]>([]);
  let suggestionsLoading = $state(false);
  let emailError = $state<string | null>(null);
  let chips = $state<Chip[]>([]);
  let spaceName = $state('');
  let spaceDescription = $state('');
  let submitting = $state(false);
  let inputEl = $state<HTMLInputElement | null>(null);
  let selectedSuggestionIndex = $state(-1);

  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  // Reset all transient state whenever the picker is opened or mode changes.
  $effect(() => {
    if (ctx) {
      untrack(() => {
        mode = ctx!.mode;
        reset();
      });
    }
  });

  $effect(() => {
    if (!ctx) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        close();
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  $effect(() => {
    if (ctx) {
      requestAnimationFrame(() => inputEl?.focus());
    }
  });

  function reset(): void {
    inputValue = '';
    suggestions = [];
    suggestionsLoading = false;
    emailError = null;
    chips = [];
    spaceName = '';
    spaceDescription = '';
    submitting = false;
    selectedSuggestionIndex = -1;
    if (debounceTimer !== null) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
  }

  function close(): void {
    if (debounceTimer !== null) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }
    newChatPicker.close();
  }

  function switchMode(m: PickerMode): void {
    mode = m;
    reset();
    requestAnimationFrame(() => inputEl?.focus());
  }

  // ------------------------------------------------------------------
  // Input / typeahead
  // ------------------------------------------------------------------

  function handleInput(): void {
    emailError = null;
    selectedSuggestionIndex = -1;
    const raw = inputValue.trim();

    if (debounceTimer !== null) clearTimeout(debounceTimer);

    if (raw.length === 0) {
      suggestions = [];
      return;
    }

    // If it looks like an email address, skip typeahead — user will commit
    // with Enter / button press and we validate via lookupPrincipalByEmail.
    if (raw.includes('@')) {
      suggestions = [];
      return;
    }

    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      suggestionsLoading = true;
      chat
        .searchPrincipals(raw, 10)
        .then((results) => {
          // Filter out already-added chips.
          const added = new Set(chips.map((c) => c.principal.id));
          suggestions = results.filter((p) => !added.has(p.id));
        })
        .catch(() => {
          suggestions = [];
        })
        .finally(() => {
          suggestionsLoading = false;
        });
    }, 150);
  }

  /** Pick a suggestion from the dropdown list. */
  function pickSuggestion(principal: Principal): void {
    addChip(principal);
    inputValue = '';
    suggestions = [];
    emailError = null;
    selectedSuggestionIndex = -1;
    requestAnimationFrame(() => inputEl?.focus());
  }

  /** Add a chip, deduping by principal id. */
  function addChip(principal: Principal): void {
    if (chips.some((c) => c.principal.id === principal.id)) return;
    // In DM mode, only one recipient.
    if (mode === 'dm') {
      chips = [{ principal }];
    } else {
      chips = [...chips, { principal }];
    }
  }

  function removeChip(principalId: string): void {
    chips = chips.filter((c) => c.principal.id !== principalId);
  }

  /**
   * Commit a free-text email address: validate against Principal/query.
   * Called on Enter (when no suggestion is highlighted) or blur.
   */
  async function commitEmail(): Promise<void> {
    const raw = inputValue.trim();
    if (!raw.includes('@') || raw.length === 0) return;

    emailError = null;
    try {
      const principal = await chat.lookupPrincipalByEmail(raw);
      if (!principal) {
        emailError = `${raw} is not a Herold user on this server`;
        return;
      }
      pickSuggestion(principal);
    } catch {
      emailError = `${raw} is not a Herold user on this server`;
    }
  }

  async function handleKeydown(e: KeyboardEvent): Promise<void> {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      selectedSuggestionIndex = Math.min(
        selectedSuggestionIndex + 1,
        suggestions.length - 1,
      );
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      selectedSuggestionIndex = Math.max(selectedSuggestionIndex - 1, -1);
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (selectedSuggestionIndex >= 0 && suggestions[selectedSuggestionIndex]) {
        pickSuggestion(suggestions[selectedSuggestionIndex]!);
      } else {
        await commitEmail();
      }
    } else if (e.key === 'Backspace' && inputValue === '') {
      chips = chips.slice(0, -1);
    }
  }

  // ------------------------------------------------------------------
  // Submit
  // ------------------------------------------------------------------

  let dmCanSubmit = $derived(chips.length === 1 && !submitting);
  let spaceCanSubmit = $derived(
    spaceName.trim().length > 0 && chips.length >= 1 && !submitting,
  );

  async function submit(): Promise<void> {
    if (mode === 'dm') await submitDM();
    else await submitSpace();
  }

  async function submitDM(): Promise<void> {
    if (!dmCanSubmit) return;
    const chip = chips[0]!;
    submitting = true;
    try {
      const existing = chat.findExistingDM(chip.principal.id);
      if (existing) {
        close();
        chatOverlay.openWindow(existing.id);
        return;
      }
      const { id } = await chat.createConversation({
        kind: 'dm',
        members: [chip.principal.id],
      });
      close();
      chatOverlay.openWindow(id);
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to start conversation';
      toast.show({ message: msg, kind: 'error' });
    } finally {
      submitting = false;
    }
  }

  async function submitSpace(): Promise<void> {
    if (!spaceCanSubmit) return;
    submitting = true;
    try {
      const { id } = await chat.createConversation({
        kind: 'space',
        members: chips.map((c) => c.principal.id),
        name: spaceName.trim(),
        topic: spaceDescription.trim() || undefined,
      });
      close();
      chatOverlay.openWindow(id);
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Failed to create space';
      toast.show({ message: msg, kind: 'error' });
    } finally {
      submitting = false;
    }
  }
</script>

{#if ctx}
  <div class="backdrop" aria-hidden="true" onclick={close}></div>
  <div
    class="modal-wrapper"
    role="dialog"
    aria-modal="true"
    aria-labelledby="ncp-title"
  >
    <div class="modal">
      <div class="modal-header">
        <h2 id="ncp-title" class="title">
          {mode === 'dm' ? 'New direct message' : 'Create space'}
        </h2>
        <button
          type="button"
          class="close-btn"
          aria-label="Close"
          onclick={close}
        >x</button>
      </div>

      <div class="mode-tabs" role="tablist" aria-label="Conversation type">
        <button
          type="button"
          role="tab"
          aria-selected={mode === 'dm'}
          class="tab"
          class:active={mode === 'dm'}
          onclick={() => switchMode('dm')}
        >Direct message</button>
        <button
          type="button"
          role="tab"
          aria-selected={mode === 'space'}
          class="tab"
          class:active={mode === 'space'}
          onclick={() => switchMode('space')}
        >Create Space</button>
      </div>

      <div class="modal-body">
        {#if mode === 'space'}
          <label class="field">
            <span class="field-label">Space name <span class="required" aria-hidden="true">*</span></span>
            <input
              type="text"
              class="text-input"
              bind:value={spaceName}
              placeholder="e.g. Project Hermes"
              autocomplete="off"
            />
          </label>
          <label class="field">
            <span class="field-label">Description (optional)</span>
            <input
              type="text"
              class="text-input"
              bind:value={spaceDescription}
              placeholder="What is this space for?"
              autocomplete="off"
            />
          </label>
        {/if}

        <div class="field">
          <span class="field-label">
            {mode === 'dm' ? 'To' : 'Members'}
          </span>

          <div class="recipient-box" class:has-error={!!emailError}>
            {#each chips as chip (chip.principal.id)}
              <span class="chip" aria-label="Recipient: {chip.principal.displayName}">
                <span class="chip-name">{chip.principal.displayName}</span>
                <span class="chip-email">{chip.principal.email}</span>
                <button
                  type="button"
                  class="chip-remove"
                  aria-label="Remove {chip.principal.displayName}"
                  onclick={() => removeChip(chip.principal.id)}
                >x</button>
              </span>
            {/each}

            {#if mode === 'dm' && chips.length === 1}
              <!-- DM: one recipient picked, hide input -->
            {:else}
              <input
                type="text"
                class="recipient-input"
                bind:value={inputValue}
                bind:this={inputEl}
                placeholder={chips.length === 0 ? 'Name or email' : 'Add another'}
                autocomplete="off"
                spellcheck="false"
                aria-label="Search for a person"
                aria-autocomplete="list"
                oninput={handleInput}
                onkeydown={(e) => void handleKeydown(e)}
              />
            {/if}
          </div>

          {#if emailError}
            <p class="field-error" role="alert">{emailError}</p>
          {/if}

          {#if suggestions.length > 0}
            <ul class="suggestions" role="listbox" aria-label="People suggestions">
              {#each suggestions as suggestion, idx (suggestion.id)}
                <li
                  role="option"
                  aria-selected={idx === selectedSuggestionIndex}
                  class="suggestion-row"
                  class:highlighted={idx === selectedSuggestionIndex}
                >
                  <button
                    type="button"
                    class="suggestion-btn"
                    onclick={() => pickSuggestion(suggestion)}
                  >
                    <span class="suggestion-name">{suggestion.displayName}</span>
                    <span class="suggestion-email">{suggestion.email}</span>
                  </button>
                </li>
              {/each}
            </ul>
          {:else if suggestionsLoading}
            <p class="suggestions-loading" aria-live="polite">Searching...</p>
          {/if}
        </div>
      </div>

      <div class="modal-footer">
        <button type="button" class="btn-secondary" onclick={close}>Cancel</button>
        {#if mode === 'dm'}
          <button
            type="button"
            class="btn-primary"
            disabled={!dmCanSubmit}
            onclick={() => void submit()}
          >
            {submitting ? 'Starting...' : 'Start chat'}
          </button>
        {:else}
          <button
            type="button"
            class="btn-primary"
            disabled={!spaceCanSubmit}
            onclick={() => void submit()}
          >
            {submitting ? 'Creating...' : 'Create Space'}
          </button>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 999;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    pointer-events: none;
    padding: var(--spacing-05);
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 520px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-01-size);
    font-weight: var(--type-heading-01-weight);
    line-height: var(--type-heading-01-line);
    color: var(--text-primary);
  }

  .close-btn {
    width: 28px;
    height: 28px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-md);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .close-btn:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .mode-tabs {
    display: flex;
    gap: var(--spacing-01);
    border-bottom: 1px solid var(--border-subtle-01);
    margin-bottom: var(--spacing-02);
  }

  .tab {
    padding: var(--spacing-02) var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    color: var(--text-secondary);
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;
    transition:
      color var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .tab:hover {
    color: var(--text-primary);
  }

  .tab.active {
    color: var(--text-primary);
    border-bottom-color: var(--interactive);
  }

  .modal-body {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .field-label {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
  }

  .required {
    color: var(--support-error);
  }

  .text-input {
    width: 100%;
    background: var(--layer-01);
    color: var(--text-primary);
    padding: var(--spacing-03) var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-size: var(--type-body-01-size);
  }

  .text-input:focus {
    outline: 2px solid var(--interactive);
    outline-offset: -1px;
  }

  .recipient-box {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    align-items: center;
    min-height: 40px;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    cursor: text;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }

  .recipient-box:focus-within {
    outline: 2px solid var(--interactive);
    outline-offset: -1px;
  }

  .recipient-box.has-error {
    border-color: var(--support-error);
  }

  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    border-radius: var(--radius-pill);
    padding: var(--spacing-01) var(--spacing-03);
    font-size: var(--type-helper-text-01-size);
    max-width: 200px;
  }

  .chip-name {
    font-weight: 600;
    color: var(--text-primary);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .chip-email {
    color: var(--text-helper);
    font-size: 11px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .chip-remove {
    width: 16px;
    height: 16px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-pill);
    color: var(--text-helper);
    font-size: 10px;
    flex-shrink: 0;
  }

  .chip-remove:hover {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  .recipient-input {
    flex: 1;
    min-width: 140px;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-01) 0;
    border: none;
    outline: none;
  }

  .recipient-input::placeholder {
    color: var(--text-placeholder);
  }

  .field-error {
    color: var(--support-error);
    font-size: var(--type-helper-text-01-size);
    margin: 0;
  }

  .suggestions {
    list-style: none;
    margin: 0;
    padding: 0;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.3);
    max-height: 240px;
    overflow-y: auto;
  }

  .suggestion-row {
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .suggestion-row:last-child {
    border-bottom: none;
  }

  .suggestion-btn {
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--spacing-03) var(--spacing-04);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .suggestion-btn:hover,
  .highlighted .suggestion-btn {
    background: var(--layer-02);
  }

  .suggestion-name {
    font-weight: 600;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .suggestion-email {
    font-size: var(--type-helper-text-01-size);
    color: var(--text-helper);
  }

  .suggestions-loading {
    color: var(--text-helper);
    font-size: var(--type-helper-text-01-size);
    font-style: italic;
    margin: var(--spacing-02) var(--spacing-04);
  }

  .modal-footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  .btn-primary,
  .btn-secondary {
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      filter var(--duration-fast-02) var(--easing-productive-enter);
  }

  .btn-primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border: 1px solid transparent;
  }

  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .btn-primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .btn-secondary {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }

  .btn-secondary:hover {
    background: var(--layer-03);
  }
</style>
