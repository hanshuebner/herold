<script lang="ts">
  /**
   * Contacts duplicate-check review view (REQ-CONT-90, 91, 94; re #220).
   *
   * Renders duplicate-check results as one flat, selectable list -- the
   * same interaction model as the mail message list / the contacts
   * bulk-select toolbar: a checkbox per row, select-all, a running
   * selected count, and bulk actions (merge / export / delete) instead of
   * independent per-cluster cards with only a per-cluster merge/dismiss
   * pair. Hovering a row reveals what it matched on (shared email, shared
   * phone, close name) and the specific values involved. Results load
   * incrementally (infinite scroll) via `duplicatesStore`, which pages
   * candidates in and reclusters over the accumulated set rather than
   * fetching the whole address book up front.
   */

  import { onMount, onDestroy } from 'svelte';
  import { jmap } from '../lib/jmap/client';
  import { Capability } from '../lib/jmap/types';
  import { auth } from '../lib/auth/auth.svelte';
  import { router } from '../lib/router/router.svelte';
  import { toast } from '../lib/toast/toast.svelte';
  import { confirm } from '../lib/dialog/confirm.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import ContactAvatar from '../lib/contacts/ContactAvatar.svelte';
  import CheckSquareIcon from '../lib/icons/CheckSquareIcon.svelte';
  import TrashIcon from '../lib/icons/TrashIcon.svelte';
  import { duplicatesStore, type DuplicateRow } from '../lib/contacts/duplicates-store.svelte';
  import { deriveFallbackInitial } from '../lib/contacts/list-store.svelte';
  import {
    buildExportArgs,
    triggerDownload,
    type ExportResponse,
  } from '../lib/contacts/vcard-import';
  import { allVisibleSelected } from '../lib/list-selection/whole-set-selection';

  onMount(() => {
    void duplicatesStore.init();
  });

  onDestroy(() => {
    observer?.disconnect();
    observer = null;
  });

  // ── Sentinel / infinite scroll ───────────────────────────────────────────

  let sentinelEl = $state<Element | null>(null);
  let observer: IntersectionObserver | null = null;

  function setupObserver(el: Element | null): void {
    observer?.disconnect();
    observer = null;
    if (!el) return;
    observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) {
          void duplicatesStore.loadMore();
        }
      },
      { rootMargin: '200px' },
    );
    observer.observe(el);
  }

  $effect(() => {
    setupObserver(sentinelEl);
  });

  // ── Selection ─────────────────────────────────────────────────────────────

  let visibleIds = $derived(duplicatesStore.rows.map((r) => r.id));
  let selectedCount = $derived(duplicatesStore.selectedIds.size);
  let everythingSelected = $derived(allVisibleSelected(visibleIds, duplicatesStore.selectedIds));
  let someSelected = $derived(selectedCount > 0 && !everythingSelected);

  function toggleSelectAllVisible(): void {
    duplicatesStore.toggleSelectAllVisible(visibleIds);
  }

  // ── Bulk actions ──────────────────────────────────────────────────────────

  let deleting = $state(false);
  let merging = $state(false);
  let exporting = $state(false);

  async function bulkDeleteSelected(): Promise<void> {
    const ids = [...duplicatesStore.selectedIds];
    if (ids.length === 0) return;
    const n = ids.length;
    const ok = await confirm.ask({
      title: t(n === 1 ? 'contacts.bulk.deleteConfirm.titleOne' : 'contacts.bulk.deleteConfirm.titleMany', {
        n: String(n),
      }),
      message: t(n === 1 ? 'contacts.bulk.deleteConfirm.messageOne' : 'contacts.bulk.deleteConfirm.messageMany', {
        n: String(n),
      }),
      confirmLabel: t('contacts.bulk.deleteConfirm.confirm'),
      cancelLabel: t('contacts.bulk.deleteConfirm.cancel'),
      kind: 'danger',
    });
    if (!ok) return;

    deleting = true;
    try {
      const failed = await duplicatesStore.bulkDelete(ids);
      if (failed.length > 0) {
        toast.show({ message: t('contacts.bulk.deleteError', { n: String(failed.length) }), kind: 'error' });
      }
    } finally {
      deleting = false;
    }
  }

  async function bulkExportSelected(): Promise<void> {
    const ids = [...duplicatesStore.selectedIds];
    if (ids.length === 0) return;
    const accountId = auth.session?.primaryAccounts[Capability.Contacts] ?? null;
    if (!accountId) return;

    exporting = true;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Contact/export', buildExportArgs(accountId, { ids }), [Capability.Contacts]);
      });
      const resp = responses[0];
      if (!resp || resp[0] === 'error') {
        toast.show({ message: t('contacts.bulk.exportError'), kind: 'error' });
        return;
      }
      const args = resp[1] as ExportResponse;
      const url = jmap.downloadUrl({
        accountId,
        blobId: args.blobId,
        type: 'text/vcard',
        name: 'duplicates.vcf',
      });
      if (url) triggerDownload(url, 'duplicates.vcf');
    } catch {
      toast.show({ message: t('contacts.bulk.exportError'), kind: 'error' });
    } finally {
      exporting = false;
    }
  }

  async function bulkMergeSelected(): Promise<void> {
    const ids = [...duplicatesStore.selectedIds];
    if (ids.length === 0) return;

    const ok = await confirm.ask({
      title: t('contacts.duplicates.mergeConfirm.title', { n: String(ids.length) }),
      message: t('contacts.duplicates.mergeConfirm.message'),
      confirmLabel: t('contacts.duplicates.merge'),
      cancelLabel: t('contacts.bulk.deleteConfirm.cancel'),
      kind: 'default',
    });
    if (!ok) return;

    merging = true;
    try {
      const { mergedClusters, skipped } = await duplicatesStore.bulkMerge(ids);
      if (mergedClusters > 0) {
        toast.show({ message: t('contacts.duplicates.mergeSuccess', { n: String(mergedClusters) }) });
      }
      if (skipped.length > 0) {
        toast.show({
          message: t('contacts.duplicates.mergeSkipped', { n: String(skipped.length) }),
          kind: 'error',
        });
      }
    } catch {
      toast.show({ message: t('contacts.duplicates.mergeError'), kind: 'error' });
    } finally {
      merging = false;
    }
  }

  function dismissRow(row: DuplicateRow): void {
    duplicatesStore.dismissRow(row.id);
  }

  function reasonLabel(reason: string): string {
    switch (reason) {
      case 'email': return t('contacts.duplicates.reason.email');
      case 'phone': return t('contacts.duplicates.reason.phone');
      case 'name': return t('contacts.duplicates.reason.name');
      default: return reason;
    }
  }

  function rowSecondary(row: DuplicateRow): string {
    return row.emails[0] ?? row.phones[0] ?? '';
  }
</script>

<div class="duplicates-view">
  <div class="toolbar">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/contacts')}
    >
      {t('contacts.list.title')}
    </button>
  </div>

  <h1 class="page-title">{t('contacts.duplicates.title')}</h1>

  {#if duplicatesStore.status === 'loading'}
    <p class="state-msg">{t('contacts.duplicates.loading')}</p>
  {:else if duplicatesStore.status === 'error'}
    <p class="state-msg error">{t('contacts.duplicates.error')}</p>
  {:else if duplicatesStore.rows.length === 0 && !duplicatesStore.hasMore}
    <p class="state-msg">{t('contacts.duplicates.empty')}</p>
  {:else}
    <div class="bulk-toolbar" role="toolbar" aria-label={t('bulk.selected', { count: selectedCount })}>
      <button
        type="button"
        class="select-all-btn"
        aria-label={everythingSelected ? t('select.deselectAll') : t('select.selectAll')}
        aria-pressed={everythingSelected}
        title={everythingSelected ? t('select.deselectAll') : t('select.selectAll')}
        onclick={toggleSelectAllVisible}
      >
        <CheckSquareIcon size={18} checked={everythingSelected} indeterminate={someSelected} />
      </button>
      {#if selectedCount > 0}
        <span class="bulk-count">{t('bulk.selected', { count: selectedCount })}</span>
        <button
          type="button"
          class="icon-btn primary"
          onclick={() => void bulkMergeSelected()}
          disabled={merging}
        >
          {t('contacts.duplicates.merge')}
        </button>
        <button
          type="button"
          class="icon-btn"
          onclick={() => void bulkExportSelected()}
          disabled={exporting}
        >
          {t('contacts.list.export')}
        </button>
        <button
          type="button"
          class="icon-btn danger"
          onclick={() => void bulkDeleteSelected()}
          disabled={deleting}
        >
          <TrashIcon size={16} />
          {t('bulk.delete')}
        </button>
      {/if}
    </div>

    <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
    <ul class="row-list" role="listbox" aria-label={t('contacts.duplicates.ariaLabel')}>
      {#each duplicatesStore.rows as row (row.id)}
        <li
          role="option"
          aria-selected={duplicatesStore.selectedIds.has(row.id)}
          class:selected={duplicatesStore.selectedIds.has(row.id)}
          class="dup-row"
        >
          <input
            type="checkbox"
            class="row-check"
            aria-label={t('contacts.list.selectRowAria')}
            checked={duplicatesStore.selectedIds.has(row.id)}
            onclick={(e) => {
              e.stopPropagation();
              if (e.shiftKey) {
                e.preventDefault();
                duplicatesStore.selectRowClick(row.id, true, visibleIds);
              }
            }}
            onchange={() => duplicatesStore.toggleSelected(row.id)}
          />
          <ContactAvatar
            blobId={row.photoBlobId}
            fallbackInitial={deriveFallbackInitial(row.displayName || rowSecondary(row))}
            displayName={row.displayName}
            size={36}
          />
          <div class="row-text">
            <span class="row-name">{row.displayName || t('contacts.list.unnamed')}</span>
            {#if rowSecondary(row)}
              <span class="row-secondary">{rowSecondary(row)}</span>
            {/if}
            <div class="reason-badges">
              {#each row.reasons as reason}
                <span class="reason-badge">{reasonLabel(reason)}</span>
              {/each}
            </div>
          </div>

          <!-- Hover detail: what this row specifically matched on (re #220). -->
          <div class="match-detail" role="tooltip">
            {#if row.match.emails.length > 0}
              <p>{t('contacts.duplicates.matchEmail')}: {row.match.emails.join(', ')}</p>
            {/if}
            {#if row.match.phones.length > 0}
              <p>{t('contacts.duplicates.matchPhone')}: {row.match.phones.join(', ')}</p>
            {/if}
            {#if row.match.closeNames.length > 0}
              <p>{t('contacts.duplicates.matchName')}: {row.match.closeNames.join(', ')}</p>
            {/if}
            <button type="button" class="dismiss-row-btn" onclick={() => dismissRow(row)}>
              {t('contacts.duplicates.dismiss')}
            </button>
          </div>
        </li>
      {/each}
    </ul>

    {#if duplicatesStore.hasMore}
      <div class="sentinel" bind:this={sentinelEl} aria-hidden="true">
        {#if duplicatesStore.status === 'loading-more'}
          <span class="loading-spinner" aria-hidden="true"></span>
          <span class="sr-only">{t('contacts.duplicates.loadingMore')}</span>
        {/if}
      </div>
    {/if}
  {/if}
</div>

<style>
  .duplicates-view {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
    box-sizing: border-box;
    background: var(--background);
  }

  .toolbar {
    flex-shrink: 0;
    padding: var(--spacing-05) var(--spacing-05) 0;
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

  .page-title {
    flex-shrink: 0;
    font-size: var(--type-heading-04-size, 1.25rem);
    font-weight: 600;
    color: var(--text-primary);
    margin: var(--spacing-03) var(--spacing-05) 0;
  }

  .state-msg {
    padding: var(--spacing-06);
    color: var(--text-secondary);
    font-size: var(--type-body-01-size);
    margin: 0;
  }

  .state-msg.error {
    color: var(--support-error);
  }

  /* ── Bulk toolbar ────────────────────────────────────────────────────── */

  .bulk-toolbar {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-05);
    margin-top: var(--spacing-04);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }

  .select-all-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    color: var(--text-secondary);
    background: transparent;
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }

  .select-all-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .bulk-count {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .icon-btn {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    height: 32px;
    padding: 0 var(--spacing-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--field-01);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    white-space: nowrap;
  }

  .icon-btn:hover:not(:disabled) {
    background: var(--layer-02);
  }

  .icon-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .icon-btn.primary {
    background: var(--interactive);
    color: var(--text-on-color);
    border-color: var(--interactive);
  }

  .icon-btn.primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }

  .icon-btn.danger {
    color: var(--support-error);
    border-color: var(--support-error);
  }

  .icon-btn.danger:hover:not(:disabled) {
    background: var(--support-error-bg, rgba(218, 30, 40, 0.1));
  }

  /* ── Row list ────────────────────────────────────────────────────────── */

  .row-list {
    flex: 1 1 auto;
    overflow-y: auto;
    list-style: none;
    margin: 0;
    padding: 0;
  }

  .dup-row {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }

  .dup-row.selected {
    background: var(--layer-02);
  }

  .row-check {
    flex-shrink: 0;
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
  }

  .row-text {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }

  .row-name {
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row-secondary {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .reason-badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    margin-top: 2px;
  }

  .reason-badge {
    font-size: var(--type-label-01-size, 0.7rem);
    background: var(--layer-02);
    color: var(--text-helper);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    padding: 1px var(--spacing-02);
    line-height: 1.4;
  }

  /* Hover detail (re #220): hidden by default, revealed on row hover/focus. */
  .match-detail {
    display: none;
    position: absolute;
    right: var(--spacing-05);
    top: 100%;
    z-index: 10;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-md, 0 2px 8px rgba(0, 0, 0, 0.15));
    padding: var(--spacing-03) var(--spacing-04);
    min-width: 220px;
    max-width: 360px;
  }

  .dup-row:hover .match-detail,
  .dup-row:focus-within .match-detail {
    display: block;
  }

  .match-detail p {
    margin: 0 0 var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    word-break: break-word;
  }

  .dismiss-row-btn {
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    background: none;
    border: none;
    padding: 0;
    cursor: pointer;
  }

  .dismiss-row-btn:hover {
    text-decoration: underline;
  }

  /* ── Sentinel ────────────────────────────────────────────────────────── */

  .sentinel {
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    padding: var(--spacing-03);
  }

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
