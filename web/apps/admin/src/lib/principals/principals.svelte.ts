/**
 * Principals list state class.
 *
 * Paginates GET /api/v1/principals using an after_id cursor + limit.
 * Client-side substring filter on the loaded list (protoadmin has no
 * search query parameter; see Phase 2 audit section 3).
 */

import { apiGet, apiPost } from '../api/client';

export interface PrincipalSummary {
  id: string;
  email: string;
  display_name: string;
  flags: number;
  created_at: string;
  quota_bytes?: number;
}

/** Wire shape for POST /api/v1/principals */
export interface CreatePrincipalPayload {
  email: string;
  password: string;
  display_name?: string;
  admin?: boolean;
}

export type PrincipalsStatus = 'idle' | 'loading' | 'ready' | 'error';

// Flag bit constants mirrored from internal/store (store.PrincipalFlag*).
export const FLAG_ADMIN = 1 << 0;
export const FLAG_TOTP_ENABLED = 1 << 1;
export const FLAG_DISABLED = 1 << 2;
export const FLAG_OIDC = 1 << 3;
export const FLAG_IGNORE_DOWNLOAD_LIMITS = 1 << 4;

const PAGE_LIMIT = 50;

class PrincipalsState {
  status = $state<PrincipalsStatus>('idle');
  items = $state<PrincipalSummary[]>([]);
  cursor = $state<string>('0');
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);
  search = $state('');

  /** Items filtered by the current search string (client-side). */
  filtered = $derived(
    this.search.trim() === ''
      ? this.items
      : (() => {
          const needle = this.search.trim().toLowerCase();
          return this.items.filter(
            (p) =>
              p.email.toLowerCase().includes(needle) ||
              (p.display_name && p.display_name.toLowerCase().includes(needle)),
          );
        })(),
  );

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = '0';
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<PrincipalSummary[]>(
      `/api/v1/principals?after_id=0&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load principals';
      this.status = 'error';
      return;
    }

    this.items = result.data;
    this.hasMore = result.data.length === PAGE_LIMIT;
    const lastItem = result.data[result.data.length - 1];
    if (lastItem !== undefined) {
      this.cursor = lastItem.id;
    }
    this.status = 'ready';
  }

  async loadMore(): Promise<void> {
    if (!this.hasMore || this.status === 'loading') return;
    this.status = 'loading';

    const result = await apiGet<PrincipalSummary[]>(
      `/api/v1/principals?after_id=${this.cursor}&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load more principals';
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...result.data];
    this.hasMore = result.data.length === PAGE_LIMIT;
    const lastItem = result.data[result.data.length - 1];
    if (lastItem !== undefined) {
      this.cursor = lastItem.id;
    }
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    await this.load();
  }

  async create(payload: CreatePrincipalPayload): Promise<{ ok: boolean; errorMessage: string | null; id?: string }> {
    const result = await apiPost<{ id: string }>('/api/v1/principals', payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Create failed' };
    }
    // Reload the full list so the new item appears.
    await this.load();
    return { ok: true, errorMessage: null, id: result.data?.id };
  }
}

export const principals = new PrincipalsState();
