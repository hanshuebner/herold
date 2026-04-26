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

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

class MailStore {
  mailboxes = $state(new Map<string, Mailbox>());
  emails = $state(new Map<string, Email>());
  threads = $state(new Map<string, Thread>());
  identities = $state(new Map<string, Identity>());

  /** Ordered (most-recent first) email ids visible in the current inbox view. */
  inboxEmailIds = $state<string[]>([]);
  inboxLoadStatus = $state<LoadStatus>('idle');
  inboxError = $state<string | null>(null);

  /** Per-thread load status keyed by threadId. */
  threadLoadStatus = $state(new Map<string, LoadStatus>());
  threadLoadError = $state(new Map<string, string>());

  /** Index into inboxEmailIds of the keyboard-focused row; -1 = none. */
  inboxFocusedIndex = $state<number>(-1);

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
    // First-iteration sync: the cheapest correct thing is to refresh the
    // active inbox view. Email/changes-driven incremental update lands
    // when other views (labels / search / threads) start needing it.
    if (this.inboxLoadStatus === 'ready') {
      try {
        await this.refreshInbox();
      } catch (err) {
        console.error('inbox refresh after state change failed', err);
      }
    }
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
  focusInboxNext(): string | null {
    if (this.inboxEmailIds.length === 0) return null;
    const next =
      this.inboxFocusedIndex < 0
        ? 0
        : Math.min(this.inboxFocusedIndex + 1, this.inboxEmailIds.length - 1);
    this.inboxFocusedIndex = next;
    return this.inboxEmailIds[next] ?? null;
  }

  /** Move focus to the previous row, clamped. */
  focusInboxPrev(): string | null {
    if (this.inboxEmailIds.length === 0) return null;
    const next =
      this.inboxFocusedIndex < 0 ? 0 : Math.max(this.inboxFocusedIndex - 1, 0);
    this.inboxFocusedIndex = next;
    return this.inboxEmailIds[next] ?? null;
  }

  /** The threadId of the currently-focused inbox row, or null. */
  focusedInboxThreadId(): string | null {
    const emailId = this.inboxEmailIds[this.inboxFocusedIndex];
    if (!emailId) return null;
    return this.emails.get(emailId)?.threadId ?? null;
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

  inboxEmails = $derived(
    this.inboxEmailIds
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
   * Initial inbox load: fetch mailboxes if needed, then run a batched
   * Email/query + Email/get for the inbox's most recent threads (collapsed,
   * one Email per thread). Idempotent for already-loaded inboxes.
   */
  async loadInbox(): Promise<void> {
    if (this.inboxLoadStatus === 'loading') return;
    if (this.inboxLoadStatus === 'ready') return;
    this.inboxLoadStatus = 'loading';
    this.inboxError = null;
    try {
      // Mailboxes + identities both feed compose / list-rendering paths;
      // load them in parallel on first use.
      const setup: Promise<unknown>[] = [];
      if (this.mailboxes.size === 0) setup.push(this.loadMailboxes());
      if (this.identities.size === 0) setup.push(this.loadIdentities());
      if (setup.length > 0) await Promise.all(setup);
      const inbox = this.inbox;
      if (!inbox) {
        throw new Error('No inbox mailbox in this account');
      }
      const accountId = this.mailAccountId;
      if (!accountId) throw new Error('No Mail account on this session');

      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Email/query',
          {
            accountId,
            filter: { inMailbox: inbox.id },
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
      const getResult = invocationArgs<{ list: Email[]; state: string }>(
        responses[1],
      );

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;
      this.inboxEmailIds = queryResult.ids;
      if (typeof getResult.state === 'string') this.emailState = getResult.state;
      this.inboxLoadStatus = 'ready';
    } catch (err) {
      this.inboxLoadStatus = 'error';
      this.inboxError = err instanceof Error ? err.message : String(err);
    }
  }

  /** Force a refresh of the inbox view. Drops cached state for the view. */
  async refreshInbox(): Promise<void> {
    this.inboxLoadStatus = 'idle';
    this.inboxEmailIds = [];
    await this.loadInbox();
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
    const prevInboxIds = [...this.inboxEmailIds];
    const prevFocused = this.inboxFocusedIndex;

    // Optimistic apply
    const nextMailboxIds = { ...prevMailboxIds };
    delete nextMailboxIds[inbox.id];
    this.#patchEmail(emailId, { mailboxIds: nextMailboxIds });
    this.#removeFromInbox(emailId);

    const revert = (): void => {
      this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
      this.inboxEmailIds = prevInboxIds;
      this.inboxFocusedIndex = prevFocused;
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
          this.inboxEmailIds = prevInboxIds;
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
    const prevInboxIds = [...this.inboxEmailIds];
    const prevFocused = this.inboxFocusedIndex;

    this.#patchEmail(emailId, { mailboxIds: { [trash.id]: true } });
    this.#removeFromInbox(emailId);

    const revert = (): void => {
      this.#patchEmail(emailId, { mailboxIds: prevMailboxIds });
      this.inboxEmailIds = prevInboxIds;
      this.inboxFocusedIndex = prevFocused;
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
          this.inboxEmailIds = prevInboxIds;
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

  #removeFromInbox(emailId: string): void {
    const idx = this.inboxEmailIds.indexOf(emailId);
    if (idx < 0) return;
    this.inboxEmailIds = [
      ...this.inboxEmailIds.slice(0, idx),
      ...this.inboxEmailIds.slice(idx + 1),
    ];
    // Clamp focus to the new bounds.
    if (this.inboxFocusedIndex >= this.inboxEmailIds.length) {
      this.inboxFocusedIndex = this.inboxEmailIds.length - 1;
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
