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
import { EMAIL_LIST_PROPERTIES, type Email, type Mailbox } from './types';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

class MailStore {
  mailboxes = $state(new Map<string, Mailbox>());
  emails = $state(new Map<string, Email>());

  /** Ordered (most-recent first) email ids visible in the current inbox view. */
  inboxEmailIds = $state<string[]>([]);
  inboxLoadStatus = $state<LoadStatus>('idle');
  inboxError = $state<string | null>(null);

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
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

export const mail = new MailStore();
