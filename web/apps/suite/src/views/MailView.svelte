<script lang="ts">
  import { untrack } from 'svelte';
  import { router } from '../lib/router/router.svelte';
  import { mail, type FolderID } from '../lib/mail/store.svelte';
  import { keyboard } from '../lib/keyboard/engine.svelte';
  import { compose } from '../lib/compose/compose.svelte';
  import { movePicker } from '../lib/mail/move-picker.svelte';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';
  import type { Email } from '../lib/mail/types';

  const VALID_FOLDERS: FolderID[] = ['inbox', 'sent', 'drafts', 'trash', 'all'];

  let threadId = $derived(router.parts[1] === 'thread' ? router.parts[2] : undefined);
  let label = $derived(router.parts[1] === 'label' ? router.parts[2] : undefined);
  let searchQuery = $derived(
    router.parts[1] === 'search' ? decodeURIComponent(router.parts[2] ?? '') : undefined,
  );
  // Folder routes:
  //   /mail                       → inbox (legacy path)
  //   /mail/folder/<id>           → generic folder view (sent / drafts / trash / all)
  let folder = $derived.by<FolderID | undefined>(() => {
    if (router.matches('mail') && !router.parts[1]) return 'inbox';
    if (router.parts[1] !== 'folder') return undefined;
    const id = router.parts[2];
    return VALID_FOLDERS.includes(id as FolderID) ? (id as FolderID) : undefined;
  });
  let isListRoute = $derived(folder !== undefined);
  let isSearchRoute = $derived(router.matches('mail', 'search'));
  let isInboxRoute = $derived(folder === 'inbox');
  let folderLabel = $derived(folder ? mail.listFolderLabel : '');

  // Kick off the list load when a folder route is shown. The load call is
  // wrapped in untrack() so the synchronous loadFolder/loadStatus read-
  // modify-write inside the store does not register the status cell as a
  // dep of this effect; otherwise a JMAP error (status -> 'error') re-
  // fires the effect, which retries the call, which writes 'error' again,
  // in a tight loop.
  $effect(() => {
    const f = folder;
    if (f) {
      untrack(() => {
        void mail.loadFolder(f);
      });
    }
  });

  // Run the search whenever the search route's query changes. Same untrack
  // rationale as the inbox effect above.
  $effect(() => {
    if (isSearchRoute && searchQuery !== undefined) {
      const q = searchQuery;
      untrack(() => {
        void mail.runSearch(q);
      });
    }
  });

  // Search-view keyboard layer.
  $effect(() => {
    if (!isSearchRoute) return;

    const focusedSearchId = (): string | null => {
      const idx = mail.searchFocusedIndex;
      if (idx < 0) return null;
      return mail.searchEmailIds[idx] ?? null;
    };

    const pop = keyboard.pushLayer([
      {
        key: 'j',
        description: 'Next result',
        action: () => mail.focusSearchNext(),
      },
      {
        key: 'k',
        description: 'Previous result',
        action: () => mail.focusSearchPrev(),
      },
      {
        key: 'Enter',
        description: 'Open focused thread',
        action: () => {
          const tid = mail.focusedSearchThreadId();
          if (tid) router.navigate(`/mail/thread/${encodeURIComponent(tid)}`);
        },
      },
      {
        key: 'Escape',
        description: 'Clear search',
        action: () => router.navigate('/mail'),
      },
      {
        key: 'e',
        description: 'Archive',
        action: () => {
          const id = focusedSearchId();
          if (id) void mail.archiveEmail(id);
        },
      },
      {
        key: 's',
        description: 'Star / unstar',
        action: () => {
          const id = focusedSearchId();
          if (id) void mail.toggleFlagged(id);
        },
      },
    ]);
    return pop;
  });

  // List-view keyboard bindings are pushed while any folder list is
  // showing. j/k focus, Enter opens the thread, s toggles the flag, I/U
  // toggle the read keyword. Archive (e) and Delete (#) are wired only
  // for the inbox: archiving from Sent / Drafts / All Mail / Trash has
  // no useful semantic at v1.
  $effect(() => {
    if (!isListRoute) return;

    const focusedEmailId = (): string | null => {
      const idx = mail.listFocusedIndex;
      if (idx < 0) return null;
      return mail.listEmailIds[idx] ?? null;
    };

    const layer = [
      {
        key: 'j',
        description: 'Next thread',
        action: () => mail.focusListNext(),
      },
      {
        key: 'k',
        description: 'Previous thread',
        action: () => mail.focusListPrev(),
      },
      {
        key: 'Enter',
        description: 'Open focused thread',
        action: () => {
          const id = focusedEmailId();
          if (!id) return;
          const email = mail.emails.get(id);
          if (!email) return;
          if (folder === 'drafts') {
            void openListRow(email);
          } else {
            router.navigate(`/mail/thread/${encodeURIComponent(email.threadId)}`);
          }
        },
      },
      {
        key: 'Escape',
        description: 'Clear focus',
        action: () => {
          mail.listFocusedIndex = -1;
        },
      },
      {
        key: 's',
        description: 'Star / unstar',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.toggleFlagged(id);
        },
      },
      {
        key: 'I',
        description: 'Mark as read',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.setSeen(id, true);
        },
      },
      {
        key: 'U',
        description: 'Mark as unread',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.setSeen(id, false);
        },
      },
      {
        key: 'v',
        description: 'Move to mailbox',
        action: () => {
          const id = focusedEmailId();
          if (id) movePicker.open(id);
        },
      },
    ];
    if (isInboxRoute) {
      layer.push(
        {
          key: 'e',
          description: 'Archive',
          action: () => {
            const id = focusedEmailId();
            if (id) void mail.archiveEmail(id);
          },
        },
        {
          key: '#',
          description: 'Delete',
          action: () => {
            const id = focusedEmailId();
            if (id) void mail.deleteEmail(id);
          },
        },
      );
    }
    const pop = keyboard.pushLayer(layer);
    return pop;
  });

  // Thread-view bindings.
  $effect(() => {
    if (!threadId) return;
    const tid = threadId;
    const replyTarget = (): Email | null => {
      const emails = mail.threadEmails(tid);
      return emails[emails.length - 1] ?? null;
    };
    const pop = keyboard.pushLayer([
      {
        key: 'Escape',
        description: `Back to ${mail.listFolderLabel}`,
        action: () => router.navigate(folderHref(mail.listFolder)),
      },
      {
        key: 'r',
        description: 'Reply',
        action: () => {
          const e = replyTarget();
          if (e) compose.openReply(e);
        },
      },
      {
        key: 'R',
        description: 'Reply all',
        action: () => {
          const e = replyTarget();
          if (e) compose.openReplyAll(e);
        },
      },
      {
        key: 'f',
        description: 'Forward',
        action: () => {
          const e = replyTarget();
          if (e) compose.openForward(e);
        },
      },
      {
        key: 'u',
        description: `Back to ${mail.listFolderLabel}`,
        action: () => router.navigate(folderHref(mail.listFolder)),
      },
      {
        key: 'v',
        description: 'Move to mailbox',
        action: () => {
          const e = replyTarget();
          if (e) movePicker.open(e.id);
        },
      },
    ]);
    return pop;
  });

  // Scroll the focused row into view whenever the index changes.
  let listEl = $state<HTMLUListElement | null>(null);
  $effect(() => {
    if (!isListRoute) return;
    const idx = mail.listFocusedIndex;
    if (idx < 0 || !listEl) return;
    const row = listEl.querySelector<HTMLElement>(`[data-row-index="${idx}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  });

  function folderHref(f: FolderID): string {
    return f === 'inbox' ? '/mail' : `/mail/folder/${f}`;
  }

  function emptyMessage(f: FolderID | undefined): string {
    if (!f) return '';
    if (f === 'inbox') return 'Inbox is empty.';
    if (f === 'all') return 'No mail.';
    return `${FOLDER_DISPLAY[f]} is empty.`;
  }
  const FOLDER_DISPLAY: Record<FolderID, string> = {
    inbox: 'Inbox',
    sent: 'Sent',
    drafts: 'Drafts',
    trash: 'Trash',
    all: 'All Mail',
  };

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

  /**
   * Drafts folder activation: instead of opening the thread reader (a
   * draft is rarely interesting to read), load the body and open
   * compose with the draft pre-populated. The send / discard paths in
   * compose then update / destroy the same row.
   */
  async function openListRow(email: Email): Promise<void> {
    if (folder === 'drafts') {
      try {
        await mail.loadDraftBody(email.id);
        const full = mail.emails.get(email.id);
        if (full) compose.openDraft(full);
      } catch (err) {
        console.error('open draft failed', err);
        // Fall back to the thread reader so the user is not stuck.
        openThread(email);
      }
      return;
    }
    openThread(email);
  }
</script>

<div class="mail">
  {#if threadId}
    <div class="thread-frame">
      <header class="thread-frame-bar">
        <button
          type="button"
          class="back"
          onclick={() => router.navigate(folderHref(mail.listFolder))}
        >
          ← Back to {mail.listFolderLabel}
        </button>
      </header>
      <ThreadReader {threadId} />
    </div>
  {:else if isSearchRoute}
    <header class="list-header">
      <h1>
        Search: <span class="query-echo">{searchQuery || '(empty)'}</span>
      </h1>
      <button type="button" class="back" onclick={() => router.navigate('/mail')}>
        ← Back to inbox
      </button>
    </header>

    {#if mail.searchLoadStatus === 'idle' || mail.searchLoadStatus === 'loading'}
      <div class="state">Searching…</div>
    {:else if mail.searchLoadStatus === 'error'}
      <div class="state error">
        <p>Search failed.</p>
        {#if mail.searchError}<p class="detail">{mail.searchError}</p>{/if}
        <button type="button" onclick={() => mail.runSearch(mail.searchQuery)}>
          Retry
        </button>
      </div>
    {:else if mail.searchEmails.length === 0}
      <div class="state">No matches.</div>
    {:else}
      <ul class="thread-list" role="listbox" aria-label="Search results">
        {#each mail.searchEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.searchFocusedIndex === i}
            data-row-index={i}
          >
            <button
              type="button"
              role="option"
              aria-selected={mail.searchFocusedIndex === i}
              onclick={() => {
                mail.searchFocusedIndex = i;
                openThread(email);
              }}
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
  {:else if label}
    <header>
      <h1>Label: {label}</h1>
      <p class="lead">Label-view querying arrives after inbox.</p>
    </header>
  {:else if isListRoute}
    <header class="list-header">
      <h1>{folderLabel}</h1>
      <button
        type="button"
        class="refresh"
        aria-label="Refresh"
        onclick={() => mail.refreshFolder()}
        disabled={mail.listLoadStatus === 'loading'}
      >
        ↻
      </button>
    </header>

    {#if mail.listLoadStatus === 'idle' || mail.listLoadStatus === 'loading'}
      <div class="state">Loading…</div>
    {:else if mail.listLoadStatus === 'error'}
      <div class="state error">
        <p>Couldn't load {folderLabel.toLowerCase()}.</p>
        {#if mail.listError}<p class="detail">{mail.listError}</p>{/if}
        <button type="button" onclick={() => mail.refreshFolder()}>Retry</button>
      </div>
    {:else if mail.listEmails.length === 0}
      <div class="state">{emptyMessage(folder)}</div>
    {:else}
      <ul
        class="thread-list"
        role="listbox"
        aria-label="{folderLabel} threads"
        bind:this={listEl}
      >
        {#each mail.listEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.listFocusedIndex === i}
            data-row-index={i}
          >
            <button
              type="button"
              role="option"
              aria-selected={mail.listFocusedIndex === i}
              onclick={() => {
                mail.listFocusedIndex = i;
                void openListRow(email);
              }}
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
  {:else}
    <header>
      <h1>Not found</h1>
      <p class="lead">No mail folder at that URL.</p>
    </header>
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
  .query-echo {
    font-family: var(--font-mono);
    font-size: var(--type-code-02-size);
    color: var(--text-primary);
    background: var(--layer-02);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    font-weight: 500;
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
    border-left: 3px solid transparent;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .thread-row.focused {
    border-left-color: var(--interactive);
    background: var(--layer-01);
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
