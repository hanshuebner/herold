/**
 * Dashboard state class.
 *
 * Aggregates data from three parallel fetches:
 *   GET /api/v1/queue/stats  -> queue counts by state
 *   GET /api/v1/audit        -> recent audit log entries (limit=10)
 *   GET /api/v1/domains      -> local domain list
 *
 * Uses Promise.allSettled so partial failures show degraded cards rather
 * than nuking the entire page.
 *
 * Refresh on window 'focus' is wired by the DashboardView via a $effect.
 */

import { apiGet } from '../api/client';

export interface QueueStats {
  queued?: number;
  deferred?: number;
  delivered?: number;
  failed?: number;
  held?: number;
  [key: string]: number | undefined;
}

export interface AuditEntry {
  id: string;
  action: string;
  principal_id?: string;
  principal_email?: string;
  target_id?: string;
  detail?: string;
  created_at: string;
}

export interface Domain {
  name: string;
  created_at: string;
}

export type DashboardStatus = 'idle' | 'loading' | 'ready' | 'error';

class DashboardState {
  status = $state<DashboardStatus>('idle');

  queueStats = $state<QueueStats | null>(null);
  queueError = $state<string | null>(null);

  auditEntries = $state<AuditEntry[]>([]);
  auditError = $state<string | null>(null);

  domains = $state<Domain[]>([]);
  domainsError = $state<string | null>(null);

  /** Total active queue items for the summary card. */
  queueTotal = $derived(
    (this.queueStats?.queued ?? 0) +
    (this.queueStats?.deferred ?? 0) +
    (this.queueStats?.held ?? 0),
  );

  async load(): Promise<void> {
    this.status = 'loading';

    const [queueResult, auditResult, domainsResult] = await Promise.allSettled([
      apiGet<QueueStats>('/api/v1/queue/stats'),
      apiGet<{ entries: AuditEntry[] } | AuditEntry[]>('/api/v1/audit?limit=10'),
      apiGet<Domain[]>('/api/v1/domains'),
    ]);

    // Queue stats
    if (queueResult.status === 'fulfilled' && queueResult.value.ok && queueResult.value.data) {
      this.queueStats = queueResult.value.data;
      this.queueError = null;
    } else {
      this.queueStats = null;
      this.queueError =
        queueResult.status === 'fulfilled'
          ? (queueResult.value.errorMessage ?? 'Failed to load queue stats')
          : 'Network error loading queue stats';
    }

    // Audit entries -- API may return array or {entries:[...]} envelope
    if (auditResult.status === 'fulfilled' && auditResult.value.ok && auditResult.value.data) {
      const raw = auditResult.value.data;
      this.auditEntries = Array.isArray(raw) ? raw : (raw as { entries: AuditEntry[] }).entries ?? [];
      this.auditError = null;
    } else {
      this.auditEntries = [];
      this.auditError =
        auditResult.status === 'fulfilled'
          ? (auditResult.value.errorMessage ?? 'Failed to load audit log')
          : 'Network error loading audit log';
    }

    // Domains
    if (domainsResult.status === 'fulfilled' && domainsResult.value.ok && domainsResult.value.data) {
      this.domains = domainsResult.value.data;
      this.domainsError = null;
    } else {
      this.domains = [];
      this.domainsError =
        domainsResult.status === 'fulfilled'
          ? (domainsResult.value.errorMessage ?? 'Failed to load domains')
          : 'Network error loading domains';
    }

    this.status = 'ready';
  }
}

export const dashboard = new DashboardState();
