/**
 * Queue list state class.
 *
 * Paginates GET /api/v1/queue using after_id cursor + limit.
 * Supports state and principal_id server-side filters.
 * Client-side substring filter applied to the loaded page for
 * sender / recipient search (no server-side search param; see Phase 2
 * audit section 7).
 */

import { apiGet, apiPost } from '../api/client';

export interface QueueItem {
  id: string;
  principal_id: string;
  mail_from: string;
  rcpt_to: string;
  envelope_id: string;
  body_blob_hash?: string;
  headers_blob_hash?: string;
  state: string;
  attempts: number;
  last_attempt_at?: string;
  next_attempt_at?: string;
  last_error?: string;
  idempotency_key?: string;
  created_at?: string;
}

export type QueueStatus = 'idle' | 'loading' | 'ready' | 'error';

export type QueueStateFilter = 'all' | 'queued' | 'deferred' | 'held' | 'failed' | 'inflight' | 'done';

const PAGE_LIMIT = 50;

class QueueState {
  status = $state<QueueStatus>('idle');
  items = $state<QueueItem[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  /** Server-side filters -- set these before calling load(). */
  stateFilter = $state<QueueStateFilter>('all');
  principalIdFilter = $state<string>('');

  /** Client-side substring filter for sender/recipient. */
  search = $state('');

  /** Items filtered client-side by search string. */
  filtered = $derived(
    this.search.trim() === ''
      ? this.items
      : (() => {
          const needle = this.search.trim().toLowerCase();
          return this.items.filter(
            (q) =>
              q.mail_from.toLowerCase().includes(needle) ||
              q.rcpt_to.toLowerCase().includes(needle),
          );
        })(),
  );

  private buildUrl(afterId?: string): string {
    const params = new URLSearchParams();
    params.set('limit', String(PAGE_LIMIT));
    if (this.stateFilter !== 'all') {
      params.set('state', this.stateFilter);
    }
    if (this.principalIdFilter.trim()) {
      params.set('principal_id', this.principalIdFilter.trim());
    }
    if (afterId) {
      params.set('after_id', afterId);
    }
    return `/api/v1/queue?${params.toString()}`;
  }

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: QueueItem[]; next: string | null }>(this.buildUrl());

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load queue';
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

    const result = await apiGet<{ items: QueueItem[]; next: string | null }>(
      this.buildUrl(this.cursor),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load more queue items';
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...(result.data.items ?? [])];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async flush(): Promise<{ ok: boolean; errorMessage: string | null; flushed?: number }> {
    const result = await apiPost<{ flushed: number }>('/api/v1/queue/flush?state=deferred');
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Flush failed' };
    }
    await this.load();
    return { ok: true, errorMessage: null, flushed: result.data?.flushed };
  }
}

export const queue = new QueueState();
