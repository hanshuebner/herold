/**
 * System events state class.
 *
 * Loads GET /api/v1/admin/system-events with before_id cursor + limit.
 * Supports action (exact match), actor_id (exact match), since, until
 * server-side filters. Default limit 50; "load more" appends via cursor.
 *
 * Results are returned newest-first by the server; this module does not
 * re-sort them.
 *
 * Timestamps (at) are RFC3339 strings as returned by the server.
 */

import { apiGet } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface SystemEvent {
  id: number;
  at: string;
  action: string;
  actor_id: string;
  subject: string;
  remote_addr: string;
  outcome: 'success' | 'failure' | 'unknown';
  message: string;
  domain: string;
  metadata?: Record<string, unknown>;
}

export type EventsStatus = 'idle' | 'loading' | 'ready' | 'error';

const PAGE_LIMIT = 50;

class EventsState {
  status = $state<EventsStatus>('idle');
  items = $state<SystemEvent[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  /** Filter state. Set before calling load(). */
  actionFilter = $state('');
  actorIdFilter = $state('');
  sinceFilter = $state('');
  untilFilter = $state('');

  private buildUrl(beforeId?: string): string {
    const params = new URLSearchParams();
    params.set('limit', String(PAGE_LIMIT));
    if (this.actionFilter.trim()) {
      params.set('action', this.actionFilter.trim());
    }
    if (this.actorIdFilter.trim()) {
      params.set('actor_id', this.actorIdFilter.trim());
    }
    if (this.sinceFilter.trim()) {
      const since = toRFC3339(this.sinceFilter.trim());
      if (since) params.set('since', since);
    }
    if (this.untilFilter.trim()) {
      const until = toRFC3339(this.untilFilter.trim());
      if (until) params.set('until', until);
    }
    if (beforeId) {
      params.set('before_id', beforeId);
    }
    return `/api/v1/admin/system-events?${params.toString()}`;
  }

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: SystemEvent[]; next: string | null }>(
      this.buildUrl(),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('events.error.loadFailed');
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

    const result = await apiGet<{ items: SystemEvent[]; next: string | null }>(
      this.buildUrl(this.cursor),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('events.error.loadMoreFailed');
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
 * The input is treated as UTC. Returns null if the input does not parse.
 */
function toRFC3339(raw: string): string | null {
  const d = new Date(raw.includes(':') ? raw + ':00Z' : raw + 'T00:00:00Z');
  if (isNaN(d.getTime())) return null;
  return d.toISOString().replace('.000Z', 'Z');
}

export const events = new EventsState();
