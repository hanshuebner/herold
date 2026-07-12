<script lang="ts">
  /**
   * vCard import view — file picker, blob upload, Contact/import call,
   * per-card result summary, and duplicate-candidate handling.
   *
   * Flow:
   *   1. User picks or drops a .vcf file.
   *   2. File is uploaded via jmap.uploadBlob() with Content-Type text/vcard.
   *   3. Contact/import { accountId, blobId, addressBookId? } is called.
   *   4. Summary shows created / skipped / conflict / failed-with-reason
   *      per card (skip/conflict: server matched an existing contact by
   *      uid or primary email and either found identical data -- skipped,
   *      no duplicate created -- or differing data -- conflict, listed
   *      with the matched contact's name and the field-level diff;
   *      REQ-CTS-21, re #206).
   *   5. Cards with an *ambiguous* duplicateCandidates match (zero or more
   *      than one candidate; the card was created) offer Merge /
   *      Delete (skip) / Keep both.
   *
   * REQ-CONT-80 (summary), REQ-CONT-82 (duplicates, cancel), REQ-CONT-83 (error).
   */

  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { contacts } from '../lib/contacts/store.svelte';
  import { contactsListStore } from '../lib/contacts/list-store.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import {
    buildImportArgs,
    parseImportSummary,
    type ImportCardResult,
    type ImportResponse,
  } from '../lib/contacts/vcard-import';

  // ── Phase state machine ───────────────────────────────────────────────────

  type Phase = 'idle' | 'uploading' | 'importing' | 'done' | 'error';

  let phase = $state<Phase>('idle');
  let errorMsg = $state('');
  let importResults = $state<ImportCardResult[]>([]);
  let cancelled = $state(false);

  // Decision per card index for duplicate handling.
  type DupDecision = 'pending' | 'keep' | 'skipping' | 'skipped' | 'merging';
  let dupDecisions = $state<Record<number, DupDecision>>({});

  let summary = $derived(parseImportSummary(importResults));

  // ── File input ────────────────────────────────────────────────────────────

  let fileInputEl = $state<HTMLInputElement | null>(null);
  let isDragOver = $state(false);

  function openFilePicker(): void {
    fileInputEl?.click();
  }

  function handleFileChange(e: Event): void {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) void doImport(file);
    // Reset so the same file can be re-selected after an error.
    if (fileInputEl) fileInputEl.value = '';
  }

  function handleDrop(e: DragEvent): void {
    e.preventDefault();
    isDragOver = false;
    const file = e.dataTransfer?.files[0];
    if (file) void doImport(file);
  }

  function handleDragOver(e: DragEvent): void {
    e.preventDefault();
    isDragOver = true;
  }

  function handleDragLeave(): void {
    isDragOver = false;
  }

  // ── Import flow ───────────────────────────────────────────────────────────

  async function doImport(file: File): Promise<void> {
    const accountId =
      auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) {
      errorMsg = t('contacts.import.error.import');
      phase = 'error';
      return;
    }

    cancelled = false;
    importResults = [];
    dupDecisions = {};

    // Step 1: Upload the .vcf as a blob (REQ-CONT-80).
    phase = 'uploading';
    let blobId: string;
    try {
      const upload = await jmap.uploadBlob({
        accountId,
        body: file,
        type: 'text/vcard',
      });
      blobId = upload.blobId;
    } catch (err) {
      if (cancelled) {
        phase = 'idle';
        return;
      }
      const e = err as { status?: number };
      if (e.status === 413) {
        errorMsg = t('contacts.import.error.tooLarge');
      } else {
        errorMsg = t('contacts.import.error.upload');
      }
      phase = 'error';
      return;
    }

    if (cancelled) {
      phase = 'idle';
      return;
    }

    // Step 2: Call Contact/import.
    phase = 'importing';
    try {
      const addressBookId = contactsListStore.activeBookId ?? undefined;
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/import',
          buildImportArgs(accountId, blobId, addressBookId),
          [Capability.Contacts],
        );
      });

      if (cancelled) {
        phase = 'idle';
        return;
      }

      const resp = responses[0];
      if (!resp) {
        errorMsg = t('contacts.import.error.import');
        phase = 'error';
        return;
      }

      if (resp[0] === 'error') {
        const args = resp[1] as { type?: string };
        if (args.type === 'requestTooLarge') {
          errorMsg = t('contacts.import.error.tooLarge');
        } else {
          errorMsg = t('contacts.import.error.import');
        }
        phase = 'error';
        return;
      }

      const args = resp[1] as ImportResponse;
      importResults = args.results ?? [];
      phase = 'done';

      // Refresh contacts caches after import (REQ-CONT-26 / list-store).
      void contacts.reload();
      void contactsListStore.init();
    } catch {
      if (cancelled) {
        phase = 'idle';
        return;
      }
      errorMsg = t('contacts.import.error.import');
      phase = 'error';
    }
  }

  function handleCancel(): void {
    cancelled = true;
    // Any contacts already created by the server remain (REQ-CONT-82).
    phase = 'idle';
  }

  // ── Duplicate handling (REQ-CONT-82) ─────────────────────────────────────

  async function skipDuplicate(result: ImportCardResult): Promise<void> {
    if (!result.id) return;
    const accountId =
      auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;

    dupDecisions = { ...dupDecisions, [result.index]: 'skipping' };
    try {
      await jmap.batch((b) => {
        b.call(
          'Contact/set',
          { accountId, destroy: [result.id] },
          [Capability.Contacts],
        );
      });
      dupDecisions = { ...dupDecisions, [result.index]: 'skipped' };
      void contactsListStore.init();
    } catch {
      const next = { ...dupDecisions };
      delete next[result.index];
      dupDecisions = next;
      toast.show({ message: t('contacts.detail.deleteError'), kind: 'error' });
    }
  }

  function mergeDuplicate(result: ImportCardResult): void {
    if (!result.id || !result.duplicateCandidates?.length) return;
    dupDecisions = { ...dupDecisions, [result.index]: 'merging' };
    const ids = [result.id, ...result.duplicateCandidates].join(',');
    router.navigate(`/contacts/merge?ids=${encodeURIComponent(ids)}`);
  }

  function keepDuplicate(result: ImportCardResult): void {
    dupDecisions = { ...dupDecisions, [result.index]: 'keep' };
  }

  // ── Summary label helpers ─────────────────────────────────────────────────

  function createdLabel(n: number): string {
    return n === 1
      ? t('contacts.import.summary.created', { n })
      : t('contacts.import.summary.created_many', { n });
  }

  function skippedLabel(n: number): string {
    return n === 1
      ? t('contacts.import.summary.skipped', { n })
      : t('contacts.import.summary.skipped_many', { n });
  }

  function failedLabel(n: number): string {
    return n === 1
      ? t('contacts.import.summary.failed', { n })
      : t('contacts.import.summary.failed_many', { n });
  }
</script>

<div class="import-view">
  <!-- Back button -->
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/contacts')}
    >
      {t('contacts.list.title')}
    </button>
  </div>

  <h1 class="title">{t('contacts.import.title')}</h1>

  <!-- Hidden file input -->
  <input
    bind:this={fileInputEl}
    type="file"
    accept=".vcf,text/vcard"
    class="sr-only"
    aria-hidden="true"
    onchange={handleFileChange}
  />

  {#if phase === 'idle'}
    <!-- File picker / drop zone -->
    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div
      class="drop-zone"
      class:drag-over={isDragOver}
      role="button"
      aria-label={t('contacts.import.dropHint')}
      onclick={openFilePicker}
      onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') openFilePicker(); }}
      ondrop={handleDrop}
      ondragover={handleDragOver}
      ondragleave={handleDragLeave}
    >
      <p class="drop-hint">{t('contacts.import.dropHint')}</p>
      <button type="button" class="pick-btn" onclick={(e) => { e.stopPropagation(); openFilePicker(); }}>
        {t('contacts.import.pickFile')}
      </button>
    </div>

  {:else if phase === 'uploading' || phase === 'importing'}
    <!-- Progress -->
    <div class="progress-area" role="status" aria-live="polite">
      <span class="loading-spinner" aria-hidden="true"></span>
      <span class="progress-msg">
        {phase === 'uploading'
          ? t('contacts.import.uploading')
          : t('contacts.import.processing')}
      </span>
    </div>
    <button
      type="button"
      class="cancel-btn"
      onclick={handleCancel}
    >
      {t('contacts.import.cancel')}
    </button>

  {:else if phase === 'error'}
    <!-- Error -->
    <div class="error-msg" role="alert">
      {errorMsg}
    </div>
    <button
      type="button"
      class="pick-btn"
      onclick={openFilePicker}
    >
      {t('contacts.import.pickFile')}
    </button>

  {:else if phase === 'done'}
    <!-- Import summary (REQ-CONT-80) -->
    <div class="summary" role="region" aria-label={t('contacts.import.done')}>
      <h2 class="summary-title">{t('contacts.import.done')}</h2>

      {#if importResults.length === 0}
        <p class="summary-empty">{t('contacts.import.summary.noResults')}</p>
      {:else}
        <!-- Created count -->
        {#if summary.created.length > 0}
          <p class="summary-stat created">
            {createdLabel(summary.created.length)}
          </p>
        {/if}
        <!-- Skipped count: idempotent re-import, matched an existing
             contact with identical data (REQ-CTS-21, re #206). -->
        {#if summary.skipped.length > 0}
          <p class="summary-stat skipped">
            {skippedLabel(summary.skipped.length)}
          </p>
        {/if}
        <!-- Failed count + reasons (REQ-CONT-80: never silently drop) -->
        {#if summary.failed.length > 0}
          <p class="summary-stat failed">
            {failedLabel(summary.failed.length)}
          </p>
          <ul class="failed-list" aria-label={t('contacts.import.summary.failed_many', { n: summary.failed.length })}>
            {#each summary.failed as card (card.index)}
              <li class="failed-item">
                <span class="failed-card">{t('contacts.import.failedCard', { n: card.index + 1 })}</span>
                {#if card.reason}
                  <span class="failed-reason">
                    {t('contacts.import.failedReason', { reason: card.reason })}
                  </span>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}

        <!-- Conflicts: card matched an existing contact by uid/email but
             its data differs; nothing was created or overwritten
             (REQ-CTS-21, re #206). -->
        {#if summary.conflicts.length > 0}
          <div class="conflict-section" role="region" aria-label={t('contacts.import.conflicts.heading')}>
            <h3 class="conflict-heading">{t('contacts.import.conflicts.heading')}</h3>
            {#each summary.conflicts as card (card.index)}
              <div class="conflict-row" role="group" aria-label={card.matchedName || t('contacts.import.failedCard', { n: card.index + 1 })}>
                <span class="conflict-name">
                  {t('contacts.import.conflicts.matchedWith', { name: card.matchedName ?? '' })}
                </span>
                {#if card.diff && card.diff.length > 0}
                  <ul class="conflict-diff">
                    {#each card.diff as d (d.field)}
                      <li class="conflict-diff-item">
                        {t('contacts.import.conflicts.fieldChanged', {
                          field: d.field,
                          existing: d.existing ?? '–',
                          incoming: d.incoming ?? '–',
                        })}
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/each}
          </div>
        {/if}

        <!-- Duplicate candidates (REQ-CONT-82) -->
        {#if summary.withDuplicates.length > 0}
          <div class="dup-section" role="region" aria-label={t('contacts.import.duplicates.heading')}>
            <h3 class="dup-heading">{t('contacts.import.duplicates.heading')}</h3>
            {#each summary.withDuplicates as card (card.index)}
              {@const decision = dupDecisions[card.index] ?? 'pending'}
              <div class="dup-row" role="group" aria-label={t('contacts.import.failedCard', { n: card.index + 1 })}>
                <span class="dup-card">{t('contacts.import.failedCard', { n: card.index + 1 })}</span>
                {#if decision === 'pending'}
                  <div class="dup-actions">
                    <button
                      type="button"
                      class="dup-btn primary"
                      onclick={() => mergeDuplicate(card)}
                    >
                      {t('contacts.import.duplicates.merge')}
                    </button>
                    <button
                      type="button"
                      class="dup-btn danger"
                      onclick={() => void skipDuplicate(card)}
                    >
                      {t('contacts.import.duplicates.skip')}
                    </button>
                    <button
                      type="button"
                      class="dup-btn secondary"
                      onclick={() => keepDuplicate(card)}
                    >
                      {t('contacts.import.duplicates.keep')}
                    </button>
                  </div>
                {:else if decision === 'skipping'}
                  <span class="dup-status">{t('contacts.import.duplicates.deleting')}</span>
                {:else if decision === 'skipped'}
                  <span class="dup-status resolved">{t('contacts.import.duplicates.deleted')}</span>
                {:else if decision === 'keep'}
                  <span class="dup-status resolved">{t('contacts.import.duplicates.keep')}</span>
                {:else if decision === 'merging'}
                  <span class="dup-status">{t('contacts.import.duplicates.merge')}&hellip;</span>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      {/if}

      <!-- Done actions -->
      <div class="done-actions">
        <button
          type="button"
          class="back-to-list-btn"
          onclick={() => router.navigate('/contacts')}
        >
          {t('contacts.import.back')}
        </button>
        <button
          type="button"
          class="import-again-btn"
          onclick={() => { phase = 'idle'; importResults = []; dupDecisions = {}; }}
        >
          {t('contacts.import.pickFile')}
        </button>
      </div>
    </div>
  {/if}
</div>

<style>
  .import-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: auto;
    padding: var(--spacing-05);
    box-sizing: border-box;
    gap: var(--spacing-05);
  }

  .toolbar {
    flex-shrink: 0;
  }

  .back-btn {
    color: var(--interactive);
    font-weight: 500;
    font-size: var(--type-body-compact-01-size);
    padding: 0;
    background: none;
    border: none;
    cursor: pointer;
  }

  .back-btn::before {
    content: '\2190\00a0';
  }

  .back-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  .title {
    font-size: var(--type-heading-03-size);
    margin: 0;
    color: var(--text-primary);
  }

  /* ── Drop zone ─────────────────────────────────────────────────────────── */

  .drop-zone {
    border: 2px dashed var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-08);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--spacing-04);
    cursor: pointer;
    transition: border-color var(--duration-fast-01) var(--easing-productive-enter),
                background var(--duration-fast-01) var(--easing-productive-enter);
    max-width: 480px;
  }

  .drop-zone:hover,
  .drop-zone:focus {
    border-color: var(--interactive);
    background: var(--layer-01);
    outline: none;
  }

  .drop-zone.drag-over {
    border-color: var(--interactive);
    background: var(--layer-02);
  }

  .drop-hint {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
    text-align: center;
    margin: 0;
  }

  .pick-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    background: var(--interactive);
    color: var(--text-on-color);
    border: none;
    cursor: pointer;
  }

  .pick-btn:hover {
    filter: brightness(1.1);
  }

  .pick-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: 2px;
  }

  /* ── Progress ──────────────────────────────────────────────────────────── */

  .progress-area {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
  }

  .progress-msg {
    font-size: var(--type-body-01-size);
    color: var(--text-secondary);
  }

  .cancel-btn {
    align-self: flex-start;
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    background: transparent;
    border: 1px solid var(--border-subtle-01);
    color: var(--text-primary);
    cursor: pointer;
  }

  .cancel-btn:hover {
    background: var(--layer-02);
  }

  .cancel-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  /* ── Error ─────────────────────────────────────────────────────────────── */

  .error-msg {
    color: var(--support-error);
    font-size: var(--type-body-01-size);
  }

  /* ── Summary ───────────────────────────────────────────────────────────── */

  .summary {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    max-width: 560px;
  }

  .summary-title {
    font-size: var(--type-heading-03-size);
    margin: 0;
    color: var(--text-primary);
  }

  .summary-empty {
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .summary-stat {
    font-size: var(--type-body-01-size);
    margin: 0;
    font-weight: 500;
  }

  .summary-stat.created {
    color: var(--support-success, #24a148);
  }

  .summary-stat.failed {
    color: var(--support-error);
  }

  .summary-stat.skipped {
    color: var(--text-secondary);
  }

  /* ── Failed list ───────────────────────────────────────────────────────── */

  .failed-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .failed-item {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border-left: 3px solid var(--support-error);
    border-radius: 0 var(--radius-md) var(--radius-md) 0;
    font-size: var(--type-body-compact-01-size);
  }

  .failed-card {
    font-weight: 600;
    color: var(--text-primary);
  }

  .failed-reason {
    color: var(--text-secondary);
  }

  /* ── Conflict section ──────────────────────────────────────────────────── */

  .conflict-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border-radius: var(--radius-lg);
    border: 1px solid var(--support-warning, #f1c21b);
  }

  .conflict-heading {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .conflict-row {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    padding: var(--spacing-02) var(--spacing-03);
    border-left: 3px solid var(--support-warning, #f1c21b);
    border-radius: 0 var(--radius-md) var(--radius-md) 0;
  }

  .conflict-name {
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    color: var(--text-primary);
  }

  .conflict-diff {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .conflict-diff-item {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  /* ── Duplicate section ─────────────────────────────────────────────────── */

  .dup-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-01);
    border-radius: var(--radius-lg);
    border: 1px solid var(--border-subtle-01);
  }

  .dup-heading {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-helper);
    margin: 0;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .dup-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .dup-card {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    font-weight: 500;
    flex-shrink: 0;
  }

  .dup-actions {
    display: flex;
    gap: var(--spacing-02);
    flex-wrap: wrap;
  }

  .dup-btn {
    padding: var(--spacing-02) var(--spacing-03);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    cursor: pointer;
    border: none;
    min-height: 32px;
  }

  .dup-btn.primary {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .dup-btn.danger {
    background: var(--support-error);
    color: #fff;
  }

  .dup-btn.secondary {
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
  }

  .dup-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .dup-status {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .dup-status.resolved {
    color: var(--support-success, #24a148);
  }

  /* ── Done actions ──────────────────────────────────────────────────────── */

  .done-actions {
    display: flex;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    padding-top: var(--spacing-03);
    border-top: 1px solid var(--border-subtle-01);
  }

  .back-to-list-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    background: var(--interactive);
    color: var(--text-on-color);
    border: none;
    cursor: pointer;
  }

  .back-to-list-btn:hover {
    filter: brightness(1.1);
  }

  .back-to-list-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  .import-again-btn {
    padding: var(--spacing-03) var(--spacing-05);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    background: transparent;
    border: 1px solid var(--border-subtle-01);
    color: var(--text-primary);
    cursor: pointer;
  }

  .import-again-btn:hover {
    background: var(--layer-02);
  }

  .import-again-btn:focus {
    outline: 2px solid var(--focus);
    outline-offset: -2px;
  }

  /* ── Loading spinner ───────────────────────────────────────────────────── */

  .loading-spinner {
    display: inline-block;
    width: 16px;
    height: 16px;
    border: 2px solid var(--border-subtle-01);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 0.7s linear infinite;
    flex-shrink: 0;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  /* ── A11y ──────────────────────────────────────────────────────────────── */

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
</style>
