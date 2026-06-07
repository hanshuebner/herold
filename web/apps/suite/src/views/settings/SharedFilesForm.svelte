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
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';

  // ── State ─────────────────────────────────────────────────────────────────

  let shares = $state<FileShare[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let filter = $state<FileShareState | 'all'>('all');
  let sortBy = $state<'createdAt' | 'expiresAt'>('createdAt');
  let revoking = $state<Set<string>>(new Set());

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
    if (diffMs <= 0) return 'Expired';
    const diffHours = diffMs / (1000 * 60 * 60);
    // Sub-day expiries (notably pending shares, which live for pending_ttl
    // ~1h) must not round up to whole days, or a share expiring in minutes
    // reads "Expires tomorrow".
    if (diffHours < 1) return 'Expires in less than an hour';
    if (diffHours < 24) {
      const h = Math.round(diffHours);
      return `Expires in ${h} hour${h === 1 ? '' : 's'}`;
    }
    const diffDays = Math.ceil(diffHours / 24);
    if (diffDays === 1) return 'Expires tomorrow';
    if (diffDays <= 7) return `Expires in ${diffDays} days`;
    return `Expires ${formatDate(expiresAt)}`;
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
      loadError = err instanceof Error ? err.message : 'Could not load shared files';
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
      title: 'Revoke this shared link?',
      message:
        `"${share.name}" — any already-sent message linking this share will show a dead link.`,
      confirmLabel: 'Revoke',
      cancelLabel: 'Cancel',
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
      toast.show({ message: `"${share.name}" revoked.`, timeoutMs: 4000 });
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Could not revoke share';
      toast.show({ message: msg, kind: 'error', timeoutMs: 6000 });
    } finally {
      const next = new Set(revoking);
      next.delete(share.id);
      revoking = next;
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
      toast.show({ message: 'Could not copy link', kind: 'error', timeoutMs: 4000 });
    }
  }
</script>

<!-- Quota usage (REQ-ATT-72) -->
{#if quotaMax !== null}
  <div class="quota-row">
    <span class="quota-label">Share storage</span>
    <span class="quota-value">
      {quotaUsed !== null ? formatBytes(quotaUsed) : '—'}
      {' '}of{' '}
      {formatBytes(quotaMax)}
      {' '}used
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
    <label for="share-filter">Filter</label>
    <select id="share-filter" bind:value={filter}>
      <option value="all">All</option>
      <option value="active">Active</option>
      <option value="pending">Pending</option>
      <option value="revoked">Revoked</option>
    </select>
  </div>
  <div class="sort-group">
    <label for="share-sort">Sort by</label>
    <select id="share-sort" bind:value={sortBy}>
      <option value="createdAt">Created (newest first)</option>
      <option value="expiresAt">Expiry (soonest first)</option>
    </select>
  </div>
  <button
    type="button"
    class="refresh-btn"
    onclick={() => void load()}
    disabled={loading}
    aria-label="Refresh shared files"
  >
    {loading ? 'Loading...' : 'Refresh'}
  </button>
</div>

{#if loading}
  <p class="muted">Loading shared files...</p>
{:else if loadError}
  <p class="error-text" role="alert">{loadError}</p>
  <button type="button" onclick={() => void load()}>Retry</button>
{:else if visibleShares.length === 0}
  <p class="muted">
    {filter === 'all' ? 'No shared files.' : `No ${filter} shares.`}
  </p>
{:else}
  <ul class="shares-list" aria-label="Shared files">
    {#each visibleShares as share (share.id)}
      <li class="share-row" class:revoked={share.state === 'revoked'}>
        <div class="share-main">
          <span class="share-name" title={share.name}>{share.name}</span>
          <span class="share-meta">
            {formatBytes(share.size)}
            {' · '}
            Created {formatDate(share.createdAt)}
            {' · '}
            {formatExpiry(share.expiresAt)}
          </span>
          <span class="share-counts">
            {share.downloadCount} download{share.downloadCount !== 1 ? 's' : ''}
            {#if share.maxDownloads !== null}
              {' of '}
              {share.maxDownloads} max
            {/if}
            {#if share.lastDownloadedAt}
              {' · last '}
              {formatDate(share.lastDownloadedAt)}
            {/if}
          </span>
          {#if share.sourceMessageId || share.sourceSubject || (share.sourceRecipients && share.sourceRecipients.length > 0)}
            <!-- REQ-ATT-70a: back-reference to the originating message. Shown
                 whenever any source field is present (a no-subject message
                 still carries recipients + a message id). -->
            <!-- TODO REQ-ATT-70a: make subject a clickable link to open the
                 message in the mail view once the route supports opening by
                 Email id. Currently sourceMessageId is an Email id but the
                 router expects a threadId (#/mail/thread/<threadId>), so a
                 safe deep-link requires a JMAP lookup that is deferred. -->
            <span class="share-source">
              {'Shared in '}
              <span class="share-source-subject">"{share.sourceSubject || '(no subject)'}"</span>
              {#if share.sourceRecipients && share.sourceRecipients.length > 0}
                {' — to '}
                <span class="share-source-recipients">{share.sourceRecipients.join(', ')}</span>
              {/if}
            </span>
          {/if}
          <div class="share-badges">
            <span
              class="state-badge state-{share.state}"
              title="State: {share.state}"
            >
              {share.state}
            </span>
            {#if share.hasPassword}
              <span class="lock-badge" title="Password protected">lock</span>
            {/if}
          </div>
        </div>

        {#if share.state === 'active'}
          <div class="share-actions">
            <button
              type="button"
              class="action-btn copy-btn"
              onclick={() => void copyLink(share)}
              aria-label="Copy link for {share.name}"
            >
              {copyingId === share.id ? 'Copied!' : 'Copy link'}
            </button>
            <button
              type="button"
              class="action-btn revoke-btn"
              onclick={() => void revokeShare(share)}
              disabled={revoking.has(share.id)}
              aria-label="Revoke {share.name}"
            >
              {revoking.has(share.id) ? 'Revoking...' : 'Revoke'}
            </button>
          </div>
        {:else if share.state === 'pending'}
          <div class="share-actions">
            <span class="pending-note">Waiting for message to be sent</span>
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
