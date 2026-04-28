<script lang="ts">
  /**
   * Category picker overlay — Wave 3.13, REQ-CAT-20..22.
   *
   * A small modal that lists the user's configured categories. Selecting
   * one fires `Email/set` to move the email (or whole thread) to that
   * category. Opened via `categoryPicker.open(emailId)` or
   * `categoryPicker.openForThread(threadId, emailId)`.
   *
   * The `m` shortcut in MailView triggers this overlay.
   */
  import { categoryPicker } from './category-picker.svelte';
  import { categorySettings, categoryKeyword } from '../settings/category-settings.svelte';
  import { mail } from './store.svelte';

  function handleBackdrop(e: MouseEvent): void {
    if (e.target === e.currentTarget) categoryPicker.close();
  }

  function handleKeydown(e: KeyboardEvent): void {
    if (e.key === 'Escape') categoryPicker.close();
  }

  async function pick(catName: string | null): Promise<void> {
    const { emailId, threadGranular } = categoryPicker;
    if (!emailId) return;
    categoryPicker.close();
    const kw = catName ? categoryKeyword(catName) : null;
    await mail.setCategoryKeyword(emailId, kw, threadGranular);
  }
</script>

{#if categoryPicker.isOpen}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="picker-backdrop"
    role="dialog"
    aria-modal="true"
    aria-label="Move to category"
    tabindex="-1"
    onclick={handleBackdrop}
    onkeydown={handleKeydown}
  >
    <div class="picker-panel">
      <h2>Move to category</h2>
      <ul class="cat-list">
        {#each categorySettings.derivedCategories as name (name)}
          <li>
            <button type="button" onclick={() => pick(name)}>
              {name}
            </button>
          </li>
        {/each}
        {#if categorySettings.derivedCategories.length === 0}
          <li class="empty-hint">No categories available yet.</li>
        {/if}
      </ul>
      <div class="picker-footer">
        <button type="button" class="cancel" onclick={() => categoryPicker.close()}>
          Cancel
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .picker-backdrop {
    position: fixed;
    inset: 0;
    z-index: 200;
    background: rgba(0, 0, 0, 0.35);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .picker-panel {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-md, 0 4px 24px rgba(0,0,0,.18));
    padding: var(--spacing-05);
    min-width: 220px;
    max-width: 320px;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }
  h2 {
    font-size: var(--type-heading-compact-02-size);
    font-weight: var(--type-heading-compact-02-weight);
    line-height: var(--type-heading-compact-02-line);
    margin: 0;
    color: var(--text-primary);
  }
  .cat-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .cat-list button {
    width: 100%;
    text-align: left;
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .cat-list button:hover {
    background: var(--layer-02);
  }
  .picker-footer {
    display: flex;
    justify-content: flex-end;
  }
  .cancel {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
  }
  .cancel:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .empty-hint {
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    list-style: none;
  }
</style>
