/**
 * SeenAddresses store — caches the principal's seen-address history for use
 * by the compose-window recipient autocomplete (REQ-MAIL-11e..m).
 *
 * The seen-address history is a per-principal sliding window of recently-used
 * email addresses, exposed as the `SeenAddress` JMAP type on the Mail account
 * under the existing `urn:ietf:params:jmap:mail` capability.
 *
 * Phase 1: load all entries once on first compose (`SeenAddress/get` with
 * `ids: null`), refresh on `SeenAddress` state-change events from the
 * EventSource feed (App.svelte adds `'SeenAddress'` to the subscribed types).
 */

import { jmap, strict } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';

export interface SeenAddress {
  id: string;
  /** Canonical lowercased email address. */
  email: string;
  /** Most recently observed display name — may be empty string. */
  displayName: string;
  /** ISO 8601 timestamp of first sighting. */
  firstSeenAt: string;
  /** ISO 8601 timestamp of most recent use. */
  lastUsedAt: string;
  /** Number of outbound sends to this address. */
  sendCount: number;
  /** Number of inbound messages from this address. */
  receivedCount: number;
}

class SeenAddresses {
  status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  entries = $state<SeenAddress[]>([]);

  constructor() {
    // Re-fetch whenever the server advances the SeenAddress state string.
    sync.on('SeenAddress', (_newState: string) => {
      void this.#reload();
    });
  }

  /**
   * Load all seen-address entries from the server. Idempotent: no-op when
   * already loading or ready. Call before the first compose autocomplete
   * query (RecipientField triggers this on focus via contacts.load()).
   */
  async load(): Promise<void> {
    if (this.status === 'loading' || this.status === 'ready') return;
    await this.#fetch();
  }

  /**
   * Destroy (remove) a single seen-address entry by id.
   * Issues `SeenAddress/set { destroy: [id] }`.
   */
  async destroy(id: string): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;
    const { responses } = await jmap.batch((b) => {
      b.call(
        'SeenAddress/set',
        { accountId, destroy: [id] },
        [Capability.Mail],
      );
    });
    strict(responses);
    // Optimistically remove from local list; the EventSource will reconcile
    // if the server disagrees.
    this.entries = this.entries.filter((e) => e.id !== id);
  }

  /**
   * Clear all local entries immediately (used by the privacy toggle when the
   * user disables seen-address history — REQ-MAIL-11m).
   */
  clear(): void {
    this.entries = [];
    // Reset to idle so load() can populate again if the toggle is re-enabled.
    this.status = 'idle';
  }

  // ── Private helpers ────────────────────────────────────────────────────────

  /** Force-reload regardless of current status. Called by the sync handler. */
  async #reload(): Promise<void> {
    await this.#fetch();
  }

  async #fetch(): Promise<void> {
    const accountId = this.#accountId();
    if (!accountId) return;
    this.status = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'SeenAddress/get',
          { accountId, ids: null },
          [Capability.Mail],
        );
      });
      strict(responses);
      const args = responses[0]![1] as {
        list: Array<{
          id: string;
          email: string;
          displayName?: string;
          firstSeenAt?: string;
          lastUsedAt?: string;
          sendCount?: number;
          receivedCount?: number;
        }>;
      };
      this.entries = (args.list ?? []).map((row) => ({
        id: row.id,
        email: row.email,
        displayName: row.displayName ?? '',
        firstSeenAt: row.firstSeenAt ?? '',
        lastUsedAt: row.lastUsedAt ?? '',
        sendCount: row.sendCount ?? 0,
        receivedCount: row.receivedCount ?? 0,
      }));
      this.status = 'ready';
    } catch {
      // Soft-fail: seen-address autocomplete is a nice-to-have. The compose
      // form still works without it.
      this.status = 'error';
      this.entries = [];
    }
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Mail] ?? null;
  }
}

export const seenAddresses = new SeenAddresses();
