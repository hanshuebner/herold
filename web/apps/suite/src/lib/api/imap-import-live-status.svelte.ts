/**
 * Live IMAP import worker status for the authenticated user.
 *
 * Fetches GET /api/v1/me/imap-imports/status (auth1, no elevation required)
 * which returns a filtered snapshot of the pool limited to the caller's own
 * accounts. Used by IdentityImportSection to complement the durable
 * lastSuccessAt with live per-session stats (connected, phase, watch mode,
 * messages fetched this run, last error).
 *
 * REQ-IMAP-IMP-65, re #138.
 */

import { get } from './client';

/** Wire shape of one item from GET /api/v1/me/imap-imports/status. */
export interface IMAPImportMyStatus {
  account_id: string;
  account_name: string;
  connected: boolean;
  phase: string;
  watch_mode?: string;
  /** RFC 3339 timestamp; present only when a sync has completed this run. */
  last_sync_at?: string;
  messages_fetched: number;
  last_error?: string;
}

interface StatusPage {
  items: IMAPImportMyStatus[];
}

export type LiveStatusLoadState = 'idle' | 'loading' | 'ready' | 'error';

class IMAPImportLiveStatusStore {
  status = $state<LiveStatusLoadState>('idle');
  items = $state<IMAPImportMyStatus[]>([]);
  error = $state<string | null>(null);

  /** Fetch once; a no-op if already loading or ready. */
  async load(): Promise<void> {
    if (this.status === 'loading' || this.status === 'ready') return;
    await this.#fetch();
  }

  /** Force a fresh fetch regardless of current status. */
  async refresh(): Promise<void> {
    await this.#fetch();
  }

  async #fetch(): Promise<void> {
    this.status = 'loading';
    this.error = null;
    try {
      const page = await get<StatusPage>('/api/v1/me/imap-imports/status');
      this.items = page?.items ?? [];
      this.status = 'ready';
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err);
      this.status = 'error';
    }
  }

  /** Return the live status for one account, or null if not found. */
  forAccount(accountId: string): IMAPImportMyStatus | null {
    return this.items.find((i) => i.account_id === accountId) ?? null;
  }
}

/** Module-level singleton. Import as `imapImportLiveStatus`. */
export const imapImportLiveStatus = new IMAPImportLiveStatusStore();
