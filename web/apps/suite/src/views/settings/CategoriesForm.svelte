<script lang="ts">
  /**
   * Categories settings form -- REQ-CAT-40..45, REQ-CAT-30.
   *
   * Sections (revised 2026-04-28, REQ-CAT-40 withdrawn):
   *   0. Transparency disclosure: current effective prompt + disclosure note
   *      (REQ-CAT-45).
   *   1. Classification prompt editor (REQ-CAT-41).
   *      "Reset to default" button (REQ-CAT-42).
   *   2. Current categories: read-only chip display of server-derived
   *      `derivedCategories` (REQ-CAT-40, REQ-FILT-217).
   *      Empty-state hint when no categories yet.
   *   3. Re-categorise inbox button (REQ-CAT-30).
   *
   * The reorderable category list editor is intentionally removed.
   * The prompt is the only user-editable lever; categories are derived
   * server-side and shown here read-only.
   */
  import { untrack } from 'svelte';
  import { categorySettings } from '../../lib/settings/category-settings.svelte';
  import { llmTransparency } from '../../lib/llm/transparency.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';

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

  let promptDraft = $state('');

  // Sync prompt draft from the store whenever it loads.
  $effect(() => {
    if (categorySettings.loadStatus === 'ready') {
      untrack(() => {
        promptDraft = categorySettings.systemPrompt;
      });
    }
  });

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
</script>

{#if categorySettings.loadStatus === 'loading' || categorySettings.loadStatus === 'idle'}
  <p class="hint">{t('common.loading')}</p>
{:else if categorySettings.loadStatus === 'error'}
  <p class="error" role="alert">{categorySettings.loadError}</p>
  <button type="button" onclick={() => void categorySettings.load(true)}>{t('common.retry')}</button>
{:else}
  {#if categorySettings.recategorising}
    <div class="progress-banner" role="status">
      {t('cat.recategorise.inProgress')}
    </div>
  {/if}

  <!-- REQ-CAT-45: transparency at rest -- show the current effective prompt. -->
  <section class="form-section">
    <h3>{t('cat.disclosure.heading')}</h3>
    <p class="hint">
      {t('cat.disclosure.hint')}
    </p>
    {#if effectivePrompt}
      <pre class="prompt-display">{effectivePrompt}</pre>
    {:else}
      <p class="hint">{t('cat.disclosure.defaultNotLoaded')}</p>
    {/if}
    {#if disclosureNote}
      <div class="disclosure-note" role="note">
        <p>{disclosureNote}</p>
      </div>
    {/if}
  </section>

  <!-- REQ-CAT-41: prompt editor. -->
  <section class="form-section">
    <h3>{t('cat.prompt.heading')}</h3>
    <p class="hint">
      {t('cat.prompt.hint')}
    </p>
    <textarea
      rows="8"
      bind:value={promptDraft}
      aria-label={t('cat.prompt.heading')}
      maxlength="32768"
    ></textarea>
    <div class="action-row">
      <button type="button" class="primary" onclick={() => void savePrompt()}>
        {t('cat.prompt.save')}
      </button>
      <button
        type="button"
        onclick={() => void resetPrompt()}
        title={t('cat.prompt.resetTitle')}
      >
        {t('cat.prompt.reset')}
      </button>
    </div>
  </section>

  <!-- REQ-CAT-40: derived categories, read-only. -->
  <section class="form-section">
    <h3>{t('cat.currentCategories')}</h3>
    <p class="hint">
      {t('cat.currentCategories.hint')}
    </p>
    {#if categorySettings.derivedCategories.length > 0}
      <ul class="chip-list" aria-label={t('cat.currentCategories')}>
        {#each categorySettings.derivedCategories as name (name)}
          <li class="chip">{name}</li>
        {/each}
      </ul>
    {:else}
      <p class="hint empty-state">
        {t('cat.currentCategories.empty')}
      </p>
    {/if}
  </section>

  <!-- REQ-CAT-30: bulk re-categorisation. -->
  <section class="form-section">
    <h3>{t('cat.recategorise.heading')}</h3>
    <p class="hint">
      {t('cat.recategorise.hint')}
    </p>
    <div class="action-row">
      <button
        type="button"
        class="primary"
        onclick={() => void recategorise()}
        disabled={!categorySettings.bulkRecategoriseEnabled || categorySettings.recategorising}
        title={categorySettings.bulkRecategoriseEnabled
          ? t('cat.recategorise.runTitle')
          : t('cat.recategorise.disabledTitle')}
      >
        {categorySettings.recategorising ? t('cat.recategorise.running') : t('cat.recategorise.run')}
      </button>
      {#if !categorySettings.bulkRecategoriseEnabled}
        <span class="hint">{t('cat.recategorise.notAvailable')}</span>
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

  /* Read-only chip list for derivedCategories (REQ-CAT-40). */
  .chip-list {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }

  .empty-state {
    font-style: italic;
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

  button:not(.primary) {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  button:not(.primary):hover {
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
