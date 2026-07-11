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

  /** Identities with a fetch already scheduled or in flight. Plain
   *  (non-reactive) set, not #entries: it exists only to dedupe the
   *  synchronous portion of load() (see IdentitySubmissionHandle#fetch)
   *  and must never itself be read from a reactive context. */
  #pending = new Set<string>();

  /** Default entry for an identity we have never queried. Shared so
   *  every caller observes the same reference; never mutated. Reading
   *  `handle.status` / `.data` / `.error` for an unseen identity must
   *  NOT write to #entries (Svelte 5 forbids state mutation inside a
   *  reactive read — `state_unsafe_mutation`). Entries are added to
   *  #entries by _patch, which is only reached from the async
   *  load()/refresh() path. */
  static readonly #defaultEntry: SubmissionEntry = {
    status: 'idle',
    data: null,
    error: null,
  };

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

  /** Internal: return the cached entry for an identity, or the shared
   *  immutable default. Pure read — never mutates #entries. The
   *  earlier version of this method created a fresh idle entry when
   *  one was missing and assigned it back to #entries, which broke
   *  any caller that reached _entry from a Svelte 5 reactive read
   *  context (state_unsafe_mutation: navigating to /settings blanked
   *  the page because handle.data read here mutated state during the
   *  render pass). Entry creation now happens only inside _patch. */
  _entry(identityId: string): SubmissionEntry {
    return this.#entries.get(identityId) ?? IdentitySubmissionStore.#defaultEntry;
  }

  /** Internal: update fields on an entry and trigger reactivity. */
  _patch(identityId: string, patch: Partial<SubmissionEntry>): void {
    const entry = this._entry(identityId);
    const updated = { ...entry, ...patch };
    this.#entries = new Map(this.#entries).set(identityId, updated);
  }

  /** Internal: true while a fetch for this identity is scheduled or
   *  in flight (see IdentitySubmissionHandle#fetch). */
  _isPending(identityId: string): boolean {
    return this.#pending.has(identityId);
  }

  /** Internal: mark a fetch as scheduled/in-flight for this identity. */
  _markPending(identityId: string): void {
    this.#pending.add(identityId);
  }

  /** Internal: clear the pending marker once the fetch has settled. */
  _clearPending(identityId: string): void {
    this.#pending.delete(identityId);
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
   * If the entry has terminally errored, this is also a no-op — the
   * caller must use refresh() to retry. Without this guard, a $effect
   * that reads the entry's status (any callsite of submissionStore
   * indirectly does, since _entry is reactive) would re-run on every
   * patch and call load() again, looping forever against a 4xx
   * response. Caching the failure breaks that cycle; sync.on('Identity')
   * still invalidates entries on a JMAP push and clears the error.
   *
   * Callers include $derived computations that resolve a per-identity
   * submission summary while rendering (e.g. ThreadInlineComposer's and
   * ComposeWindow's submissionResolver, invoked from both a showPicker
   * and a fromGating $derived in the same render pass) — see #fetch for
   * why the guard here alone is not sufficient to prevent a duplicate
   * fetch (re #176).
   */
  async load(): Promise<void> {
    const entry = this.#store._entry(this.identityId);
    if (entry.status === 'loading') return;
    if (entry.status === 'ready') return;
    if (entry.status === 'error') return;
    if (this.#store._isPending(this.identityId)) return;
    this.#store._markPending(this.identityId);
    await this.#fetch();
  }

  /** Force a re-fetch regardless of current status. */
  async refresh(): Promise<void> {
    this.#store._markPending(this.identityId);
    await this.#fetch();
  }

  /**
   * Perform the actual fetch. The first `await` below is a bare
   * microtask yield, not the network call itself — it exists so the
   * subsequent `_patch` (a $state write) never lands inside the
   * synchronous call stack of whatever invoked load()/refresh(). Both
   * are called from $derived computations (ThreadInlineComposer /
   * ComposeWindow submissionResolver), and Svelte 5 forbids a $state
   * write while a $derived is being evaluated on the same stack
   * (state_unsafe_mutation, re #176). Yielding first defers the write
   * to a microtask that runs after the derived's synchronous evaluation
   * has already completed. #pending (see load()/refresh()) dedupes the
   * two-derived-in-one-render-pass case so only one fetch is issued.
   */
  async #fetch(): Promise<void> {
    await Promise.resolve();
    this.#store._patch(this.identityId, { status: 'loading', error: null });
    try {
      const data = await getSubmission(this.identityId);
      this.#store._patch(this.identityId, { status: 'ready', data, error: null });
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.#store._patch(this.identityId, { status: 'error', error: msg });
    } finally {
      this.#store._clearPending(this.identityId);
    }
  }
}

/** Module-level singleton. Import and use `submissionStore.forIdentity(id)`. */
export const submissionStore = new IdentitySubmissionStore();
