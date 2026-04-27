/**
 * Domains list state class.
 *
 * Loads GET /api/v1/domains (returns {items: domainDTO[], next: string|null}).
 * The domains list is small enough to load fully; cursor pagination is
 * supported for parity with the principals pattern.
 */

import { apiGet, apiPost } from '../api/client';

export interface DomainSummary {
  name: string;
  local: boolean;
  created_at: string;
}

export type DomainsStatus = 'idle' | 'loading' | 'ready' | 'error';

const PAGE_LIMIT = 100;

class DomainsState {
  status = $state<DomainsStatus>('idle');
  items = $state<DomainSummary[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: DomainSummary[]; next: string | null }>(
      `/api/v1/domains?limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load domains';
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

    const result = await apiGet<{ items: DomainSummary[]; next: string | null }>(
      `/api/v1/domains?after_id=${this.cursor}&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load more domains';
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...(result.data.items ?? [])];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    await this.load();
  }

  async create(payload: { name: string }): Promise<{ ok: boolean; errorMessage: string | null }> {
    const result = await apiPost<DomainSummary>('/api/v1/domains', payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Create failed' };
    }
    await this.load();
    return { ok: true, errorMessage: null };
  }
}

export const domains = new DomainsState();
