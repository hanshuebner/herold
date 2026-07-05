<script lang="ts">
  import { untrack } from 'svelte';
  import { mail } from './store.svelte';
  import MessageAccordion from './MessageAccordion.svelte';
  import ThreadToolbar from './ThreadToolbar.svelte';
  import ThreadReplyBar from './ThreadReplyBar.svelte';
  import ThreadInlineComposer from './ThreadInlineComposer.svelte';
  import TaggedAddressBanner from './TaggedAddressBanner.svelte';
  import ThreadNewReplyBanner from './ThreadNewReplyBanner.svelte';
  import { keyboard } from '../keyboard/engine.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { labelForeground } from './label-color';
  import type { Email } from './types';

  interface Props {
    threadId: string;
    /**
     * When true, the reader is running inside the chrome-less standalone
     * thread-window popup. Forwarded to ThreadToolbar so it can hide the
     * back arrow and close the popup instead of navigating on leave actions.
     */
    standalone?: boolean;
  }
  let { threadId, standalone = false }: Props = $props();

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

  // Register the currently-open thread with the store so the Email/changes
  // handler can populate `pendingArrivals` for it (issue #118). Cleared
  // on unmount and when the threadId prop changes; the store also wipes
  // any stale banner entries for other threads when the open thread
  // changes.
  $effect(() => {
    const tid = threadId;
    mail.setOpenThread(tid);
    return () => {
      if (mail.openThreadId === tid) mail.setOpenThread(null);
    };
  });

  let scrollContainerEl = $state<HTMLDivElement | null>(null);

  /**
   * Accept all pending arrivals for the current thread: advance the
   * committed snapshot so they appear in the rendered list and scroll the
   * first new message into view (issue #118). Wired to the new-reply
   * banner's "Neue Antwort anzeigen" button and the `n` keyboard shortcut.
   */
  async function handleAccept(): Promise<void> {
    const newEmails = mail.acceptPendingArrivals(threadId);
    if (newEmails.length === 0) return;
    // Wait two animation frames: first for Svelte to update `expanded` via
    // the auto-expand effect, second for the DOM to lay out the new accordions.
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    await new Promise((r) => requestAnimationFrame(() => r(null)));
    const container = scrollContainerEl;
    if (!container) return;
    const firstNew = newEmails[0];
    if (!firstNew) return;
    const target = container.querySelector<HTMLElement>(
      `[data-email-id="${CSS.escape(firstNew.id)}"]`,
    );
    target?.scrollIntoView({ block: 'start', behavior: 'smooth' });
  }

  /**
   * Dismiss all pending arrivals for the current thread without inserting
   * them into the rendered list (issue #118). Wired to the banner's
   * "Verstanden" button.
   */
  function handleDismiss(): void {
    mail.dismissPendingArrivals(threadId);
  }

  /**
   * Pager-style scroll bindings: Space scrolls the thread down by ~one
   * viewport height; Shift+Space scrolls back up. Suppressed automatically
   * by the engine's focus carve-out when an input or compose surface has
   * focus (re #89).
   */
  $effect(() => {
    const pop = keyboard.pushLayer([
      {
        key: ' ',
        description: 'Weiter scrollen',
        action: () => {
          const el = scrollContainerEl;
          if (el) el.scrollBy({ top: el.clientHeight * 0.9, behavior: 'smooth' });
        },
      },
      {
        key: 'Shift+ ',
        description: 'Zurück scrollen',
        action: () => {
          const el = scrollContainerEl;
          if (el) el.scrollBy({ top: -(el.clientHeight * 0.9), behavior: 'smooth' });
        },
      },
    ]);
    return pop;
  });

  /**
   * While a new-reply banner is up, push an `n` shortcut that triggers
   * "Neue Antwort anzeigen" (issue #118 open questions). The binding lives
   * in a dedicated layer so it disappears the moment the banner is dismissed
   * or no replies are pending.
   */
  $effect(() => {
    if (pendingArrivals.length === 0) return;
    const pop = keyboard.pushLayer([
      {
        key: 'n',
        description: 'Neue Antwort anzeigen',
        action: () => {
          void handleAccept();
        },
      },
    ]);
    return pop;
  });

  let status = $derived(mail.threadStatus(threadId));
  let emails = $derived(mail.threadEmails(threadId));
  let pendingArrivals = $derived(mail.pendingArrivalsForThread(threadId));
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
  let threadLabels = $derived.by<{ name: string; color: string | null | undefined }[]>(() => {
    // Collect the union of all mailboxIds across thread messages.
    const seen = new Set<string>();
    for (const email of emails) {
      for (const id of Object.keys(email.mailboxIds)) {
        seen.add(id);
      }
    }
    const activeFolder = mail.listFolder;
    const labels: { name: string; color: string | null | undefined }[] = [];
    for (const m of mail.customMailboxes) {
      if (!seen.has(m.id)) continue;
      if (m.id === activeFolder) continue;
      labels.push({ name: m.name, color: m.color });
    }
    return labels.sort((a, b) => a.name.localeCompare(b.name));
  });

  /**
   * REQ-EXTIMG-BG-31: when any message in the rendered thread still carries
   * `internalizePending = true`, show a non-modal banner above the body so
   * the user understands why placeholder icons appear in place of the
   * external images. Cleared automatically when the push event fires and
   * the suite re-fetches the affected Email object (REQ-EXTIMG-BG-33).
   */
  let anyInternalizePending = $derived(
    emails.some((e) => e.internalizePending === true),
  );

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

  // Plain (non-reactive) set that tracks which email IDs were present when
  // the initial expansion ran. Used by the new-message auto-expand effect
  // below to detect arrivals (e.g. a self-sent reply) without tracking
  // prevEmailIds as $state — which would loop through the write-from-effect
  // anti-pattern described in web/CLAUDE.md.
  let prevEmailIds = new Set<string>();

  $effect(() => {
    if (!initialised && emails.length > 0) {
      const initial = pickInitialExpanded(emails);
      if (initial) {
        expanded = new Set([initial]);
      }
      // Capture the baseline ID set so the new-message effect can diff
      // against it on the very first tick (prevents it from marking all
      // initial emails as "new" on the same render).
      prevEmailIds = new Set(emails.map((e) => e.id));
      initialised = true;
    }
  });

  // Auto-expand messages that arrive AFTER the initial expansion (re #34).
  // The primary use-case is the just-sent reply appearing in the thread
  // while the thread is still open. Uses untrack() around the expanded
  // write to avoid the read-write loop described in web/CLAUDE.md.
  $effect(() => {
    if (!initialised) return;
    const currentIds = new Set(emails.map((e) => e.id));
    const newIds: string[] = [];
    for (const id of currentIds) {
      if (!prevEmailIds.has(id)) newIds.push(id);
    }
    prevEmailIds = currentIds;
    if (newIds.length > 0) {
      untrack(() => {
        expanded = new Set([...expanded, ...newIds]);
      });
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
    <ThreadToolbar {threadId} {latest} {standalone} onPrint={() => void printThread()} />
    <!-- New-reply banner lives OUTSIDE the scroll container so it is always
         visible and never displaces the user's reading position when an
         external message arrives (issue #56). The user must explicitly
         accept before the message appears in the rendered list. -->
    <ThreadNewReplyBanner
      {threadId}
      onAccept={() => void handleAccept()}
      onDismiss={handleDismiss}
    />
    <div class="scroll" bind:this={scrollContainerEl}>
      <header>
        <h1>{subject}</h1>
        {#if threadLabels.length > 0}
          <div class="thread-labels" aria-label="Labels">
            {#each threadLabels as lbl (lbl.name)}
              <span
                class="label-badge"
                style={lbl.color
                  ? `background:${lbl.color};color:${labelForeground(lbl.color)};`
                  : undefined}
              >{lbl.name}</span>
            {/each}
          </div>
        {/if}
      </header>
      {#if anyInternalizePending}
        <div class="internalize-pending-banner" role="status" aria-live="polite">
          <strong>{t('mail.threadReader.internalizePending.heading')}</strong>
          <span>{t('mail.threadReader.internalizePending.body')}</span>
        </div>
      {/if}
      <!-- REQ-MAIL-12c: per-message tagged-address banner. Anchored to
           the latest message in the thread so the suffix matches the
           message the user just opened. -->
      <TaggedAddressBanner email={latest} />
      <div class="messages">
        {#each emails as email (email.id)}
          <div data-email-id={email.id}>
            <MessageAccordion {email} expanded={expanded.has(email.id)} onToggle={toggle} {standalone} />
          </div>
        {/each}
      </div>
      <!-- Inline reply / forward composer (re #38): mounted below the
           messages so the user can see the conversation while composing.
           Rendered only when compose.inlineMode is true; the ThreadReplyBar
           hides itself while this is visible. -->
      <ThreadInlineComposer {threadId} />
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
  /* The middle region scrolls; toolbar + reply bar stay pinned.
     min-width:0 keeps the scroll region from being widened by a wide
     message body, so wide content scrolls inside .scroll instead of
     stretching the whole reader / app layout. */
  .scroll {
    flex: 1;
    overflow: auto;
    min-width: 0;
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

  /* Non-modal banner shown above the message bodies when any message in
     the thread is still waiting for the background-internalize worker
     (REQ-EXTIMG-BG-31). The banner clears automatically on the push
     event that follows the worker's rewrite (REQ-EXTIMG-BG-33). */
  .internalize-pending-banner {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    padding: var(--spacing-03) var(--spacing-05);
    margin: var(--spacing-04) var(--spacing-05) 0;
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .internalize-pending-banner strong {
    color: var(--text-primary);
    font-weight: 600;
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
