/**
 * Admin IMAP import diagnostics state.
 *
 * Fetches the live worker snapshot from GET /api/v1/imap-imports/status and
 * provides a per-account debug-log toggle via
 * PATCH /api/v1/principals/{pid}/imap-imports/{aid}.
 *
 * REQ-ADM-305, re #138.
 */

import { apiGet, apiPatch } from '../api/client';
import { t } from '../i18n/i18n.svelte';

/** Wire shape of one item from GET /api/v1/imap-imports/status. */
export interface IMAPImportWorkerStatus {
  account_id: string;
  principal_id: string;
  account_name: string;
  host: string;
  phase: string;
  current_folder?: string;
  conn_mode?: string;
  watch_mode?: string;
  connected: boolean;
  phase_since: string;
  last_sync_at?: string;
  consecutive_failures: number;
  next_poll_at?: string;
  messages_fetched: number;
  flags_propagated: number;
  last_error?: string;
  debug_log: boolean;
}

interface StatusPage {
  items: IMAPImportWorkerStatus[];
}

export type DiagnosticsStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

class IMAPImportsState {
  status = $state<DiagnosticsStatus>('idle');
  workers = $state<IMAPImportWorkerStatus[]>([]);
  errorMessage = $state<string | null>(null);

  async load(): Promise<void> {
    if (this.status === 'loading') return;
    this.status = 'loading';
    this.errorMessage = null;
    const result = await apiGet<StatusPage>('/api/v1/imap-imports/status');
    if (!result.ok || !result.data) {
      this.errorMessage = result.errorMessage ?? t('imapImports.error.loadFailed');
      this.status = 'error';
      return;
    }
    this.workers = result.data.items ?? [];
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    this.status = 'idle';
    await this.load();
  }

  /**
   * Toggle debug logging for one account.
   * Calls PATCH /api/v1/principals/{pid}/imap-imports/{aid} with
   * {"debug_log": enabled}. On success updates the local worker entry
   * optimistically so the toggle reflects immediately.
   */
  async setDebugLog(
    principalId: string,
    accountId: string,
    enabled: boolean,
  ): Promise<OpResult> {
    const result = await apiPatch<unknown>(
      `/api/v1/principals/${principalId}/imap-imports/${accountId}`,
      { debug_log: enabled },
    );
    if (!result.ok) {
      return {
        ok: false,
        errorMessage: result.errorMessage ?? t('imapImports.error.setDebugLogFailed'),
      };
    }
    // Optimistically update local state so the toggle reflects immediately.
    this.workers = this.workers.map((w) =>
      w.account_id === accountId ? { ...w, debug_log: enabled } : w,
    );
    return { ok: true, errorMessage: null };
  }
}

/** Module-level singleton. Import as `imapImports`. */
export const imapImports = new IMAPImportsState();
