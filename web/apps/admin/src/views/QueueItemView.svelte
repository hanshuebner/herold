<script lang="ts">
  import { queueItem } from '../lib/queue/queue-item.svelte';
  import { router } from '../lib/router/router.svelte';

  interface Props {
    id: string;
  }
  let { id }: Props = $props();

  $effect(() => {
    void queueItem.load(id);
  });

  let actionError = $state<string | null>(null);
  let actionWorking = $state(false);

  async function doRetry(): Promise<void> {
    actionWorking = true;
    actionError = null;
    const result = await queueItem.retry(id);
    actionWorking = false;
    if (!result.ok) actionError = result.errorMessage;
  }

  async function doHold(): Promise<void> {
    actionWorking = true;
    actionError = null;
    const result = await queueItem.hold(id);
    actionWorking = false;
    if (!result.ok) actionError = result.errorMessage;
  }

  async function doRelease(): Promise<void> {
    actionWorking = true;
    actionError = null;
    const result = await queueItem.release(id);
    actionWorking = false;
    if (!result.ok) actionError = result.errorMessage;
  }

  let deleteConfirmOpen = $state(false);

  async function doDelete(): Promise<void> {
    actionWorking = true;
    actionError = null;
    const result = await queueItem.deleteItem(id);
    actionWorking = false;
    if (!result.ok) {
      actionError = result.errorMessage;
      deleteConfirmOpen = false;
    } else {
      router.navigate('/queue');
    }
  }

  function formatDate(iso: string | null | undefined): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    });
  }

  function stateChipClass(state: string): string {
    switch (state) {
      case 'queued': return 'chip-blue';
      case 'deferred': return 'chip-amber';
      case 'held': return 'chip-grey';
      case 'failed': return 'chip-red';
      case 'inflight': return 'chip-green';
      case 'done': return 'chip-grey';
      default: return 'chip-grey';
    }
  }
</script>

<div class="detail-page">
  <div class="page-header">
    <button
      type="button"
      class="back-btn"
      onclick={() => router.navigate('/queue')}
      aria-label="Back to queue"
    >
      Back
    </button>
    {#if queueItem.item}
      <h1 class="page-title">Queue item <span class="id-mono">{queueItem.item.id}</span></h1>
    {:else if queueItem.status === 'loading'}
      <div class="spinner" role="status" aria-label="Loading"></div>
    {/if}
  </div>

  {#if queueItem.status === 'error'}
    <div class="page-error" role="alert">{queueItem.errorMessage}</div>
  {:else if queueItem.status === 'ready' && queueItem.item}
    {@const item = queueItem.item}

    <!-- Action bar -->
    <div class="action-bar">
      {#if item.state === 'deferred' || item.state === 'failed'}
        <button type="button" class="btn-primary" onclick={() => void doRetry()} disabled={actionWorking}>
          {actionWorking ? 'Working...' : 'Retry'}
        </button>
      {/if}
      {#if item.state !== 'held' && item.state !== 'done'}
        <button type="button" class="btn-secondary" onclick={() => void doHold()} disabled={actionWorking}>
          Hold
        </button>
      {/if}
      {#if item.state === 'held'}
        <button type="button" class="btn-primary" onclick={() => void doRelease()} disabled={actionWorking}>
          {actionWorking ? 'Working...' : 'Release'}
        </button>
      {/if}

      {#if deleteConfirmOpen}
        <span class="confirm-inline">Delete?</span>
        <button type="button" class="btn-danger" onclick={() => void doDelete()} disabled={actionWorking}>
          {actionWorking ? 'Deleting...' : 'Confirm delete'}
        </button>
        <button type="button" class="btn-ghost" onclick={() => { deleteConfirmOpen = false; }}>
          Cancel
        </button>
      {:else}
        <button type="button" class="btn-danger-outline" onclick={() => { deleteConfirmOpen = true; }}>
          Delete
        </button>
      {/if}
    </div>

    {#if actionError}
      <div class="page-error" role="alert">{actionError}</div>
    {/if}

    <!-- Detail definition list -->
    <dl class="detail-list">
      <div class="detail-row">
        <dt>ID</dt>
        <dd class="mono">{item.id}</dd>
      </div>
      <div class="detail-row">
        <dt>State</dt>
        <dd>
          <span class="chip {stateChipClass(item.state)}">{item.state}</span>
        </dd>
      </div>
      <div class="detail-row">
        <dt>Principal ID</dt>
        <dd class="mono">{item.principal_id}</dd>
      </div>
      <div class="detail-row">
        <dt>Sender</dt>
        <dd class="mono">{item.mail_from}</dd>
      </div>
      <div class="detail-row">
        <dt>Recipient</dt>
        <dd class="mono">{item.rcpt_to}</dd>
      </div>
      <div class="detail-row">
        <dt>Envelope ID</dt>
        <dd class="mono">{item.envelope_id}</dd>
      </div>
      <div class="detail-row">
        <dt>Attempts</dt>
        <dd>{item.attempts}</dd>
      </div>
      {#if item.created_at}
        <div class="detail-row">
          <dt>Created</dt>
          <dd>{formatDate(item.created_at)}</dd>
        </div>
      {/if}
      {#if item.last_attempt_at}
        <div class="detail-row">
          <dt>Last attempt</dt>
          <dd>{formatDate(item.last_attempt_at)}</dd>
        </div>
      {/if}
      {#if item.next_attempt_at}
        <div class="detail-row">
          <dt>Next attempt</dt>
          <dd>{formatDate(item.next_attempt_at)}</dd>
        </div>
      {/if}
      {#if item.last_error}
        <div class="detail-row">
          <dt>Last error</dt>
          <dd class="error-text">{item.last_error}</dd>
        </div>
      {/if}
      {#if item.idempotency_key}
        <div class="detail-row">
          <dt>Idempotency key</dt>
          <dd class="mono">{item.idempotency_key}</dd>
        </div>
      {/if}
      {#if item.body_blob_hash}
        <div class="detail-row">
          <dt>Body blob hash</dt>
          <dd class="mono small">{item.body_blob_hash}</dd>
        </div>
      {/if}
      {#if item.headers_blob_hash}
        <div class="detail-row">
          <dt>Headers blob hash</dt>
          <dd class="mono small">{item.headers_blob_hash}</dd>
        </div>
      {/if}
    </dl>
  {/if}
</div>

<style>
  .detail-page {
    max-width: 800px;
  }

  .page-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-05);
    margin-bottom: var(--spacing-06);
    flex-wrap: wrap;
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--interactive);
    font-size: var(--type-body-compact-01-size);
    cursor: pointer;
    padding: var(--spacing-02) 0;
    flex-shrink: 0;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .back-btn:hover {
    opacity: 0.8;
  }

  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0;
  }

  .id-mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
  }

  .spinner {
    width: 18px;
    height: 18px;
    border: 2px solid var(--layer-02);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    flex-shrink: 0;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }

  .page-error {
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
    margin-bottom: var(--spacing-05);
  }

  /* Action bar */
  .action-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    flex-wrap: wrap;
    margin-bottom: var(--spacing-06);
    padding: var(--spacing-04) var(--spacing-05);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
  }

  .confirm-inline {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  /* Detail list */
  .detail-list {
    display: grid;
    grid-template-columns: 180px 1fr;
    gap: 1px;
    background: var(--border-subtle-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    overflow: hidden;
  }

  .detail-row {
    display: contents;
  }

  .detail-row dt,
  .detail-row dd {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--layer-01);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .detail-row dt {
    font-weight: 600;
    color: var(--text-secondary);
    white-space: nowrap;
  }

  .detail-row dd {
    color: var(--text-primary);
    word-break: break-all;
  }

  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
  .small {
    font-size: calc(var(--type-code-01-size) * 0.9);
  }

  .error-text {
    color: var(--support-error);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    word-break: break-all;
  }

  /* Chips */
  .chip {
    display: inline-block;
    padding: 1px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    line-height: 18px;
  }
  .chip-blue {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
  }
  .chip-amber {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: var(--support-warning);
  }
  .chip-grey {
    background: color-mix(in srgb, var(--text-helper) 15%, transparent);
    color: var(--text-helper);
  }
  .chip-red {
    background: color-mix(in srgb, var(--support-error) 15%, transparent);
    color: var(--support-error);
  }
  .chip-green {
    background: color-mix(in srgb, var(--support-success) 15%, transparent);
    color: var(--support-success);
  }

  /* Buttons */
  .btn-primary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter),
      opacity var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .btn-primary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-secondary {
    padding: var(--spacing-03) var(--spacing-06);
    background: var(--layer-02);
    color: var(--text-primary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--border-subtle-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-secondary:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .btn-secondary:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--support-error);
    color: var(--text-on-color);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-danger:hover:not(:disabled) {
    filter: brightness(0.9);
  }
  .btn-danger:disabled {
    opacity: 0.6;
    cursor: not-allowed;
  }

  .btn-danger-outline {
    padding: var(--spacing-02) var(--spacing-04);
    background: none;
    color: var(--support-error);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: 1px solid var(--support-error);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .btn-danger-outline:hover {
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
  }

  .btn-ghost {
    padding: var(--spacing-03) var(--spacing-05);
    background: none;
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    font-family: var(--font-sans);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    min-height: var(--touch-min);
    cursor: pointer;
    border: none;
    white-space: nowrap;
    transition: color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .btn-ghost:hover {
    color: var(--text-primary);
  }
</style>
