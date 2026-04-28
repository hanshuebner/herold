<script lang="ts">
  /**
   * Categories settings form — Wave 3.13, REQ-CAT-40..45, REQ-CAT-30.
   *
   * Sections:
   *   0. Transparency disclosure: current effective prompt + disclosure note
   *      (REQ-CAT-45).
   *   1. Category list editor: rename, reorder (up/down), add, remove.
   *      Primary cannot be removed (REQ-CAT-40).
   *   2. Advanced: edit the system prompt textarea (REQ-CAT-41).
   *      "Reset to default" button (REQ-CAT-42).
   *   3. Re-categorise inbox button (REQ-CAT-30). Disabled when
   *      bulkRecategoriseEnabled is false on the capability.
   *
   * Patterns from VacationForm.svelte: load on mount, optimistic set,
   * toast on conflict.
   */
  import { untrack } from 'svelte';
  import {
    categorySettings,
    type Category,
  } from '../../lib/settings/category-settings.svelte';
  import { llmTransparency } from '../../lib/llm/transparency.svelte';

  $effect(() => {
    if (categorySettings.loadStatus === 'idle') {
      untrack(() => {
        void categorySettings.load();
      });
    }
  });

  // Load LLM transparency data for the prompt-disclosure section (REQ-CAT-45).
  $effect(() => {
    if (llmTransparency.loadStatus === 'idle') {
      untrack(() => {
        void llmTransparency.load();
      });
    }
  });

  let disclosureNote = $derived(llmTransparency.data?.disclosureNote ?? '');
  let effectivePrompt = $derived(
    llmTransparency.data?.categoriserPrompt ?? categorySettings.systemPrompt,
  );

  // Local copy of the category list for the editor. We keep a local mutable
  // copy so the user can manipulate the list and submit it atomically.
  let localCategories = $state<Category[]>([]);
  let promptDraft = $state('');
  let editing = $state(false);
  let addingName = $state('');

  // Sync local copy from the store whenever it reloads.
  $effect(() => {
    if (categorySettings.loadStatus === 'ready') {
      untrack(() => {
        localCategories = categorySettings.categories.map((c) => ({ ...c }));
        promptDraft = categorySettings.systemPrompt;
      });
    }
  });

  function isPrimary(cat: Category): boolean {
    // Primary is the first category in the default set, or any whose name
    // (case-insensitive) is "primary".
    return cat.name.toLowerCase() === 'primary';
  }

  function moveUp(i: number): void {
    if (i <= 0) return;
    const next = [...localCategories];
    [next[i - 1], next[i]] = [next[i]!, next[i - 1]!];
    localCategories = reorder(next);
  }

  function moveDown(i: number): void {
    if (i >= localCategories.length - 1) return;
    const next = [...localCategories];
    [next[i], next[i + 1]] = [next[i + 1]!, next[i]!];
    localCategories = reorder(next);
  }

  function reorder(cats: Category[]): Category[] {
    return cats.map((c, idx) => ({ ...c, order: idx }));
  }

  function startRename(cat: Category): void {
    // Inline edit: store the index being edited.
    editingId = cat.id;
    editingName = cat.name;
  }

  function commitRename(cat: Category): void {
    const trimmed = editingName.trim();
    if (!trimmed) {
      cancelEdit();
      return;
    }
    localCategories = localCategories.map((c) =>
      c.id === cat.id ? { ...c, name: trimmed } : c,
    );
    editingId = null;
    editingName = '';
  }

  function cancelEdit(): void {
    editingId = null;
    editingName = '';
  }

  let editingId = $state<string | null>(null);
  let editingName = $state('');

  function removeCategory(cat: Category): void {
    localCategories = localCategories.filter((c) => c.id !== cat.id);
  }

  function addCategory(): void {
    const name = addingName.trim();
    if (!name) return;
    const id = name.toLowerCase().replace(/\s+/g, '-');
    const maxOrder = localCategories.reduce((m, c) => Math.max(m, c.order), -1);
    localCategories = [...localCategories, { id, name, order: maxOrder + 1 }];
    addingName = '';
  }

  async function saveCategories(): Promise<void> {
    await categorySettings.setCategories(localCategories);
  }

  async function savePrompt(): Promise<void> {
    await categorySettings.setSystemPrompt(promptDraft);
  }

  async function resetPrompt(): Promise<void> {
    promptDraft = categorySettings.defaultPrompt;
    await categorySettings.reset();
  }

  async function recategorise(): Promise<void> {
    await categorySettings.recategorise('inbox-recent');
  }

  function handleRenameKeydown(e: KeyboardEvent, cat: Category): void {
    if (e.key === 'Enter') {
      e.preventDefault();
      commitRename(cat);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      cancelEdit();
    }
  }

  function handleAddKeydown(e: KeyboardEvent): void {
    if (e.key === 'Enter') {
      e.preventDefault();
      addCategory();
    }
  }
</script>

{#if categorySettings.loadStatus === 'loading' || categorySettings.loadStatus === 'idle'}
  <p class="hint">Loading…</p>
{:else if categorySettings.loadStatus === 'error'}
  <p class="error" role="alert">{categorySettings.loadError}</p>
  <button type="button" onclick={() => void categorySettings.load(true)}>Retry</button>
{:else}
  {#if categorySettings.recategorising}
    <div class="progress-banner" role="status">
      Re-categorisation in progress — results will update automatically.
    </div>
  {/if}

  <!-- REQ-CAT-45: transparency at rest — show the current effective prompt. -->
  <section class="form-section">
    <h3>How your mail is classified</h3>
    <p class="hint">
      This is the prompt used to categorise your mail. Your messages are sent to
      herold's configured classifier endpoint along with this prompt.
    </p>
    {#if effectivePrompt}
      <pre class="prompt-display">{effectivePrompt}</pre>
    {:else}
      <p class="hint">(Default prompt — not yet loaded.)</p>
    {/if}
    {#if disclosureNote}
      <div class="disclosure-note" role="note">
        <p>{disclosureNote}</p>
      </div>
    {/if}
  </section>

  <section class="form-section">
    <h3>Category list</h3>
    <p class="hint">
      Drag or use the arrows to reorder. Primary is required and cannot be removed.
    </p>

    <ul class="cat-list">
      {#each localCategories as cat, i (cat.id)}
        <li class="cat-row">
          <div class="cat-order-btns">
            <button
              type="button"
              class="icon-btn"
              aria-label="Move up"
              disabled={i === 0}
              onclick={() => moveUp(i)}
            >
              &#8593;
            </button>
            <button
              type="button"
              class="icon-btn"
              aria-label="Move down"
              disabled={i === localCategories.length - 1}
              onclick={() => moveDown(i)}
            >
              &#8595;
            </button>
          </div>

          {#if editingId === cat.id}
            <input
              type="text"
              class="rename-input"
              bind:value={editingName}
              onkeydown={(e) => handleRenameKeydown(e, cat)}
              aria-label="Category name"
            />
            <button type="button" class="small-btn" onclick={() => commitRename(cat)}>
              Save
            </button>
            <button type="button" class="small-btn" onclick={cancelEdit}>
              Cancel
            </button>
          {:else}
            <span class="cat-name">{cat.name}</span>
            <button
              type="button"
              class="icon-btn"
              aria-label="Rename {cat.name}"
              onclick={() => startRename(cat)}
            >
              ✎
            </button>
            <button
              type="button"
              class="icon-btn danger"
              aria-label="Remove {cat.name}"
              disabled={isPrimary(cat)}
              title={isPrimary(cat) ? 'Primary cannot be removed' : `Remove ${cat.name}`}
              onclick={() => removeCategory(cat)}
            >
              &#10005;
            </button>
          {/if}
        </li>
      {/each}
    </ul>

    <div class="add-row">
      <input
        type="text"
        placeholder="New category name"
        bind:value={addingName}
        onkeydown={handleAddKeydown}
        aria-label="New category name"
      />
      <button
        type="button"
        class="small-btn"
        onclick={addCategory}
        disabled={!addingName.trim()}
      >
        Add
      </button>
    </div>

    <div class="action-row">
      <button type="button" class="primary" onclick={() => void saveCategories()}>
        Save categories
      </button>
    </div>
  </section>

  <section class="form-section">
    <h3>Advanced — Edit prompt</h3>
    <p class="hint">
      The system prompt used by the LLM to classify your mail into categories.
      Editing this changes how future mail (and re-categorised mail) is classified.
      Max 32 KB.
    </p>
    <textarea
      rows="8"
      bind:value={promptDraft}
      aria-label="Classification prompt"
      maxlength="32768"
    ></textarea>
    <div class="action-row">
      <button type="button" class="primary" onclick={() => void savePrompt()}>
        Save prompt
      </button>
      <button
        type="button"
        onclick={() => void resetPrompt()}
        title="Revert to the shipped default prompt"
      >
        Reset to default
      </button>
    </div>
  </section>

  <section class="form-section">
    <h3>Re-categorise inbox</h3>
    <p class="hint">
      Run the classifier on your recent inbox (up to 1000 messages). Results
      appear as the job progresses in the background.
    </p>
    <div class="action-row">
      <button
        type="button"
        class="primary"
        onclick={() => void recategorise()}
        disabled={!categorySettings.bulkRecategoriseEnabled || categorySettings.recategorising}
        title={categorySettings.bulkRecategoriseEnabled
          ? 'Re-categorise recent inbox'
          : 'Bulk re-categorisation is not enabled on this server'}
      >
        {categorySettings.recategorising ? 'Running…' : 'Re-categorise inbox'}
      </button>
      {#if !categorySettings.bulkRecategoriseEnabled}
        <span class="hint">Not available on this server.</span>
      {/if}
    </div>
  </section>
{/if}

<style>
  .form-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-06);
  }
  h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    margin: 0;
    color: var(--text-secondary);
  }
  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .progress-banner {
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-left: 4px solid var(--interactive);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    margin-bottom: var(--spacing-04);
  }

  .cat-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .cat-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
  }
  .cat-order-btns {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .cat-name {
    flex: 1;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
  }

  .icon-btn {
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-size: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .icon-btn:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .icon-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .icon-btn.danger:hover:not(:disabled) {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  .rename-input {
    flex: 1;
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--interactive);
    border-radius: var(--radius-md);
    padding: var(--spacing-01) var(--spacing-03);
    font-family: inherit;
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
  }

  .add-row {
    display: flex;
    gap: var(--spacing-03);
    align-items: center;
  }
  .add-row input {
    flex: 1;
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    font-family: inherit;
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
  }
  .add-row input:focus {
    border-color: var(--interactive);
    outline: none;
  }

  textarea {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    resize: vertical;
    min-height: 160px;
  }
  textarea:focus {
    border-color: var(--interactive);
    outline: none;
  }

  .action-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .small-btn {
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .small-btn:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .small-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }

  .primary {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  button:not(.icon-btn):not(.small-btn):not(.primary) {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  button:not(.icon-btn):not(.small-btn):not(.primary):hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .prompt-display {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 200px;
    overflow-y: auto;
    margin: 0;
  }

  .disclosure-note {
    background: var(--layer-01);
    border-left: 3px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
  }

  .disclosure-note p {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }
</style>
