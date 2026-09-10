/**
 * Sub-account discovery and scoped access (issue #212, REQ-MAIL-SUB-
 * 01..09, REQ-SUBACCT-01..11).
 *
 * Deliberately independent of `lib/mail/store.svelte.ts`, mirroring
 * `lib/archive/archive-store.svelte.ts`'s rationale: the primary mail
 * store hardcodes every call to `auth.session.primaryAccounts` (the
 * caller's own account) and its action surface has no per-account
 * gating. A separated identity's mail lives under a *different*
 * accountId (the sub-principal `Identity/set{separated:true}` moved it
 * to), so scoped reads and writes need their own accountId threaded
 * through explicitly rather than the store-wide `mailAccountId` getter.
 *
 * Discovery: the JMAP session descriptor's `accounts` map lists every
 * account the caller can reach (`buildSessionDescriptor` /
 * `AccountIDForPrincipal`, docs/design/server/architecture/
 * 03-protocol-architecture.md "Sub-accounts and Identity separation").
 * For this principal that is exactly the primary account(s) plus one
 * entry per separated identity's sub-principal -- there is no ACL/grant
 * account model mixed in here (that is lib/archive/archive-store.svelte.ts's
 * concern). Subtracting `session.primaryAccounts` from `session.accounts`
 * therefore yields exactly the sub-account id set.
 *
 * EventSource state changes are keyed by accountId in the StateChange
 * payload (`lib/jmap/sync.svelte.ts`); this module's handlers check the
 * accountId against the known sub-account set before doing anything, so
 * a push for the caller's own primary account (already handled by the
 * mail store's own, unrelated handlers) is a no-op here, and vice
 * versa -- REQ-MAIL-SUB-06's "own notification channel, mutable
 * independently" starts from this per-account keying.
 */

import { jmap, strict } from '../jmap/client';
import { auth, registerAccountResetCallback } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { Capability, type Invocation } from '../jmap/types';
import type { Email, Identity, Mailbox } from './types';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/** One separated identity's sub-account, as discovered from the session. */
export interface SubAccountEntry {
  accountId: string;
  /** Display name from the session descriptor (`AccountInfo.name`). */
  name: string;
  /** The separated Identity itself, fetched via Identity/get(accountId). */
  identity: Identity | null;
  /** This account's full top-level Mailbox tree. */
  mailboxes: Mailbox[];
  /** Unread-thread count of this account's Inbox, for the switcher badge. */
  unreadThreads: number;
  loadStatus: LoadStatus;
  errorMessage: string | null;
}

const LIST_PROPERTIES = [
  'id',
  'threadId',
  'mailboxIds',
  'keywords',
  'from',
  'to',
  'subject',
  'preview',
  'receivedAt',
  'hasAttachment',
] as const;

const BODY_PROPERTIES = [
  'id',
  'threadId',
  'mailboxIds',
  'keywords',
  'from',
  'to',
  'cc',
  'bcc',
  'replyTo',
  'subject',
  'preview',
  'receivedAt',
  'sentAt',
  'hasAttachment',
  'bodyValues',
  'htmlBody',
  'textBody',
  'attachments',
  'blobId',
] as const;

const PAGE_SIZE = 50;

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

/** The set of sub-account ids visible on the current session, or empty when none. */
function knownSubAccountIds(session: NonNullable<typeof auth.session>): Set<string> {
  const primary = new Set(Object.values(session.primaryAccounts));
  return new Set(Object.keys(session.accounts).filter((id) => !primary.has(id)));
}

class SubAccountsStore {
  status = $state<LoadStatus>('idle');
  errorMessage = $state<string | null>(null);
  entries = $state<SubAccountEntry[]>([]);

  /** Per-account message list + reading pane, for the scoped mail view. */
  listAccountId = $state<string | null>(null);
  listMailboxId = $state<string | null>(null);
  emails = $state<Email[]>([]);
  listStatus = $state<LoadStatus>('idle');
  listErrorMessage = $state<string | null>(null);

  reading = $state<Email | null>(null);
  readingStatus = $state<LoadStatus>('idle');

  get list(): SubAccountEntry[] {
    return this.entries;
  }

  find(accountId: string): SubAccountEntry | null {
    return this.entries.find((e) => e.accountId === accountId) ?? null;
  }

  /** Idempotent: no-op once loaded. Call refresh() to force a reload. */
  async load(): Promise<void> {
    if (this.status === 'ready' || this.status === 'loading') return;
    await this.refresh();
  }

  /** Full rediscovery -- call after separateIdentity() / removeSeparation(). */
  async refresh(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    try {
      const session = auth.session;
      if (!session || !jmap.hasCapability(Capability.HeroldSubAccounts)) {
        this.entries = [];
        this.status = 'ready';
        return;
      }
      const subIds = knownSubAccountIds(session);
      const results: SubAccountEntry[] = [];
      for (const accountId of subIds) {
        const info = session.accounts[accountId];
        results.push(await this.#loadOne(accountId, info?.name ?? accountId));
      }
      results.sort((a, b) => a.name.localeCompare(b.name));
      this.entries = results;
      this.status = 'ready';
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : String(err);
      this.status = 'error';
    }
  }

  /**
   * Refresh a single known sub-account (EventSource Mailbox/Email push,
   * keyed by accountId). A push for an accountId this store has not
   * discovered yet (e.g. a separation that just started elsewhere) is a
   * no-op here; the caller's explicit refresh() after separateIdentity()
   * is what picks up brand-new sub-accounts.
   */
  async refreshOne(accountId: string): Promise<void> {
    const existing = this.find(accountId);
    if (!existing) return;
    try {
      const updated = await this.#loadOne(accountId, existing.name);
      this.entries = this.entries.map((e) => (e.accountId === accountId ? updated : e));
    } catch {
      // Best-effort background refresh; keep the stale entry rather than
      // dropping the row out of the switcher on a transient failure.
    }
    if (this.listAccountId === accountId) {
      const mailboxId = this.listMailboxId;
      if (mailboxId) void this.loadEmails(accountId, mailboxId);
    }
  }

  /**
   * Reverse a completed (or in-progress) separation via
   * `Identity/set{separated: false}`, addressed via the sub-account's OWN
   * accountId -- per REQ-SUBACCT-09/10 a separated Identity is only
   * reachable from the account it currently lives under, never the
   * parent (`resolveTargetPrincipal` never honours the parent for a
   * sub-account it does not itself own).
   *
   * `keepMail: true` (default) moves the mail back to the parent account
   * and reports the Identity updated; `keepMail: false` purges it and
   * reports the Identity destroyed -- either way this method refreshes
   * the discovered sub-account list afterwards so the removed row drops
   * out of the switcher / Accounts section. It does NOT refresh
   * `lib/mail/store.svelte.ts`'s identities cache (a `keepMail: true`
   * reversal moves the Identity back there) -- callers that need the
   * primary Identity list to reflect a kept-mail reversal immediately
   * call `mail.loadIdentities()` themselves (Settings -> Accounts does).
   */
  async removeSeparation(accountId: string, identityId: string, keepMail: boolean): Promise<void> {
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Identity/set',
        {
          accountId,
          update: { [identityId]: { separated: false, keepMail } },
        },
        [Capability.Submission],
      );
    });
    strict(responses);
    const result = invocationArgs<{
      notUpdated?: Record<string, { type: string; description?: string }>;
      notDestroyed?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[identityId] ?? result.notDestroyed?.[identityId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    await this.refresh();
  }

  async #loadOne(accountId: string, name: string): Promise<SubAccountEntry> {
    try {
      const { responses } = await jmap.batch((b) => {
        b.call('Identity/get', { accountId, ids: null }, [Capability.Submission]);
        b.call('Mailbox/get', { accountId, ids: null }, [Capability.Mail]);
      });
      strict(responses);
      const idArgs = invocationArgs<{ list: Identity[] }>(responses[0]);
      const mbArgs = invocationArgs<{ list: Mailbox[] }>(responses[1]);
      const identity = idArgs.list[0] ?? null;
      const inbox = mbArgs.list.find((m) => m.role === 'inbox');
      return {
        accountId,
        name,
        identity,
        mailboxes: mbArgs.list,
        unreadThreads: inbox?.unreadThreads ?? 0,
        loadStatus: 'ready',
        errorMessage: null,
      };
    } catch (err) {
      return {
        accountId,
        name,
        identity: null,
        mailboxes: [],
        unreadThreads: 0,
        loadStatus: 'error',
        errorMessage: err instanceof Error ? err.message : String(err),
      };
    }
  }

  /** Loads (or searches, when `searchText` is set) a sub-account mailbox's message list. */
  async loadEmails(accountId: string, mailboxId: string, searchText = ''): Promise<void> {
    this.listAccountId = accountId;
    this.listMailboxId = mailboxId;
    this.listStatus = 'loading';
    this.listErrorMessage = null;
    try {
      const trimmed = searchText.trim();
      const filter = trimmed
        ? { operator: 'AND', conditions: [{ inMailbox: mailboxId }, { text: trimmed }] }
        : { inMailbox: mailboxId };

      const { responses } = await jmap.batch((b) => {
        const q = b.call(
          'Email/query',
          {
            accountId,
            filter,
            sort: [{ property: 'receivedAt', isAscending: false }],
            collapseThreads: false,
            limit: PAGE_SIZE,
            calculateTotal: false,
          },
          [Capability.Mail],
        );
        b.call(
          'Email/get',
          { accountId, '#ids': q.ref('/ids'), properties: LIST_PROPERTIES },
          [Capability.Mail],
        );
      });
      strict(responses);
      const getResult = invocationArgs<{ list: Email[] }>(responses[1]);
      this.emails = getResult.list;
      this.listStatus = 'ready';
    } catch (err) {
      this.listErrorMessage = err instanceof Error ? err.message : String(err);
      this.listStatus = 'error';
    }
  }

  /** Loads one message's full body for reading, and marks it seen (REQ-MAIL-SUB-04/06). */
  async openEmail(accountId: string, emailId: string): Promise<void> {
    this.readingStatus = 'loading';
    this.reading = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/get',
          {
            accountId,
            ids: [emailId],
            properties: BODY_PROPERTIES,
            bodyProperties: ['partId', 'blobId', 'size', 'type', 'charset', 'disposition', 'name', 'cid'],
            fetchTextBodyValues: true,
            fetchHTMLBodyValues: true,
          },
          [Capability.Mail],
        );
      });
      strict(responses);
      const args = invocationArgs<{ list: Email[] }>(responses[0]);
      const email = args.list[0] ?? null;
      this.reading = email;
      this.readingStatus = 'ready';

      if (email && !email.keywords.$seen) {
        void this.#markSeen(accountId, email);
      }
    } catch (err) {
      this.readingStatus = 'error';
      throw err;
    }
  }

  async #markSeen(accountId: string, email: Email): Promise<void> {
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/set',
          { accountId, update: { [email.id]: { 'keywords/$seen': true } } },
          [Capability.Mail],
        );
      });
      strict(responses);
      const updatedKeywords: Email['keywords'] = { ...email.keywords, $seen: true as const };
      if (this.reading?.id === email.id) {
        this.reading = { ...this.reading, keywords: updatedKeywords };
      }
      this.emails = this.emails.map((e) =>
        e.id === email.id ? { ...e, keywords: updatedKeywords } : e,
      );
      // The account's unread count changed; refresh its switcher badge.
      void this.refreshOne(accountId);
    } catch {
      // Best-effort -- reading the message succeeded regardless.
    }
  }

  closeReading(): void {
    this.reading = null;
    this.readingStatus = 'idle';
  }

  reset(): void {
    this.status = 'idle';
    this.errorMessage = null;
    this.entries = [];
    this.listAccountId = null;
    this.listMailboxId = null;
    this.emails = [];
    this.listStatus = 'idle';
    this.listErrorMessage = null;
    this.reading = null;
    this.readingStatus = 'idle';
  }
}

export const subAccounts = new SubAccountsStore();
registerAccountResetCallback(() => subAccounts.reset());

// Keyed EventSource handling (REQ-MAIL-SUB-06): only ever touches a
// sub-account this store already knows about. Pushes for the caller's own
// primary account are handled entirely by lib/mail/store.svelte.ts's own
// handlers and are inert here (refreshOne() no-ops on an unknown accountId).
sync.on('Mailbox', (_newState, accountId) => {
  void subAccounts.refreshOne(accountId);
});
sync.on('Email', (_newState, accountId) => {
  void subAccounts.refreshOne(accountId);
});
