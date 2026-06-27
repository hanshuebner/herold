/**
 * Per-identity IMAP import state store.
 *
 * Wraps JMAP IMAPImport/get and provides a reactive per-identity handle
 * that loads on demand and can be refreshed. Pattern mirrors
 * lib/identities/identity-submission.svelte.ts.
 *
 * Usage:
 *   const handle = imapImportStore.forIdentity(identityId, accountId);
 *   await handle.load();    // fire an initial fetch
 *   handle.status           // 'idle' | 'loading' | 'ready' | 'error'
 *   handle.account          // IMAPImportAccount | null (null = unconfigured)
 *
 * REQ-SET-IMAPIMP-01..04, REQ-IMAP-IMP-61.
 */

import { jmap, strict } from './client';
import { Capability } from './types';
import type {
  IMAPImportAccount,
  IMAPImportGetResponse,
  IMAPImportTLSMode,
  IMAPImportAuthMethod,
  IMAPImportState,
  IMAPImportSetRequest,
  IMAPImportSetResponse,
} from './imap-import';

export type IMAPImportLoadStatus = 'idle' | 'loading' | 'ready' | 'error';

interface IMAPImportEntry {
  status: IMAPImportLoadStatus;
  /** null = capability absent or identity has no import config. */
  account: IMAPImportAccount | null;
  error: string | null;
  /** The accountId used for the JMAP call, cached at load time. */
  accountId: string | null;
}

class IMAPImportStoreImpl {
  /** Map from identity JMAP id to its cached import state. */
  #entries = $state(new Map<string, IMAPImportEntry>());

  static readonly #defaultEntry: IMAPImportEntry = {
    status: 'idle',
    account: null,
    error: null,
    accountId: null,
  };

  _entry(identityId: string): IMAPImportEntry {
    return this.#entries.get(identityId) ?? IMAPImportStoreImpl.#defaultEntry;
  }

  _patch(identityId: string, patch: Partial<IMAPImportEntry>): void {
    const entry = this._entry(identityId);
    const updated = { ...entry, ...patch };
    this.#entries = new Map(this.#entries).set(identityId, updated);
  }

  evict(identityId: string): void {
    const next = new Map(this.#entries);
    next.delete(identityId);
    this.#entries = next;
  }

  /**
   * Return a reactive handle for a single identity's IMAP import state.
   * `accountId` is the JMAP mail account id (from `mail.mailAccountId`).
   */
  forIdentity(identityId: string, accountId: string): IMAPImportHandle {
    return new IMAPImportHandle(identityId, accountId, this);
  }
}

export class IMAPImportHandle {
  readonly identityId: string;
  readonly #accountId: string;
  readonly #store: IMAPImportStoreImpl;

  constructor(
    identityId: string,
    accountId: string,
    store: IMAPImportStoreImpl,
  ) {
    this.identityId = identityId;
    this.#accountId = accountId;
    this.#store = store;
  }

  get status(): IMAPImportLoadStatus {
    return this.#store._entry(this.identityId).status;
  }

  /** The configured IMAPImport account for this identity, or null when unconfigured. */
  get account(): IMAPImportAccount | null {
    return this.#store._entry(this.identityId).account;
  }

  get error(): string | null {
    return this.#store._entry(this.identityId).error;
  }

  /**
   * Fetch IMAP import status from the server via IMAPImport/get.
   * No-op if already loading or ready; call refresh() to force re-fetch.
   */
  async load(): Promise<void> {
    const entry = this.#store._entry(this.identityId);
    if (entry.status === 'loading') return;
    if (entry.status === 'ready') return;
    if (entry.status === 'error') return;
    await this.#fetch();
  }

  /** Force a re-fetch regardless of current status. */
  async refresh(): Promise<void> {
    await this.#fetch();
  }

  async #fetch(): Promise<void> {
    this.#store._patch(this.identityId, {
      status: 'loading',
      error: null,
      accountId: this.#accountId,
    });
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'IMAPImport/get',
          { accountId: this.#accountId },
          [Capability.HeroldIMAPImport],
        );
      });
      strict(responses);
      const result = (responses[0]?.[1] ?? {}) as IMAPImportGetResponse;
      // Find the account whose identityId matches.
      const account =
        result.list?.find((a) => a.identityId === this.identityId) ?? null;
      this.#store._patch(this.identityId, {
        status: 'ready',
        account,
        error: null,
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.#store._patch(this.identityId, {
        status: 'error',
        account: null,
        error: msg,
      });
    }
  }

  /**
   * Create a new IMAP import account for this identity.
   * Returns the created IMAPImportAccount on success.
   * Throws an Error with the server's description on failure.
   */
  async create(args: {
    accountName: string;
    host: string;
    port: number;
    tlsMode: IMAPImportTLSMode;
    username: string;
    authMethod: IMAPImportAuthMethod;
    backfillHorizon: string;
    credential: string;
    deletePropagates?: boolean;
  }): Promise<IMAPImportAccount> {
    const req: IMAPImportSetRequest = {
      accountId: this.#accountId,
      create: {
        new1: { ...args, identityId: this.identityId },
      },
    };
    const { responses } = await jmap.batch((b) => {
      b.call('IMAPImport/set', req, [Capability.HeroldIMAPImport]);
    });
    strict(responses);
    const result = (responses[0]?.[1] ?? {}) as IMAPImportSetResponse;
    const failure = result.notCreated?.['new1'];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    const created = result.created?.['new1'];
    if (!created) {
      throw new Error('IMAPImport/set create returned no account');
    }
    this.#store._patch(this.identityId, { status: 'ready', account: created, error: null });
    return created;
  }

  /**
   * Update the IMAP import account for this identity.
   * Throws an Error with the server's description on failure.
   */
  async update(id: string, args: {
    accountName?: string;
    host?: string;
    port?: number;
    tlsMode?: IMAPImportTLSMode;
    username?: string;
    authMethod?: IMAPImportAuthMethod;
    backfillHorizon?: string;
    credential?: string;
    state?: IMAPImportState;
    deletePropagates?: boolean;
  }): Promise<IMAPImportAccount> {
    const req: IMAPImportSetRequest = {
      accountId: this.#accountId,
      update: { [id]: args },
    };
    const { responses } = await jmap.batch((b) => {
      b.call('IMAPImport/set', req, [Capability.HeroldIMAPImport]);
    });
    strict(responses);
    const result = (responses[0]?.[1] ?? {}) as IMAPImportSetResponse;
    const failure = result.notUpdated?.[id];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    // Refresh to get the updated account state.
    await this.refresh();
    const updated = this.#store._entry(this.identityId).account;
    if (!updated) {
      throw new Error('IMAPImport/set update: account missing after refresh');
    }
    return updated;
  }

  /**
   * Destroy the IMAP import account for this identity.
   *
   * `deleteImportedMail` controls the keep-or-purge behaviour
   * (REQ-IMAP-IMP-102 / REQ-SET-IMAPIMP-04):
   *   false (default) — keep imported mail; only the config is removed.
   *   true  — purge mail imported through this account (dedup-safe).
   *
   * Throws an Error with the server's description on failure.
   */
  async destroy(id: string, deleteImportedMail: boolean): Promise<void> {
    const req: IMAPImportSetRequest = {
      accountId: this.#accountId,
      destroy: [id],
      deleteImportedMail,
    };
    const { responses } = await jmap.batch((b) => {
      b.call('IMAPImport/set', req, [Capability.HeroldIMAPImport]);
    });
    strict(responses);
    const result = (responses[0]?.[1] ?? {}) as IMAPImportSetResponse;
    const failure = result.notDestroyed?.[id];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    this.#store._patch(this.identityId, {
      status: 'ready',
      account: null,
      error: null,
    });
  }
}

/** Module-level singleton. Import and use `imapImportStore.forIdentity(id, accountId)`. */
export const imapImportStore = new IMAPImportStoreImpl();
