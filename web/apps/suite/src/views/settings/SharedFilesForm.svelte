<script lang="ts">
  /**
   * "Shared files" management view (REQ-ATT-70..73).
   *
   * Lists the principal's shares via FileShare/query + get. Allows:
   *   - Revoking an active share (FileShare/set update → revoked).
   *   - Copying the share URL to the clipboard.
   *   - Shortening the expiry (not in v1 UI for brevity).
   * Shows share-quota usage from the capability (REQ-ATT-72).
   * Download counts are not live; the view refreshes on open (REQ-ATT-73).
   */

  import { onMount } from 'svelte';
  import {
    queryFileShares,
    updateFileShare,
    fileShareCapability,
    type FileShare,
    type FileShareState,
  } from '../../lib/jmap/file-shares';
  import { jmap } from '../../lib/jmap/client';
  import { Capability, type Invocation } from '../../lib/jmap/types';
  import { mail } from '../../lib/mail/store.svelte';
  import { router } from '../../lib/router/router.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';

  // ── State ─────────────────────────────────────────────────────────────────

  let shares = $state<FileShare[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let filter = $state<FileShareState | 'all'>('all');
  let sortBy = $state<'createdAt' | 'expiresAt'>('createdAt');
  let revoking = $state<Set<string>>(new Set());
  /** Set of sourceMessageIds currently being resolved to a threadId. */
  let resolving = $state<Set<string>>(new Set());

  // ── Capability ────────────────────────────────────────────────────────────

  let cap = $derived(fileShareCapability());
  let quotaUsed = $derived(cap?.quota_used_bytes ?? null);
  let quotaMax = $derived(cap?.quota_max_bytes ?? null);

  // ── Helpers ───────────────────────────────────────────────────────────────

  function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }

  function formatDate(iso: string): string {
    return new Date(iso).toLocaleDateString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    });
  }

  function formatExpiry(expiresAt: string): string {
    const now = Date.now();
    const exp = new Date(expiresAt).getTime();
    const diffMs = exp - now;
    if (diffMs <= 0) return t('settings.sharedFiles.expiry.expired');
    const diffHours = diffMs / (1000 * 60 * 60);
    // Sub-day expiries (notably pending shares, which live for pending_ttl
    // ~1h) must not round up to whole days, or a share expiring in minutes
    // reads "Expires tomorrow".
    if (diffHours < 1) return t('settings.sharedFiles.expiry.lessThanHour');
    if (diffHours < 24) {
      const h = Math.round(diffHours);
      return h === 1
        ? t('settings.sharedFiles.expiry.hour', { n: String(h) })
        : t('settings.sharedFiles.expiry.hours', { n: String(h) });
    }
    const diffDays = Math.ceil(diffHours / 24);
    if (diffDays === 1) return t('settings.sharedFiles.expiry.tomorrow');
    if (diffDays <= 7) return t('settings.sharedFiles.expiry.days', { n: String(diffDays) });
    return t('settings.sharedFiles.expiry.on', { date: formatDate(expiresAt) });
  }

  // ── Sorted + filtered view ────────────────────────────────────────────────

  let visibleShares = $derived.by(() => {
    let list = shares;
    if (filter !== 'all') {
      list = list.filter((s) => s.state === filter);
    }
    return list.slice().sort((a, b) => {
      const aVal = sortBy === 'createdAt' ? a.createdAt : a.expiresAt;
      const bVal = sortBy === 'createdAt' ? b.createdAt : b.expiresAt;
      return bVal.localeCompare(aVal); // descending
    });
  });

  // ── Load ──────────────────────────────────────────────────────────────────

  async function load(): Promise<void> {
    loading = true;
    loadError = null;
    try {
      shares = await queryFileShares();
    } catch (err) {
      loadError = err instanceof Error ? err.message : t('settings.sharedFiles.toast.couldNotLoad');
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
  });

  // ── Actions ───────────────────────────────────────────────────────────────

  async function revokeShare(share: FileShare): Promise<void> {
    const ok = await confirm.ask({
      title: t('settings.sharedFiles.confirmRevoke.title'),
      message: t('settings.sharedFiles.confirmRevoke.message', { name: share.name }),
      confirmLabel: t('settings.sharedFiles.confirmRevoke.confirm'),
      cancelLabel: t('settings.sharedFiles.confirmRevoke.cancel'),
      kind: 'danger',
    });
    if (!ok) return;

    revoking = new Set([...revoking, share.id]);
    try {
      await updateFileShare(share.id, { state: 'revoked' });
      // Update local state immediately so the UI reflects the change.
      shares = shares.map((s) =>
        s.id === share.id ? { ...s, state: 'revoked' as FileShareState } : s,
      );
      toast.show({ message: t('settings.sharedFiles.toast.revoked', { name: share.name }), timeoutMs: 4000 });
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('settings.sharedFiles.toast.couldNotRevoke');
      toast.show({ message: msg, kind: 'error', timeoutMs: 6000 });
    } finally {
      const next = new Set(revoking);
      next.delete(share.id);
      revoking = next;
    }
  }

  /**
   * Resolve a sourceMessageId to its threadId via JMAP Email/get, then
   * navigate to the thread in the mail view. Shows a toast when the message
   * has been deleted and does not navigate in that case (REQ-ATT-70a).
   */
  async function openSourceMessage(sourceMessageId: string): Promise<void> {
    if (resolving.has(sourceMessageId)) return;
    const accountId = mail.mailAccountId;
    if (!accountId) {
      toast.show({ message: t('settings.sharedFiles.toast.noAccount'), kind: 'error', timeoutMs: 4000 });
      return;
    }
    resolving = new Set([...resolving, sourceMessageId]);
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/get',
          { accountId, ids: [sourceMessageId], properties: ['id', 'threadId'] },
          [Capability.Mail],
        );
      });
      const result = (responses[0] as Invocation)[1] as {
        list?: { id: string; threadId: string }[];
      };
      const email = result.list?.[0];
      if (!email) {
        toast.show({ message: t('settings.sharedFiles.toast.messageGone'), timeoutMs: 4000 });
        return;
      }
      // Prime the thread into the mail store BEFORE navigating. Opening
      // /mail/thread/<id> cold (the source thread is rarely already loaded
      // when coming from Settings) otherwise lands on the inbox; once the
      // thread is in the store the thread route renders it directly.
      await mail.loadThread(email.threadId);
      router.navigate('/mail/thread/' + encodeURIComponent(email.threadId));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('settings.sharedFiles.toast.couldNotOpen');
      toast.show({ message: msg, kind: 'error', timeoutMs: 6000 });
    } finally {
      const next = new Set(resolving);
      next.delete(sourceMessageId);
      resolving = next;
    }
  }

  let copyingId = $state<string | null>(null);

  async function copyLink(share: FileShare): Promise<void> {
    try {
      await navigator.clipboard.writeText(share.url);
      copyingId = share.id;
      setTimeout(() => {
        copyingId = null;
      }, 2000);
    } catch {
      toast.show({ message: t('settings.sharedFiles.toast.couldNotCopy'), kind: 'error', timeoutMs: 4000 });
    }
  }
</script>

<!-- Quota usage (REQ-ATT-72) -->
{#if quotaMax !== null}
  <div class="quota-row">
    <span class="quota-label">{t('settings.sharedFiles.quotaLabel')}</span>
    <span class="quota-value">
      {quotaUsed !== null
        ? t('settings.sharedFiles.quotaUsed', { used: formatBytes(quotaUsed), max: formatBytes(quotaMax) })
        : '—'}
    </span>
    {#if quotaMax > 0 && quotaUsed !== null}
      <div class="quota-bar" role="progressbar" aria-valuemin={0} aria-valuemax={quotaMax} aria-valuenow={quotaUsed}>
        <div
          class="quota-fill"
          style="width: {Math.min(100, (quotaUsed / quotaMax) * 100).toFixed(1)}%"
        ></div>
      </div>
    {/if}
  </div>
{/if}

<!-- Controls -->
<div class="controls">
  <div class="filter-group">
    <label for="share-filter">{t('settings.sharedFiles.filterLabel')}</label>
    <select id="share-filter" bind:value={filter}>
      <option value="all">{t('settings.sharedFiles.filterAll')}</option>
      <option value="active">{t('settings.sharedFiles.filterActive')}</option>
      <option value="pending">{t('settings.sharedFiles.filterPending')}</option>
      <option value="revoked">{t('settings.sharedFiles.filterRevoked')}</option>
    </select>
  </div>
  <div class="sort-group">
    <label for="share-sort">{t('settings.sharedFiles.sortLabel')}</label>
    <select id="share-sort" bind:value={sortBy}>
      <option value="createdAt">{t('settings.sharedFiles.sortCreated')}</option>
      <option value="expiresAt">{t('settings.sharedFiles.sortExpiry')}</option>
    </select>
  </div>
  <button
    type="button"
    class="refresh-btn"
    onclick={() => void load()}
    disabled={loading}
    aria-label={t('settings.sharedFiles.refreshAriaLabel')}
  >
    {loading ? t('settings.sharedFiles.loading') : t('settings.sharedFiles.refresh')}
  </button>
</div>

{#if loading}
  <p class="muted">{t('settings.sharedFiles.loadingFiles')}</p>
{:else if loadError}
  <p class="error-text" role="alert">{loadError}</p>
  <button type="button" onclick={() => void load()}>{t('settings.sharedFiles.retry')}</button>
{:else if visibleShares.length === 0}
  <p class="muted">
    {filter === 'all' ? t('settings.sharedFiles.emptyAll') : t('settings.sharedFiles.emptyFilter', { filter })}
  </p>
{:else}
  <ul class="shares-list" aria-label={t('settings.sharedFiles.listAriaLabel')}>
    {#each visibleShares as share (share.id)}
      <li class="share-row" class:revoked={share.state === 'revoked'}>
        <div class="share-main">
          <span class="share-name" title={share.name}>{share.name}</span>
          <span class="share-meta">
            {formatBytes(share.size)}
            {' · '}
            {t('settings.sharedFiles.created', { date: formatDate(share.createdAt) })}
            {' · '}
            {formatExpiry(share.expiresAt)}
          </span>
          <span class="share-counts">
            {share.downloadCount === 1
              ? t('settings.sharedFiles.downloads.one', { count: String(share.downloadCount) })
              : t('settings.sharedFiles.downloads.other', { count: String(share.downloadCount) })}
            {#if share.maxDownloads !== null}
              {' '}
              {t('settings.sharedFiles.ofMax', { max: String(share.maxDownloads) })}
            {/if}
            {#if share.lastDownloadedAt}
              {' · '}
              {t('settings.sharedFiles.lastAt', { date: formatDate(share.lastDownloadedAt) })}
            {/if}
          </span>
          {#if share.sourceMessageId || share.sourceSubject || (share.sourceRecipients && share.sourceRecipients.length > 0)}
            <!-- REQ-ATT-70a: back-reference to the originating message. When
                 sourceMessageId is present the subject is a deep-link button
                 that resolves the Email id to a threadId via JMAP and navigates
                 to the thread. When no id is available (deleted-without-id
                 snapshot) the subject renders as plain text. -->
            <span class="share-source">
              {t('settings.sharedFiles.sharedIn')}
              {' '}
              {#if share.sourceMessageId}
                <button
                  type="button"
                  class="share-source-link"
                  onclick={() => void openSourceMessage(share.sourceMessageId!)}
                  disabled={resolving.has(share.sourceMessageId)}
                  aria-label={t('settings.sharedFiles.openMessage', { subject: share.sourceSubject || t('settings.sharedFiles.noSubject') })}
                >"{share.sourceSubject || t('settings.sharedFiles.noSubject')}"</button>
              {:else}
                <span class="share-source-subject">"{share.sourceSubject || t('settings.sharedFiles.noSubject')}"</span>
              {/if}
              {#if share.sourceRecipients && share.sourceRecipients.length > 0}
                {t('settings.sharedFiles.to')}
                <span class="share-source-recipients">{share.sourceRecipients.join(', ')}</span>
              {/if}
            </span>
          {/if}
          <div class="share-badges">
            <span
              class="state-badge state-{share.state}"
              title={t('settings.sharedFiles.stateTitle', { state: share.state })}
            >
              {share.state}
            </span>
            {#if share.hasPassword}
              <span class="lock-badge" title={t('settings.sharedFiles.passwordProtected')}>{t('settings.sharedFiles.lockBadge')}</span>
            {/if}
          </div>
        </div>

        {#if share.state === 'active'}
          <div class="share-actions">
            <button
              type="button"
              class="action-btn copy-btn"
              onclick={() => void copyLink(share)}
              aria-label={t('settings.sharedFiles.copyLinkAria', { name: share.name })}
            >
              {copyingId === share.id ? t('settings.sharedFiles.copied') : t('settings.sharedFiles.copyLink')}
            </button>
            <button
              type="button"
              class="action-btn revoke-btn"
              onclick={() => void revokeShare(share)}
              disabled={revoking.has(share.id)}
              aria-label={t('settings.sharedFiles.revokeAria', { name: share.name })}
            >
              {revoking.has(share.id) ? t('settings.sharedFiles.revoking') : t('settings.sharedFiles.revoke')}
            </button>
          </div>
        {:else if share.state === 'pending'}
          <div class="share-actions">
            <span class="pending-note">{t('settings.sharedFiles.pendingNote')}</span>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .quota-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) 0;
    border-bottom: 1px solid var(--border-subtle-01);
    margin-bottom: var(--spacing-04);
  }
  .quota-label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-width: 10em;
  }
  .quota-value {
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
  }
  .quota-bar {
    flex: 1;
    min-width: 120px;
    height: 6px;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    overflow: hidden;
  }
  .quota-fill {
    height: 100%;
    background: var(--interactive);
    border-radius: var(--radius-pill);
    transition: width 0.3s;
  }

  .controls {
    display: flex;
    gap: var(--spacing-04);
    align-items: flex-end;
    flex-wrap: wrap;
    margin-bottom: var(--spacing-04);
  }

  .filter-group,
  .sort-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }

  .filter-group label,
  .sort-group label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  select {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
  }

  .refresh-btn {
    padding: var(--spacing-02) var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    align-self: flex-end;
  }
  .refresh-btn:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .refresh-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .shares-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .share-row {
    display: flex;
    gap: var(--spacing-04);
    align-items: flex-start;
    padding: var(--spacing-04);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .share-row.revoked {
    opacity: 0.6;
  }

  .share-main {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    min-width: 0;
  }

  .share-name {
    color: var(--text-primary);
    font-weight: 500;
    font-size: var(--type-body-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .share-meta,
  .share-counts {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .share-source {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .share-source-subject {
    font-style: italic;
  }

  .share-source-link {
    font-style: italic;
    color: var(--interactive);
    text-decoration: underline;
    text-decoration-color: color-mix(in srgb, var(--interactive) 50%, transparent);
    text-underline-offset: 2px;
    background: none;
    border: none;
    padding: 0;
    font-size: inherit;
    font-family: inherit;
    line-height: inherit;
    cursor: pointer;
  }
  .share-source-link:hover:not(:disabled) {
    text-decoration-color: var(--interactive);
  }
  .share-source-link:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .share-source-recipients {
    color: var(--text-secondary);
  }

  .share-badges {
    display: flex;
    gap: var(--spacing-02);
    margin-top: var(--spacing-01);
  }

  .state-badge {
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .state-active {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
    border: 1px solid color-mix(in srgb, var(--support-success) 30%, transparent);
  }

  .state-pending {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    border: 1px solid color-mix(in srgb, var(--support-warning) 30%, transparent);
  }

  .state-revoked {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
    border: 1px solid color-mix(in srgb, var(--text-helper) 30%, transparent);
  }

  .lock-badge {
    padding: 2px var(--spacing-02);
    font-size: 11px;
    font-variant: small-caps;
    color: var(--text-helper);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
  }

  .share-actions {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    flex: 0 0 auto;
    align-items: flex-end;
  }

  .action-btn {
    padding: var(--spacing-02) var(--spacing-03);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    white-space: nowrap;
  }

  .copy-btn {
    border: 1px solid var(--interactive);
    color: var(--interactive);
    background: transparent;
  }
  .copy-btn:hover {
    background: color-mix(in srgb, var(--interactive) 10%, transparent);
  }

  .revoke-btn {
    border: 1px solid var(--support-error);
    color: var(--support-error);
    background: transparent;
  }
  .revoke-btn:hover:not(:disabled) {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }
  .revoke-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .pending-note {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
    font-style: italic;
  }

  .muted {
    color: var(--text-helper);
    font-style: italic;
    margin: 0;
  }

  .error-text {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
</style>
