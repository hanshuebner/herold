/**
 * Reactive cache for tagged-address filters and dismissals.
 *
 * Drives the per-message banner (REQ-MAIL-12c). The store loads both
 * collections lazily on first use, subscribes to `Identity/changes`
 * push events (REQ-TAG-72: tagged-address mutations bump the Identity
 * state token), and re-reads on every advance so the banner disappears
 * the moment the user creates a filter or dismisses the suffix on
 * another device.
 *
 * Filters are read via JMAP `TaggedAddressFilter/get` so they share the
 * existing JMAP transport and capability gate. Dismissals are read via
 * the REST endpoint at `/api/v1/tagged-address-dismissals` because
 * dismissals never appear on the JMAP wire (REQ-TAG-71).
 *
 * The store is a module-level singleton. Tests import the class as a
 * named export to instantiate isolated copies.
 */

import { jmap, strict } from '../jmap/client';
import { sync } from '../jmap/sync.svelte';
import { Capability, type Invocation } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import {
  convertFilterToSieve,
  createDismissal,
  deleteDismissal,
  listDismissals,
  type ConvertToSieveResponse,
  type TaggedAddressDismissal,
} from '../api/tagged-addresses';
import type { FilterRecord, DismissalRecord } from './tagged-address';

/** Wire-form TaggedAddressFilter (matches the Go-side jmapTaggedAddressFilter). */
export interface TaggedAddressFilter {
  id: string;
  baseIdentityId: string;
  suffix: string;
  action: 'label' | 'label_archive' | 'label_archive_read';
  labelName: string;
  createdAt: string;
  updatedAt: string;
}

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

export class TaggedAddressStore {
  filters = $state<TaggedAddressFilter[]>([]);
  dismissals = $state<TaggedAddressDismissal[]>([]);
  status = $state<LoadStatus>('idle');
  error = $state<string | null>(null);

  #syncRegistered = false;
  #inflight: Promise<void> | null = null;

  /** Whether the JMAP session advertises the tagged-addresses capability. */
  get capabilityAdvertised(): boolean {
    return jmap.hasCapability(Capability.HeroldTaggedAddresses);
  }

  /** The principal's JMAP Mail account id, or null when no session is bootstrapped. */
  get accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Mail] ?? null;
  }

  /**
   * Reactive view returning the subset of filters and dismissals
   * relevant to the banner-visibility check.
   */
  get filterRecords(): FilterRecord[] {
    return this.filters.map((f) => ({
      id: f.id,
      baseIdentityId: f.baseIdentityId,
      suffix: f.suffix,
    }));
  }

  get dismissalRecords(): DismissalRecord[] {
    return this.dismissals.map((d) => ({
      baseIdentityId: d.base_identity_id,
      suffix: d.suffix,
    }));
  }

  /** Register the Identity state-change hook once (REQ-TAG-72). */
  #ensureSyncHandler(): void {
    if (this.#syncRegistered) return;
    this.#syncRegistered = true;
    sync.on('Identity', (_newState, _accountId) => {
      // Coarse invalidation; refetch both collections. The push fires
      // on every identity-set / tagged-address mutation, so the cost is
      // bounded by user attention.
      void this.refresh();
    });
  }

  /**
   * Lazily load both collections. Cached after the first ready load;
   * use refresh() to force a re-fetch.
   */
  async load(): Promise<void> {
    this.#ensureSyncHandler();
    if (this.status === 'loading' && this.#inflight) {
      await this.#inflight;
      return;
    }
    if (this.status === 'ready') return;
    if (!this.capabilityAdvertised) {
      // Banner is hidden when the capability is missing; surface an
      // empty cache so callers do not block on the load.
      this.filters = [];
      this.dismissals = [];
      this.status = 'ready';
      return;
    }
    await this.refresh();
  }

  /** Force a re-fetch regardless of cached status. */
  async refresh(): Promise<void> {
    if (!this.capabilityAdvertised) {
      this.filters = [];
      this.dismissals = [];
      this.status = 'ready';
      return;
    }
    const accountId = this.accountId;
    if (!accountId) {
      this.status = 'error';
      this.error = 'no mail account';
      return;
    }
    this.status = 'loading';
    this.error = null;
    const p = this.#doRefresh(accountId);
    this.#inflight = p;
    try {
      await p;
    } finally {
      if (this.#inflight === p) this.#inflight = null;
    }
  }

  async #doRefresh(accountId: string): Promise<void> {
    try {
      const [filters, dismissals] = await Promise.all([
        this.#loadFilters(accountId),
        this.#loadDismissals(),
      ]);
      this.filters = filters;
      this.dismissals = dismissals;
      this.status = 'ready';
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err);
      this.status = 'error';
    }
  }

  async #loadFilters(accountId: string): Promise<TaggedAddressFilter[]> {
    const { responses } = await jmap.batch((b) => {
      b.call(
        'TaggedAddressFilter/get',
        { accountId, ids: null },
        [Capability.HeroldTaggedAddresses],
      );
    });
    strict(responses);
    const args = invocationArgs<{ list: TaggedAddressFilter[] }>(responses[0]);
    return args.list ?? [];
  }

  async #loadDismissals(): Promise<TaggedAddressDismissal[]> {
    const res = await listDismissals();
    return res.dismissals ?? [];
  }

  /**
   * Create a managed filter via JMAP `TaggedAddressFilter/set`. The
   * server enforces the per-principal cap and resolves the label name
   * (REQ-TAG-32) atomically with the row insert. Returns the created
   * row on success.
   *
   * The Identity state token advances on success (REQ-TAG-72); the
   * sync handler will refetch and the banner disappears reactively. We
   * also mirror the result into the local cache so the banner clears
   * without waiting for the push event.
   */
  async createFilter(input: {
    baseIdentityId: string;
    suffix: string;
    action: 'label' | 'label_archive' | 'label_archive_read';
    labelName: string;
  }): Promise<TaggedAddressFilter> {
    const accountId = this.accountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const clientId = 'new';
    const { responses } = await jmap.batch((b) => {
      b.call(
        'TaggedAddressFilter/set',
        {
          accountId,
          create: {
            [clientId]: {
              baseIdentityId: input.baseIdentityId,
              suffix: input.suffix,
              action: input.action,
              labelName: input.labelName,
            },
          },
        },
        [Capability.HeroldTaggedAddresses],
      );
    });
    strict(responses);
    const result = invocationArgs<{
      created?: Record<string, TaggedAddressFilter>;
      notCreated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notCreated?.[clientId];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    const created = result.created?.[clientId];
    if (!created) {
      throw new Error('TaggedAddressFilter/set did not echo the created row');
    }
    // Mirror into the cache so the banner-visibility derived value
    // updates synchronously after the action completes.
    this.filters = [...this.filters, created];
    return created;
  }

  /**
   * Update a managed filter's action and / or label name via
   * `TaggedAddressFilter/set update`. The store's local cache mirrors
   * the server response so the Settings table updates without waiting
   * for the Identity-state push (REQ-TAG-72).
   *
   * Throws on JMAP-level failure or a per-id notUpdated entry so the
   * caller can surface a toast / inline error.
   */
  async updateFilter(
    id: string,
    patch: {
      action?: 'label' | 'label_archive' | 'label_archive_read';
      labelName?: string;
    },
  ): Promise<TaggedAddressFilter> {
    const accountId = this.accountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const { responses } = await jmap.batch((b) => {
      b.call(
        'TaggedAddressFilter/set',
        {
          accountId,
          update: { [id]: patch },
        },
        [Capability.HeroldTaggedAddresses],
      );
    });
    strict(responses);
    const result = invocationArgs<{
      updated?: Record<string, TaggedAddressFilter | null>;
      notUpdated?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notUpdated?.[id];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    const server = result.updated?.[id];
    let next: TaggedAddressFilter | null = null;
    if (server) next = server;
    // Merge into local cache; fall back to the prior row + patch when
    // the server echoed null (RFC 8620 allows omitting the updated row
    // when no derived properties changed).
    this.filters = this.filters.map((f) => {
      if (f.id !== id) return f;
      if (next) return next;
      const merged: TaggedAddressFilter = {
        ...f,
        ...(patch.action !== undefined ? { action: patch.action } : {}),
        ...(patch.labelName !== undefined ? { labelName: patch.labelName } : {}),
      };
      next = merged;
      return merged;
    });
    if (!next) throw new Error('TaggedAddressFilter/set update did not match any cached row');
    return next;
  }

  /**
   * Destroy a managed filter via `TaggedAddressFilter/set destroy`. The
   * row's matching dismissal (if any) is dropped atomically server-side
   * (REQ-TAG-61) but the dismissal table is REST-only on the wire, so
   * the local cache reconciles via the next Identity-state push.
   *
   * Throws on JMAP-level failure or per-id notDestroyed.
   */
  async deleteFilter(id: string): Promise<void> {
    const accountId = this.accountId;
    if (!accountId) throw new Error('No Mail account on this session');
    const { responses } = await jmap.batch((b) => {
      b.call(
        'TaggedAddressFilter/set',
        { accountId, destroy: [id] },
        [Capability.HeroldTaggedAddresses],
      );
    });
    strict(responses);
    const result = invocationArgs<{
      destroyed?: string[];
      notDestroyed?: Record<string, { type: string; description?: string }>;
    }>(responses[0]);
    const failure = result.notDestroyed?.[id];
    if (failure) {
      throw new Error(failure.description ?? failure.type);
    }
    this.filters = this.filters.filter((f) => f.id !== id);
  }

  /**
   * One-way conversion of a managed filter row to a hand-written Sieve
   * fragment (REQ-TAG-50). On success the filter row disappears from
   * both the server and the local cache; the response carries the
   * generated fragment + the merged user-Sieve script for the caller
   * to surface in a toast or settings view.
   *
   * Throws on REST-level failure; the filter row stays in place.
   */
  async convertToSieve(id: string): Promise<ConvertToSieveResponse> {
    const res = await convertFilterToSieve(id);
    this.filters = this.filters.filter((f) => f.id !== id);
    return res;
  }

  /**
   * Create a dismissal row for (baseIdentityId, suffix) via REST. The
   * server is idempotent on duplicates; the local cache mirrors the row
   * eagerly so the banner clears immediately.
   */
  async dismiss(baseIdentityId: string, suffix: string): Promise<void> {
    const row = await createDismissal({ base_identity_id: baseIdentityId, suffix });
    const lowerSuffix = row.suffix.toLowerCase();
    // De-dupe in case the user double-clicked.
    const without = this.dismissals.filter(
      (d) => !(d.base_identity_id === baseIdentityId && d.suffix.toLowerCase() === lowerSuffix),
    );
    this.dismissals = [...without, row];
  }

  /**
   * Remove a dismissal so the banner re-surfaces on the next message.
   * Used by the Settings → Tagged addresses surface; the per-message
   * banner only writes dismissals, never removes them.
   */
  async undismiss(baseIdentityId: string, suffix: string): Promise<void> {
    await deleteDismissal(baseIdentityId, suffix);
    const lowerSuffix = suffix.toLowerCase();
    this.dismissals = this.dismissals.filter(
      (d) => !(d.base_identity_id === baseIdentityId && d.suffix.toLowerCase() === lowerSuffix),
    );
  }
}

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

/** Module-level singleton — one cache per browser session. */
export const taggedAddressStore = new TaggedAddressStore();
