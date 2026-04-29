/**
 * Per-identity external submission state store.
 *
 * Wraps GET /api/v1/identities/{id}/submission and subscribes to JMAP
 * push events for `Identity` type state changes so the suite can
 * re-fetch when the server reports auth-failed / unreachable
 * (REQ-AUTH-EXT-SUBMIT-07, REQ-MAIL-SUBMIT-04).
 *
 * Usage:
 *   const sub = submissionStore.forIdentity(identityId);
 *   sub.load();          // fire an initial fetch
 *   sub.status          // 'idle' | 'loading' | 'ready' | 'error'
 *   sub.data            // SubmissionStatus | null
 *
 * The store is a singleton per the module; `forIdentity` returns a
 * per-identity handle backed by shared $state cells.
 */

import { getSubmission, type SubmissionStatus } from '../api/identity-submission';
import { sync } from '../jmap/sync.svelte';

export type SubmissionLoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/** Per-identity in-memory record. */
interface SubmissionEntry {
  status: SubmissionLoadStatus;
  data: SubmissionStatus | null;
  error: string | null;
}

class IdentitySubmissionStore {
  /** Map from identity JMAP id to its cached submission status. */
  #entries = $state(new Map<string, SubmissionEntry>());

  /** Whether the JMAP sync handler has been registered. */
  #syncRegistered = false;

  /** Register the JMAP push handler for Identity state changes. Idempotent. */
  #ensureSyncHandler(): void {
    if (this.#syncRegistered) return;
    this.#syncRegistered = true;
    // When the server advances the Identity state (e.g. after an external
    // submission fails and sets state to auth-failed), invalidate all cached
    // entries and re-fetch. This is a coarse invalidation; a fine-grained
    // per-identity push would require the server to emit per-identity events.
    sync.on('Identity', (_newState, _accountId) => {
      this.invalidateAll();
    });
  }

  /**
   * Return a reactive handle for a single identity's submission status.
   * The caller is responsible for calling `.load()` once to initiate the
   * first fetch.
   */
  forIdentity(identityId: string): IdentitySubmissionHandle {
    this.#ensureSyncHandler();
    return new IdentitySubmissionHandle(identityId, this);
  }

  /** Internal: get or create the entry for an identity. */
  _entry(identityId: string): SubmissionEntry {
    let entry = this.#entries.get(identityId);
    if (!entry) {
      entry = { status: 'idle', data: null, error: null };
      this.#entries = new Map(this.#entries).set(identityId, entry);
    }
    return entry;
  }

  /** Internal: update fields on an entry and trigger reactivity. */
  _patch(identityId: string, patch: Partial<SubmissionEntry>): void {
    const entry = this._entry(identityId);
    const updated = { ...entry, ...patch };
    this.#entries = new Map(this.#entries).set(identityId, updated);
  }

  /** Invalidate all cached entries (e.g. on JMAP push). */
  invalidateAll(): void {
    const next = new Map<string, SubmissionEntry>();
    for (const [id, entry] of this.#entries) {
      next.set(id, { ...entry, status: 'idle' });
    }
    this.#entries = next;
  }

  /** Evict a single entry (e.g. after PUT / DELETE so the next read re-fetches). */
  evict(identityId: string): void {
    const next = new Map(this.#entries);
    next.delete(identityId);
    this.#entries = next;
  }
}

export class IdentitySubmissionHandle {
  readonly identityId: string;
  readonly #store: IdentitySubmissionStore;

  constructor(identityId: string, store: IdentitySubmissionStore) {
    this.identityId = identityId;
    this.#store = store;
  }

  get status(): SubmissionLoadStatus {
    return this.#store._entry(this.identityId).status;
  }

  get data(): SubmissionStatus | null {
    return this.#store._entry(this.identityId).data;
  }

  get error(): string | null {
    return this.#store._entry(this.identityId).error;
  }

  /**
   * Fetch the submission status from the server.
   * If a fetch is already in flight, this is a no-op.
   * If the entry is already 'ready' and not stale, this is a no-op.
   */
  async load(): Promise<void> {
    const entry = this.#store._entry(this.identityId);
    if (entry.status === 'loading') return;
    if (entry.status === 'ready') return;
    await this.#fetch();
  }

  /** Force a re-fetch regardless of current status. */
  async refresh(): Promise<void> {
    await this.#fetch();
  }

  async #fetch(): Promise<void> {
    this.#store._patch(this.identityId, { status: 'loading', error: null });
    try {
      const data = await getSubmission(this.identityId);
      this.#store._patch(this.identityId, { status: 'ready', data, error: null });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.#store._patch(this.identityId, { status: 'error', error: msg });
    }
  }
}

/** Module-level singleton. Import and use `submissionStore.forIdentity(id)`. */
export const submissionStore = new IdentitySubmissionStore();
