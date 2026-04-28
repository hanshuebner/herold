/**
 * Mail cache + actions.
 *
 * Holds normalised views of `Mailbox` and `Email` objects, plus the ordered
 * email-id list that backs the inbox view. Per docs/architecture/
 * 01-system-overview.md § Layers, this is the single source of truth that
 * mail views render from.
 *
 * Phase 1: minimal — load mailboxes, load inbox emails, expose a derived
 * inboxEmails list. Pagination, search, and label views build on top.
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { toast } from '../toast/toast.svelte';
import { Capability, type Invocation } from '../jmap/types';
import {
  EMAIL_BODY_PROPERTIES,
  EMAIL_LIST_PROPERTIES,
  type Email,
  type Identity,
  type Mailbox,
  type Thread,
} from './types';
import { parseQuery } from './search-query';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/**
 * Identifier for the folder rendered by the generic list view. "inbox",
 * "sent", "drafts", "trash" map to the matching mailbox role; "all"
 * spans every folder visible to this account.
 */
export type FolderID = 'inbox' | 'sent' | 'drafts' | 'trash' | 'all';

const FOLDER_ROLE: Record<Exclude<FolderID, 'all'>, string> = {
  inbox: 'inbox',
  sent: 'sent',
  drafts: 'drafts',
  trash: 'trash',
};

const FOLDER_LABEL: Record<FolderID, string> = {
  inbox: 'Inbox',
  sent: 'Sent',
  drafts: 'Drafts',
  trash: 'Trash',
  all: 'All Mail',
};

class MailStore {
  mailboxes = $state(new Map<string, Mailbox>());
  emails = $state(new Map<string, Email>());
  threads = $state(new Map<string, Thread>());
  identities = $state(new Map<string, Identity>());

  /** Which folder the generic list slice currently holds. */
  listFolder = $state<FolderID>('inbox');
  /** Ordered (most-recent first) email ids visible in the current list view. */
  listEmailIds = $state<string[]>([]);
  listLoadStatus = $state<LoadStatus>('idle');
  listError = $state<string | null>(null);
  /** Index into listEmailIds of the keyboard-focused row; -1 = none. */
  listFocusedIndex = $state<number>(-1);
  /** Bulk-selected email ids in the current list view. Cleared on folder switch. */
  listSelectedIds = $state(new Set<string>());

  /** Per-thread load status keyed by threadId. */
  threadLoadStatus = $state(new Map<string, LoadStatus>());
  threadLoadError = $state(new Map<string, string>());

  /** Most recent search query string (raw, user-typed). */
  searchQuery = $state('');
  searchEmailIds = $state<string[]>([]);
  searchLoadStatus = $state<LoadStatus>('idle');
  searchError = $state<string | null>(null);
  searchFocusedIndex = $state<number>(-1);

  /**
   * Most recent state strings per JMAP type. Updated from `Foo/get`
   * responses and from sync handlers. Used to dedupe redundant refreshes.
   */
  emailState = $state<string | null>(null);
  mailboxState = $state<string | null>(null);

  constructor() {
    // Register sync handlers at module init so we don't miss events that
    // arrive between app mount and the first store call.
    sync.on('Email', (newState) => {
      void this.#onEmailStateChange(newState);
    });
    sync.on('Mailbox', (newState) => {
      void this.#onMailboxStateChange(newState);
    });
  }

  async #onEmailStateChange(newState: string): Promise<void> {
    if (newState === this.emailState) return;
    // First-iteration sync: the cheapest correct thing is to refresh
    // every view we already have ready — the active list slice plus
    // every cached-ready thread. The thread refresh is what surfaces
    // a freshly-arrived reply in the open ThreadReader without the
    // user reloading the page; without it the thread cache stays
    // 'ready' and loadThread short-circuits.
    const tasks: Promise<unknown>[] = [];
    if (this.listLoadStatus === 'ready') {
      tasks.push(
        this.refreshFolder().catch((err) => {
          console.error('list refresh after state change failed', err);
        }),
      );
    }
    for (const [tid, status] of this.threadLoadStatus) {
      if (status !== 'ready') continue;
      tasks.push(
        this.refreshThread(tid).catch((err) => {
          console.error('thread refresh after state change failed', err);
        }),
      );
    }
    if (tasks.length > 0) await Promise.all(tasks);
    this.emailState = newState;
  }

  async #onMailboxStateChange(newState: string): Promise<void> {
    if (newState === this.mailboxState) return;
    try {
      await this.loadMailboxes();
    } catch (err) {
      console.error('mailbox reload after state change failed', err);
    }
    this.mailboxState = newState;
  }

  /** Move focus to the next row, clamped. Returns the new focused id, if any. */
  focusListNext(): string | null {
    if (this.listEmailIds.length === 0) return null;
    const next =
      this.listFocusedIndex < 0
        ? 0
        : Math.min(this.listFocusedIndex + 1, this.listEmailIds.length - 1);
    this.listFocusedIndex = next;
    return this.listEmailIds[next] ?? null;
  }

  /** Move focus to the previous row, clamped. */
  focusListPrev(): string | null {
    if (this.listEmailIds.length === 0) return null;
    const next =
      this.listFocusedIndex < 0 ? 0 : Math.max(this.listFocusedIndex - 1, 0);
    this.listFocusedIndex = next;
    return this.listEmailIds[next] ?? null;
  }

  /** The threadId of the currently-focused list row, or null. */
  focusedListThreadId(): string | null {
    const emailId = this.listEmailIds[this.listFocusedIndex];
    if (!emailId) return null;
    return this.emails.get(emailId)?.threadId ?? null;
  }

  /** Human-readable label for the folder currently held in the list slice. */
  get listFolderLabel(): string {
    return FOLDER_LABEL[this.listFolder] ?? 'Inbox';
  }

  /** Resolved search-result emails in result order. */
  searchEmails = $derived(
    this.searchEmailIds
      .map((id) => this.emails.get(id))
      .filter((e): e is Email => e !== undefined),
  );

  focusSearchNext(): string | null {
    if (this.searchEmailIds.length === 0) return null;
    const next =
      this.searchFocusedIndex < 0
        ? 0
        : Math.min(this.searchFocusedIndex + 1, this.searchEmailIds.length - 1);
    this.searchFocusedIndex = next;
    return this.searchEmailIds[next] ?? null;
  }

  focusSearchPrev(): string | null {
    if (this.searchEmailIds.length === 0) return null;
    const next =
      this.searchFocusedIndex < 0 ? 0 : Math.max(this.searchFocusedIndex - 1, 0);
    this.searchFocusedIndex = next;
    return this.searchEmailIds[next] ?? null;
  }

  focusedSearchThreadId(): string | null {
    const emailId = this.searchEmailIds[this.searchFocusedIndex];
    if (!emailId) return null;
    return this.emails.get(emailId)?.threadId ?? null;
  }

  /**
   * Run a search. Idempotent for the same query — if the most recent
   * search produced ready results for the same query, no-op.
   */
  async runSearch(query: string): Promise<void> {
    if (this.searchLoadStatus === 'ready' && this.searchQuery === query) return;
    this.searchQuery = query;
    this.searchLoadStatus = 'loading';
    this.searchError = null;
    this.searchFocusedIndex = -1;

    try {
      // Make sure mailboxes are warm so `label:` resolves.
      if (this.mailboxes.size === 0) await this.loadMailboxes();
      const accountId = this.mailAccountId;
      if (!accountId) throw new Error('No Mail account on this session');

      const { filter } = parseQuery(query, { mailboxes: this.mailboxes });

      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Email/query',
          {
            accountId,
            filter,
            sort: [{ property: 'receivedAt', isAscending: false }],
            collapseThreads: true,
            limit: 50,
            calculateTotal: false,
          },
          [Capability.Mail],
        );
        b.call(
          'Email/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: EMAIL_LIST_PROPERTIES,
          },
          [Capability.Mail],
        );
      });
      strict(responses);

      const queryResult = invocationArgs<{ ids: string[] }>(responses[0]);
      const getResult = invocationArgs<{ list: Email[] }>(responses[1]);

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;
      this.searchEmailIds = queryResult.ids;
      this.searchLoadStatus = 'ready';
    } catch (err) {
      this.searchLoadStatus = 'error';
      this.searchError = err instanceof Error ? err.message : String(err);
    }
  }

  clearSearch(): void {
    this.searchQuery = '';
    this.searchEmailIds = [];
    this.searchLoadStatus = 'idle';
    this.searchError = null;
    this.searchFocusedIndex = -1;
  }

  /** The id of the JMAP Mail account this principal uses. */
  get mailAccountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Mail] ?? null;
  }

  /** The Mailbox row whose `role` is `'inbox'`, if any. */
  get inbox(): Mailbox | null {
    return this.#mailboxByRole('inbox');
  }

  /** The Mailbox row whose `role` is `'trash'`, if any. */
  get trash(): Mailbox | null {
    return this.#mailboxByRole('trash');
  }

  /** The Mailbox row whose `role` is `'drafts'`, if any. */
  get drafts(): Mailbox | null {
    return this.#mailboxByRole('drafts');
  }

  /** The Mailbox row whose `role` is `'sent'`, if any. */
  get sent(): Mailbox | null {
    return this.#mailboxByRole('sent');
  }

  /** The first available Identity — used as the default From for compose. */
  get primaryIdentity(): Identity | null {
    for (const id of this.identities.values()) return id;
    return null;
  }

  #mailboxByRole(role: string): Mailbox | null {
    for (const m of this.mailboxes.values()) {
      if (m.role === role) return m;
    }
    return null;
  }

  /** Resolved list emails in display order. */
  listEmails = $derived(
    this.listEmailIds
      .map((id) => this.emails.get(id))
      .filter((e): e is Email => e !== undefined),
  );

  async loadMailboxes(): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Mailbox/get',
        { accountId, ids: null },
        [Capability.Mail],
      );
    });
    strict(responses);

    const args = invocationArgs<{ list: Mailbox[]; state: string }>(responses[0]);
    const next = new Map<string, Mailbox>();
    for (const m of args.list) next.set(m.id, m);
    this.mailboxes = next;
    if (typeof args.state === 'string') this.mailboxState = args.state;
  }

  async loadIdentities(): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call('Identity/get', { accountId, ids: null }, [Capability.Submission]);
    });
    strict(responses);

    const args = invocationArgs<{ list: Identity[] }>(responses[0]);
    const next = new Map<string, Identity>();
    for (const id of args.list) next.set(id.id, id);
    this.identities = next;
  }

  /**
   * Load the email list for the given folder. Idempotent: when the
   * requested folder is already showing 'ready' state, the call is a
   * no-op so route effects can fire freely. Switching to a different
   * folder always re-runs.
   *
   * "all" maps to an account-scoped Email/query with no inMailbox
   * filter; everything else maps to the matching role mailbox. When a
   * role mailbox is missing for the principal (e.g. a brand-new account
   * with no Trash row yet) the slice lands in 'error' state with a
   * clear message — the sidebar still renders, the user just sees the
   * cause.
   */
  async loadFolder(folder: FolderID): Promise<void> {
    const sameFolder = this.listFolder === folder;
    if (sameFolder && this.listLoadStatus === 'loading') return;
    if (sameFolder && this.listLoadStatus === 'ready') return;
    this.listFolder = folder;
    this.listFocusedIndex = -1;
    this.listSelectedIds = new Set();
    this.listLoadStatus = 'loading';
    this.listError = null;
    try {
      // Mailboxes + identities both feed compose / list-rendering paths;
      // load them in parallel on first use.
      const setup: Promise<unknown>[] = [];
      if (this.mailboxes.size === 0) setup.push(this.loadMailboxes());
      if (this.identities.size === 0) setup.push(this.loadIdentities());
      if (setup.length > 0) await Promise.all(setup);
      const accountId = this.mailAccountId;
      if (!accountId) throw new Error('No Mail account on this session');

      let filter: Record<string, unknown> | undefined;
      let sortProperty: 'receivedAt' | 'sentAt' = 'receivedAt';
      if (folder !== 'all') {
        const role = FOLDER_ROLE[folder];
        const mailbox = this.#mailboxByRole(role);
        if (!mailbox) {
          throw new Error(`No ${FOLDER_LABEL[folder]} mailbox in this account`);
        }
        filter = { inMailbox: mailbox.id };
        // Sent / Drafts have no externally-set receivedAt the way inbound
        // mail does; sentAt is the natural ordering.
        if (folder === 'sent' || folder === 'drafts') sortProperty = 'sentAt';
      }

      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Email/query',
          {
            accountId,
            ...(filter ? { filter } : {}),
            sort: [{ property: sortProperty, isAscending: false }],
            collapseThreads: true,
            limit: 50,
            calculateTotal: false,
          },
          [Capability.Mail],
        );
        b.call(
          'Email/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: EMAIL_LIST_PROPERTIES,
          },
          [Capability.Mail],
        );
      });
      strict(responses);

      const queryResult = invocationArgs<{ ids: string[] }>(responses[0]);
      const getResult = invocationArgs<{ list: Email[]; state: string }>(
        responses[1],
      );

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;
      this.listEmailIds = queryResult.ids;
      if (typeof getResult.state === 'string') this.emailState = getResult.state;
      this.listLoadStatus = 'ready';
    } catch (err) {
      this.listLoadStatus = 'error';
      this.listError = err instanceof Error ? err.message : String(err);
    }
  }

  /** Inbox-specific entry point retained for callers that don't yet know
   * about generic folders. New code should call loadFolder('inbox'). */
  loadInbox(): Promise<void> {
    return this.loadFolder('inbox');
  }

  /** Force a refresh of the current folder view. Drops cached state. */
  async refreshFolder(): Promise<void> {
    const folder = this.listFolder;
    this.listLoadStatus = 'idle';
    this.listEmailIds = [];
    await this.loadFolder(folder);
  }

  threadStatus(threadId: string): LoadStatus {
    return this.threadLoadStatus.get(threadId) ?? 'idle';
  }

  threadError(threadId: string): string | null {
    return this.threadLoadError.get(threadId) ?? null;
  }

  /**
   * Load a thread's emails with body content. Idempotent — already-loaded
   * threads are no-ops.
   */
  async loadThread(threadId: string): Promise<void> {
    const status = this.threadStatus(threadId);
    if (status === 'loading' || status === 'ready') return;
    this.#setThreadStatus(threadId, 'loading');
    this.#clearThreadError(threadId);
    try {
      const accountId = this.mailAccountId;
      if (!accountId) throw new Error('No Mail account on this session');

      const { responses } = await jmap.batch((b) => {
        const t = b.call(
          'Thread/get',
          { accountId, ids: [threadId] },
          [Capability.Mail],
        );
        b.call(
          'Email/get',
          {
            accountId,
            '#ids': t.ref('/list/0/emailIds'),
            properties: EMAIL_BODY_PROPERTIES,
            fetchHTMLBodyValues: true,
            fetchTextBodyValues: true,
            maxBodyValueBytes: 256 * 1024,
          },
          [Capability.Mail],
        );
      });
      strict(responses);

      const threadResult = invocationArgs<{ list: Thread[] }>(responses[0]);
      const emailResult = invocationArgs<{ list: Email[]; state: string }>(
        responses[1],
      );
      if (typeof emailResult.state === 'string') this.emailState = emailResult.state;

      const thread = threadResult.list.find((t) => t.id === threadId);
      if (!thread) throw new Error('Thread not found');

      const nextThreads = new Map(this.threads);
      nextThreads.set(thread.id, thread);
      this.threads = nextThreads;

      const nextEmails = new Map(this.emails);
      for (const e of emailResult.list) nextEmails.set(e.id, e);
      this.emails = nextEmails;

      this.#setThreadStatus(threadId, 'ready');
    } catch (err) {
      this.#setThreadStatus(threadId, 'error');
      this.#setThreadError(threadId, err instanceof Error ? err.message : String(err));
    }
  }

  /**
   * Fetch a single email's body content into the cache. Used by
   * compose's "open existing draft" path so we don't need to load the
   * whole thread reader. Idempotent in the sense that a cached email
   * with body values present is replaced with a fresh fetch.
   */
  async loadDraftBody(emailId: string): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Email/get',
        {
          accountId,
          ids: [emailId],
          properties: EMAIL_BODY_PROPERTIES,
          fetchHTMLBodyValues: true,
          fetchTextBodyValues: true,
          maxBodyValueBytes: 256 * 1024,
        },
        [Capability.Mail],
      );
    });
    strict(responses);
    const result = invocationArgs<{ list: Email[]; state: string }>(responses[0]);
    if (typeof result.state === 'string') this.emailState = result.state;
    if (result.list.length === 0) {
      throw new Error('Email not found');
    }
    const next = new Map(this.emails);
    for (const e of result.list) next.set(e.id, e);
    this.emails = next;
  }

  /**
   * Re-fetch a thread that is already cached as 'ready'. Used by the
   * Email-state-change handler to surface freshly-arrived replies in
   * the open ThreadReader without forcing a full route reload. Keeps
   * the thread's status as 'ready' throughout so subscribers don't
   * flash the "loading" spinner; on failure we log and leave the
   * stale cache in place rather than dropping the open thread.
   */
  async refreshThread(threadId: string): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) return;
    const { responses } = await jmap.batch((b) => {
      const t = b.call(
        'Thread/get',
        { accountId, ids: [threadId] },
        [Capability.Mail],
      );
      b.call(
        'Email/get',
        {
          accountId,
          '#ids': t.ref('/list/0/emailIds'),
          properties: EMAIL_BODY_PROPERTIES,
          fetchHTMLBodyValues: true,
          fetchTextBodyValues: true,
          maxBodyValueBytes: 256 * 1024,
        },
        [Capability.Mail],
      );
    });
    strict(responses);

    const threadResult = invocationArgs<{ list: Thread[] }>(responses[0]);
    const emailResult = invocationArgs<{ list: Email[]; state: string }>(
      responses[1],
    );
    if (typeof emailResult.state === 'string') this.emailState = emailResult.state;

    const thread = threadResult.list.find((t) => t.id === threadId);
    if (!thread) return;
    const nextThreads = new Map(this.threads);
    nextThreads.set(thread.id, thread);
    this.threads = nextThreads;

    const nextEmails = new Map(this.emails);
    for (const e of emailResult.list) nextEmails.set(e.id, e);
    this.emails = nextEmails;
  }

  /** Resolved thread emails in display order (per Thread.emailIds). */
  threadEmails(threadId: string): Email[] {
    const thread = this.threads.get(threadId);
    if (!thread) return [];
    const out: Email[] = [];
    for (const id of thread.emailIds) {
      const e = this.emails.get(id);
      if (e) out.push(e);
    }
    return out;
  }

  // ── Optimistic actions ────────────────────────────────────────────────
  //
  // Pattern per docs/requirements/11-optimistic-ui.md REQ-OPT-01..04:
  //   1. Snapshot the relevant cache state
  //   2. Apply the change locally and remove from inbox if needed
  //   3. Fire Email/set
  //   4. On failure, restore the snapshot and toast an error
  //   5. For archive / delete, show an Undo toast (REQ-OPT-10..12)

  /** Archive: remove the inbox mailbox from this email's mailboxIds. */
  async archiveEmail(emailId: string): Promise<void> {
    const email = this.emails.get(emailId);
    const inbox = this.inbox;
    if (!email || !inbox) return;
    if (!email.mailboxIds[inbox.id]) return; // already not in inbox

    const prevMailboxIds = { ...email.mailboxIds };
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;

    // Optimistic apply
    const nextMailboxIds = { ...prevMailboxIds };
    delete nextMailboxIds[inbox.id];
    this.#patchEmail(emailId, { mailboxIds: nextMailboxIds });
    // Only remove from the visible list when the active folder is the
    // inbox; in All Mail / Sent the message stays visible.
    if (this.listFolder === 'inbox') this.#removeFromList(emailId);

    const revert = (): void => {
      this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
    };

    try {
      await this.#emailSetUpdate(emailId, {
        [`mailboxIds/${inbox.id}`]: null,
      });
    } catch (err) {
      revert();
      toast.show({
        message: errMessage(err, 'Archive failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return;
    }

    toast.show({
      message: 'Message archived',
      undo: async () => {
        // Replay the inverse — REQ-OPT-12.
        try {
          await this.#emailSetUpdate(emailId, {
            [`mailboxIds/${inbox.id}`]: true,
          });
          // Server state will refresh via sync; meanwhile keep our local
          // "back in inbox" state visible.
          this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
          this.listEmailIds = prevListIds;
        } catch (err) {
          toast.show({
            message: errMessage(err, 'Undo failed'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
      },
    });
  }

  /** Delete: replace mailboxIds with `{<trashId>: true}`. */
  async deleteEmail(emailId: string): Promise<void> {
    const email = this.emails.get(emailId);
    const trash = this.trash;
    if (!email || !trash) return;
    if (email.mailboxIds[trash.id] && Object.keys(email.mailboxIds).length === 1) {
      return; // already only-in-trash
    }

    const prevMailboxIds = { ...email.mailboxIds };
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;

    this.#patchEmail(emailId, { mailboxIds: { [trash.id]: true } });
    // Move-to-trash removes the email from the current view in every
    // folder except trash itself.
    if (this.listFolder !== 'trash') this.#removeFromList(emailId);

    const revert = (): void => {
      this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
    };

    try {
      await this.#emailSetUpdate(emailId, {
        mailboxIds: { [trash.id]: true },
      });
    } catch (err) {
      revert();
      toast.show({
        message: errMessage(err, 'Delete failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return;
    }

    toast.show({
      message: 'Message deleted',
      undo: async () => {
        try {
          await this.#emailSetUpdate(emailId, { mailboxIds: prevMailboxIds });
          this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
          this.listEmailIds = prevListIds;
        } catch (err) {
          toast.show({
            message: errMessage(err, 'Undo failed'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
      },
    });
  }

  /**
   * Move an email to a single target mailbox: replaces mailboxIds with
   * `{[targetId]: true}`. Optimistic; restored on failure with a toast.
   * The Undo path replays the prior mailboxIds set.
   */
  async moveEmailToMailbox(emailId: string, targetId: string): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    if (email.mailboxIds[targetId] && Object.keys(email.mailboxIds).length === 1) {
      return; // already only-in-target
    }
    const prevMailboxIds = { ...email.mailboxIds };
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;

    this.#patchEmail(emailId, { mailboxIds: { [targetId]: true } });
    // Whether to drop from the visible list depends on the active
    // folder. If we're showing the target mailbox the email stays;
    // otherwise drop it. All Mail keeps the email visible regardless.
    if (this.listFolder !== 'all') {
      const activeRole = this.listFolder;
      const target = this.mailboxes.get(targetId);
      const targetRole = target?.role ?? '';
      if (targetRole !== activeRole) this.#removeFromList(emailId);
    }

    const revert = (): void => {
      this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
    };

    try {
      await this.#emailSetUpdate(emailId, {
        mailboxIds: { [targetId]: true },
      });
    } catch (err) {
      revert();
      toast.show({
        message: errMessage(err, 'Move failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return;
    }

    const targetName = this.mailboxes.get(targetId)?.name ?? 'mailbox';
    toast.show({
      message: `Moved to ${targetName}`,
      undo: async () => {
        try {
          await this.#emailSetUpdate(emailId, { mailboxIds: prevMailboxIds });
          this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
          this.listEmailIds = prevListIds;
        } catch (err) {
          toast.show({
            message: errMessage(err, 'Undo failed'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
      },
    });
  }

  /**
   * Restore an email from Trash to Inbox: replaces mailboxIds with
   * `{<inboxId>: true}`. Same optimistic + undo pattern as move.
   */
  async restoreFromTrash(emailId: string): Promise<void> {
    const inbox = this.inbox;
    if (!inbox) return;
    return this.moveEmailToMailbox(emailId, inbox.id);
  }

  /**
   * Permanently delete every email currently in the Trash mailbox.
   * Issues Email/query to enumerate the ids, then a single Email/set
   * with `destroy: [...]`. No undo: destroy is final.
   *
   * Returns the number of emails deleted, or 0 on failure (with toast).
   */
  async emptyTrash(): Promise<number> {
    const accountId = this.mailAccountId;
    const trash = this.trash;
    if (!accountId || !trash) return 0;

    let ids: string[] = [];
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/query',
          {
            accountId,
            filter: { inMailbox: trash.id },
            limit: 10000,
          },
          [Capability.Mail],
        );
      });
      strict(responses);
      const args = invocationArgs<{ ids: string[] }>(responses[0]);
      ids = args.ids ?? [];
    } catch (err) {
      toast.show({
        message: errMessage(err, 'Empty trash failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return 0;
    }
    if (ids.length === 0) {
      toast.show({ message: 'Trash is already empty' });
      return 0;
    }

    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;
    // Optimistic: drop everything from the list view if Trash is open.
    if (this.listFolder === 'trash') {
      this.listEmailIds = [];
      this.listFocusedIndex = -1;
    }
    for (const id of ids) this.emails.delete(id);

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/set',
          { accountId, destroy: ids },
          [Capability.Mail],
        );
      });
      strict(responses);
      const result = invocationArgs<{
        destroyed?: string[] | null;
        notDestroyed?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      const destroyed = (result.destroyed ?? []).length;
      const failed = result.notDestroyed
        ? Object.keys(result.notDestroyed).length
        : 0;
      if (failed > 0) {
        toast.show({
          message: `Deleted ${destroyed}, ${failed} could not be deleted`,
          kind: 'error',
          timeoutMs: 6000,
        });
      } else {
        toast.show({ message: `Deleted ${destroyed} message${destroyed === 1 ? '' : 's'}` });
      }
      return destroyed;
    } catch (err) {
      // Best-effort recovery: refetch the trash list so the UI is consistent.
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
      toast.show({
        message: errMessage(err, 'Empty trash failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return 0;
    }
  }

  // ── Bulk-selection helpers ────────────────────────────────────────────

  /** Toggle whether `id` is in the bulk selection set. */
  toggleSelected(id: string): void {
    const next = new Set(this.listSelectedIds);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    this.listSelectedIds = next;
  }

  /** Replace the selection with every id currently visible in the list. */
  selectAllVisible(): void {
    this.listSelectedIds = new Set(this.listEmailIds);
  }

  /** Clear the bulk selection set. */
  clearSelection(): void {
    if (this.listSelectedIds.size === 0) return;
    this.listSelectedIds = new Set();
  }

  /**
   * Issue a single `Email/set` with one entry in `update` per id. Used
   * by every bulk action — archive / delete / mark / move — so the
   * server gets one round-trip and we can present one summary toast.
   */
  async #emailSetUpdateBulk(
    updates: Record<string, Record<string, unknown>>,
  ): Promise<{ updated: string[]; failed: Record<string, string> }> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const { responses } = await jmap.batch((b) => {
      b.call('Email/set', { accountId, update: updates }, [Capability.Mail]);
    });
    strict(responses);
    const result = invocationArgs<{
      updated?: Record<string, unknown> | null;
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const updated = Object.keys(result.updated ?? {});
    const failed: Record<string, string> = {};
    for (const [id, info] of Object.entries(result.notUpdated ?? {})) {
      failed[id] = info.description ?? info.type;
    }
    return { updated, failed };
  }

  /** Bulk archive: remove the inbox mailbox from every id. Inbox-only. */
  async bulkArchive(ids: string[]): Promise<void> {
    const inbox = this.inbox;
    if (!inbox || ids.length === 0) return;
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      if (!e.mailboxIds[inbox.id]) continue;
      prevById.set(id, { ...e.mailboxIds });
      updates[id] = { [`mailboxIds/${inbox.id}`]: null };
      const next: Record<string, true> = { ...e.mailboxIds };
      delete next[inbox.id];
      this.#patchEmail(id, { mailboxIds: next });
    }
    if (Object.keys(updates).length === 0) return;
    if (this.listFolder === 'inbox') {
      for (const id of Object.keys(updates)) this.#removeFromList(id);
    }
    this.clearSelection();
    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      this.#summarizeBulk('archived', Object.keys(updates).length, failed);
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { mailboxIds: prev });
      }
      toast.show({
        message: errMessage(err, 'Bulk archive failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Bulk delete: replace every id's mailboxIds with `{<trashId>: true}`. */
  async bulkDelete(ids: string[]): Promise<void> {
    const trash = this.trash;
    if (!trash || ids.length === 0) return;
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      if (e.mailboxIds[trash.id] && Object.keys(e.mailboxIds).length === 1) continue;
      prevById.set(id, { ...e.mailboxIds });
      updates[id] = { mailboxIds: { [trash.id]: true } };
      this.#patchEmail(id, { mailboxIds: { [trash.id]: true } });
    }
    if (Object.keys(updates).length === 0) return;
    if (this.listFolder !== 'trash') {
      for (const id of Object.keys(updates)) this.#removeFromList(id);
    }
    this.clearSelection();
    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      this.#summarizeBulk('deleted', Object.keys(updates).length, failed);
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { mailboxIds: prev });
      }
      toast.show({
        message: errMessage(err, 'Bulk delete failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Bulk mark-read / mark-unread: set $seen on every id. */
  async bulkSetSeen(ids: string[], seen: boolean): Promise<void> {
    if (ids.length === 0) return;
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true | undefined>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      const wasSeen = Boolean(e.keywords.$seen);
      if (wasSeen === seen) continue;
      prevById.set(id, { ...e.keywords });
      updates[id] = { 'keywords/$seen': seen ? true : null };
      const nextKeywords: Record<string, true | undefined> = { ...e.keywords };
      if (seen) nextKeywords.$seen = true;
      else delete nextKeywords.$seen;
      this.#patchEmail(id, { keywords: nextKeywords });
    }
    if (Object.keys(updates).length === 0) return;
    this.clearSelection();
    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      this.#summarizeBulk(
        seen ? 'marked read' : 'marked unread',
        Object.keys(updates).length,
        failed,
      );
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { keywords: prev });
      }
      toast.show({
        message: errMessage(err, 'Bulk mark failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Bulk move: replace every id's mailboxIds with `{[targetId]: true}`. */
  async bulkMoveToMailbox(ids: string[], targetId: string): Promise<void> {
    if (ids.length === 0) return;
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      if (e.mailboxIds[targetId] && Object.keys(e.mailboxIds).length === 1) continue;
      prevById.set(id, { ...e.mailboxIds });
      updates[id] = { mailboxIds: { [targetId]: true } };
      this.#patchEmail(id, { mailboxIds: { [targetId]: true } });
    }
    if (Object.keys(updates).length === 0) return;
    if (this.listFolder !== 'all') {
      const target = this.mailboxes.get(targetId);
      const targetRole = target?.role ?? '';
      if (targetRole !== this.listFolder) {
        for (const id of Object.keys(updates)) this.#removeFromList(id);
      }
    }
    this.clearSelection();
    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      const targetName = this.mailboxes.get(targetId)?.name ?? 'mailbox';
      this.#summarizeBulk(
        `moved to ${targetName}`,
        Object.keys(updates).length,
        failed,
      );
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { mailboxIds: prev });
      }
      toast.show({
        message: errMessage(err, 'Bulk move failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Render a "X messages <verb>" / partial-failure toast for bulk ops. */
  #summarizeBulk(verb: string, total: number, failed: Record<string, string>): void {
    const failCount = Object.keys(failed).length;
    const ok = total - failCount;
    if (failCount > 0) {
      toast.show({
        message: `${ok} ${verb}, ${failCount} failed`,
        kind: 'error',
        timeoutMs: 6000,
      });
    } else {
      toast.show({
        message: `${ok} message${ok === 1 ? '' : 's'} ${verb}`,
      });
    }
  }

  /** Toggle the $flagged keyword. No toast / no undo (toggle is itself the undo). */
  async toggleFlagged(emailId: string): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    const wasFlagged = Boolean(email.keywords.$flagged);
    const nextKeywords = { ...email.keywords };
    if (wasFlagged) delete nextKeywords.$flagged;
    else nextKeywords.$flagged = true;

    this.#patchEmail(emailId, { keywords: nextKeywords });
    try {
      await this.#emailSetUpdate(emailId, {
        'keywords/$flagged': wasFlagged ? null : true,
      });
    } catch (err) {
      this.#patchEmail(emailId, { keywords: email.keywords });
      toast.show({
        message: errMessage(err, 'Star failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  async setSeen(emailId: string, seen: boolean): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    const wasSeen = Boolean(email.keywords.$seen);
    if (wasSeen === seen) return;

    const nextKeywords = { ...email.keywords };
    if (seen) nextKeywords.$seen = true;
    else delete nextKeywords.$seen;

    this.#patchEmail(emailId, { keywords: nextKeywords });
    try {
      await this.#emailSetUpdate(emailId, {
        'keywords/$seen': seen ? true : null,
      });
    } catch (err) {
      this.#patchEmail(emailId, { keywords: email.keywords });
      toast.show({
        message: errMessage(err, 'Mark read failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  // ── Internals ─────────────────────────────────────────────────────────

  #patchEmail(id: string, patch: Partial<Email>): void {
    const cur = this.emails.get(id);
    if (!cur) return;
    const next = new Map(this.emails);
    next.set(id, { ...cur, ...patch });
    this.emails = next;
  }

  #removeFromList(emailId: string): void {
    const idx = this.listEmailIds.indexOf(emailId);
    if (idx < 0) return;
    this.listEmailIds = [
      ...this.listEmailIds.slice(0, idx),
      ...this.listEmailIds.slice(idx + 1),
    ];
    // Clamp focus to the new bounds.
    if (this.listFocusedIndex >= this.listEmailIds.length) {
      this.listFocusedIndex = this.listEmailIds.length - 1;
    }
  }

  /**
   * Issue an `Email/set { update }` for one email and surface per-id
   * errors as throws. Caller is responsible for revert on failure.
   */
  async #emailSetUpdate(
    emailId: string,
    patches: Record<string, unknown>,
  ): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Email/set',
        {
          accountId,
          update: { [emailId]: patches },
        },
        [Capability.Mail],
      );
    });
    strict(responses);
    const result = invocationArgs<{
      updated?: Record<string, unknown> | null;
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[emailId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
  }

  #setThreadStatus(id: string, status: LoadStatus): void {
    const next = new Map(this.threadLoadStatus);
    next.set(id, status);
    this.threadLoadStatus = next;
  }

  #setThreadError(id: string, msg: string): void {
    const next = new Map(this.threadLoadError);
    next.set(id, msg);
    this.threadLoadError = next;
  }

  #clearThreadError(id: string): void {
    if (!this.threadLoadError.has(id)) return;
    const next = new Map(this.threadLoadError);
    next.delete(id);
    this.threadLoadError = next;
  }
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

function errMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}

export const mail = new MailStore();
