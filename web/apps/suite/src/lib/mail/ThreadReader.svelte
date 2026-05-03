<script lang="ts">
  import { untrack } from 'svelte';
  import { mail } from './store.svelte';
  import MessageAccordion from './MessageAccordion.svelte';
  import ThreadToolbar from './ThreadToolbar.svelte';
  import ThreadReplyBar from './ThreadReplyBar.svelte';
  import { t } from '../i18n/i18n.svelte';
  import type { Email, Mailbox } from './types';

  interface Props {
    threadId: string;
  }
  let { threadId }: Props = $props();

  // Kick off thread load on prop change. untrack() prevents the load
  // function's synchronous read-modify-write of its status cell from
  // becoming a dep of this effect, which would otherwise produce a tight
  // retry loop on JMAP errors.
  $effect(() => {
    const tid = threadId;
    untrack(() => {
      void mail.loadThread(tid);
    });
  });

  let status = $derived(mail.threadStatus(threadId));
  let emails = $derived(mail.threadEmails(threadId));
  let subject = $derived(emails[0]?.subject || t('thread.subject.none'));

  // Most recent email — what reply / reply-all / forward target by
  // default, and the seed for thread-scoped bulk operations
  // (REQ-MAIL-51..54 expansion handles the rest).
  let latest = $derived(emails[emails.length - 1]);

  /**
   * Union of custom-mailbox labels across all messages in the thread.
   * Excludes system mailboxes (via mail.customMailboxes) and the
   * currently-viewed folder (so browsing a label does not produce a
   * redundant badge). Sorted alphabetically.
   * Rendered under the thread subject so badges are always visible
   * regardless of which messages are expanded (re #66, re #70).
   */
  let threadLabels = $derived.by<string[]>(() => {
    // Collect the union of all mailboxIds across thread messages.
    const seen = new Set<string>();
    for (const email of emails) {
      for (const id of Object.keys(email.mailboxIds)) {
        seen.add(id);
      }
    }
    const activeFolder = mail.listFolder;
    const labels: string[] = [];
    for (const m of mail.customMailboxes) {
      if (!seen.has(m.id)) continue;
      if (m.id === activeFolder) continue;
      labels.push(m.name);
    }
    return labels.sort((a, b) => a.localeCompare(b));
  });

  /**
   * Per docs/requirements/09-ui-layout.md REQ-UI-20: collapsed except the
   * latest unread message — or the latest if all are read.
   */
  function pickInitialExpanded(emails: Email[]): string | null {
    if (emails.length === 0) return null;
    for (let i = emails.length - 1; i >= 0; i--) {
      const e = emails[i];
      if (e && !e.keywords.$seen) return e.id;
    }
    return emails[emails.length - 1]?.id ?? null;
  }

  // Track expanded state by email id. Initial set computed once when
  // emails first arrive; subsequent toggles are user-driven.
  let expanded = $state(new Set<string>());
  let initialised = $state(false);

  $effect(() => {
    if (!initialised && emails.length > 0) {
      const initial = pickInitialExpanded(emails);
      if (initial) {
        expanded = new Set([initial]);
      }
      initialised = true;
    }
  });

  function toggle(id: string): void {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expanded = next;
  }

  function expandAll(): void {
    expanded = new Set(emails.map((e) => e.id));
  }

  // Open the browser's print dialog with every message expanded. The
  // print stylesheet (defined in this component plus app.css) hides the
  // surrounding shell and renders only the thread inline, with each
  // message a page-break-avoid block.
  async function printThread(): Promise<void> {
    expandAll();
    // Defer one frame so the DOM update lands before the print dialog
    // captures the document.
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    window.print();
  }
</script>

<div class="thread-reader">
  {#if status === 'idle' || status === 'loading'}
    <div class="state">{t('thread.loading')}</div>
  {:else if status === 'error'}
    <div class="state error">
      <p>{t('thread.couldNotLoad')}</p>
      {#if mail.threadError(threadId)}
        <p class="detail">{mail.threadError(threadId)}</p>
      {/if}
      <button type="button" onclick={() => mail.loadThread(threadId)}>{t('thread.retry')}</button>
    </div>
  {:else if emails.length === 0 || !latest}
    <div class="state">{t('thread.empty')}</div>
  {:else}
    <ThreadToolbar {threadId} {latest} onPrint={() => void printThread()} />
    <div class="scroll">
      <header>
        <h1>{subject}</h1>
        {#if threadLabels.length > 0}
          <div class="thread-labels" aria-label="Labels">
            {#each threadLabels as lname (lname)}
              <span class="label-badge">{lname}</span>
            {/each}
          </div>
        {/if}
      </header>
      <div class="messages">
        {#each emails as email (email.id)}
          <MessageAccordion {email} expanded={expanded.has(email.id)} onToggle={toggle} />
        {/each}
      </div>
    </div>
    <ThreadReplyBar target={latest} />
  {/if}
</div>

<style>
  .thread-reader {
    height: 100%;
    display: flex;
    flex-direction: column;
    background: var(--background);
    overflow: hidden;
  }
  /* The middle region scrolls; toolbar + reply bar stay pinned. */
  .scroll {
    flex: 1;
    overflow: auto;
  }
  header {
    padding: var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  h1 {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    margin: 0;
    word-break: break-word;
  }

  /* Thread-level label badges shown under the subject (re #66, re #70).
     Always visible regardless of message accordion expansion state.
     Badge set is the union of custom-mailbox memberships across all
     thread messages, excluding system and currently-viewed-folder
     mailboxes. */
  .thread-labels {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    margin-top: var(--spacing-03);
  }
  .label-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px var(--spacing-02);
    background: var(--layer-03);
    color: var(--text-secondary);
    border-radius: var(--radius-sm);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    white-space: nowrap;
  }

  @media print {
    .thread-reader {
      overflow: visible !important;
      height: auto !important;
      display: block;
    }
    .scroll {
      overflow: visible !important;
    }
    header {
      background: transparent;
      border-bottom-color: #000;
    }
    :global(.message) {
      page-break-inside: avoid;
    }
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
</style>
