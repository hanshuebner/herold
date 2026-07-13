/**
 * Mailing-list archive access (REQ-MLIST-73, issue #187 Stage S4).
 *
 * The server surfaces a list's archive mailbox as a genuine JMAP
 * secondary account: `session.accounts` carries one entry per foreign
 * principal the caller can reach via a mailbox grant (REQ-PROTO-33,
 * internal/protojmap/session.go's appendSecondaryAccounts), and
 * `Mailbox/get` scoped to that accountId returns only the mailboxes the
 * grant covers. This module is deliberately independent of
 * `lib/mail/store.svelte.ts`: that store hardcodes every call to the
 * caller's own primary Mail account (`auth.session.primaryAccounts`),
 * and its action surface (delete, move, expunge, flag/keyword edits,
 * drag-to-move, mark read/unread) has no per-mailbox gating. Keeping
 * archive access in its own store means there is no shared code path
 * that could accidentally fire a mutating JMAP call against a mailbox
 * the grant does not cover.
 *
 * The archive grant is `lookup+read` WITHOUT the Seen right (see
 * internal/maillist/archive.go's SyncMemberArchiveGrant doc comment):
 * the server treats Email/set's Seen-keyword write as a mutation and
 * will refuse it. This module never calls Email/set, so opening an
 * archived message cannot trigger a silent failure or an error toast.
 * All rendered actions are additionally gated on the server-reported
 * `myRights` mask (requested explicitly below; RFC 8621 only returns it
 * when asked for) so a future change to the grant shape is reflected in
 * the UI without a code change here.
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { Capability, type Invocation } from '../jmap/types';
import type { Email } from '../mail/types';

const ARCHIVE_LIST_PROPERTIES = [
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

const ARCHIVE_BODY_PROPERTIES = [
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

/** RFC 8621 SS2.1.1 myRights, projected to the fields this view gates on. */
export interface ArchiveMailboxRights {
  mayReadItems: boolean;
  maySetSeen: boolean;
  maySetKeywords: boolean;
  mayAddItems: boolean;
  mayRemoveItems: boolean;
  maySubmit: boolean;
}

export interface ArchiveMailbox {
  id: string;
  name: string;
  totalEmails: number;
  unreadEmails: number;
  rights: ArchiveMailboxRights;
}

/** One foreign (non-personal) account visible on this session, e.g. a list archive. */
export interface SharedAccount {
  accountId: string;
  name: string;
  isReadOnly: boolean;
  mailboxes: ArchiveMailbox[];
}

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

function defaultRights(): ArchiveMailboxRights {
  return {
    mayReadItems: false,
    maySetSeen: false,
    maySetKeywords: false,
    mayAddItems: false,
    mayRemoveItems: false,
    maySubmit: false,
  };
}

class ArchiveStore {
  status = $state<LoadStatus>('idle');
  errorMessage = $state<string | null>(null);
  sharedAccounts = $state<SharedAccount[]>([]);

  emails = $state<Email[]>([]);
  listStatus = $state<LoadStatus>('idle');
  listErrorMessage = $state<string | null>(null);

  reading = $state<Email | null>(null);
  readingStatus = $state<LoadStatus>('idle');

  /**
   * Discover every non-personal account on the session and load its
   * mailboxes (with `myRights`). A member typically sees one such
   * account per list they belong to; each account typically has one
   * mailbox (the archive). Idempotent no-op once loaded -- call
   * `refreshSharedAccounts` to force a reload (e.g. after a session
   * refresh added a new grant).
   */
  async loadSharedAccounts(): Promise<void> {
    if (this.status === 'ready' || this.status === 'loading') return;
    await this.refreshSharedAccounts();
  }

  async refreshSharedAccounts(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    try {
      const session = auth.session;
      if (!session) throw new Error('No session');
      const primary = new Set(Object.values(session.primaryAccounts));
      const foreignIds = Object.keys(session.accounts).filter((id) => !primary.has(id));

      const results: SharedAccount[] = [];
      for (const accountId of foreignIds) {
        const info = session.accounts[accountId];
        if (!info || info.isPersonal) continue;
        const mailboxes = await this.#loadMailboxesForAccount(accountId);
        if (mailboxes.length === 0) continue;
        results.push({
          accountId,
          name: info.name,
          isReadOnly: info.isReadOnly,
          mailboxes,
        });
      }
      this.sharedAccounts = results;
      this.status = 'ready';
    } catch (err) {
      this.errorMessage = err instanceof Error ? err.message : String(err);
      this.status = 'error';
    }
  }

  async #loadMailboxesForAccount(accountId: string): Promise<ArchiveMailbox[]> {
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Mailbox/get',
        {
          accountId,
          ids: null,
          properties: ['id', 'name', 'totalEmails', 'unreadEmails', 'myRights'],
        },
        [Capability.Mail],
      );
    });
    strict(responses);
    const args = invocationArgs<{
      list: Array<{
        id: string;
        name: string;
        totalEmails: number;
        unreadEmails: number;
        myRights?: Partial<ArchiveMailboxRights>;
      }>;
    }>(responses[0]);
    return args.list.map((m) => ({
      id: m.id,
      name: m.name,
      totalEmails: m.totalEmails,
      unreadEmails: m.unreadEmails,
      rights: { ...defaultRights(), ...(m.myRights ?? {}) },
    }));
  }

  findMailbox(accountId: string, mailboxId?: string): ArchiveMailbox | null {
    const account = this.sharedAccounts.find((a) => a.accountId === accountId);
    if (!account) return null;
    if (mailboxId) return account.mailboxes.find((m) => m.id === mailboxId) ?? null;
    return account.mailboxes[0] ?? null;
  }

  /** Loads (or searches, when `searchText` is set) the archive's message list. */
  async loadEmails(accountId: string, mailboxId: string, searchText = ''): Promise<void> {
    this.listStatus = 'loading';
    this.listErrorMessage = null;
    this.emails = [];
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
          {
            accountId,
            '#ids': q.ref('/ids'),
            properties: ARCHIVE_LIST_PROPERTIES,
          },
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

  /**
   * Loads the full body of one archived message for reading. Read-only:
   * this NEVER issues Email/set, so opening a message cannot attempt to
   * set the Seen keyword the archive grant does not include.
   */
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
            properties: ARCHIVE_BODY_PROPERTIES,
            bodyProperties: ['partId', 'blobId', 'size', 'type', 'charset', 'disposition', 'name', 'cid'],
            fetchTextBodyValues: true,
            fetchHTMLBodyValues: true,
          },
          [Capability.Mail],
        );
      });
      strict(responses);
      const args = invocationArgs<{ list: Email[] }>(responses[0]);
      this.reading = args.list[0] ?? null;
      this.readingStatus = 'ready';
    } catch (err) {
      this.readingStatus = 'error';
      throw err;
    }
  }

  closeReading(): void {
    this.reading = null;
    this.readingStatus = 'idle';
  }
}

export const archive = new ArchiveStore();
