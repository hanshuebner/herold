<script lang="ts">
  import { untrack } from 'svelte';
  import { router } from '../lib/router/router.svelte';
  import { mail, type FolderID } from '../lib/mail/store.svelte';
  import { keyboard } from '../lib/keyboard/engine.svelte';
  import { compose } from '../lib/compose/compose.svelte';
  import { movePicker } from '../lib/mail/move-picker.svelte';
  import { decodeChips } from '../lib/mail/search-query';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';
  import type { Email } from '../lib/mail/types';

  const ROLED_FOLDERS = new Set<FolderID>(['inbox', 'sent', 'drafts', 'trash', 'all']);

  let threadId = $derived(router.parts[1] === 'thread' ? router.parts[2] : undefined);
  let label = $derived(router.parts[1] === 'label' ? router.parts[2] : undefined);
  let searchQuery = $derived(
    router.parts[1] === 'search' ? decodeURIComponent(router.parts[2] ?? '') : undefined,
  );
  // Folder routes:
  //   /mail                       → inbox (legacy path)
  //   /mail/folder/<role>         → roled folder (inbox / sent / drafts / trash / all)
  //   /mail/folder/<id>           → custom mailbox by Mailbox.id
  let folder = $derived.by<FolderID | undefined>(() => {
    if (router.matches('mail') && !router.parts[1]) return 'inbox';
    if (router.parts[1] !== 'folder') return undefined;
    const id = router.parts[2];
    if (!id) return undefined;
    if (ROLED_FOLDERS.has(id)) return id;
    // Custom mailbox: only accept if the id resolves to a real mailbox
    // we know about (otherwise we'd start a load that will 'error').
    return mail.mailboxes.has(id) ? id : undefined;
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
      {
        key: 'x',
        description: 'Toggle selection',
        action: () => {
          const id = focusedEmailId();
          if (id) mail.toggleSelected(id);
        },
      },
      {
        key: '*',
        description: 'Select all visible',
        action: () => mail.selectAllVisible(),
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
    if (folder === 'trash') {
      layer.push({
        key: 'Z',
        description: 'Restore from trash',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.restoreFromTrash(id);
        },
      });
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
      {
        key: 'I',
        description: 'Mark thread read',
        action: () => void mail.markThreadSeen(tid, true),
      },
      {
        key: 'U',
        description: 'Mark thread unread',
        action: () => void mail.markThreadSeen(tid, false),
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

  function confirmEmptyTrash(): void {
    const n = mail.listEmails.length;
    const ok = confirm(
      `Permanently delete ${n} message${n === 1 ? '' : 's'} from Trash? This cannot be undone.`,
    );
    if (ok) void mail.emptyTrash();
  }

  function selectedIds(): string[] {
    return [...mail.listSelectedIds];
  }
  function bulkArchive(): void {
    void mail.bulkArchive(selectedIds());
  }
  function bulkDelete(): void {
    const ids = selectedIds();
    if (ids.length === 0) return;
    const ok = confirm(`Move ${ids.length} message${ids.length === 1 ? '' : 's'} to Trash?`);
    if (ok) void mail.bulkDelete(ids);
  }
  function bulkMarkRead(): void {
    void mail.bulkSetSeen(selectedIds(), true);
  }
  function bulkMarkUnread(): void {
    void mail.bulkSetSeen(selectedIds(), false);
  }
  function bulkMove(): void {
    const ids = selectedIds();
    if (ids.length === 0) return;
    movePicker.openBulk(ids);
  }

  function emptyMessage(f: FolderID | undefined): string {
    if (!f) return '';
    if (f === 'inbox') return 'Inbox is empty.';
    if (f === 'all') return 'No mail.';
    if (FOLDER_DISPLAY[f]) return `${FOLDER_DISPLAY[f]} is empty.`;
    return `${mail.mailboxes.get(f)?.name ?? 'Mailbox'} is empty.`;
  }
  const FOLDER_DISPLAY: Record<string, string> = {
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

    {#if searchQuery}
      <div class="search-chips" aria-label="Recognised query">
        {#each decodeChips(searchQuery) as chip, i (i + chip.raw)}
          <span class="chip" class:text={chip.operator === 'text'}>
            {#if chip.operator !== 'text'}<span class="op">{chip.operator}</span>{/if}
            <span class="value">{chip.value || chip.label}</span>
          </span>
        {/each}
      </div>
    {/if}

    {#if mail.searchHistory.length > 0}
      <div class="search-history" aria-label="Recent searches">
        <span class="history-label">Recent:</span>
        {#each mail.searchHistory.slice(0, 6) as q (q)}
          <button
            type="button"
            class="history-chip"
            onclick={() =>
              router.navigate(`/mail/search/${encodeURIComponent(q)}`)}
            title="Re-run search"
          >
            {q}
          </button>
        {/each}
        <button
          type="button"
          class="history-clear"
          onclick={() => mail.clearSearchHistory()}
          aria-label="Clear search history"
          title="Clear history"
        >
          Clear
        </button>
      </div>
    {/if}

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
      {#if folder === 'trash' && mail.listEmails.length > 0}
        <button
          type="button"
          class="danger"
          onclick={confirmEmptyTrash}
          disabled={mail.listLoadStatus === 'loading'}
        >
          Empty trash
        </button>
      {/if}
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
      {#if mail.listSelectedIds.size > 0}
        <div class="bulk-bar" role="toolbar" aria-label="Bulk actions">
          <span class="bulk-count">
            {mail.listSelectedIds.size} selected
          </span>
          <button type="button" onclick={() => mail.clearSelection()}>Clear</button>
          <span class="bulk-spacer"></span>
          {#if folder === 'inbox'}
            <button type="button" onclick={bulkArchive}>Archive</button>
          {/if}
          <button type="button" onclick={bulkMarkRead}>Mark read</button>
          <button type="button" onclick={bulkMarkUnread}>Mark unread</button>
          <button type="button" onclick={bulkMove}>Move…</button>
          <button type="button" class="danger" onclick={bulkDelete}>Delete</button>
        </div>
      {/if}
      <ul
        class="thread-list"
        role="listbox"
        aria-label="{folderLabel} threads"
        aria-multiselectable="true"
        bind:this={listEl}
      >
        {#each mail.listEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.listFocusedIndex === i}
            class:selected={mail.listSelectedIds.has(email.id)}
            data-row-index={i}
          >
            <input
              type="checkbox"
              class="row-check"
              aria-label="Select message"
              checked={mail.listSelectedIds.has(email.id)}
              onchange={() => mail.toggleSelected(email.id)}
              onclick={(e) => e.stopPropagation()}
            />
            <button
              type="button"
              class="row-activate"
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
  .search-chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }
  .chip .op {
    color: var(--interactive);
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    font-weight: 600;
  }
  .chip.text {
    background: var(--layer-01);
    border: 1px dashed var(--border-subtle-01);
  }

  .search-history {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-02);
    align-items: center;
    padding: var(--spacing-02) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .history-label {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin-right: var(--spacing-02);
  }
  .history-chip {
    padding: var(--spacing-01) var(--spacing-03);
    background: var(--layer-02);
    color: var(--text-secondary);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .history-chip:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .history-clear {
    margin-left: auto;
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    padding: var(--spacing-01) var(--spacing-03);
  }
  .history-clear:hover {
    color: var(--text-primary);
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
  .danger {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--support-error);
    font-weight: 600;
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .danger:hover:not(:disabled) {
    background: var(--support-error);
    color: var(--text-on-color);
  }
  .danger:disabled {
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
    display: grid;
    grid-template-columns: auto 1fr;
    align-items: center;
    border-bottom: 1px solid var(--border-subtle-01);
    border-left: 3px solid transparent;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .thread-row.focused {
    border-left-color: var(--interactive);
    background: var(--layer-01);
  }
  .thread-row.selected {
    background: var(--layer-02);
  }
  .row-check {
    margin: 0 var(--spacing-03) 0 var(--spacing-04);
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
  }
  .row-activate {
    display: grid;
    grid-template-columns: 24px 14ch 1fr auto auto;
    gap: var(--spacing-04);
    align-items: center;
    width: 100%;
    padding: var(--spacing-03) var(--spacing-04) var(--spacing-03) 0;
    color: var(--text-secondary);
    text-align: left;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .row-activate:hover {
    background: var(--layer-01);
  }
  .thread-row.unread .row-activate {
    color: var(--text-primary);
  }

  .bulk-bar {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-02);
  }
  .bulk-count {
    color: var(--text-primary);
    font-weight: 600;
    margin-right: var(--spacing-02);
  }
  .bulk-spacer {
    flex: 1;
  }
  .bulk-bar button {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-01);
    color: var(--text-primary);
    font-weight: 500;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .bulk-bar button:hover {
    background: var(--layer-03);
  }
  .bulk-bar button.danger {
    color: var(--support-error);
  }
  .bulk-bar button.danger:hover {
    background: var(--support-error);
    color: var(--text-on-color);
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

  /* Narrow viewport: compress the row grid so the date and sender stay
     visible on a phone-width screen. The from cell shrinks to the
     sender's initials-region; the subject + preview gets the rest. */
  @media (max-width: 560px) {
    .row-activate {
      grid-template-columns: 18px 8ch 1fr auto;
      gap: var(--spacing-02);
      padding: var(--spacing-02) var(--spacing-03) var(--spacing-02) 0;
    }
    .row-activate .attachment {
      display: none;
    }
    .from {
      font-size: var(--type-body-compact-01-size);
    }
    .date {
      font-size: var(--type-body-compact-01-size);
    }
    .row-check {
      margin-left: var(--spacing-02);
      margin-right: var(--spacing-02);
    }
    .list-header {
      padding: var(--spacing-03) var(--spacing-04);
    }
    .bulk-bar {
      flex-wrap: wrap;
      padding: var(--spacing-02) var(--spacing-03);
    }
    .search-history,
    .search-chips {
      padding: var(--spacing-02) var(--spacing-04);
    }
  }
</style>
