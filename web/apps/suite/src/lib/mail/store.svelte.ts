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
import { i18n, localeTag } from '../i18n/i18n.svelte';
import { Capability, type Invocation } from '../jmap/types';
import {
  EMAIL_BODY_PROPERTIES,
  EMAIL_LIST_PROPERTIES,
  type Email,
  type Identity,
  type Mailbox,
  type Thread,
} from './types';
import { parseQuery, type FilterCondition, type FilterOperator } from './search-query';
import { sounds } from '../notifications/sounds.svelte';
import { shouldPlayMailCue } from '../notifications/cue-gates';
import { settings } from '../settings/settings.svelte';
import { router } from '../router/router.svelte';
import { buildSelfEmailSet, isFromSelf } from './identity-match';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/**
 * Identifier for the folder rendered by the generic list view. "inbox",
 * "sent", "drafts", "trash" map to the matching mailbox role; "all"
 * spans every folder visible to this account.
 */
/**
 * The folder slice's identifier. The well-known names below resolve via
 * mailbox role; 'all' is account-scoped and has no mailbox; any other
 * value is taken as a literal Mailbox.id and resolved directly. This
 * union encoding lets `/mail/folder/<x>` route both kinds of folder
 * without splitting the slice.
 */
export type FolderID = string;

const ROLED_FOLDERS = new Set(['inbox', 'sent', 'drafts', 'trash']);

/**
 * Structured error thrown by Identity/set actions (create / destroy)
 * when the server returns a `setError`. Carries the machine-readable
 * `type` and the offending `properties` so callers can map known
 * cases (e.g. a duplicate email) to localized strings instead of
 * surfacing the raw English `description`.
 */
export class IdentitySetError extends Error {
  readonly type: string;
  readonly properties: string[];
  constructor(type: string, description: string | undefined, properties: string[] = []) {
    super(description ?? type);
    this.name = 'IdentitySetError';
    this.type = type;
    this.properties = properties;
  }
}

const SEARCH_HISTORY_MAX = 12;
const SEARCH_HISTORY_KEY = 'herold.suite.search.history';

function readSearchHistory(): string[] {
  try {
    const raw = localStorage.getItem(SEARCH_HISTORY_KEY);
    if (raw === null) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((x): x is string => typeof x === 'string');
  } catch {
    return [];
  }
}

function persistSearchHistory(history: string[]): void {
  try {
    localStorage.setItem(SEARCH_HISTORY_KEY, JSON.stringify(history));
  } catch {
    // Quota / private mode — history just doesn't persist this run.
  }
}

const FOLDER_ROLE: Record<string, string> = {
  inbox: 'inbox',
  sent: 'sent',
  drafts: 'drafts',
  trash: 'trash',
};

const FOLDER_LABEL: Record<string, string> = {
  inbox: 'Inbox',
  sent: 'Sent',
  drafts: 'Drafts',
  trash: 'Trash',
  all: 'All Mail',
  important: 'Important',
  snoozed: 'Snoozed',
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

  /**
   * Id of the thread currently rendered by ThreadReader, or null when no
   * thread reader is mounted. Set by ThreadReader on mount and cleared on
   * unmount; consulted by the Email/changes handler to decide whether a
   * fresh arrival should populate `pendingArrivals`. See issue #118.
   */
  openThreadId = $state<string | null>(null);

  /**
   * Per-thread set of email ids that arrived via push while the user was
   * reading that thread. Rendered as an inline "new reply" banner in
   * ThreadReader (re #118). Cleared by an explicit user dismiss; the
   * `openThreadId` setter also wipes entries for threads other than the
   * one currently open so banners don't leak across navigations.
   */
  pendingArrivals = $state(new Map<string, Set<string>>());

  /** Most recent search query string (raw, user-typed). */
  searchQuery = $state('');
  searchEmailIds = $state<string[]>([]);
  searchLoadStatus = $state<LoadStatus>('idle');
  searchError = $state<string | null>(null);
  searchFocusedIndex = $state<number>(-1);

  /**
   * Recent search queries, most-recent first, capped at SEARCH_HISTORY_MAX.
   * Persisted in localStorage so the suggestions survive reload.
   */
  searchHistory = $state<string[]>([]);

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
    // Search history is local-only and survives reload.
    if (typeof localStorage !== 'undefined') {
      this.searchHistory = readSearchHistory();
    }
  }

  async #onEmailStateChange(newState: string): Promise<void> {
    if (newState === this.emailState) return;
    // Compute the delta via Email/changes so we can scope the refetch
    // to views actually affected. Without this scoping, every backend
    // mutation (extimg internalize, fts indexer, etc.) fans out into
    // refreshFolder + Thread/get + Email/get for every cached thread,
    // generating O(open-threads) requests per state advance during
    // worker churn. With the delta in hand we only refresh threads
    // whose emailIds intersect, and we only re-run the folder query
    // when a creation or destruction may have changed list membership.

    const sinceState = this.emailState;
    const accountId = this.mailAccountId;

    // No baseline yet — typically because the initial inbox load
    // hasn't finished (or failed with a deadline-exceeded). Seed the
    // baseline from this push so the next push can be diffed via
    // Email/changes; don't fire any refresh here. The user-driven
    // load (or its retry) will populate the cache and overwrite this
    // state in lockstep with the data it returns.
    //
    // Without this seed, the previous behaviour was: every push
    // falls through to the blanket refreshFolder + refresh-every-
    // cached-thread path, piling on concurrent requests against a
    // server that already showed it could not keep up. That was the
    // observed flashing-inbox loop after a failed initial load.
    if (!sinceState) {
      this.emailState = newState;
      return;
    }

    // Snapshot known email ids before the refresh so we can detect arrivals.
    const knownEmailIds = new Set(this.emails.keys());

    let delta: {
      created: Set<string>;
      updated: Set<string>;
      destroyed: Set<string>;
    } | null = null;
    if (accountId) {
      try {
        const { responses } = await jmap.batch((b) => {
          b.call(
            'Email/changes',
            { accountId, sinceState },
            [Capability.Mail],
          );
        });
        strict(responses);
        const args = invocationArgs<{
          created: string[];
          updated: string[];
          destroyed: string[];
        }>(responses[0]);
        delta = {
          created: new Set(args.created),
          updated: new Set(args.updated),
          destroyed: new Set(args.destroyed),
        };
      } catch (err) {
        // cannotCalculateChanges or transport error — fall back to a
        // full refresh below.
        console.warn(
          'Email/changes unavailable; falling back to full refresh',
          err,
        );
      }
    }

    const tasks: Promise<unknown>[] = [];

    // Folder list refresh:
    //   - delta unavailable → blanket refresh (correctness floor).
    //   - creations or destroys → list membership may have changed.
    //   - updates only → list shape unchanged; per-row data refreshed
    //     transparently when the Email/get below repopulates `emails`.
    if (this.listLoadStatus === 'ready') {
      const listMayHaveChanged =
        delta === null || delta.created.size > 0 || delta.destroyed.size > 0;
      if (listMayHaveChanged) {
        tasks.push(
          this.refreshFolder().catch((err) => {
            console.error('list refresh after state change failed', err);
          }),
        );
      }
    }

    // Thread refresh:
    //   - delta unavailable → refresh every cached-ready thread.
    //   - delta + creations → still refresh every cached-ready thread
    //     since a created email might be a new reply in any of them
    //     (Thread membership isn't in the delta; learning it would
    //     need an extra Email/get for thread_id only).
    //   - delta with only updates/destroys → refresh only threads
    //     whose emailIds intersect the changed set.
    const refreshAllThreads =
      delta === null || delta.created.size > 0;
    for (const [tid, status] of this.threadLoadStatus) {
      if (status !== 'ready') continue;
      let needsRefresh = refreshAllThreads;
      if (!needsRefresh && delta !== null) {
        const thread = this.threads.get(tid);
        if (!thread) continue;
        for (const eid of thread.emailIds) {
          if (delta.updated.has(eid) || delta.destroyed.has(eid)) {
            needsRefresh = true;
            break;
          }
        }
      }
      if (needsRefresh) {
        tasks.push(
          this.refreshThread(tid).catch((err) => {
            console.error('thread refresh after state change failed', err);
          }),
        );
      }
    }
    if (tasks.length > 0) await Promise.all(tasks);
    this.emailState = newState;

    // REQ-EXTIMG-BG-INTERNAL-32: the settings → privacy "Image processing"
    // section's pending count (REQ-EXTIMG-BG-32) is no longer refreshed
    // here. The background-internalize worker writes its state-change
    // rows with cause = 'background', so they no longer reach this
    // handler. The pending count refreshes on the dedicated
    // InternalizeStatus push subscription wired up in App.svelte.

    // After the refresh, find emails that were not in the cache before
    // and evaluate the mail-cue gate for each. Play at most one cue per
    // state-change event to avoid a burst of sounds when the user has
    // been offline.
    if (knownEmailIds.size > 0) {
      // Only trigger cues on state-change refreshes (not the initial load
      // where knownEmailIds would be empty).
      let notifEmail: Email | null = null;
      for (const [id, email] of this.emails) {
        if (knownEmailIds.has(id)) continue;
        if (this.#shouldMailCue(email)) {
          sounds.play('mail');
          notifEmail = email;
          break; // one cue per event
        }
      }
      // Desktop notification for new inbox mail when the tab is open (re #23a).
      if (notifEmail !== null && settings.desktopNotifEnabled) {
        this.#fireDesktopNotification(notifEmail);
      }
    }

    // Refresh mailbox counters so favicon badge and title update on new
    // mail arrival (re #36). Coalesced via #refreshMailboxesSoon so rapid
    // state-change bursts collapse into a single loadMailboxes call.
    if (
      knownEmailIds.size > 0 &&
      (delta === null ||
        delta.created.size > 0 ||
        delta.updated.size > 0 ||
        delta.destroyed.size > 0)
    ) {
      this.#refreshMailboxesSoon();
    }

    // Record fresh arrivals in the currently-open thread so ThreadReader
    // can surface a "new reply" banner (issue #118). Skip messages the
    // user sent themselves (replying to your own thread shouldn't ping
    // you about your own reply).
    if (knownEmailIds.size > 0 && this.openThreadId !== null) {
      await this.#recordPendingArrivals(knownEmailIds, delta);
    }
  }

  async #recordPendingArrivals(
    knownEmailIds: Set<string>,
    delta: { created: Set<string>; updated: Set<string>; destroyed: Set<string> } | null,
  ): Promise<void> {
    const open = this.openThreadId;
    if (open === null) return;
    const candidates = new Set<string>();
    if (delta) {
      for (const id of delta.created) candidates.add(id);
    } else {
      for (const id of this.emails.keys()) {
        if (!knownEmailIds.has(id)) candidates.add(id);
      }
    }
    if (candidates.size === 0) return;
    const selfEmails = buildSelfEmailSet(this.identities.values());
    const arrivals: string[] = [];
    for (const id of candidates) {
      const email = this.emails.get(id);
      if (!email) continue;
      if (email.threadId !== open) continue;
      if (isFromSelf(email, selfEmails)) continue;
      arrivals.push(id);
    }
    if (arrivals.length === 0) return;

    // Ensure body values are present before surfacing the arrival in the
    // thread reader so it does not render "(no body)" until reload (re #31).
    // refreshFolder (list-props only) and refreshThread (body-props) run
    // concurrently; if refreshFolder wins it overwrites the email object
    // with one that has no bodyValues. An explicit Email/get here with
    // body properties wins that race by writing after both tasks settle.
    const accountId = this.mailAccountId;
    if (accountId) {
      const needsBody = arrivals.filter((id) => {
        const e = this.emails.get(id);
        return e !== undefined && !e.bodyValues;
      });
      if (needsBody.length > 0) {
        try {
          const { responses } = await jmap.batch((b) => {
            b.call(
              'Email/get',
              {
                accountId,
                ids: needsBody,
                properties: EMAIL_BODY_PROPERTIES,
                fetchHTMLBodyValues: true,
                fetchTextBodyValues: true,
                maxBodyValueBytes: 256 * 1024,
              },
              [Capability.Mail],
            );
          });
          strict(responses);
          const result = invocationArgs<{ list: Email[] }>(responses[0]);
          const updated = new Map(this.emails);
          for (const e of result.list) updated.set(e.id, e);
          this.emails = updated;
        } catch (err) {
          // Body pre-fetch failure is non-fatal: the arrival still appears
          // in the banner; the user can reload to see the full content.
          console.warn('body pre-fetch for pending arrival failed', err);
        }
      }
    }

    const next = new Map(this.pendingArrivals);
    const merged = new Set(next.get(open) ?? []);
    for (const id of arrivals) merged.add(id);
    next.set(open, merged);
    this.pendingArrivals = next;
  }

  /**
   * Mark `threadId` as the thread the user is currently reading. Pass
   * null when leaving the reader. Wipes pending-arrival entries for
   * threads other than the one being opened so a banner from a previous
   * thread doesn't follow the user across navigations.
   */
  setOpenThread(threadId: string | null): void {
    if (this.openThreadId === threadId) return;
    this.openThreadId = threadId;
    if (this.pendingArrivals.size === 0) return;
    if (threadId === null) {
      this.pendingArrivals = new Map();
      return;
    }
    const keep = this.pendingArrivals.get(threadId);
    if (keep === undefined) {
      this.pendingArrivals = new Map();
      return;
    }
    this.pendingArrivals = new Map([[threadId, keep]]);
  }

  /** Resolved Email rows pending arrival in `threadId`, chronological. */
  pendingArrivalsForThread(threadId: string): Email[] {
    const ids = this.pendingArrivals.get(threadId);
    if (!ids || ids.size === 0) return [];
    const out: Email[] = [];
    for (const id of ids) {
      const e = this.emails.get(id);
      if (e) out.push(e);
    }
    out.sort((a, b) => a.receivedAt.localeCompare(b.receivedAt));
    return out;
  }

  /** Drop the pending-arrival entry for `threadId` (user dismissed banner). */
  dismissPendingArrivals(threadId: string): void {
    if (!this.pendingArrivals.has(threadId)) return;
    const next = new Map(this.pendingArrivals);
    next.delete(threadId);
    this.pendingArrivals = next;
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
    const wellKnown = FOLDER_LABEL[this.listFolder];
    if (wellKnown) return wellKnown;
    // Custom mailbox: render its name.
    return this.mailboxes.get(this.listFolder)?.name ?? 'Mailbox';
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

      const { filter, includesTrashOrJunk } = parseQuery(query, { mailboxes: this.mailboxes });
      // REQ-SRC-06/07: default scope excludes Trash + Junk; the user opts
      // in explicitly via `in:trash` / `in:junk` / `in:anywhere` /
      // `label:Trash` / `label:Junk`.
      const scopedFilter = includesTrashOrJunk
        ? filter
        : applyTrashJunkExclusion(filter, this.mailboxes);

      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Email/query',
          {
            accountId,
            filter: scopedFilter,
            sort: [{ property: 'receivedAt', isAscending: false }],
            collapseThreads: true,
            limit: 50,
            calculateTotal: false,
          },
          [Capability.Mail],
        );
        const eg = b.call(
          'Email/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: EMAIL_LIST_PROPERTIES,
          },
          [Capability.Mail],
        );
        // Fetch thread membership so search results can show per-thread message
        // counts (issue #64).
        b.call(
          'Thread/get',
          {
            accountId,
            '#ids': eg.ref('/list/*/threadId'),
          },
          [Capability.Mail],
        );
      });
      strict(responses);

      const queryResult = invocationArgs<{ ids: string[] }>(responses[0]);
      const getResult = invocationArgs<{ list: Email[] }>(responses[1]);
      const threadResult = invocationArgs<{ list: Thread[] }>(responses[2]);

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;

      const nextThreads = new Map(this.threads);
      for (const t of threadResult.list) nextThreads.set(t.id, t);
      this.threads = nextThreads;

      this.searchEmailIds = queryResult.ids;
      this.searchLoadStatus = 'ready';
      this.#recordSearchHistory(query);
    } catch (err) {
      this.searchLoadStatus = 'error';
      this.searchError = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Push `q` onto the front of search history, dedupe, cap at
   * SEARCH_HISTORY_MAX, and persist. Empty queries are ignored.
   */
  #recordSearchHistory(q: string): void {
    const trimmed = q.trim();
    if (!trimmed) return;
    const next = [trimmed, ...this.searchHistory.filter((x) => x !== trimmed)];
    if (next.length > SEARCH_HISTORY_MAX) next.length = SEARCH_HISTORY_MAX;
    this.searchHistory = next;
    persistSearchHistory(next);
  }

  /** Clear search history entirely. */
  clearSearchHistory(): void {
    this.searchHistory = [];
    persistSearchHistory([]);
  }

  /** Hydrate search history from localStorage. Idempotent. */
  hydrateSearchHistory(): void {
    this.searchHistory = readSearchHistory();
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

  /** The Mailbox row whose `role` is `'junk'`, if any. */
  get junk(): Mailbox | null {
    return this.#mailboxByRole('junk');
  }

  /** The Mailbox row whose `role` is `'archive'`, if any. RFC 8621 §4.2
   * requires every Email to be in at least one mailbox, so archiving
   * must MOVE the message to this mailbox rather than just removing
   * the inbox membership.
   */
  get archive(): Mailbox | null {
    return this.#mailboxByRole('archive');
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

  /**
   * Create a new top-level mailbox with the given name. Returns the
   * server-assigned mailbox id on success, or null on failure (with toast).
   * The mailbox cache is repopulated from the server response so callers
   * can immediately route to the new id.
   */
  async createMailbox(
    name: string,
    parentId: string | null = null,
    color?: string | null,
  ): Promise<string | null> {
    const accountId = this.mailAccountId;
    if (!accountId) return null;
    const trimmed = name.trim();
    if (!trimmed) return null;
    try {
      const { responses } = await jmap.batch((b) => {
        const props: Record<string, unknown> = { name: trimmed, parentId };
        if (color !== undefined) props.color = color ?? null;
        b.call(
          'Mailbox/set',
          {
            accountId,
            create: { new: props },
          },
          [Capability.Mail],
        );
      });
      strict(responses);
      const result = invocationArgs<{
        newState?: string;
        created?: Record<string, Mailbox> | null;
        notCreated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      this.#captureMailboxSetNewState(result);
      const failure = result.notCreated?.new;
      if (failure) {
        toast.show({
          message: failure.description ?? `Create failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return null;
      }
      const created = result.created?.new;
      if (created) {
        const next = new Map(this.mailboxes);
        next.set(created.id, created);
        this.mailboxes = next;
        toast.show({ message: `Created ${created.name}` });
        return created.id;
      }
      // Server applied the create but didn't echo the row; refetch.
      await this.loadMailboxes();
      return null;
    } catch (err) {
      toast.show({
        message: errMessage(err, 'Create mailbox failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return null;
    }
  }

  /** Rename an existing mailbox, optionally updating its color. */
  async renameMailbox(id: string, name: string, color?: string | null): Promise<boolean> {
    const accountId = this.mailAccountId;
    if (!accountId) return false;
    const trimmed = name.trim();
    if (!trimmed) return false;
    const prev = this.mailboxes.get(id);
    if (!prev) return false;
    if (prev.name === trimmed && color === undefined) return false;
    // Optimistic update.
    const optimistic: typeof prev = { ...prev, name: trimmed };
    if (color !== undefined) optimistic.color = color ?? null;
    const next = new Map(this.mailboxes);
    next.set(id, optimistic);
    this.mailboxes = next;
    try {
      const patch: Record<string, unknown> = { name: trimmed };
      if (color !== undefined) patch.color = color ?? null;
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Mailbox/set',
          { accountId, update: { [id]: patch } },
          [Capability.Mail],
        );
      });
      strict(responses);
      const result = invocationArgs<{
        newState?: string;
        notUpdated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      this.#captureMailboxSetNewState(result);
      const failure = result.notUpdated?.[id];
      if (failure) {
        // Roll back.
        const back = new Map(this.mailboxes);
        back.set(id, prev);
        this.mailboxes = back;
        toast.show({
          message: failure.description ?? `Rename failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return false;
      }
      toast.show({ message: i18n.t('sidebar.editFolder.toastChanged') });
      return true;
    } catch (err) {
      const back = new Map(this.mailboxes);
      back.set(id, prev);
      this.mailboxes = back;
      toast.show({
        message: errMessage(err, 'Rename failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return false;
    }
  }

  /**
   * Permanently delete a mailbox. The server side may refuse for roled
   * mailboxes (Inbox / Sent / Drafts / Trash) -- the toast surfaces that.
   * `onMailRemoval` controls server-side disposition of the mailbox's
   * messages (RFC 8621 Mailbox/set §2.5):
   *   - "destroy": permanently delete every email only-in-this-mailbox
   *   - "removeOnly" (default): leave emails alone, just unmount this id
   */
  async destroyMailbox(
    id: string,
    onMailRemoval: 'destroy' | 'removeOnly' = 'removeOnly',
  ): Promise<boolean> {
    const accountId = this.mailAccountId;
    if (!accountId) return false;
    const prev = this.mailboxes.get(id);
    if (!prev) return false;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Mailbox/set',
          {
            accountId,
            destroy: [id],
            onDestroyRemoveEmails: onMailRemoval === 'destroy',
          },
          [Capability.Mail],
        );
      });
      strict(responses);
      const result = invocationArgs<{
        newState?: string;
        destroyed?: string[] | null;
        notDestroyed?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      this.#captureMailboxSetNewState(result);
      const failure = result.notDestroyed?.[id];
      if (failure) {
        toast.show({
          message: failure.description ?? `Delete failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return false;
      }
      const next = new Map(this.mailboxes);
      next.delete(id);
      this.mailboxes = next;
      toast.show({ message: `Deleted ${prev.name}` });
      // If we were viewing this mailbox, fall back to the inbox.
      if (this.listFolder === id) {
        void this.loadFolder('inbox');
      }
      return true;
    } catch (err) {
      toast.show({
        message: errMessage(err, 'Delete mailbox failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return false;
    }
  }

  /**
   * Mailboxes that should appear in the "More" sidebar list — every
   * mailbox without a JMAP role (or with a role we don't surface
   * elsewhere). Sorted by name. Roled mailboxes that already have their
   * own sidebar entry (inbox / sent / drafts / trash) are excluded.
   */
  get customMailboxes(): Mailbox[] {
    const out: Mailbox[] = [];
    for (const m of this.mailboxes.values()) {
      if (isSystemRole(m.role ?? '')) continue;
      out.push(m);
    }
    out.sort((a, b) => a.name.localeCompare(b.name));
    return out;
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
   * Create a new Identity via `Identity/set { create }`
   * (REQ-SET-IDENT-30 step 1). The server commits the row in the
   * unverified state and asynchronously dispatches a verification
   * email (REQ-IDENT-30); the suite's wizard then transitions to
   * the verification-pending pane.
   *
   * Returns the freshly-created `Identity` row on success. On a
   * `setError` (forbiddenFrom, invalidProperties), throws an Error
   * whose `.message` is the server-provided description so the
   * wizard can surface it inline with the offending field
   * highlighted (REQ-SET-IDENT-30).
   *
   * The created row is mirrored into the local identities cache
   * (REQ-SET-IDENT-40); the cache reads as "verification pending"
   * because `verifiedAt` is null on a freshly-created row, and
   * `verificationPendingSince` is set by the server.
   */
  async createIdentity(email: string, name: string): Promise<Identity> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const trimmedEmail = email.trim();
    const trimmedName = name.trim();
    if (trimmedEmail === '') throw new Error('email is required');

    const clientID = 'new';
    const props: Record<string, unknown> = { email: trimmedEmail };
    // The server treats name as optional; empty-string falls back to
    // the local-part on the wire.
    if (trimmedName !== '') props.name = trimmedName;

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          create: { [clientID]: props },
        },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      created?: Record<string, Identity>;
      notCreated?: Record<
        string,
        { type: string; description?: string; properties?: string[] }
      >;
    }>(responses[0]);

    const failure = result.notCreated?.[clientID];
    if (failure) {
      // Surface a STRUCTURED error so the wizard can map known cases
      // (e.g. invalidProperties on `email` = duplicate) to localized
      // strings (REQ-SET-IDENT-30). The forbiddenFrom case carries a
      // domain-policy explanation; invalidProperties carries the RFC
      // 5321 syntactic complaint or the duplicate-email message.
      throw new IdentitySetError(
        failure.type,
        failure.description,
        failure.properties ?? [],
      );
    }
    const created = result.created?.[clientID];
    if (!created) {
      throw new Error('Identity/set did not echo the created row');
    }

    // Mirror into the cache so the row renders in the list as
    // verification-pending without waiting for a JMAP push.
    const next = new Map(this.identities);
    next.set(created.id, created);
    this.identities = next;
    return created;
  }

  /**
   * Destroy an existing Identity via `Identity/set { destroy }`.
   * Used by the wizard's Cancel-at-step-2 path when the user wants
   * to discard the just-created pending identity (REQ-SET-IDENT-33).
   * Mirrors the destroy into the local cache; throws an Error
   * carrying the server's `notDestroyed` description on failure.
   */
  async destroyIdentity(identityId: string): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          destroy: [identityId],
        },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      destroyed?: string[] | null;
      notDestroyed?: Record<
        string,
        { type: string; description?: string; properties?: string[] }
      >;
    }>(responses[0]);

    const failure = result.notDestroyed?.[identityId];
    if (failure) {
      throw new IdentitySetError(
        failure.type,
        failure.description,
        failure.properties ?? [],
      );
    }

    const next = new Map(this.identities);
    next.delete(identityId);
    this.identities = next;
  }

  /**
   * Delete the identity identified by `identityId`. Thin wrapper over
   * `destroyIdentity` used by the per-row kebab menu in the Settings
   * identity list (re #20). The synthesized "default" identity (id
   * "default", `mayDelete: false`) is not deletable — the server
   * refuses the destroy and the kebab hides the menu item — but we
   * guard here too so a stale UI cannot issue a doomed request. On
   * success the row is dropped from the local cache optimistically.
   */
  async deleteIdentity(identityId: string): Promise<void> {
    if (identityId === 'default') {
      throw new IdentitySetError(
        'forbidden',
        'the default identity cannot be deleted',
      );
    }
    await this.destroyIdentity(identityId);
  }

  /**
   * Update the display name of the identity identified by `identityId`
   * via `Identity/set update`, then mirror the change into the local
   * identities cache so compose / reply flows pick up the new name
   * immediately without a round-trip Identity/get.
   */
  async updateIdentityName(identityId: string, name: string): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          update: {
            [identityId]: { name },
          },
        },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[identityId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }

    // Optimistic mirror — update the cache so subsequent compose / reply
    // flows see the new name without waiting for a full reload.
    const next = new Map(this.identities);
    const cur = next.get(identityId);
    if (cur) next.set(identityId, { ...cur, name });
    this.identities = next;
  }

  /**
   * Update the avatar blob ID for the identity identified by `identityId`
   * via `Identity/set update`. Pass null to clear the avatar. Mirrors the
   * change into the local identities cache immediately (optimistic update)
   * so the settings view reflects the new state without a round-trip.
   *
   * This writes the herold extension property `avatarBlobId`. The server-
   * side handler lands separately; until it does, the server may return an
   * `invalidProperties` error which will surface as a toast from the caller.
   */
  async updateIdentityAvatar(identityId: string, blobId: string | null): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          update: {
            [identityId]: { avatarBlobId: blobId },
          },
        },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[identityId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }

    const next = new Map(this.identities);
    const cur = next.get(identityId);
    if (cur) next.set(identityId, { ...cur, avatarBlobId: blobId });
    this.identities = next;
  }

  /**
   * Promote `identityId` to be the principal's default From identity
   * per REQ-SET-IDENT-04. Issues a single `Identity/set update` that
   * flips `isDefault: true` on the new default and `isDefault: false`
   * on the previous default (if any). Mirrors the change into the
   * local identities cache so the compose From picker reflects the
   * new default without a refetch.
   *
   * Server-side property landing as part of REQ-IDENT-70 (TBD). Until
   * the server emits / honours `isDefault`, the optimistic cache update
   * lets the SPA exercise the UI flow; the server will surface a
   * `notUpdated` entry which the caller can map to a toast.
   */
  async setDefaultIdentity(identityId: string): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    // Compute the previous-default id so we can clear it in the same
    // batch — keeping the "exactly one default" invariant locally.
    let prevDefault: string | null = null;
    for (const cur of this.identities.values()) {
      if (cur.isDefault && cur.id !== identityId) {
        prevDefault = cur.id;
        break;
      }
    }

    const update: Record<string, { isDefault: boolean }> = {
      [identityId]: { isDefault: true },
    };
    if (prevDefault) update[prevDefault] = { isDefault: false };

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        { accountId, update },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[identityId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }

    // Optimistic mirror: clear the previous-default flag and set the new one.
    const next = new Map(this.identities);
    if (prevDefault) {
      const prev = next.get(prevDefault);
      if (prev) next.set(prevDefault, { ...prev, isDefault: false });
    }
    const cur = next.get(identityId);
    if (cur) next.set(identityId, { ...cur, isDefault: true });
    this.identities = next;
  }

  /**
   * Update the `xFaceEnabled` extension property for the identity identified
   * by `identityId` via `Identity/set update`. Mirrors the change into the
   * local identities cache immediately.
   */
  async updateIdentityXFaceEnabled(identityId: string, enabled: boolean): Promise<void> {
    const accountId = this.mailAccountId;
    if (!accountId) throw new Error('No Mail account on this session');

    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          update: {
            [identityId]: { xFaceEnabled: enabled },
          },
        },
        [Capability.Submission],
      );
    });
    strict(responses);

    const result = invocationArgs<{
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[identityId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }

    const next = new Map(this.identities);
    const cur = next.get(identityId);
    if (cur) next.set(identityId, { ...cur, xFaceEnabled: enabled });
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
      if (folder === 'important') {
        // Virtual folder: every email with the $important keyword,
        // regardless of which mailbox it lives in.
        filter = { hasKeyword: '$important' };
      } else if (folder === 'snoozed') {
        // Virtual folder: every email currently snoozed
        // ($snoozed keyword, set by the server alongside snoozedUntil).
        filter = { hasKeyword: '$snoozed' };
      } else if (folder !== 'all') {
        let mailboxId: string | null = null;
        if (ROLED_FOLDERS.has(folder)) {
          const role = FOLDER_ROLE[folder] ?? folder;
          const mailbox = this.#mailboxByRole(role);
          if (!mailbox) {
            throw new Error(`No ${FOLDER_LABEL[folder]} mailbox in this account`);
          }
          mailboxId = mailbox.id;
        } else if (this.mailboxes.has(folder)) {
          // Custom mailbox: folder is the Mailbox.id.
          mailboxId = folder;
        } else {
          throw new Error(`Unknown mailbox: ${folder}`);
        }
        filter = { inMailbox: mailboxId };
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
        const eg = b.call(
          'Email/get',
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: EMAIL_LIST_PROPERTIES,
          },
          [Capability.Mail],
        );
        // Fetch thread membership so the list can show per-thread message counts
        // (issue #64). The wildcard path extracts all threadIds from the email list.
        b.call(
          'Thread/get',
          {
            accountId,
            '#ids': eg.ref('/list/*/threadId'),
          },
          [Capability.Mail],
        );
      });
      strict(responses);

      const queryResult = invocationArgs<{ ids: string[] }>(responses[0]);
      const getResult = invocationArgs<{ list: Email[]; state: string }>(
        responses[1],
      );
      const threadResult = invocationArgs<{ list: Thread[] }>(responses[2]);

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;
      this.listEmailIds = queryResult.ids;
      if (typeof getResult.state === 'string') this.emailState = getResult.state;

      const nextThreads = new Map(this.threads);
      for (const t of threadResult.list) nextThreads.set(t.id, t);
      this.threads = nextThreads;

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
    return resolveThreadEmails(thread.emailIds, this.emails);
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
   * Toggle whether a single mailbox-as-label is attached to an email.
   * Unlike moveEmailToMailbox this preserves all other mailbox
   * memberships -- the message is multi-labelled. Used by the label
   * picker (REQ-LBL-10..13, issue #16). Optimistic; reverts on failure.
   */
  async setEmailLabel(
    emailId: string,
    mailboxId: string,
    on: boolean,
  ): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    const has = Boolean(email.mailboxIds[mailboxId]);
    if (has === on) return;
    const prev = { ...email.mailboxIds };
    const next: Record<string, true> = { ...prev };
    if (on) next[mailboxId] = true;
    else delete next[mailboxId];
    if (Object.keys(next).length === 0) return; // never strand an email
    this.#patchEmail(emailId, { mailboxIds: next });
    try {
      await this.#emailSetUpdate(emailId, { mailboxIds: next });
    } catch (err) {
      this.#patchEmail(emailId, { mailboxIds: prev });
      toast.show({
        message: errMessage(err, 'Label update failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /**
   * Bulk version of setEmailLabel: add or remove a single mailbox-label
   * across many emails. Other mailbox memberships are preserved per
   * email. Empty no-op when nothing would change.
   */
  async bulkSetLabel(
    ids: string[],
    mailboxId: string,
    on: boolean,
  ): Promise<void> {
    if (ids.length === 0) return;
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      const has = Boolean(e.mailboxIds[mailboxId]);
      if (has === on) continue;
      const next: Record<string, true> = { ...e.mailboxIds };
      if (on) next[mailboxId] = true;
      else delete next[mailboxId];
      if (Object.keys(next).length === 0) continue;
      prevById.set(id, { ...e.mailboxIds });
      updates[id] = { mailboxIds: next };
      this.#patchEmail(id, { mailboxIds: next });
    }
    if (Object.keys(updates).length === 0) return;
    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      const name = this.mailboxes.get(mailboxId)?.name ?? 'label';
      this.#summarizeBulk(
        on ? `labelled ${name}` : `unlabelled ${name}`,
        Object.keys(updates).length,
        failed,
      );
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { mailboxIds: prev });
      }
      toast.show({
        message: errMessage(err, 'Bulk label update failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
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
        newState?: string;
        destroyed?: string[] | null;
        notDestroyed?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      this.#captureEmailSetNewState(result);
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
      // Refresh mailbox counts (issue #24).
      this.#refreshMailboxesSoon();
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

  /**
   * Toggle select-all for the visible list (REQ-KEY-06 / issue #36).
   * If every id in `visibleIds` is already selected, clear the selection;
   * otherwise select all of them.
   */
  toggleSelectAllVisible(visibleIds: string[]): void {
    if (allVisibleSelected(visibleIds, this.listSelectedIds)) {
      this.listSelectedIds = new Set();
    } else {
      this.listSelectedIds = new Set(visibleIds);
    }
  }

  /** Clear the bulk selection set. */
  clearSelection(): void {
    if (this.listSelectedIds.size === 0) return;
    this.listSelectedIds = new Set();
  }

  /**
   * Replace the selection with every visible email matching a predicate.
   * Used by the message-list select dropdown's Read / Unread / Starred /
   * Unstarred entries (REQ-MAIL-LIST-SELECT, issue #10).
   */
  selectVisibleWhere(predicate: (email: Email) => boolean): void {
    const next = new Set<string>();
    for (const e of this.listEmails) {
      if (predicate(e)) next.add(e.id);
    }
    this.listSelectedIds = next;
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
      newState?: string;
      updated?: Record<string, unknown> | null;
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    this.#captureEmailSetNewState(result);
    const updated = Object.keys(result.updated ?? {});
    const failed: Record<string, string> = {};
    for (const [id, info] of Object.entries(result.notUpdated ?? {})) {
      failed[id] = setErrorToUserMessage(info);
    }
    // Refresh sidebar mailbox counts after a bulk mutation. Issue #24.
    this.#refreshMailboxesSoon();
    return { updated, failed };
  }

  /** Bulk archive: move the email out of Inbox and into Archive.
   * Inbox-only. Thread-scoped per REQ-MAIL-51: every email in each
   * affected thread is archived together, regardless of which row the
   * user clicked.
   *
   * The naive "just remove the inbox membership" patch fails on the
   * server with `invalidProperties: cannot remove all mailbox
   * memberships` because RFC 8621 §4.2 requires every Email to be in
   * at least one mailbox. We therefore add the Archive mailbox in the
   * same patch -- atomic from the JMAP /set perspective.
   */
  async bulkArchive(ids: string[]): Promise<void> {
    const inbox = this.inbox;
    const archive = this.archive;
    if (!inbox || ids.length === 0) return;
    if (!archive) {
      toast.show({
        message: 'Cannot archive: no Archive folder exists on this account.',
        kind: 'error',
        timeoutMs: 6000,
      });
      return;
    }
    ids = expandToThreadIds(ids, this.threads, this.emails);
    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      if (!e.mailboxIds[inbox.id]) continue;
      prevById.set(id, { ...e.mailboxIds });
      // RFC 8621 §4.2: at least one mailbox membership must remain.
      // Patch both keys in one /set update so the server sees a
      // valid post-patch membership set in a single atomic apply.
      updates[id] = {
        [`mailboxIds/${inbox.id}`]: null,
        [`mailboxIds/${archive.id}`]: true,
      };
      const next: Record<string, true> = { ...e.mailboxIds, [archive.id]: true };
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

  /**
   * Permanently destroy a list of emails (Email/set { destroy: [...] }).
   * Use only after the user confirms; there is no undo. Issue #29.
   */
  async bulkDestroy(ids: string[]): Promise<void> {
    if (ids.length === 0) return;
    const accountId = this.mailAccountId;
    if (!accountId) return;
    // Optimistic: drop from the visible list and the email cache.
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;
    const prevEmails = new Map<string, Email>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (e) prevEmails.set(id, e);
      this.#removeFromList(id);
      this.emails.delete(id);
    }
    this.clearSelection();
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Email/set', { accountId, destroy: ids }, [Capability.Mail]);
      });
      strict(responses);
      const result = invocationArgs<{
        newState?: string;
        destroyed?: string[] | null;
        notDestroyed?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);
      this.#captureEmailSetNewState(result);
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
        toast.show({
          message: `Deleted ${destroyed} message${destroyed === 1 ? '' : 's'}`,
        });
      }
      this.#refreshMailboxesSoon();
    } catch (err) {
      // Best-effort restore: put the rows back.
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
      for (const [id, e] of prevEmails) {
        this.emails.set(id, e);
      }
      toast.show({
        message: errMessage(err, 'Delete failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Permanently destroy one email; thin wrapper around bulkDestroy. */
  async destroyEmail(id: string): Promise<void> {
    return this.bulkDestroy([id]);
  }

  /** Bulk delete: replace every id's mailboxIds with `{<trashId>: true}`. */
  async bulkDelete(ids: string[]): Promise<void> {
    const trash = this.trash;
    if (!trash || ids.length === 0) return;
    // Thread-scoped per REQ-MAIL-52: delete every email in the thread.
    ids = expandToThreadIds(ids, this.threads, this.emails);
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

  /**
   * Mark every email in a thread as read or unread. Filters out emails
   * already in the desired state, then defers to the bulk path so a
   * single Email/set covers the whole thread.
   */
  async markThreadSeen(threadId: string, seen: boolean): Promise<void> {
    const ids: string[] = [];
    for (const e of this.threadEmails(threadId)) {
      const wasSeen = Boolean(e.keywords.$seen);
      if (wasSeen !== seen) ids.push(e.id);
    }
    if (ids.length === 0) return;
    return this.bulkSetSeen(ids, seen);
  }

  /**
   * Mark this message and every later message in the same thread as unread
   * (REQ-MAIL-133a). The anchor is the chosen message; emails whose
   * `receivedAt >= anchor.receivedAt` AND currently `$seen=true` are flipped
   * to unread. Optimistic with Undo: the undo restores each affected email's
   * prior `keywords` map.
   */
  async markUnreadFromHere(threadId: string, anchorEmailId: string): Promise<void> {
    const ids = pickEmailsToMarkUnreadFromHere(this.threadEmails(threadId), anchorEmailId);
    if (ids.length === 0) return;

    const updates: Record<string, Record<string, unknown>> = {};
    const prevById = new Map<string, Record<string, true | undefined>>();
    for (const id of ids) {
      const e = this.emails.get(id);
      if (!e) continue;
      prevById.set(id, { ...e.keywords });
      updates[id] = { 'keywords/$seen': null };
      const nextKeywords: Record<string, true | undefined> = { ...e.keywords };
      delete nextKeywords.$seen;
      this.#patchEmail(id, { keywords: nextKeywords });
    }
    if (Object.keys(updates).length === 0) return;

    try {
      const { failed } = await this.#emailSetUpdateBulk(updates);
      this.#summarizeBulk('marked unread', Object.keys(updates).length, failed, async () => {
        const undoUpdates: Record<string, Record<string, unknown>> = {};
        for (const [id, prev] of prevById) {
          undoUpdates[id] = { 'keywords/$seen': prev.$seen ? true : null };
          this.#patchEmail(id, { keywords: prev });
        }
        try {
          await this.#emailSetUpdateBulk(undoUpdates);
        } catch (err) {
          toast.show({
            message: errMessage(err, 'Undo failed'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
      });
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { keywords: prev });
      }
      toast.show({
        message: errMessage(err, 'Mark unread failed'),
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

  /**
   * Ensure the `threads` map contains entries for every thread ID in the
   * given list. Thread IDs already present are skipped; missing ones are
   * fetched in a single `Thread/get` call. Only the thread membership
   * record (id + emailIds) is fetched here — body content is not loaded.
   *
   * This is necessary for the drag-and-drop move path (re #52): the thread
   * list renders one row per thread (`collapseThreads: true`), so only the
   * representative email ID is known until the thread has been explicitly
   * opened. Without pre-caching, `expandToThreadIds` cannot see the rest of
   * the thread's emails and moves only the one representative message.
   */
  async #ensureThreadsCached(threadIds: string[]): Promise<void> {
    const missing = threadIds.filter((tid) => !this.threads.has(tid));
    if (missing.length === 0) return;
    const accountId = this.mailAccountId;
    if (!accountId) return;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Thread/get', { accountId, ids: missing }, [Capability.Mail]);
      });
      const result = invocationArgs<{ list: Thread[] }>(responses[0]);
      const nextThreads = new Map(this.threads);
      for (const t of result.list) nextThreads.set(t.id, t);
      this.threads = nextThreads;
    } catch {
      // Best-effort: if Thread/get fails the move will still proceed but
      // will only cover the representative email for uncached threads.
    }
  }

  /** Bulk move: replace every id's mailboxIds with `{[targetId]: true}`.
   * Thread-scoped per REQ-MAIL-54: a thread move relocates every email
   * in the thread (including replies and the original message), not just
   * the one whose row the user dragged or right-clicked. */
  async bulkMoveToMailbox(ids: string[], targetId: string): Promise<void> {
    if (ids.length === 0) return;
    // Fetch thread membership for any threads not already in the cache so
    // expandToThreadIds can expand to all emails in the thread (re #52).
    const threadIds = [
      ...new Set(
        ids
          .map((id) => this.emails.get(id)?.threadId)
          .filter((tid): tid is string => tid !== undefined),
      ),
    ];
    await this.#ensureThreadsCached(threadIds);
    ids = expandToThreadIds(ids, this.threads, this.emails);
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
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;
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
      const okIds = Object.keys(updates).filter((id) => !(id in failed));
      this.#summarizeBulk(
        `moved to ${targetName}`,
        Object.keys(updates).length,
        failed,
        okIds.length > 0
          ? async () => {
              const undoUpdates: Record<string, Record<string, unknown>> = {};
              for (const id of okIds) {
                const prev = prevById.get(id);
                if (prev) undoUpdates[id] = { mailboxIds: prev };
              }
              try {
                await this.#emailSetUpdateBulk(undoUpdates);
                for (const id of okIds) {
                  const prev = prevById.get(id);
                  if (prev) this.#patchEmail(id, { mailboxIds: prev });
                }
                this.listEmailIds = prevListIds;
                this.listFocusedIndex = prevFocused;
              } catch (err) {
                toast.show({
                  message: errMessage(err, 'Undo failed'),
                  kind: 'error',
                  timeoutMs: 6000,
                });
              }
            }
          : undefined,
      );
    } catch (err) {
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { mailboxIds: prev });
      }
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
      toast.show({
        message: errMessage(err, 'Bulk move failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Render a "X messages <verb>" / partial-failure toast for bulk ops. */
  #summarizeBulk(
    verb: string,
    total: number,
    failed: Record<string, string>,
    undo?: () => void | Promise<void>,
  ): void {
    const failCount = Object.keys(failed).length;
    const ok = total - failCount;
    if (failCount > 0) {
      toast.show({
        message: `${ok} ${verb}, ${failCount} failed`,
        kind: 'error',
        timeoutMs: 6000,
        ...(undo ? { undo } : {}),
      });
    } else {
      toast.show({
        message: `${ok} message${ok === 1 ? '' : 's'} ${verb}`,
        ...(undo ? { undo } : {}),
      });
    }
  }

  /**
   * Snooze an email until the given ISO date. Sets `snoozedUntil` on
   * the message; the server pairs that with the $snoozed keyword and
   * removes both when the wake-up timer fires (or when the user
   * unsnooze early).
   */
  async snoozeEmail(emailId: string, until: Date): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    const iso = until.toISOString();
    const prevUntil = email.snoozedUntil;
    this.#patchEmail(emailId, { snoozedUntil: iso });
    if (this.listFolder === 'inbox') this.#removeFromList(emailId);
    try {
      await this.#emailSetUpdate(emailId, { snoozedUntil: iso });
      toast.show({
        message: `Snoozed until ${formatSnoozeTarget(until)}`,
        undo: async () => {
          try {
            await this.#emailSetUpdate(emailId, { snoozedUntil: null });
            this.#patchEmail(emailId, { snoozedUntil: null });
          } catch (err) {
            toast.show({
              message: errMessage(err, 'Undo failed'),
              kind: 'error',
              timeoutMs: 6000,
            });
          }
        },
      });
    } catch (err) {
      this.#patchEmail(emailId, { snoozedUntil: prevUntil ?? null });
      toast.show({
        message: errMessage(err, 'Snooze failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Wake an email from snooze immediately. */
  async unsnoozeEmail(emailId: string): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email || !email.snoozedUntil) return;
    const prev = email.snoozedUntil;
    this.#patchEmail(emailId, { snoozedUntil: null });
    if (this.listFolder === 'snoozed') this.#removeFromList(emailId);
    try {
      await this.#emailSetUpdate(emailId, { snoozedUntil: null });
    } catch (err) {
      this.#patchEmail(emailId, { snoozedUntil: prev });
      toast.show({
        message: errMessage(err, 'Unsnooze failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /**
   * Apply a category keyword to an email (or every email in the thread when
   * `threadGranular` is true). Sets `$category-<name>` and removes all other
   * `$category-*` keywords. Optimistic; reverts on failure.
   *
   * REQ-CAT-20..22: used by the "Move to category" action and the `m` shortcut.
   */
  async setCategoryKeyword(
    emailId: string,
    categoryKeyword: string | null,
    threadGranular: boolean,
  ): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;

    // Collect the ids to patch (thread-granular or single).
    const targetIds: string[] = threadGranular
      ? (this.threads.get(email.threadId)?.emailIds ?? [emailId])
      : [emailId];

    // Build the keyword patches for each target.
    const prevById = new Map<string, Record<string, true | undefined>>();
    const updates: Record<string, Record<string, unknown>> = {};

    for (const id of targetIds) {
      const e = this.emails.get(id);
      if (!e) continue;
      prevById.set(id, { ...e.keywords });

      // Remove all existing $category-* keywords.
      const nextKeywords: Record<string, true | undefined> = {};
      for (const [kw, v] of Object.entries(e.keywords)) {
        if (!kw.startsWith('$category-')) nextKeywords[kw] = v;
      }
      if (categoryKeyword) {
        nextKeywords[categoryKeyword] = true;
      }
      this.#patchEmail(id, { keywords: nextKeywords });

      // Build the Email/set patches: null each old $category-* key, then set new.
      const setPatches: Record<string, unknown> = {};
      for (const kw of Object.keys(e.keywords)) {
        if (kw.startsWith('$category-')) {
          setPatches[`keywords/${kw}`] = null;
        }
      }
      if (categoryKeyword) {
        setPatches[`keywords/${categoryKeyword}`] = true;
      }
      updates[id] = setPatches;
    }

    if (Object.keys(updates).length === 0) return;

    try {
      await this.#emailSetUpdateBulk(updates);
    } catch (err) {
      // Revert all patches.
      for (const [id, prev] of prevById) {
        this.#patchEmail(id, { keywords: prev });
      }
      toast.show({
        message: errMessage(err, 'Move to category failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /** Toggle the $important keyword. No toast (toggle is itself the undo). */
  async toggleImportant(emailId: string): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;
    const wasImportant = Boolean(email.keywords.$important);
    const nextKeywords = { ...email.keywords };
    if (wasImportant) delete nextKeywords.$important;
    else nextKeywords.$important = true;
    this.#patchEmail(emailId, { keywords: nextKeywords });
    try {
      await this.#emailSetUpdate(emailId, {
        'keywords/$important': wasImportant ? null : true,
      });
    } catch (err) {
      this.#patchEmail(emailId, { keywords: email.keywords });
      toast.show({
        message: errMessage(err, 'Mark important failed'),
        kind: 'error',
        timeoutMs: 6000,
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

  /**
   * Add or remove the current user's reaction on an email.
   * Optimistic: applies the change locally, fires `Email/set` with a
   * JSON-patch path, reverts and toasts on failure.
   *
   * Per REQ-MAIL-171/173: `reactions/<emoji>/<principalId>: true` to add,
   * `... null` to remove. A `forbidden` response means the server rejected
   * a mutation of someone else's entry — should not occur via this UI path
   * but handled defensively.
   */
  async toggleReaction(emailId: string, emoji: string, principalId: string): Promise<void> {
    const email = this.emails.get(emailId);
    if (!email) return;

    const prevReactions = email.reactions ? { ...email.reactions } : {};
    const reactors = prevReactions[emoji] ?? [];
    const alreadyReacted = reactors.includes(principalId);

    // Optimistic patch.
    const nextReactions: Record<string, string[]> = { ...prevReactions };
    if (alreadyReacted) {
      const filtered = reactors.filter((p) => p !== principalId);
      if (filtered.length === 0) {
        delete nextReactions[emoji];
      } else {
        nextReactions[emoji] = filtered;
      }
    } else {
      nextReactions[emoji] = [...reactors, principalId];
    }
    this.#patchEmail(emailId, { reactions: nextReactions });

    try {
      await this.#emailSetUpdate(emailId, {
        [`reactions/${emoji}/${principalId}`]: alreadyReacted ? null : true,
      });
    } catch (err) {
      // Revert the optimistic patch.
      this.#patchEmail(emailId, { reactions: Object.keys(prevReactions).length === 0 ? null : prevReactions });
      toast.show({
        message: errMessage(err, 'React failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }

  /**
   * Report spam (REQ-MAIL-135): sets $junk keyword optimistically, moves
   * the email to the Junk mailbox, and posts a feedback signal to the
   * spam classifier endpoint if available. Undo toast reverts both moves.
   *
   * NOTE: The spam-feedback HTTP endpoint (/api/v1/spam-feedback) described
   * in the Wave 3.15 plan was not implemented server-side in commits c799e7a
   * or 14cca4f. The POST is attempted but silently dropped on 404/501 so
   * the user-visible flow is not blocked. This is documented as a server-side
   * gap in the Wave 3.15 implementation report.
   */
  async reportSpam(emailId: string, kind: 'spam' | 'phishing' = 'spam'): Promise<void> {
    const email = this.emails.get(emailId);
    const junkMailbox = this.junk;
    if (!email) return;

    const prevMailboxIds = { ...email.mailboxIds };
    const prevKeywords = { ...email.keywords };
    const prevListIds = [...this.listEmailIds];
    const prevFocused = this.listFocusedIndex;

    // Optimistic apply: add $junk (and $phishing for phishing reports).
    const nextKeywords: Record<string, true | undefined> = { ...prevKeywords, $junk: true };
    if (kind === 'phishing') nextKeywords.$phishing = true;
    const nextMailboxIds = junkMailbox
      ? { [junkMailbox.id]: true as const }
      : { ...prevMailboxIds };

    this.#patchEmail(emailId, { keywords: nextKeywords, mailboxIds: nextMailboxIds });
    this.#removeFromList(emailId);

    const revert = (): void => {
      this.#patchEmail(emailId, { keywords: prevKeywords, mailboxIds: prevMailboxIds });
      this.listEmailIds = prevListIds;
      this.listFocusedIndex = prevFocused;
    };

    try {
      const patches: Record<string, unknown> = { 'keywords/$junk': true };
      if (kind === 'phishing') patches['keywords/$phishing'] = true;
      if (junkMailbox) {
        // Move to junk mailbox by replacing mailboxIds.
        patches.mailboxIds = { [junkMailbox.id]: true };
      }
      await this.#emailSetUpdate(emailId, patches);
    } catch (err) {
      revert();
      toast.show({
        message: errMessage(err, 'Report failed'),
        kind: 'error',
        timeoutMs: 6000,
      });
      return;
    }

    // Fire the spam-feedback endpoint (advisory — ignore errors).
    void this.#postSpamFeedback(emailId, kind);

    const label = kind === 'phishing' ? 'Reported as phishing' : 'Reported as spam';
    toast.show({
      message: label,
      undo: async () => {
        try {
          const undoPatches: Record<string, unknown> = {
            'keywords/$junk': null,
            mailboxIds: prevMailboxIds,
          };
          if (kind === 'phishing') undoPatches['keywords/$phishing'] = null;
          await this.#emailSetUpdate(emailId, undoPatches);
          this.#patchEmail(emailId, { keywords: prevKeywords, mailboxIds: prevMailboxIds });
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

  /** Report phishing: delegates to reportSpam with kind='phishing'. */
  async reportPhishing(emailId: string): Promise<void> {
    return this.reportSpam(emailId, 'phishing');
  }

  /**
   * Post a spam-feedback signal to the server. The endpoint
   * (/api/v1/spam-feedback) is advisory and not yet implemented server-side
   * in Wave 3.15 (gap documented in implementation report). Errors are
   * silently swallowed so the user-visible report-spam flow is unaffected.
   */
  async #postSpamFeedback(emailId: string, kind: 'spam' | 'phishing'): Promise<void> {
    try {
      await fetch('/api/v1/spam-feedback', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ emailId, kind }),
      });
    } catch {
      // Network error or endpoint absent — silently drop.
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
      newState?: string;
      updated?: Record<string, unknown> | null;
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    this.#captureEmailSetNewState(result);
    const failure = result.notUpdated?.[emailId];
    if (failure) {
      throw new Error(setErrorToUserMessage(failure));
    }
    // Sidebar mailbox counters (totalEmails / unreadEmails) are
    // server-computed; refresh them after every successful Email/set
    // so the counts in App.svelte don't drift out of sync. Issue #24.
    this.#refreshMailboxesSoon();
  }

  /**
   * Capture the post-mutation Email state from a Foo/set response so the
   * EventSource StateChange handler dedupes the event we triggered
   * ourselves. Without this, every Email/set bumps the server's Email
   * state, the eventsource emits a StateChange a moment later, the
   * handler at #onEmailStateChange sees newState !== this.emailState,
   * and runs refreshFolder — which clears listEmailIds and re-fetches
   * the list, producing a visible flicker on every star / archive /
   * delete. The optimistic patch already updated the UI; the spurious
   * refresh adds nothing.
   *
   * The newState field is mandatory in JMAP Foo/set responses (RFC 8620
   * §5.3) but old servers may not echo it; the callers fall through to
   * the legacy refresh-on-StateChange path when newState is absent.
   */
  #captureEmailSetNewState(result: { newState?: string }): void {
    if (typeof result.newState === 'string' && result.newState !== '') {
      this.emailState = result.newState;
    }
  }

  /** Same as #captureEmailSetNewState but for Mailbox/set responses. */
  #captureMailboxSetNewState(result: { newState?: string }): void {
    if (typeof result.newState === 'string' && result.newState !== '') {
      this.mailboxState = result.newState;
    }
  }

  /**
   * Coalesce mailbox-count refreshes so a burst of Email/set calls (e.g.
   * a bulk operation followed by individual UI updates) only triggers
   * one Mailbox/get round-trip. Errors are swallowed: a stale count is
   * cosmetic, not catastrophic, and the EventSource Mailbox handler is
   * still around as a backstop. Issue #24.
   */
  #refreshMailboxesPending = false;
  #refreshMailboxesSoon(): void {
    if (this.#refreshMailboxesPending) return;
    this.#refreshMailboxesPending = true;
    queueMicrotask(() => {
      this.#refreshMailboxesPending = false;
      void this.loadMailboxes().catch((err) => {
        console.warn('mailbox count refresh failed', err);
      });
    });
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

  /**
   * Evaluate whether a newly-arrived email should trigger the mail audio cue.
   *
   * Gates:
   *   - email is in the inbox mailbox
   *   - not from the user themselves
   *   - focus gate: not (visible AND inbox is the active view)
   *
   * Quiet-hours gate (REQ-PUSH-97) deferred; see shouldPlayMailCue TODO.
   */
  #shouldMailCue(email: Email): boolean {
    const inboxId = this.inbox?.id ?? null;
    const senderEmail = email.from?.[0]?.email ?? null;
    const ownEmails = new Set<string>(
      Array.from(this.identities.values()).map((id) => id.email),
    );

    const documentVisible =
      typeof document !== 'undefined' &&
      document.visibilityState === 'visible';
    // Inbox is focused when visible and the route is /mail (default) or
    // /mail/folder/inbox specifically.
    const inboxFocused =
      documentVisible &&
      router.parts[0] === 'mail' &&
      (router.parts[1] === undefined ||
        (router.parts[1] === 'folder' && router.parts[2] === 'inbox'));

    return shouldPlayMailCue({
      mailboxIds: email.mailboxIds,
      inboxMailboxId: inboxId,
      senderEmail,
      ownEmails,
      inboxFocused,
    });
  }

  /**
   * Register a visibilitychange listener that re-syncs mailbox counters
   * when the tab comes back into focus, keeping the favicon badge and
   * title accurate after the tab was hidden (re #36).
   *
   * Returns the unmount function; call it to remove the listener.
   * Idempotent: a second call returns the existing unmount without
   * registering a duplicate listener.
   */
  #visibilityUnmount: (() => void) | null = null;
  installVisibilitySync(): () => void {
    if (this.#visibilityUnmount !== null) return this.#visibilityUnmount;
    if (typeof document === 'undefined') return () => {};
    const onVisible = (): void => {
      if (document.visibilityState !== 'visible') return;
      this.#refreshMailboxesSoon();
    };
    document.addEventListener('visibilitychange', onVisible);
    this.#visibilityUnmount = (): void => {
      document.removeEventListener('visibilitychange', onVisible);
      this.#visibilityUnmount = null;
    };
    return this.#visibilityUnmount;
  }

  /**
   * Fire an OS-level desktop notification for a newly-arrived email
   * while this tab is open. Called only when the user has enabled
   * `settings.desktopNotifEnabled` and the browser has granted
   * `Notification.permission === 'granted'` (re #23a).
   */
  #fireDesktopNotification(email: Email): void {
    if (typeof Notification === 'undefined') return;
    if (Notification.permission !== 'granted') return;
    const sender =
      email.from?.[0]?.name?.trim() ||
      email.from?.[0]?.email ||
      '';
    const subject = email.subject || i18n.t('thread.subject.none');
    const title = sender ? `${sender}: ${subject}` : subject;
    try {
      new Notification(title, {
        body: email.preview ?? undefined,
        tag: `mail-${email.id}`,
      });
    } catch {
      // Browser policy (e.g. secure-context check) may reject; swallow
      // silently so the sound cue path is unaffected.
    }
  }
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

/**
 * Format a snooze target relative to now: "3:00 pm tomorrow",
 * "Mon May 12 8:00 am". Used by the snooze toast's confirmation
 * message.
 */
/**
 * Role values JMAP defines for system-purpose mailboxes (RFC 8621
 * §2.1.4) plus the suite-side virtual "snooze" / "important" role.
 * Mailboxes carrying any of these are system mailboxes and the
 * sidebar must not offer rename / delete affordances on them
 * (issue #32).
 */
const SYSTEM_ROLES: ReadonlySet<string> = new Set([
  'inbox',
  'archive',
  'drafts',
  'sent',
  'trash',
  'junk',
  'spam',
  'important',
  'flagged',
  'all',
  'snoozed',
  'outbox',
  'subscribed',
  'templates',
]);

/** True when the role string identifies a system-purpose mailbox. */
export function isSystemRole(role: string | null | undefined): boolean {
  if (!role) return false;
  return SYSTEM_ROLES.has(role.toLowerCase());
}

/**
 * Splice `inMailboxOtherThan: [<trash>, <junk>]` into `parsed` so the
 * default search scope excludes those mailboxes (REQ-SRC-06). Returns
 * the original filter unchanged when neither role exists for this
 * principal — there's nothing to exclude.
 *
 * When `parsed` is itself a flat FilterCondition (no `operator` key),
 * the exclusion is spliced in directly: a single FilterCondition with
 * multiple keys is implicitly AND-ed per RFC 8621 §5.5, and the flat
 * shape is what the server's fast-pushability gate
 * (`internal/protojmap/mail/email/fastquery.go:mergeFilterIntoOpts`)
 * recognises. Wrapping with `{operator: 'AND', conditions: [...]}`
 * dropped every default-scoped search to the slow `listPrincipalMessages`
 * path even for trivially indexable predicates such as `before:` —
 * see REQ-PERF-INDEX-09 and the matching server-side flatten in
 * REQ-PERF-INDEX-10. When `parsed` is already a FilterOperator
 * (`{operator, conditions}`), we wrap with AND because we cannot
 * push a sibling key into the operator shape.
 */
export function applyTrashJunkExclusion(
  parsed: FilterCondition | FilterOperator,
  mailboxes: Map<string, Mailbox>,
): FilterCondition | FilterOperator {
  const exclude: string[] = [];
  for (const m of mailboxes.values()) {
    if (m.role === 'trash' || m.role === 'junk') exclude.push(m.id);
  }
  if (exclude.length === 0) return parsed;
  if (!('operator' in parsed)) {
    return { ...parsed, inMailboxOtherThan: exclude };
  }
  return {
    operator: 'AND',
    conditions: [parsed, { inMailboxOtherThan: exclude }],
  };
}

function formatSnoozeTarget(d: Date): string {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const target = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const dayDiff = Math.round((target.getTime() - today.getTime()) / 86400000);
  const tag = localeTag();
  const time = d.toLocaleTimeString(tag, {
    hour: 'numeric',
    minute: '2-digit',
  });
  if (dayDiff === 0) return time;
  if (dayDiff === 1) return `${time} tomorrow`;
  if (dayDiff < 7 && dayDiff > 0) {
    return `${d.toLocaleDateString(tag, { weekday: 'long' })}, ${time}`;
  }
  return `${d.toLocaleDateString(tag, {
    month: 'short',
    day: 'numeric',
  })}, ${time}`;
}

function errMessage(err: unknown, fallback: string): string {
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}

/** Render a JMAP `setError` into a user-facing message.
 *
 * The server-supplied `description` field (when present and human-
 * readable) wins. Otherwise we look up a localised string keyed on
 * the `type` enum value (`mail.setError.<type>`), so users see
 * "Diese Nachricht gibt es nicht mehr." instead of the raw
 * "notFound" code that the JMAP wire format uses. Falls back to a
 * generic "Something went wrong" key when the type is unknown
 * (issue #17).
 */
function setErrorToUserMessage(info: { type: string; description?: string }): string {
  if (info.description && info.description.length > 0) {
    return info.description;
  }
  const knownKeys: Record<string, string> = {
    notFound: 'mail.setError.notFound',
    forbidden: 'mail.setError.forbidden',
    invalidProperties: 'mail.setError.invalidProperties',
    overQuota: 'mail.setError.overQuota',
    tooManyMailboxes: 'mail.setError.tooManyMailboxes',
    mailboxHasChild: 'mail.setError.mailboxHasChild',
    serverFail: 'mail.setError.serverFail',
  };
  const key = knownKeys[info.type] ?? 'mail.setError.unknown';
  return i18n.t(key);
}

/**
 * Resolve a thread's email-id list against the email cache, deduplicating
 * any repeated ids. JMAP servers are not supposed to return duplicate
 * emailIds in a Thread object, but at least one server version did, which
 * caused Svelte's keyed `{#each}` block to throw `each_key_duplicate`
 * (issue #40). Deduplication here is cheap and defensive; it preserves
 * the first occurrence of each id (i.e. display order is maintained).
 */
export function resolveThreadEmails(emailIds: string[], emails: Map<string, Email>): Email[] {
  const seen = new Set<string>();
  const out: Email[] = [];
  for (const id of emailIds) {
    if (seen.has(id)) continue;
    seen.add(id);
    const e = emails.get(id);
    if (e) out.push(e);
  }
  return out;
}

/**
 * Returns true when every id in `visibleIds` is present in `selected`
 * AND `visibleIds` is non-empty. Used by toggleSelectAllVisible to
 * decide whether to clear or set the selection.
 */
export function allVisibleSelected(visibleIds: string[], selected: Set<string>): boolean {
  if (visibleIds.length === 0) return false;
  return visibleIds.every((id) => selected.has(id));
}

/**
 * Pure helper for `markUnreadFromHere` (REQ-MAIL-133a). Given the list of
 * emails in a thread and an anchor email id, returns the ids that should
 * be flipped to `$seen=null`: emails whose `receivedAt >= anchor.receivedAt`
 * AND that are currently `$seen=true`. Anchor itself is included when it
 * is currently seen. Emails with unparseable timestamps are skipped.
 */
export function pickEmailsToMarkUnreadFromHere(
  threadEmails: readonly Email[],
  anchorEmailId: string,
): string[] {
  const anchor = threadEmails.find((e) => e.id === anchorEmailId);
  if (!anchor) return [];
  const anchorTime = new Date(anchor.receivedAt).getTime();
  if (!Number.isFinite(anchorTime)) return [];
  const ids: string[] = [];
  for (const e of threadEmails) {
    if (!e.keywords.$seen) continue;
    const t = new Date(e.receivedAt).getTime();
    if (!Number.isFinite(t)) continue;
    if (t >= anchorTime) ids.push(e.id);
  }
  return ids;
}

/**
 * Expand a set of email ids to every email in their thread(s) so a
 * thread-scoped bulk operation (move, archive, delete) acts on the
 * whole conversation rather than just the row the user clicked
 * (REQ-MAIL-51, REQ-MAIL-52, REQ-MAIL-54).
 *
 * Each affected thread is expanded once (no duplicate work across rows
 * of the same thread). Email ids whose thread is not loaded — or whose
 * thread record is empty — pass through as-is so a partial cache can
 * still drive a single-message operation. Order is "id encounter
 * order" with thread members appended in their stored order.
 */
export function expandToThreadIds(
  ids: string[],
  threads: Map<string, { emailIds: readonly string[] }>,
  emails: Map<string, { threadId: string }>,
): string[] {
  const out: string[] = [];
  const seenEmailIds = new Set<string>();
  const seenThreadIds = new Set<string>();
  for (const id of ids) {
    const e = emails.get(id);
    if (!e) {
      // Unknown email: pass through so the caller's existing single-id
      // behaviour is preserved when the thread cache is incomplete.
      if (!seenEmailIds.has(id)) {
        seenEmailIds.add(id);
        out.push(id);
      }
      continue;
    }
    if (seenThreadIds.has(e.threadId)) continue;
    seenThreadIds.add(e.threadId);
    const t = threads.get(e.threadId);
    if (!t || t.emailIds.length === 0) {
      if (!seenEmailIds.has(id)) {
        seenEmailIds.add(id);
        out.push(id);
      }
      continue;
    }
    for (const tid of t.emailIds) {
      if (seenEmailIds.has(tid)) continue;
      seenEmailIds.add(tid);
      out.push(tid);
    }
  }
  return out;
}

export const mail = new MailStore();

/** Exported purely for unit tests; not part of the public surface. */
export const _internals_forTest = {
  errMessage,
  allVisibleSelected,
  resolveThreadEmails,
  expandToThreadIds,
  setErrorToUserMessage,
};
