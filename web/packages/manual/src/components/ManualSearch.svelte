<script lang="ts">
  /**
   * ManualSearch renders the heading/title filter input for the left rail.
   *
   * Scope is title/heading text only.  Body text search is explicitly out
   * of scope for v1.
   */
  interface Props {
    query: string;
    onQuery: (q: string) => void;
    t: (key: string) => string;
  }

  const { query, onQuery, t }: Props = $props();
</script>

<div class="manual-search">
  <label class="sr-only" for="manual-search-input">
    {t('manual.search.label')}
  </label>
  <div class="manual-search__input-wrap">
    <span class="manual-search__icon" aria-hidden="true">
      <!-- Magnifying glass (text, no emoji, no external icon dep) -->
      [S]
    </span>
    <input
      id="manual-search-input"
      type="search"
      class="manual-search__input"
      placeholder={t('manual.search.placeholder')}
      value={query}
      oninput={(e) => onQuery((e.currentTarget as HTMLInputElement).value)}
      autocomplete="off"
      spellcheck={false}
    />
    {#if query.length > 0}
      <button
        type="button"
        class="manual-search__clear"
        aria-label="Clear search"
        onclick={() => onQuery('')}
      >x</button>
    {/if}
  </div>
</div>

<style>
  .manual-search {
    padding: var(--spacing-03) var(--spacing-04);
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .manual-search__input-wrap {
    display: flex;
    align-items: center;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    transition: border-color var(--duration-fast-01) var(--easing-productive-enter);
  }

  .manual-search__input-wrap:focus-within {
    border-color: var(--focus);
    outline: 2px solid var(--focus);
    outline-offset: -1px;
  }

  .manual-search__icon {
    color: var(--text-helper);
    font-size: var(--type-code-01-size);
    font-family: var(--font-mono);
    flex-shrink: 0;
    user-select: none;
  }

  .manual-search__input {
    flex: 1;
    background: transparent;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    border: none;
    outline: none;
    min-width: 0;
  }

  .manual-search__input::placeholder {
    color: var(--text-helper);
  }

  .manual-search__clear {
    color: var(--text-helper);
    font-size: var(--type-code-01-size);
    width: 20px;
    height: 20px;
    border-radius: var(--radius-pill);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }

  .manual-search__clear:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
</style>
