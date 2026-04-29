<script lang="ts">
  /**
   * Address-field autocomplete: standard text input, plus a dropdown of
   * matching contacts that appears once the user has typed at least two
   * characters past the most recent comma. Selecting a suggestion replaces
   * the partial token with `Name <email>` and inserts a comma so the
   * user can keep typing the next address.
   *
   * Suggestions are fetched asynchronously via contacts.filterAsync() so
   * that the Directory/search query (when the server advertises the
   * directory-autocomplete capability) does not block the UI.
   * Keystrokes are debounced by 150ms; stale in-flight results are
   * discarded via a per-call sequence number.
   */
  import { contacts, type ContactSuggestion } from '../contacts/store.svelte';
  import { hasDirectoryAutocomplete } from '../auth/capabilities';

  interface Props {
    value: string;
    placeholder?: string;
    disabled?: boolean;
    onChange: (next: string) => void;
  }
  let { value = $bindable(), placeholder, disabled, onChange }: Props = $props();

  let inputEl = $state<HTMLInputElement | null>(null);
  let isOpen = $state(false);
  let focusIdx = $state(0);

  // Async suggestions list — populated by the debounced filterAsync call.
  let suggestions = $state<ContactSuggestion[]>([]);
  // True when the user has typed >= 2 chars and is waiting for results.
  let hasQueried = $state(false);

  // The last token of the input drives the filter. Tokens split on comma;
  // whitespace inside the token is allowed (so "John D" matches).
  let activeToken = $derived.by(() => {
    const idx = value.lastIndexOf(',');
    return idx === -1 ? value.trim() : value.slice(idx + 1).trim();
  });

  // Per-call sequence number for stale-result suppression.
  let seq = 0;
  let debounceTimer: ReturnType<typeof setTimeout> | null = null;

  // Reset focus when the suggestion shape changes.
  $effect(() => {
    void suggestions;
    focusIdx = 0;
  });

  // Kick off a debounced async query whenever the active token changes.
  $effect(() => {
    const token = activeToken;

    if (debounceTimer !== null) {
      clearTimeout(debounceTimer);
      debounceTimer = null;
    }

    if (!isOpen || token.length < 2) {
      suggestions = [];
      hasQueried = false;
      return;
    }

    debounceTimer = setTimeout(() => {
      debounceTimer = null;
      hasQueried = true;
      const callSeq = ++seq;
      contacts.filterAsync(token, 8).then((results) => {
        // Discard stale results from an earlier keystroke.
        if (callSeq !== seq) return;
        suggestions = results;
      }).catch(() => {
        if (callSeq !== seq) return;
        suggestions = [];
      });
    }, 150);
  });

  // Trigger contacts load lazily on first focus.
  function onFocus(): void {
    if (contacts.status === 'idle') void contacts.load();
    isOpen = true;
  }

  function onBlur(): void {
    // Defer close so a click on a suggestion lands first.
    setTimeout(() => {
      isOpen = false;
    }, 120);
  }

  function commit(s: ContactSuggestion): void {
    const idx = value.lastIndexOf(',');
    const before = idx === -1 ? '' : value.slice(0, idx + 1) + ' ';
    const renderedAddr = s.name ? `${s.name} <${s.email}>` : s.email;
    const next = `${before}${renderedAddr}, `;
    value = next;
    onChange(next);
    isOpen = false;
    focusIdx = 0;
    suggestions = [];
    hasQueried = false;
    requestAnimationFrame(() => inputEl?.focus());
  }

  function onKey(e: KeyboardEvent): void {
    if (!isOpen || suggestions.length === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      focusIdx = Math.min(focusIdx + 1, suggestions.length - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      focusIdx = Math.max(focusIdx - 1, 0);
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      const target = suggestions[focusIdx];
      if (target) {
        e.preventDefault();
        commit(target);
      }
    } else if (e.key === 'Escape') {
      isOpen = false;
    }
  }

  function onInput(e: Event): void {
    const next = (e.currentTarget as HTMLInputElement).value;
    value = next;
    onChange(next);
    isOpen = true;
  }

  // The empty-state hint is shown when the user has focused, typed >= 2 chars,
  // and the (now-resolved) query returned no results.
  let showEmptyHint = $derived(
    isOpen && hasQueried && activeToken.length >= 2 && suggestions.length === 0,
  );

  let emptyHintText = $derived(
    hasDirectoryAutocomplete()
      ? 'No matches in your contacts, recent recipients, or directory.'
      : 'No matches in your contacts or recent recipients.',
  );
</script>

<div class="address-autocomplete">
  <input
    bind:this={inputEl}
    type="text"
    {placeholder}
    {disabled}
    spellcheck="false"
    autocomplete="off"
    {value}
    oninput={onInput}
    onfocus={onFocus}
    onblur={onBlur}
    onkeydown={onKey}
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
              commit(s);
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
  {:else if showEmptyHint}
    <div class="dropdown empty-hint" role="status" aria-live="polite">
      {emptyHintText}
    </div>
  {/if}
</div>

<style>
  .address-autocomplete {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  input {
    width: 100%;
    background: transparent;
    color: var(--text-primary);
    border: none;
    border-bottom: 1px solid var(--border-subtle-01);
    padding: var(--spacing-02) 0;
    font-size: var(--type-body-01-size);
  }
  input:focus {
    outline: none;
    border-bottom-color: var(--interactive);
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
  .dropdown.empty-hint {
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-01-size);
    font-style: italic;
    list-style: none;
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
