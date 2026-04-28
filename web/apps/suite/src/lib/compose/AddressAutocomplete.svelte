<script lang="ts">
  /**
   * Address-field autocomplete: standard text input, plus a dropdown of
   * matching contacts that appears once the user has typed at least one
   * character past the most recent comma. Selecting a suggestion replaces
   * the partial token with `Name <email>` and inserts a comma so the
   * user can keep typing the next address.
   */
  import { contacts, type ContactSuggestion } from '../contacts/store.svelte';

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

  // The last token of the input drives the filter. Tokens split on comma;
  // whitespace inside the token is allowed (so "John D" matches).
  let activeToken = $derived.by(() => {
    const idx = value.lastIndexOf(',');
    return idx === -1 ? value.trim() : value.slice(idx + 1).trim();
  });

  // Don't show suggestions for an empty token; do show them once the
  // user has typed at least 2 characters or after a comma.
  let suggestions = $derived<ContactSuggestion[]>(
    isOpen && activeToken.length >= 2 ? contacts.filter(activeToken) : [],
  );

  // Reset focus when the suggestion shape changes.
  $effect(() => {
    void suggestions;
    focusIdx = 0;
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
