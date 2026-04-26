<script lang="ts">
  import { router } from '../lib/router/router.svelte';
  import { mail } from '../lib/mail/store.svelte';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';
  import type { Email } from '../lib/mail/types';

  let threadId = $derived(router.parts[1] === 'thread' ? router.parts[2] : undefined);
  let label = $derived(router.parts[1] === 'label' ? router.parts[2] : undefined);
  let isInboxRoute = $derived(router.matches('mail') && !router.parts[1]);

  // Kick off the inbox load when the inbox route is shown.
  $effect(() => {
    if (isInboxRoute) {
      void mail.loadInbox();
    }
  });

  function senderLabel(email: Email): string {
    const a = email.from?.[0];
    if (!a) return '(no sender)';
    return a.name?.trim() || a.email;
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    const now = new Date();
    const sameYear = d.getFullYear() === now.getFullYear();
    const opts: Intl.DateTimeFormatOptions = sameYear
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
    return d.toLocaleDateString(undefined, opts);
  }

  function isUnread(email: Email): boolean {
    return !email.keywords.$seen;
  }

  function isFlagged(email: Email): boolean {
    return Boolean(email.keywords.$flagged);
  }

  function openThread(email: Email): void {
    router.navigate(`/mail/thread/${encodeURIComponent(email.threadId)}`);
  }
</script>

<div class="mail">
  {#if threadId}
    <div class="thread-frame">
      <header class="thread-frame-bar">
        <button type="button" class="back" onclick={() => router.navigate('/mail')}>
          ← Back to inbox
        </button>
      </header>
      <ThreadReader {threadId} />
    </div>
  {:else if label}
    <header>
      <h1>Label: {label}</h1>
      <p class="lead">Label-view querying arrives after inbox.</p>
    </header>
  {:else}
    <header class="list-header">
      <h1>Inbox</h1>
      <button
        type="button"
        class="refresh"
        aria-label="Refresh"
        onclick={() => mail.refreshInbox()}
        disabled={mail.inboxLoadStatus === 'loading'}
      >
        ↻
      </button>
    </header>

    {#if mail.inboxLoadStatus === 'idle' || mail.inboxLoadStatus === 'loading'}
      <div class="state">Loading…</div>
    {:else if mail.inboxLoadStatus === 'error'}
      <div class="state error">
        <p>Couldn't load inbox.</p>
        {#if mail.inboxError}<p class="detail">{mail.inboxError}</p>{/if}
        <button type="button" onclick={() => mail.loadInbox()}>Retry</button>
      </div>
    {:else if mail.inboxEmails.length === 0}
      <div class="state">Inbox is empty.</div>
    {:else}
      <ul class="thread-list" role="listbox" aria-label="Inbox threads">
        {#each mail.inboxEmails as email (email.id)}
          <li class="thread-row" class:unread={isUnread(email)}>
            <button
              type="button"
              role="option"
              aria-selected="false"
              onclick={() => openThread(email)}
            >
              <span class="star" class:flagged={isFlagged(email)} aria-hidden="true">★</span>
              <span class="from">{senderLabel(email)}</span>
              <span class="subject-and-preview">
                <span class="subject">{email.subject || '(no subject)'}</span>
                <span class="preview"> — {email.preview}</span>
              </span>
              {#if email.hasAttachment}
                <span class="attachment" aria-label="Has attachment">📎</span>
              {/if}
              <span class="date">{formatDate(email.receivedAt)}</span>
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .mail {
    height: 100%;
    overflow: auto;
    background: var(--background);
  }

  .thread-frame {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .thread-frame-bar {
    flex: 0 0 auto;
    padding: var(--spacing-03) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .thread-frame :global(.thread-reader) {
    flex: 1;
    min-height: 0;
  }

  header {
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .list-header {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }
  h1 {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    margin: 0;
    flex: 1;
  }
  .lead {
    margin: var(--spacing-02) 0 0;
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .refresh,
  .back {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .refresh {
    width: 36px;
    padding: var(--spacing-02);
    text-align: center;
    font-size: 16px;
  }
  .refresh:hover:not(:disabled),
  .back:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .refresh:disabled {
    opacity: 0.4;
    cursor: progress;
  }

  .state {
    padding: var(--spacing-07) var(--spacing-05);
    text-align: center;
    color: var(--text-secondary);
  }
  .state.error {
    color: var(--support-error);
  }
  .state .detail {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-helper);
    margin: var(--spacing-03) 0;
  }
  .state button {
    margin-top: var(--spacing-04);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
  }

  .thread-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .thread-row {
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .thread-row button {
    display: grid;
    grid-template-columns: 24px 14ch 1fr auto auto;
    gap: var(--spacing-04);
    align-items: center;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-05);
    color: var(--text-secondary);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .thread-row button:hover {
    background: var(--layer-01);
  }
  .thread-row.unread button {
    color: var(--text-primary);
  }

  .star {
    color: var(--text-helper);
    font-size: 16px;
    line-height: 1;
  }
  .star.flagged {
    color: var(--support-warning);
  }

  .from {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .thread-row.unread .from {
    font-weight: 600;
  }

  .subject-and-preview {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .subject {
    color: var(--text-primary);
  }
  .thread-row.unread .subject {
    font-weight: 600;
  }
  .preview {
    color: var(--text-secondary);
  }

  .attachment {
    color: var(--text-helper);
  }
  .date {
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
  }
  .thread-row.unread .date {
    color: var(--text-secondary);
    font-weight: 600;
  }
</style>
