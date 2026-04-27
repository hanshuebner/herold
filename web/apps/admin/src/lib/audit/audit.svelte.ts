/**
 * Audit log state class.
 *
 * Loads GET /api/v1/audit with after_id cursor + limit.
 * Supports action (contains), principal_id, since, until server-side
 * filters.  Default limit 50; "load more" appends via cursor.
 *
 * Timestamps (at) are RFC3339 strings as returned by auditEntryToMap.
 */

import { apiGet } from '../api/client';

export interface AuditEntry {
  id: string;
  at: string;
  actor_kind: string;
  actor_id: string;
  action: string;
  subject: string;
  remote_addr: string;
  outcome: string;
  message: string;
  metadata?: Record<string, string>;
}

export type AuditStatus = 'idle' | 'loading' | 'ready' | 'error';

const PAGE_LIMIT = 50;

class AuditState {
  status = $state<AuditStatus>('idle');
  items = $state<AuditEntry[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  /** Filter state. Set before calling load(). */
  actionFilter = $state('');
  principalIdFilter = $state('');
  sinceFilter = $state('');
  untilFilter = $state('');

  private buildUrl(afterId?: string): string {
    const params = new URLSearchParams();
    params.set('limit', String(PAGE_LIMIT));
    if (this.actionFilter.trim()) {
      params.set('action', this.actionFilter.trim());
    }
    if (this.principalIdFilter.trim()) {
      params.set('principal_id', this.principalIdFilter.trim());
    }
    if (this.sinceFilter.trim()) {
      // datetime-local input gives "YYYY-MM-DDTHH:MM"; append Z for UTC RFC3339.
      const since = toRFC3339(this.sinceFilter.trim());
      if (since) params.set('since', since);
    }
    if (this.untilFilter.trim()) {
      const until = toRFC3339(this.untilFilter.trim());
      if (until) params.set('until', until);
    }
    if (afterId) {
      params.set('after_id', afterId);
    }
    return `/api/v1/audit?${params.toString()}`;
  }

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: AuditEntry[]; next: string | null }>(this.buildUrl());

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load audit log';
      this.status = 'error';
      return;
    }

    this.items = result.data.items ?? [];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async loadMore(): Promise<void> {
    if (!this.hasMore || this.status === 'loading' || this.cursor === null) return;
    this.status = 'loading';

    const result = await apiGet<{ items: AuditEntry[]; next: string | null }>(
      this.buildUrl(this.cursor),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load more audit entries';
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...(result.data.items ?? [])];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }
}

/**
 * Convert a datetime-local input value ("YYYY-MM-DDTHH:MM") to RFC3339.
 * The input is treated as UTC.  Returns null if the input does not parse.
 */
function toRFC3339(raw: string): string | null {
  // datetime-local gives "YYYY-MM-DDTHH:MM" (no seconds, no tz).
  const d = new Date(raw.includes(':') ? raw + ':00Z' : raw + 'T00:00:00Z');
  if (isNaN(d.getTime())) return null;
  return d.toISOString().replace('.000Z', 'Z');
}

export const audit = new AuditState();
