/**
 * Admin IMAP import diagnostics state.
 *
 * Fetches the live worker snapshot from GET /api/v1/imap-imports/status and
 * provides a per-account debug-log toggle via
 * PATCH /api/v1/principals/{pid}/imap-imports/{aid}.
 *
 * The worker snapshot does not carry configuration fields (excluded
 * folders), so after loading it the store separately fetches each distinct
 * principal's account list from GET /api/v1/principals/{pid}/imap-imports
 * and merges excluded_folders in by account id (re #305).
 *
 * REQ-ADM-305, re #138, re #305.
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

/**
 * Wire shape of one item from GET /api/v1/principals/{pid}/imap-imports
 * (imapImportAccountDTO in internal/protoadmin/imap_import.go). Only the
 * fields the admin excluded-folders editor needs are declared here.
 */
interface IMAPImportConfigDTO {
  id: string;
  excluded_folders?: string[];
}

interface ConfigPage {
  items: IMAPImportConfigDTO[];
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
  /** account_id -> excluded_folders, merged in from the per-principal config list (re #305). */
  excludedFoldersByAccount = $state<Record<string, string[]>>({});

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
    await this.#loadExcludedFolders();
  }

  /**
   * Fetch excluded_folders for every worker by listing each distinct
   * principal's configured import accounts and merging by account id.
   * Best-effort: a failed fetch for one principal leaves that principal's
   * accounts without excluded-folders data rather than failing the whole
   * diagnostics load (re #305).
   */
  async #loadExcludedFolders(): Promise<void> {
    const principalIds = [...new Set(this.workers.map((w) => w.principal_id))];
    const merged: Record<string, string[]> = {};
    await Promise.all(
      principalIds.map(async (pid) => {
        const result = await apiGet<ConfigPage>(
          `/api/v1/principals/${pid}/imap-imports`,
        );
        if (!result.ok || !result.data) return;
        for (const item of result.data.items ?? []) {
          merged[item.id] = item.excluded_folders ?? [];
        }
      }),
    );
    this.excludedFoldersByAccount = merged;
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

  /**
   * Replace the excluded-folders list for one account.
   * Calls PATCH /api/v1/principals/{pid}/imap-imports/{aid} with
   * {"excluded_folders": folders}; an explicit empty array clears the
   * list, matching the REST endpoint's absent-preserves/[]-clears contract
   * (re #305). On success updates the local cache optimistically.
   */
  async setExcludedFolders(
    principalId: string,
    accountId: string,
    folders: string[],
  ): Promise<OpResult> {
    const result = await apiPatch<unknown>(
      `/api/v1/principals/${principalId}/imap-imports/${accountId}`,
      { excluded_folders: folders },
    );
    if (!result.ok) {
      return {
        ok: false,
        errorMessage:
          result.errorMessage ?? t('imapImports.error.setExcludedFoldersFailed'),
      };
    }
    this.excludedFoldersByAccount = {
      ...this.excludedFoldersByAccount,
      [accountId]: folders,
    };
    return { ok: true, errorMessage: null };
  }
}

/** Module-level singleton. Import as `imapImports`. */
export const imapImports = new IMAPImportsState();
