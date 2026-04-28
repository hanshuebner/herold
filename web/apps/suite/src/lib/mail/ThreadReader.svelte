<script lang="ts">
  import { untrack } from 'svelte';
  import { mail } from './store.svelte';
  import MessageAccordion from './MessageAccordion.svelte';
  import PrintIcon from '../icons/PrintIcon.svelte';
  import type { Email } from './types';

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
  let subject = $derived(emails[0]?.subject ?? '(no subject)');

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
    <div class="state">Loading thread…</div>
  {:else if status === 'error'}
    <div class="state error">
      <p>Couldn't load thread.</p>
      {#if mail.threadError(threadId)}
        <p class="detail">{mail.threadError(threadId)}</p>
      {/if}
      <button type="button" onclick={() => mail.loadThread(threadId)}>Retry</button>
    </div>
  {:else if emails.length === 0}
    <div class="state">Thread has no messages.</div>
  {:else}
    <header>
      <div class="header-row">
        <h1>{subject}</h1>
        <button
          type="button"
          class="print"
          aria-label="Print thread"
          title="Print thread"
          onclick={() => void printThread()}
        >
          <PrintIcon size={18} />
        </button>
      </div>
      <p class="count">
        {emails.length} message{emails.length === 1 ? '' : 's'}
      </p>
    </header>
    <div class="messages">
      {#each emails as email (email.id)}
        <MessageAccordion {email} expanded={expanded.has(email.id)} onToggle={toggle} />
      {/each}
    </div>
  {/if}
</div>

<style>
  .thread-reader {
    height: 100%;
    overflow: auto;
    background: var(--background);
  }
  header {
    padding: var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-01);
  }
  .header-row {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-04);
  }
  h1 {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    margin: 0 0 var(--spacing-02);
    word-break: break-word;
    flex: 1;
  }
  .count {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .print {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    background: var(--layer-02);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .print:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  @media print {
    .print {
      display: none;
    }
    .thread-reader {
      overflow: visible !important;
      height: auto !important;
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
