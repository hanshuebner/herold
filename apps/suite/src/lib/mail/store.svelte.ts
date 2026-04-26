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
import { Capability, type Invocation } from '../jmap/types';
import {
  EMAIL_BODY_PROPERTIES,
  EMAIL_LIST_PROPERTIES,
  type Email,
  type Mailbox,
  type Thread,
} from './types';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

class MailStore {
  mailboxes = $state(new Map<string, Mailbox>());
  emails = $state(new Map<string, Email>());
  threads = $state(new Map<string, Thread>());

  /** Ordered (most-recent first) email ids visible in the current inbox view. */
  inboxEmailIds = $state<string[]>([]);
  inboxLoadStatus = $state<LoadStatus>('idle');
  inboxError = $state<string | null>(null);

  /** Per-thread load status keyed by threadId. */
  threadLoadStatus = $state(new Map<string, LoadStatus>());
  threadLoadError = $state(new Map<string, string>());

  /** Index into inboxEmailIds of the keyboard-focused row; -1 = none. */
  inboxFocusedIndex = $state<number>(-1);

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
    for (const m of this.mailboxes.values()) {
      if (m.role === 'inbox') return m;
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

    const args = invocationArgs<{ list: Mailbox[] }>(responses[0]);
    const next = new Map<string, Mailbox>();
    for (const m of args.list) next.set(m.id, m);
    this.mailboxes = next;
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
      if (this.mailboxes.size === 0) {
        await this.loadMailboxes();
      }
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
      const getResult = invocationArgs<{ list: Email[] }>(responses[1]);

      const next = new Map(this.emails);
      for (const e of getResult.list) next.set(e.id, e);
      this.emails = next;
      this.inboxEmailIds = queryResult.ids;
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
      const emailResult = invocationArgs<{ list: Email[] }>(responses[1]);

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

export const mail = new MailStore();
