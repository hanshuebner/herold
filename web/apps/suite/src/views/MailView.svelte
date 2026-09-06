<script lang="ts">
  import { untrack } from 'svelte';
  import { router } from '../lib/router/router.svelte';
  import { mail, type FolderID } from '../lib/mail/store.svelte';
  import { keyboard } from '../lib/keyboard/engine.svelte';
  import { compose } from '../lib/compose/compose.svelte';
  import { confirm } from '../lib/dialog/confirm.svelte';
  import { movePicker } from '../lib/mail/move-picker.svelte';
  import { snoozePicker } from '../lib/mail/snooze-picker.svelte';
  import { categoryPicker } from '../lib/mail/category-picker.svelte';
  import { categorySettings, emailMatchesTab, categoryKeyword } from '../lib/settings/category-settings.svelte';
  import { decodeChips } from '../lib/mail/search-query';
  import { threadDnd, dragIdsForRow } from '../lib/mail/dnd-thread.svelte';
  import ThreadReader from '../lib/mail/ThreadReader.svelte';
  import CategoryPicker from '../lib/mail/CategoryPicker.svelte';
  import SelectChooser from '../lib/mail/SelectChooser.svelte';
  import { shouldOfferWholeSet } from '../lib/list-selection/whole-set-selection';
  import { handleRowCheckboxClick } from '../lib/list-selection/range-select';
  import { labelPicker } from '../lib/mail/label-picker.svelte';
  import { t, localeTag } from '../lib/i18n/i18n.svelte';
  import type { Email } from '../lib/mail/types';
  import { labelForeground } from '../lib/mail/label-color';
  import { threadSubject } from '../lib/mail/thread-subject';
  import ArchiveIcon from '../lib/icons/ArchiveIcon.svelte';
  import TrashIcon from '../lib/icons/TrashIcon.svelte';
  import MarkReadIcon from '../lib/icons/MarkReadIcon.svelte';
  import MarkUnreadIcon from '../lib/icons/MarkUnreadIcon.svelte';
  import MoveIcon from '../lib/icons/MoveIcon.svelte';
  import LabelIcon from '../lib/icons/LabelIcon.svelte';
  import CategoryIcon from '../lib/icons/CategoryIcon.svelte';
  import ImageIcon from '../lib/icons/ImageIcon.svelte';

  const ROLED_FOLDERS = new Set<FolderID>([
    'inbox',
    'sent',
    'drafts',
    'trash',
    'all',
    'important',
    'snoozed',
  ]);

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

  // ── Category tabs (Inbox only, REQ-CAT-10..14) ───────────────────────────
  //
  // When the categorise capability is advertised and we're in the inbox view,
  // show the tab strip. The active tab name comes from `?tab=<name>` in the
  // URL; null means Primary (no category keyword on the email, REQ-CAT-11).
  //
  // "null" is Primary; any other value is the category name from the settings.

  let showTabs = $derived(
    isInboxRoute && categorySettings.available && categorySettings.derivedCategories.length > 0,
  );

  /**
   * Active tab name; null means "Primary" (emails without a $category-*
   * keyword, per REQ-CAT-03/11).
   */
  let activeTabName = $derived.by<string | null>(() => {
    if (!showTabs) return null;
    const param = router.getParam('tab');
    if (!param) return null; // Default = Primary
    // Validate: must be a known derived category name (case-insensitive).
    const match = categorySettings.derivedCategories.find(
      (name) => name.toLowerCase() === param.toLowerCase(),
    );
    return match ?? null;
  });

  /** Filtered list for the active inbox tab. */
  let tabFilteredEmailIds = $derived.by<string[]>(() => {
    if (!showTabs) return mail.listEmailIds;
    return mail.listEmailIds.filter((id) => {
      const e = mail.emails.get(id);
      if (!e) return false;
      return emailMatchesTab(e.keywords, activeTabName, categorySettings.derivedCategories);
    });
  });

  /** Unread count per category tab (for the badge). */
  function tabUnreadCount(tabName: string | null): number {
    if (!showTabs) return 0;
    let n = 0;
    for (const id of mail.listEmailIds) {
      const e = mail.emails.get(id);
      if (!e) continue;
      if (!emailMatchesTab(e.keywords, tabName, categorySettings.derivedCategories)) continue;
      if (!e.keywords.$seen) n++;
    }
    return n;
  }

  function selectTab(tabKey: string | null): void {
    // Primary is encoded as no param (cleaner URLs).
    // tabKey is either null (Primary) or the category name; lowercase for URL.
    router.setParam('tab', tabKey === null ? null : tabKey.toLowerCase());
  }

  /**
   * The effective list email ids — tab-filtered when in inbox, raw
   * otherwise — narrowed to ids that actually have a resolved `Email` in
   * `mail.emails`. An id can be present in `mail.listEmailIds` (from the
   * `Email/query` result) before or without its `Email/get` companion
   * landing (in-flight page load, a JMAP `notFound` for a since-deleted
   * id, ...); such an id renders no row. Keeping this list in lock-step
   * with what actually renders is what makes it safe to pass to
   * `selectRowClick` / `toggleSelectAllVisible`: a shift-click range or a
   * select-all computed over `effectiveListEmailIds` can then never
   * include an id with no on-screen checkbox, which would otherwise
   * desync the toolbar's selection count from the rendered checked rows
   * (re #202).
   */
  let effectiveListEmailIds = $derived(
    (showTabs ? tabFilteredEmailIds : mail.listEmailIds).filter((id) => mail.emails.has(id)),
  );

  /** Resolved emails for the effective list — one-to-one with `effectiveListEmailIds`. */
  let effectiveListEmails = $derived(
    effectiveListEmailIds
      .map((id) => mail.emails.get(id))
      .filter((e): e is Email => e !== undefined),
  );

  /**
   * Ids of the rendered search-result rows, one-to-one with
   * `mail.searchEmails` (which already drops any id from `searchEmailIds`
   * lacking a resolved `Email`). The search list's shift-click / select-all
   * paths use this instead of the raw `mail.searchEmailIds` for the same
   * reason as `effectiveListEmailIds` above (re #202).
   */
  let renderedSearchEmailIds = $derived(mail.searchEmails.map((email) => email.id));

  /**
   * Prune `mail.listSelectedIds` back to what's actually on screen whenever
   * the rendered set changes for a reason other than a fresh folder load or
   * an explicit clear -- a category-tab switch (`showTabs`, REQ-CAT-10..14)
   * or a background refresh dropping an id are both just `router.setParam`
   * / a store field write and neither one touches the selection Set on its
   * own. Left unreconciled, the Set can hold ids with no rendered checkbox:
   * the toolbar count (`listSelectedIds.size`) then exceeds what's checked,
   * and a bulk action reading the raw Set would silently act on hidden rows
   * too (re #202). Skipped while `listWholeMailboxSelected` is true: that
   * mode's Set is deliberately a stand-in for a server-side filter query
   * (see `selectAllVisible`'s docstring), not the literal selection.
   */
  $effect(() => {
    const rendered = isSearchRoute
      ? renderedSearchEmailIds
      : isListRoute
        ? effectiveListEmailIds
        : null;
    if (rendered === null) return;
    untrack(() => mail.pruneSelectionToRendered(rendered));
  });

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
      {
        key: 'x',
        description: 'Toggle selection',
        action: () => {
          const id = focusedSearchId();
          if (id) mail.toggleSelected(id);
        },
      },
      {
        key: '*',
        description: 'Select / deselect all visible',
        action: () => mail.toggleSelectAllVisible(renderedSearchEmailIds),
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
        key: '!',
        description: 'Toggle important',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.toggleImportant(id);
        },
      },
      {
        key: 'h',
        description: 'Snooze',
        action: () => {
          const id = focusedEmailId();
          if (id) snoozePicker.open(id);
        },
      },
      {
        key: 'm',
        description: 'Move to category',
        action: () => {
          const id = focusedEmailId();
          if (id && categorySettings.available) categoryPicker.open(id);
        },
      },
      {
        key: '*',
        description: 'Select / deselect all visible',
        action: () => mail.toggleSelectAllVisible(effectiveListEmailIds),
      },
    ];
    // Archive is inbox-only: archiving from Sent / Drafts / Trash has no
    // well-defined semantic so we keep the binding scoped.
    if (isInboxRoute) {
      layer.push({
        key: 'e',
        description: 'Archive',
        action: () => {
          const id = focusedEmailId();
          if (id) void mail.archiveEmail(id);
        },
      });
    }
    // Delete is available in every folder list (re #29):
    //   - Outside Trash: silently move to Trash with an Undo toast.
    //   - Inside Trash: permanent destroy with a confirm prompt.
    layer.push({
      key: '#',
      description: 'Delete',
      action: () => {
        const id = focusedEmailId();
        if (!id) return;
        if (folder === 'trash') {
          void confirmAndDestroy(id);
        } else {
          void mail.deleteEmail(id);
        }
      },
    });
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
        key: 'l',
        description: 'Apply labels',
        action: () => {
          const e = replyTarget();
          if (e) labelPicker.open(e.id);
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
      {
        key: '!',
        description: 'Toggle important',
        action: () => {
          const e = replyTarget();
          if (e) void mail.toggleImportant(e.id);
        },
      },
      {
        key: 'h',
        description: 'Snooze',
        action: () => {
          const e = replyTarget();
          if (e) snoozePicker.open(e.id);
        },
      },
      {
        key: 'm',
        description: 'Move to category',
        action: () => {
          const e = replyTarget();
          if (e && categorySettings.available) categoryPicker.open(e.id);
        },
      },
    ]);
    return pop;
  });

  // When the thread reader is open, watch the thread emails' mailboxIds. If
  // the current folder is a real mailbox-backed folder (inbox / sent / drafts
  // / trash / custom mailbox id) and no thread email belongs to that mailbox
  // anymore — e.g. after a Restore-from-Trash or Move-To-Mailbox operation
  // moves it away — navigate back to the folder list automatically (re #29).
  //
  // Virtual folders (all, important, snoozed) are skipped: a message remains
  // valid to view in those contexts regardless of its mailbox membership.
  //
  // Defense-in-depth (re #88): only navigate away when we have previously
  // CONFIRMED that the thread belongs to the current folder (i.e. the
  // membership check was true on at least one earlier evaluation). This
  // prevents a false bounce on the initial render when the mailboxIds union
  // across self-sent copies has not yet been computed, or when the thread
  // is opened directly by URL into a folder it does not literally live in.
  //
  // `confirmedFolderKey` is a plain variable (not reactive), so writing it
  // inside the $effect does not trigger a re-run. The key encodes both the
  // threadId and the current folder so that navigating to a different thread
  // or switching folders resets the tracking automatically.

  // Track the last thread+folder combination for which we have confirmed
  // the thread IS in the folder. Plain let — intentionally not $state.
  let confirmedFolderKey = '';

  $effect(() => {
    if (!threadId) {
      // Leaving the thread reader clears the confirmation. Without this,
      // returning to the SAME thread later (e.g. the browser Back button
      // re-reaching an archived thread's reader, re #294) finds the guard
      // still armed from the original viewing and bounces away instantly,
      // before the reader ever renders — a page reload's cold-load guard
      // (FIX B, re #88) only helps on a fresh page; it does not re-apply
      // to a route re-entered within the same session.
      confirmedFolderKey = '';
      return;
    }

    // On a cold load the folder list has never been fetched: listFolder
    // holds its default value ('inbox') with no real context. Only enforce
    // the auto-navigate-away rule after a folder has been loaded
    // (listLoadStatus === 'ready'). A thread opened directly by URL must
    // render and not be bounced to the inbox list just because it lives
    // in a non-inbox folder.
    if (mail.listLoadStatus !== 'ready') return;

    // Skip when the thread was opened from a search-results view: the
    // user's listFolder is unrelated to where this thread actually
    // lives (a Junk-mailbox hit on a search submitted from Inbox would
    // otherwise auto-bounce back to /mail before the reader renders).
    const cameFromSearch = mail.searchEmailIds.some((id) => {
      const e = mail.emails.get(id);
      return e?.threadId === threadId;
    });
    if (cameFromSearch) return;

    const currentFolder = mail.listFolder;
    // "all", "important", "snoozed" are virtual — no single mailbox to check.
    if (currentFolder === 'all' || currentFolder === 'important' || currentFolder === 'snoozed') return;

    // Resolve the mailbox id for the current folder.
    let mailboxId: string | null = null;
    if (currentFolder === 'inbox') {
      mailboxId = mail.inbox?.id ?? null;
    } else if (currentFolder === 'trash') {
      mailboxId = mail.trash?.id ?? null;
    } else if (currentFolder === 'sent') {
      mailboxId = mail.sent?.id ?? null;
    } else if (currentFolder === 'drafts') {
      mailboxId = mail.drafts?.id ?? null;
    } else {
      // Custom mailbox: listFolder IS the mailbox id.
      mailboxId = currentFolder;
    }
    if (!mailboxId) return;

    const emails = mail.threadEmails(threadId);
    // While the thread is still loading, emails is empty — don't navigate yet.
    if (emails.length === 0) return;

    const key = `${threadId}:${currentFolder}`;
    const stillInFolder = emails.some((e) => Boolean(e.mailboxIds[mailboxId!]));

    if (stillInFolder) {
      // Record that we have positively confirmed this thread in this folder.
      // Written outside untrack intentionally: it is a plain variable, not
      // reactive, so this assignment does not trigger the effect to re-run.
      confirmedFolderKey = key;
      return;
    }

    // Thread is not currently in the folder. Only navigate away when we
    // have previously confirmed it was here — that is, the user moved /
    // archived the thread while reading it (re #29). On the first render
    // where membership is already false (e.g. self-sent dedup edge case
    // or direct URL open), do not bounce.
    if (confirmedFolderKey === key) {
      untrack(() => {
        router.navigate(folderHref(currentFolder));
      });
    }
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

  // Infinite scroll (issue #161): observe a bottom sentinel row and load
  // the next folder page once it enters the viewport. The sentinel is
  // only rendered while mail.listHasMore is true, so the observer effect
  // re-runs (and re-observes) whenever that row mounts or unmounts.
  let loadMoreSentinelEl = $state<HTMLLIElement | null>(null);
  $effect(() => {
    if (!isListRoute) return;
    const el = loadMoreSentinelEl;
    if (!el) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) void mail.loadMoreFolder();
      },
      { root: null, rootMargin: '200px', threshold: 0 },
    );
    observer.observe(el);
    return () => observer.disconnect();
  });

  // Infinite scroll for search results (issue #219): same sentinel /
  // IntersectionObserver shape as the folder list above, wired to
  // mail.loadMoreSearch() / mail.searchHasMore instead of the folder
  // list's loadMoreFolder()/listHasMore.
  let loadMoreSearchSentinelEl = $state<HTMLLIElement | null>(null);
  $effect(() => {
    if (!isSearchRoute) return;
    const el = loadMoreSearchSentinelEl;
    if (!el) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0]?.isIntersecting) void mail.loadMoreSearch();
      },
      { root: null, rootMargin: '200px', threshold: 0 },
    );
    observer.observe(el);
    return () => observer.disconnect();
  });

  // Show a "sorry we're slow" hint after the list has been loading for
  // more than 1 s so the user knows something is being worked on (re #30).
  let loadingSlowly = $state(false);
  $effect(() => {
    if (mail.listLoadStatus !== 'loading') {
      untrack(() => { loadingSlowly = false; });
      return;
    }
    const timer = setTimeout(() => {
      untrack(() => { loadingSlowly = true; });
    }, 1000);
    return () => clearTimeout(timer);
  });

  // Load categorySettings when transitioning to inbox and capability available.
  $effect(() => {
    if (isInboxRoute && categorySettings.available && categorySettings.loadStatus === 'idle') {
      untrack(() => {
        void categorySettings.load();
      });
    }
  });

  function folderHref(f: FolderID): string {
    return f === 'inbox' ? '/mail' : `/mail/folder/${f}`;
  }

  async function confirmEmptyTrash(): Promise<void> {
    // Issue #179: show the true folder total (not just the loaded
    // window) so the confirmation makes the scale of a permanent,
    // whole-mailbox delete explicit before the job starts.
    const n = mail.listFolderTotal ?? mail.listEmails.length;
    const ok = await confirm.ask({
      title: t('mail.emptyTrash.title'),
      message: t(n === 1 ? 'mail.emptyTrash.messageOne' : 'mail.emptyTrash.messageMany', {
        n: String(n),
      }),
      confirmLabel: t('mail.emptyTrash.confirm'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (ok) void mail.emptyTrash();
  }

  function selectedIds(): string[] {
    return [...mail.listSelectedIds];
  }
  function bulkArchive(): void {
    void mail.bulkArchive(selectedIds());
  }
  async function confirmAndDestroy(id: string): Promise<void> {
    const ok = await confirm.ask({
      title: t('mail.deleteMsg.title'),
      message: t('mail.deleteMsg.message'),
      confirmLabel: t('mail.deleteMsg.confirm'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (ok) void mail.destroyEmail(id);
  }

  async function bulkDelete(): Promise<void> {
    const wholeMailbox = mail.listWholeMailboxSelected;
    const ids = selectedIds();
    if (!wholeMailbox && ids.length === 0) return;
    // Issue #29: moving to trash is reversible (Undo + Trash retains
    // the messages), so it should not prompt. Permanently destroying
    // from inside Trash is not reversible -- prompt for confirmation.
    if (folder === 'trash') {
      // Issue #179: in whole-mailbox mode `ids` is only the
      // loaded/visible window -- show the true folder total so the
      // confirmation makes the scale of the permanent delete explicit.
      const n = wholeMailbox && mail.listFolderTotal !== null ? mail.listFolderTotal : ids.length;
      const ok = await confirm.ask({
        title: t(n === 1 ? 'mail.deleteMsgs.titleOne' : 'mail.deleteMsgs.titleMany', {
          n: String(n),
        }),
        message: t(n === 1 ? 'mail.deleteMsgs.messageOne' : 'mail.deleteMsgs.messageMany', {
          n: String(n),
        }),
        confirmLabel: t('mail.deleteMsgs.confirm'),
        cancelLabel: t('common.cancel'),
        kind: 'danger',
      });
      if (ok) void mail.bulkDestroy(ids);
      return;
    }
    void mail.bulkDelete(ids);
  }
  function bulkMarkRead(): void {
    void mail.bulkSetSeen(selectedIds(), true);
  }
  function bulkMarkUnread(): void {
    void mail.bulkSetSeen(selectedIds(), false);
  }
  function bulkMove(): void {
    if (mail.listWholeMailboxSelected) {
      mail.wholeMailboxActionUnavailable();
      return;
    }
    const ids = selectedIds();
    if (ids.length === 0) return;
    movePicker.openBulk(ids);
  }
  function openBulkLabelPicker(): void {
    // Whole-mailbox mode routes through mail.bulkSetLabel's async
    // bulk-job path (issue #178) same as archive/delete/mark, so the
    // picker opens unconditionally here; toggle() calls bulkSetLabel
    // with the loaded/visible ids as membership-display hints only --
    // the actual patch targets the full folder server-side.
    labelPicker.openBulk(selectedIds());
  }
  function openBulkCategoryPicker(): void {
    if (mail.listWholeMailboxSelected) {
      mail.wholeMailboxActionUnavailable();
      return;
    }
    const ids = selectedIds();
    if (ids.length > 0) categoryPicker.open(ids[0]!);
  }

  function emptyMessage(f: FolderID | undefined): string {
    if (!f) return '';
    if (f === 'inbox') return t('list.empty.inbox');
    if (f === 'all') return t('list.empty.allMail');
    const folderSidebarKey = FOLDER_SIDEBAR_KEY[f];
    if (folderSidebarKey) return t('list.empty.folder', { name: t(folderSidebarKey) });
    return t('list.empty.folder', { name: mail.mailboxes.get(f)?.name ?? '' });
  }
  const FOLDER_SIDEBAR_KEY: Record<string, string> = {
    sent: 'sidebar.sent',
    drafts: 'sidebar.drafts',
    trash: 'sidebar.trash',
    important: 'sidebar.important',
    snoozed: 'sidebar.snoozed',
  };

  function senderLabel(email: Email): string {
    const a = email.from?.[0];
    if (!a) return t('msg.noSender');
    return a.name?.trim() || a.email;
  }

  /**
   * Stable list-row subject (re #246). A collapsed thread's representative
   * email is chosen server-side as the newest message in the thread
   * (`collapseByThread`, RFC 8621 §4.4.3) -- when that newest message is a
   * delivery-failure bounce/DSN, its own subject would retitle the whole
   * row. Uses the same `threadSubject()` stable-subject rule ThreadReader's
   * header and ThreadWindowView already apply to the opened thread, over
   * the same `threadEmails()` membership, so the row and the opened thread
   * always agree.
   */
  function rowSubject(email: Email): string {
    const emails = mail.threadEmails(email.threadId);
    const fallback = email.subject || '(no subject)';
    return emails.length > 0 ? threadSubject(emails, fallback) : fallback;
  }

  /**
   * Returns the deduplicated logical-message count for the thread containing
   * this email, or 0 when thread data has not been loaded yet. Uses the
   * committed snapshot length when available (after the thread has been
   * opened) so self-sent threads with Sent + Inbox copies show the logical
   * count (3) rather than the raw storage count (6) (re #88). Falls back
   * to the raw Thread.emailIds length before first open (issue #64).
   */
  function threadMessageCount(email: Email): number {
    return mail.threadDedupeCount(email.threadId);
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    const now = new Date();
    const sameYear = d.getFullYear() === now.getFullYear();
    const opts: Intl.DateTimeFormatOptions = sameYear
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
    return d.toLocaleDateString(localeTag(), opts);
  }

  function isUnread(email: Email): boolean {
    return !email.keywords.$seen;
  }

  function isFlagged(email: Email): boolean {
    return Boolean(email.keywords.$flagged);
  }

  /**
   * Returns the sorted list of custom-mailbox labels (with optional color)
   * any email in the thread belongs to, excluding the currently-viewed
   * folder and all system mailboxes.
   *
   * Uses the union of mailboxIds across all thread emails (not just the
   * thread-representative returned by the collapsed JMAP query), so a
   * label attached to any message in a multi-message thread is visible in
   * the list row immediately after attachment (re #70). Falls back to the
   * representative email alone when thread email data has not been loaded
   * yet. Each label carries its user-defined color (re #69) for badge
   * rendering.
   */
  function emailLabels(email: Email): { name: string; color: string | null | undefined }[] {
    // Collect mailboxIds union across the entire thread when available.
    const seen = new Set<string>(Object.keys(email.mailboxIds));
    const thread = mail.threads?.get(email.threadId);
    if (thread) {
      for (const id of thread.emailIds) {
        const e = mail.emails.get(id);
        if (e) for (const mbxId of Object.keys(e.mailboxIds)) seen.add(mbxId);
      }
    }
    const labels: { name: string; color: string | null | undefined }[] = [];
    for (const m of mail.customMailboxes) {
      if (!seen.has(m.id)) continue;
      // Skip the currently-viewed folder so the badge is not redundant.
      if (m.id === folder) continue;
      labels.push({ name: m.name, color: m.color });
    }
    return labels.sort((a, b) => a.name.localeCompare(b.name));
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

  /**
   * Thread row drag start (REQ-UI-17). When the dragged row is part of
   * an active multi-selection, drag every selected row together;
   * otherwise drag just this one. Multi-row drags swap the default
   * row-shaped drag image for a compact "N messages" badge so the
   * cursor doesn't trail the original row's visual.
   */
  function onRowDragStart(e: DragEvent, email: Email): void {
    const ids = dragIdsForRow(email.id);
    threadDnd.begin(ids);
    if (!e.dataTransfer) return;
    e.dataTransfer.effectAllowed = 'move';
    const label =
      ids.length > 1
        ? t('list.dragMessageCount', { count: ids.length })
        : email.subject || '(no subject)';
    e.dataTransfer.setData('text/plain', label);
    if (ids.length > 1) {
      const badge = buildMultiDragBadge(ids.length);
      e.dataTransfer.setDragImage(badge, 12, 12);
      // The element must outlive the dragstart handler for the browser
      // to snapshot it; remove on the next tick.
      setTimeout(() => badge.remove(), 0);
    }
  }

  /**
   * Build the floating "N messages" pill used as the drag image when
   * the user drags a multi-selection. The element is appended to the
   * document so the browser can snapshot it; the caller is responsible
   * for removing it once the drag has started.
   */
  function buildMultiDragBadge(count: number): HTMLElement {
    const badge = document.createElement('div');
    badge.className = 'drag-multi-badge';
    badge.textContent = t('list.dragMessageCount', { count });
    // Inline styles so the badge renders correctly even though it's
    // mounted to <body> outside the component's scoped CSS. We pin
    // it off-screen until the browser takes its snapshot.
    Object.assign(badge.style, {
      position: 'fixed',
      top: '-1000px',
      left: '-1000px',
      padding: '8px 14px',
      borderRadius: '999px',
      background: 'var(--interactive)',
      color: 'var(--text-on-color)',
      fontWeight: '600',
      fontSize: '14px',
      boxShadow: '0 4px 12px rgba(0, 0, 0, 0.25)',
      pointerEvents: 'none',
      whiteSpace: 'nowrap',
      zIndex: '9999',
    });
    document.body.appendChild(badge);
    return badge;
  }
</script>

<div class="mail">
  {#if threadId}
    <div class="thread-frame">
      <ThreadReader {threadId} />
    </div>
  {:else if isSearchRoute}
    <header class="list-header">
      <h1>
        {t('mail.search.heading')}
        <span class="query-echo">{searchQuery || t('mail.search.empty')}</span>
      </h1>
      <button type="button" class="back" onclick={() => router.navigate('/mail')}>
        {t('mail.search.backToInbox')}
      </button>
    </header>

    {#if searchQuery}
      <div class="search-chips" aria-label={t('mail.search.recognisedQueryAria')}>
        {#each decodeChips(searchQuery) as chip, i (i + chip.raw)}
          <span class="chip" class:text={chip.operator === 'text'}>
            {#if chip.operator !== 'text'}<span class="op">{chip.operator}</span>{/if}
            <span class="value">{chip.value || chip.label}</span>
          </span>
        {/each}
      </div>
    {/if}

    {#if mail.searchHistory.length > 0}
      <div class="search-history" aria-label={t('mail.search.recentSearchesAria')}>
        <span class="history-label">{t('search.history.label')}</span>
        {#each mail.searchHistory.slice(0, 6) as q (q)}
          <button
            type="button"
            class="history-chip"
            onclick={() =>
              router.navigate(`/mail/search/${encodeURIComponent(q)}`)}
            title={t('search.history.rerun')}
          >
            {q}
          </button>
        {/each}
        <button
          type="button"
          class="history-clear"
          onclick={() => mail.clearSearchHistory()}
          aria-label={t('search.history.clearAria')}
          title={t('search.history.clearTitle')}
        >
          {t('search.history.clear')}
        </button>
      </div>
    {/if}

    {#if mail.searchLoadStatus === 'idle' || mail.searchLoadStatus === 'loading'}
      <div class="state">{t('search.searching')}</div>
    {:else if mail.searchLoadStatus === 'error'}
      <div class="state error">
        <p>{t('search.failed')}</p>
        {#if mail.searchError}<p class="detail">{mail.searchError}</p>{/if}
        <button type="button" onclick={() => mail.runSearch(mail.searchQuery)}>
          {t('list.retry')}
        </button>
      </div>
    {:else if mail.searchEmails.length === 0}
      <div class="state">{t('mail.search.noMatches')}</div>
    {:else}
      <!-- Search results share the folder list's toolbar, row template
           (checkbox, drag handle, draggable), and bulk-selection state
           (mail.listSelectedIds) so the two views behave identically
           (re #159). "Empty trash" is a folder-only concept and is not
           shown here; the whole-result-set selection banner below (issue
           #207) is search's counterpart to the folder list's
           whole-mailbox banner. -->
      <div class="list-toolbar" role="toolbar" aria-label={t('mail.list.actionsAria')}>
        <SelectChooser visibleEmails={mail.searchEmails} visibleIds={renderedSearchEmailIds} />
        {#if mail.listSelectedIds.size > 0}
          <span class="bulk-count">
            {t('bulk.selected', {
              count: mail.listWholeMailboxSelected && mail.searchTotal !== null
                ? mail.searchTotal
                : mail.listSelectedIds.size,
            })}
          </span>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.archive')}
            title={t('bulk.archive')}
            onclick={bulkArchive}
          ><ArchiveIcon size={18} /></button>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.markRead')}
            title={t('bulk.markRead')}
            onclick={bulkMarkRead}
          ><MarkReadIcon size={18} /></button>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.markUnread')}
            title={t('bulk.markUnread')}
            onclick={bulkMarkUnread}
          ><MarkUnreadIcon size={18} /></button>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.move')}
            title={t('bulk.move')}
            onclick={bulkMove}
          ><MoveIcon size={18} /></button>
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.label')}
            title={t('bulk.label')}
            onclick={openBulkLabelPicker}
          ><LabelIcon size={18} /></button>
          {#if categorySettings.available}
            <button
              type="button"
              class="icon-btn"
              aria-label={t('bulk.category')}
              title={t('bulk.category')}
              onclick={openBulkCategoryPicker}
            ><CategoryIcon size={18} /></button>
          {/if}
          <button
            type="button"
            class="icon-btn danger"
            aria-label={t('bulk.delete')}
            title={t('bulk.delete')}
            onclick={bulkDelete}
          ><TrashIcon size={18} /></button>
        {/if}
        <span class="list-toolbar-spacer"></span>
      </div>

      <!-- Whole-search-result-set selection banner (issue #207): the
           search-view counterpart to the folder list's whole-mailbox
           banner (issue #149). `Email/query`'s `calculateTotal: true`
           gives `mail.searchTotal`, the true match count, independent of
           the 50-row loaded window -- so "select all" can offer (and then
           act on) every match, not just what is currently loaded. Offer
           state: all loaded results are selected and more matches exist.
           Active state: whole-result-set mode is engaged; bulk actions
           route through Email/setByQuery scoped to the search's own
           filter (`#wholeSelectionFilterOverride` in the store). -->
      {#if mail.listSelectedIds.size > 0 && mail.searchTotal !== null && mail.searchEmails.length > 0}
        {@const searchTotal = mail.searchTotal}
        {#if !mail.listWholeMailboxSelected && shouldOfferWholeSet(renderedSearchEmailIds, mail.listSelectedIds, searchTotal)}
          <div class="whole-mailbox-banner" role="status" aria-live="polite">
            <span class="banner-text">
              {t('select.allPageSelected', { count: String(mail.searchEmails.length) })}
            </span>
            <button
              type="button"
              class="banner-btn"
              onclick={() => mail.selectWholeSearchResults()}
            >
              {t('select.selectAllInSearch', { total: String(searchTotal) })}
            </button>
          </div>
        {:else if mail.listWholeMailboxSelected}
          <div class="whole-mailbox-banner whole-mailbox-banner--active" role="status" aria-live="polite">
            <span class="banner-text">
              {t('select.wholeSearchActive', { total: String(searchTotal) })}
            </span>
            <button
              type="button"
              class="banner-btn banner-btn--secondary"
              onclick={() => mail.selectAllVisible(renderedSearchEmailIds)}
            >
              {t('select.clearWholeMailbox')}
            </button>
          </div>
        {/if}
      {/if}

      <ul class="thread-list" role="listbox" aria-label={t('mail.search.resultsAria')}>
        {#each mail.searchEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.searchFocusedIndex === i}
            class:selected={mail.listSelectedIds.has(email.id)}
            class:dragging={threadDnd.current?.ids.includes(email.id) ?? false}
            data-row-index={i}
            draggable="true"
            ondragstart={(e) => onRowDragStart(e, email)}
            ondragend={() => threadDnd.end()}
          >
            <span class="drag-handle" aria-hidden="true">
              <svg viewBox="0 0 8 14" width="8" height="14" fill="currentColor">
                <circle cx="2" cy="2" r="1" />
                <circle cx="6" cy="2" r="1" />
                <circle cx="2" cy="7" r="1" />
                <circle cx="6" cy="7" r="1" />
                <circle cx="2" cy="12" r="1" />
                <circle cx="6" cy="12" r="1" />
              </svg>
            </span>
            <input
              type="checkbox"
              class="row-check"
              aria-label={t('mail.row.selectAria')}
              checked={mail.listSelectedIds.has(email.id)}
              onclick={(e) => {
                handleRowCheckboxClick(
                  e,
                  () => mail.selectRowClick(email.id, e.shiftKey, renderedSearchEmailIds),
                  () => mail.listSelectedIds.has(email.id),
                );
              }}
            />
            <button
              type="button"
              class="row-star"
              class:flagged={isFlagged(email)}
              aria-label={isFlagged(email) ? t('mail.row.unstarAria') : t('mail.row.starAria')}
              aria-pressed={isFlagged(email)}
              onclick={(e) => {
                e.stopPropagation();
                void mail.toggleFlagged(email.id);
              }}
            >
              <span aria-hidden="true">★</span>
            </button>
            <button
              type="button"
              class="row-activate"
              role="option"
              aria-selected={mail.searchFocusedIndex === i}
              onclick={() => {
                mail.searchFocusedIndex = i;
                openThread(email);
              }}
            >
              <span class="from">
                <span class="from-text">{senderLabel(email)}</span>
                {#if threadMessageCount(email) > 1}
                  <span
                    class="thread-count"
                    aria-label={t('mail.row.threadCountAria.other', {
                      count: threadMessageCount(email),
                    })}
                  >{threadMessageCount(email)}</span>
                {/if}
              </span>
              <span class="subject-and-preview">
                {#each emailLabels(email) as lbl (lbl.name)}
                  <span
                    class="label-badge"
                    style={lbl.color
                      ? `background:${lbl.color};color:${labelForeground(lbl.color)};`
                      : undefined}
                  >{lbl.name}</span>
                {/each}
                {#if email.internalizePending}
                  <span
                    class="internalize-pending-badge"
                    title={t('mail.list.internalizePending.tooltip')}
                    aria-label={t('mail.list.internalizePending.tooltip')}
                  ><ImageIcon size={14} /></span>
                {/if}
                <span class="subject">{rowSubject(email)}</span>
                <span class="preview"> — {email.preview}</span>
              </span>
              <span class="attachment" aria-hidden={!email.hasAttachment}>
                {#if email.hasAttachment}<span aria-label={t('att.headerIcon.label')}>📎</span>{/if}
              </span>
              <span class="date">{formatDate(email.receivedAt)}</span>
            </button>
          </li>
        {/each}
        {#if mail.searchHasMore}
          <li class="load-more-sentinel" role="presentation" bind:this={loadMoreSearchSentinelEl}>
            {#if mail.searchLoadingMore}
              <span class="load-more-indicator" role="status" aria-live="polite">
                {t('list.loadingMore')}
              </span>
            {/if}
          </li>
        {/if}
      </ul>
    {/if}
  {:else if label}
    <header>
      <h1>{t('mail.label.heading', { name: label })}</h1>
      <p class="lead">{t('mail.label.lead')}</p>
    </header>
  {:else if isListRoute}
    <!-- Issue #25 / #27: the mailbox name shows in the sidebar; we
         dropped the redundant <h1> header and merged its remaining
         controls (Empty trash, Refresh) into the always-visible
         list-toolbar so the toolbar height is constant whether or not
         a selection is active. -->
    <div class="list-toolbar" role="toolbar" aria-label={t('mail.list.actionsAria')}>
      {#if effectiveListEmails.length > 0}
        <!-- Explicit visibleEmails/visibleIds (re #202 follow-up): SelectChooser's
             own defaults fall back to mail.listEmails, which is tab-unaware. When
             a category tab (REQ-CAT-10..14) is active, effectiveListEmails is the
             tab-filtered set that actually renders; without this, "select all"
             from the chooser would select ids for rows other tabs render, not
             this one -- the toolbar's selection count would exceed the checked
             rows on screen, reachable through an ordinary "select all" click. -->
        <SelectChooser visibleEmails={effectiveListEmails} visibleIds={effectiveListEmailIds} />
      {/if}
      {#if mail.listSelectedIds.size > 0}
        <span class="bulk-count">
          {t('bulk.selected', {
            count: mail.listWholeMailboxSelected && mail.listFolderTotal !== null
              ? mail.listFolderTotal
              : mail.listSelectedIds.size,
          })}
        </span>
        {#if folder === 'inbox'}
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.archive')}
            title={t('bulk.archive')}
            onclick={bulkArchive}
          ><ArchiveIcon size={18} /></button>
        {/if}
        <button
          type="button"
          class="icon-btn"
          aria-label={t('bulk.markRead')}
          title={t('bulk.markRead')}
          onclick={bulkMarkRead}
        ><MarkReadIcon size={18} /></button>
        <button
          type="button"
          class="icon-btn"
          aria-label={t('bulk.markUnread')}
          title={t('bulk.markUnread')}
          onclick={bulkMarkUnread}
        ><MarkUnreadIcon size={18} /></button>
        <button
          type="button"
          class="icon-btn"
          aria-label={t('bulk.move')}
          title={t('bulk.move')}
          onclick={bulkMove}
        ><MoveIcon size={18} /></button>
        <button
          type="button"
          class="icon-btn"
          aria-label={t('bulk.label')}
          title={t('bulk.label')}
          onclick={openBulkLabelPicker}
        ><LabelIcon size={18} /></button>
        {#if categorySettings.available}
          <button
            type="button"
            class="icon-btn"
            aria-label={t('bulk.category')}
            title={t('bulk.category')}
            onclick={openBulkCategoryPicker}
          ><CategoryIcon size={18} /></button>
        {/if}
        <button
          type="button"
          class="icon-btn danger"
          aria-label={t('bulk.delete')}
          title={t('bulk.delete')}
          onclick={bulkDelete}
        ><TrashIcon size={18} /></button>
      {/if}
      <span class="list-toolbar-spacer"></span>
      {#if folder === 'trash' && mail.listEmails.length > 0}
        <button
          type="button"
          class="danger"
          onclick={confirmEmptyTrash}
          disabled={mail.listLoadStatus === 'loading'}
        >
          {t('list.emptyTrash')}
        </button>
      {/if}
      <button
        type="button"
        class="refresh"
        aria-label={t('list.refresh')}
        onclick={() => mail.refreshFolder()}
        disabled={mail.listLoadStatus === 'loading'}
      >
        ↻
      </button>
    </div>

    <!-- Whole-mailbox selection banner (issue #149). Shown below the toolbar
         as a dedicated row so it does not compete for space with the bulk
         action buttons. Offer state: appears when all currently-loaded rows
         are selected and the folder contains more messages than are loaded
         (the loaded count grows via infinite scroll, issue #161, so this is
         not tied to the 50-row initial page). Active state: appears while
         whole-mailbox mode is engaged. Selecting is free (no query, no
         size cap); executing a bulk action while active is a separate
         question -- see wholeMailboxActionUnavailable in store.svelte.ts. -->
    {#if mail.listSelectedIds.size > 0 && mail.listFolderTotal !== null && mail.listEmails.length > 0}
      {@const folderTotal = mail.listFolderTotal}
      {@const folderVisibleIds = mail.listEmails.map((e) => e.id)}
      {#if !mail.listWholeMailboxSelected && shouldOfferWholeSet(folderVisibleIds, mail.listSelectedIds, folderTotal)}
        <div class="whole-mailbox-banner" role="status" aria-live="polite">
          <span class="banner-text">
            {t('select.allPageSelected', { count: String(mail.listEmails.length) })}
          </span>
          <button
            type="button"
            class="banner-btn"
            onclick={() => mail.selectWholeMailbox()}
          >
            {t('select.selectAllInFolder', { total: String(folderTotal) })}
          </button>
        </div>
      {:else if mail.listWholeMailboxSelected}
        <div class="whole-mailbox-banner whole-mailbox-banner--active" role="status" aria-live="polite">
          <span class="banner-text">
            {t('select.wholeMailboxActive', { total: String(folderTotal) })}
          </span>
          <button
            type="button"
            class="banner-btn banner-btn--secondary"
            onclick={() => mail.selectAllVisible(effectiveListEmailIds)}
          >
            {t('select.clearWholeMailbox')}
          </button>
        </div>
      {/if}
    {/if}

    <!-- Whole-mailbox bulk-job progress/completion banner (issue #149).
         Independent of the selection banner above: starting a job clears
         the selection (the job runs server-side regardless of what is
         still loaded), so this renders from `mail.bulkJob` alone. Stays
         visible through the terminal state so the user sees the outcome;
         dismissed explicitly rather than auto-hiding. -->
    {#if mail.bulkJob}
      {@const job = mail.bulkJob}
      <div
        class="whole-mailbox-banner whole-mailbox-banner--active"
        class:whole-mailbox-banner--error={job.status === 'failed' || job.status === 'partial'}
        role="status"
        aria-live="polite"
      >
        <span class="banner-text">
          {#if job.status === 'running'}
            {job.total < 0
              ? t('bulkJob.preparing')
              : t('bulkJob.progress', { processed: job.processed, total: job.total })}
          {:else if job.status === 'done'}
            {t('bulkJob.done', { processed: job.processed })}
          {:else if job.status === 'partial'}
            {t('bulkJob.partial', {
              processed: job.processed - job.failedIds.length,
              failed: job.failedIds.length,
            })}
          {:else}
            {t('bulkJob.failed')}
          {/if}
        </span>
        {#if job.status !== 'running'}
          <button
            type="button"
            class="banner-btn banner-btn--secondary"
            onclick={() => mail.dismissBulkJob()}
          >
            {t('bulkJob.dismiss')}
          </button>
        {/if}
      </div>
    {/if}

    {#if showTabs}
      <nav class="tab-strip" aria-label={t('mail.list.tabsAria')}>
        {#each categorySettings.derivedCategories as name (name)}
          {@const tabKey = name.toLowerCase() === 'primary' ? null : name}
          {@const isActive = activeTabName === tabKey}
          {@const unread = tabUnreadCount(tabKey)}
          <button
            type="button"
            class="tab"
            class:tab-active={isActive}
            aria-current={isActive ? 'page' : undefined}
            onclick={() => selectTab(tabKey)}
          >
            {name}
            {#if unread > 0}
              <span class="tab-badge" aria-label={t('mail.list.tabUnreadAria', { count: unread })}>{unread}</span>
            {/if}
          </button>
        {/each}
      </nav>
    {/if}

    {#if mail.listLoadStatus === 'idle' || mail.listLoadStatus === 'loading'}
      <div class="state">
        {t('list.loading')}
        {#if loadingSlowly}
          <p class="slow-hint">{t('list.loadSlow')}</p>
        {/if}
      </div>
    {:else if mail.listLoadStatus === 'error'}
      <div class="state error">
        <p>{t('list.couldNotLoad', { name: folderLabel.toLowerCase() })}</p>
        <p class="detail">{t('list.couldNotLoad.systemHint')}</p>
        {#if mail.listError}<p class="detail">{mail.listError}</p>{/if}
        <button type="button" onclick={() => mail.refreshFolder()}>{t('list.retry')}</button>
      </div>
    {:else if effectiveListEmails.length === 0}
      <div class="state">
        {#if showTabs && mail.listEmails.length > 0}
          {t('mail.list.emptyTab', { name: activeTabName ?? t('mail.list.tabPrimary') })}
        {:else}
          {emptyMessage(folder)}
        {/if}
      </div>
    {:else}
      <ul
        class="thread-list"
        role="listbox"
        aria-label={t('mail.list.threadsAria', { name: folderLabel })}
        aria-multiselectable="true"
        bind:this={listEl}
      >
        {#each effectiveListEmails as email, i (email.id)}
          <li
            class="thread-row"
            class:unread={isUnread(email)}
            class:focused={mail.listFocusedIndex === i}
            class:selected={mail.listSelectedIds.has(email.id)}
            class:dragging={threadDnd.current?.ids.includes(email.id) ?? false}
            data-row-index={i}
            draggable="true"
            ondragstart={(e) => onRowDragStart(e, email)}
            ondragend={() => threadDnd.end()}
          >
            <span class="drag-handle" aria-hidden="true">
              <svg viewBox="0 0 8 14" width="8" height="14" fill="currentColor">
                <circle cx="2" cy="2" r="1" />
                <circle cx="6" cy="2" r="1" />
                <circle cx="2" cy="7" r="1" />
                <circle cx="6" cy="7" r="1" />
                <circle cx="2" cy="12" r="1" />
                <circle cx="6" cy="12" r="1" />
              </svg>
            </span>
            <input
              type="checkbox"
              class="row-check"
              aria-label={t('mail.row.selectAria')}
              checked={mail.listSelectedIds.has(email.id)}
              onclick={(e) => {
                handleRowCheckboxClick(
                  e,
                  () => mail.selectRowClick(email.id, e.shiftKey, effectiveListEmailIds),
                  () => mail.listSelectedIds.has(email.id),
                );
              }}
            />
            <button
              type="button"
              class="row-star"
              class:flagged={isFlagged(email)}
              aria-label={isFlagged(email) ? t('mail.row.unstarAria') : t('mail.row.starAria')}
              aria-pressed={isFlagged(email)}
              onclick={(e) => {
                e.stopPropagation();
                void mail.toggleFlagged(email.id);
              }}
            >
              <span aria-hidden="true">★</span>
            </button>
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
              <span class="from">
                <span class="from-text">{senderLabel(email)}</span>
                {#if threadMessageCount(email) > 1}
                  <span
                    class="thread-count"
                    aria-label={t('mail.row.threadCountAria.other', {
                      count: threadMessageCount(email),
                    })}
                  >{threadMessageCount(email)}</span>
                {/if}
              </span>
              <span class="subject-and-preview">
                {#each emailLabels(email) as lbl (lbl.name)}
                  <span
                    class="label-badge"
                    style={lbl.color
                      ? `background:${lbl.color};color:${labelForeground(lbl.color)};`
                      : undefined}
                  >{lbl.name}</span>
                {/each}
                {#if email.internalizePending}
                  <span
                    class="internalize-pending-badge"
                    title={t('mail.list.internalizePending.tooltip')}
                    aria-label={t('mail.list.internalizePending.tooltip')}
                  ><ImageIcon size={14} /></span>
                {/if}
                <span class="subject">{rowSubject(email)}</span>
                <span class="preview"> — {email.preview}</span>
              </span>
              <span class="attachment" aria-hidden={!email.hasAttachment}>
                {#if email.hasAttachment}<span aria-label={t('att.headerIcon.label')}>📎</span>{/if}
              </span>
              <span class="date">{formatDate(email.receivedAt)}</span>
            </button>
          </li>
        {/each}
        {#if mail.listHasMore}
          <li class="load-more-sentinel" role="presentation" bind:this={loadMoreSentinelEl}>
            {#if mail.listLoadingMore}
              <span class="load-more-indicator" role="status" aria-live="polite">
                {t('list.loadingMore')}
              </span>
            {/if}
          </li>
        {/if}
      </ul>
    {/if}
  {:else}
    <header>
      <h1>{t('mail.notFound.title')}</h1>
      <p class="lead">{t('mail.notFound.lead')}</p>
    </header>
  {/if}
</div>

<CategoryPicker />

<style>
  .mail {
    height: 100%;
    overflow: auto;
    background: var(--background);
  }

  /* ── Category tab strip (Inbox only) ─────────────────────────────────── */
  .tab-strip {
    display: flex;
    overflow-x: auto;
    border-bottom: 2px solid var(--border-subtle-01);
    background: var(--layer-01);
    padding: 0 var(--spacing-05);
    gap: 0;
    flex-shrink: 0;
    scrollbar-width: none;
  }
  .tab-strip::-webkit-scrollbar {
    display: none;
  }
  .tab {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    padding: var(--spacing-03) var(--spacing-04);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
    border-bottom: 2px solid transparent;
    margin-bottom: -2px;
    min-height: var(--touch-min);
    transition:
      color var(--duration-fast-02) var(--easing-productive-enter),
      border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .tab:hover {
    color: var(--text-primary);
    background: var(--layer-02);
  }
  .tab-active {
    color: var(--text-primary);
    font-weight: 600;
    border-bottom-color: var(--interactive);
  }
  .tab-badge {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 18px;
    height: 18px;
    padding: 0 var(--spacing-02);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-size: 10px;
    font-weight: 700;
    line-height: 1;
  }
  .tab:not(.tab-active) .tab-badge {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .thread-frame {
    display: flex;
    flex-direction: column;
    height: 100%;
    /* min-width:0 lets the frame shrink below the intrinsic width of
       a wide email body; the thread reader contains its own overflow
       so wide content scrolls within the content pane instead of
       widening the message-list column / app shell (re wide-content
       layout bug). */
    min-width: 0;
  }
  .thread-frame :global(.thread-reader) {
    flex: 1;
    min-height: 0;
    min-width: 0;
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
  .state .slow-hint {
    margin-top: var(--spacing-04);
    font-size: var(--type-body-compact-01-size);
    color: var(--support-warning);
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
  .load-more-sentinel {
    display: flex;
    justify-content: center;
    align-items: center;
    min-height: var(--spacing-08);
    padding: var(--spacing-04) 0;
  }
  .load-more-indicator {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }
  .thread-row {
    display: grid;
    grid-template-columns: var(--drag-handle-col, 14px) auto auto 1fr;
    align-items: center;
    border-bottom: 1px solid var(--border-subtle-01);
    border-left: 3px solid transparent;
    transition: border-color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .drag-handle {
    color: var(--text-helper);
    opacity: 0;
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
    cursor: grab;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    pointer-events: none;
  }
  .thread-row:hover .drag-handle,
  .thread-row.focused .drag-handle {
    opacity: 1;
  }
  .thread-row.dragging .drag-handle {
    cursor: grabbing;
  }
  /* Handle is desktop-only — coarse pointers don't surface drag hints. */
  @media (pointer: coarse) {
    .drag-handle {
      display: none;
    }
    .thread-row {
      grid-template-columns: auto auto 1fr;
    }
  }
  .thread-row.focused {
    border-left-color: var(--interactive);
    background: var(--layer-01);
  }
  .thread-row.selected {
    background: var(--layer-02);
  }
  .thread-row.dragging {
    opacity: 0.45;
  }
  /* dnd is desktop-only (REQ-UI-17, REQ-MOB-37): suppress draggable on
     coarse-pointer breakpoints so the row remains a normal pressable
     target on touch. */
  @media (pointer: coarse) {
    .thread-row[draggable='true'] {
      -webkit-user-drag: none;
    }
  }
  .row-check {
    margin: 0 var(--spacing-03) 0 var(--spacing-04);
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
  }
  /* Star is a real button so the click toggles $flagged instead of
     bubbling up to row-activate (issue #26). */
  .row-star {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    margin: 0 var(--spacing-02) 0 0;
    color: var(--text-helper);
    font-size: 16px;
    line-height: 1;
    border-radius: var(--radius-pill);
    background: transparent;
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .row-star.flagged {
    color: var(--support-warning);
  }
  /* Hover gives a background highlight only; the foreground colour
     stays bound to the actual flagged state. Previously hover also
     forced the warning colour, so an unflagged star looked active
     under the cursor and a freshly-unflagged star kept its old
     yellow until the cursor moved away — both reported as
     confusing. */
  .row-star:hover {
    background: var(--layer-03);
  }
  .row-activate {
    display: grid;
    grid-template-columns: 14ch 1fr auto auto;
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

  /* The list-toolbar height is fixed regardless of whether bulk-action
     buttons are visible (issue #27). The bulk buttons and the trailing
     refresh / empty-trash share the same min-height so toggling the
     selection set does not jiggle the layout. */
  .list-toolbar {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    background: var(--layer-02);
    min-height: 48px;
  }
  .list-toolbar-spacer {
    flex: 1;
  }

  /* Whole-mailbox selection banner (issue #149) */
  .whole-mailbox-banner {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-05);
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }
  .whole-mailbox-banner--active {
    background: var(--layer-03);
  }
  .whole-mailbox-banner--error {
    background: var(--layer-01);
    border-bottom-color: var(--support-error);
  }
  .banner-text {
    flex: 1;
  }
  .banner-btn {
    white-space: nowrap;
    color: var(--interactive);
    background: transparent;
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    padding: var(--spacing-01) var(--spacing-02);
    border-radius: var(--radius-sm);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .banner-btn:hover {
    background: var(--layer-02);
    color: var(--interactive-hover);
    text-decoration: underline;
  }
  .banner-btn--secondary {
    color: var(--text-secondary);
    font-weight: 400;
  }
  .banner-btn--secondary:hover {
    color: var(--text-primary);
  }
  .bulk-count {
    color: var(--text-primary);
    font-weight: 600;
    margin-right: var(--spacing-02);
  }
  .list-toolbar > button {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-01);
    color: var(--text-primary);
    font-weight: 500;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .list-toolbar > button:hover {
    background: var(--layer-03);
  }
  .list-toolbar > button.danger {
    color: var(--support-error);
  }
  .list-toolbar > button.danger:hover {
    background: var(--support-error);
    color: var(--text-on-color);
  }
  /* Bulk-action icon buttons — square, touch-friendly, icon-only. */
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--touch-min, 44px);
    height: var(--touch-min, 44px);
    padding: 0;
    border-radius: var(--radius-md);
    background: transparent;
    color: var(--text-secondary);
    transition:
      background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .icon-btn:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .icon-btn.danger {
    color: var(--support-error);
    background: transparent;
    font-weight: normal;
  }
  .icon-btn.danger:hover {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  .from {
    overflow: hidden;
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
  }
  /* Sender text inside .from truncates with ellipsis so the thread-count
     badge always has room to show. min-width: 0 lets the flex item shrink
     below its natural text width (overriding the default min-width: auto
     that would otherwise push the badge out of the visible area — issue #64). */
  .from-text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1 1 0;
  }
  .thread-row.unread .from {
    font-weight: 600;
  }

  /* Thread message count badge — shown only for multi-message threads (issue #64). */
  .thread-count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: 16px;
    height: 16px;
    padding: 0 var(--spacing-02);
    background: var(--layer-03);
    color: var(--text-secondary);
    border-radius: var(--radius-pill);
    font-size: 10px;
    font-weight: 600;
    line-height: 1;
    flex-shrink: 0;
  }
  .thread-row.unread .thread-count {
    background: var(--layer-02);
    color: var(--text-primary);
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

  /* Label badges: small pill shown before the subject for each custom
     mailbox the email belongs to (re #56). System mailboxes are excluded
     by emailLabels(); the currently-viewed folder is also skipped so the
     badge is not shown when browsing that folder directly. */
  .label-badge {
    display: inline-flex;
    align-items: center;
    padding: 1px var(--spacing-02);
    margin-right: var(--spacing-02);
    background: var(--layer-03);
    color: var(--text-secondary);
    border-radius: var(--radius-sm);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    white-space: nowrap;
    vertical-align: middle;
  }

  /* External-image internalize-pending badge (REQ-EXTIMG-BG-30): small
     icon shown next to the label badges when the body still carries
     placeholder data URIs in place of the message's external images. The
     icon disappears as soon as the background worker rewrites the body
     and the push handler refreshes the row (REQ-EXTIMG-BG-22 / 33). */
  .internalize-pending-badge {
    display: inline-flex;
    align-items: center;
    margin-right: var(--spacing-02);
    color: var(--text-helper);
    vertical-align: middle;
  }

  .attachment {
    color: var(--text-helper);
  }
  .date {
    color: var(--text-helper);
    font-variant-numeric: tabular-nums;
    font-size: var(--type-body-compact-01-size);
    white-space: nowrap;
    text-align: right;
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
      grid-template-columns: 8ch 1fr auto;
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
    .row-star {
      width: 24px;
      height: 24px;
    }
    .list-toolbar {
      /* Don't wrap on narrow screens -- the constant height is more
         important than fitting every bulk button (the menu can scroll
         horizontally if needed). */
      padding: var(--spacing-02) var(--spacing-03);
    }
    .search-history,
    .search-chips {
      padding: var(--spacing-02) var(--spacing-04);
    }
  }
</style>
