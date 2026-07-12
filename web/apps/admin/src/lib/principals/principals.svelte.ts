/**
 * Principals list state class.
 *
 * Paginates GET /api/v1/principals using an after cursor + limit; the
 * response is the {items, next} pageDTO envelope (see
 * internal/protoadmin/principals.go handleListPrincipals), which the
 * loaders unwrap into items/cursor/hasMore. Client-side substring
 * filter on the loaded list (protoadmin has no search query parameter;
 * see Phase 2 audit section 3).
 *
 * PrincipalSummary mirrors internal/protoadmin/types.go's principalDTO
 * field-for-field: `id` is a JSON number, the email field is named
 * `canonical_email` on the wire, and `flags` is an array of flag-name
 * strings (`"admin"`, `"totp_enabled"`, `"disabled"`, `"super_admin"`,
 * `"ignore_download_limits"`, `"bypass_response_deadline"`), not a
 * bitmask (re #218).
 */

import { apiGet, apiPost } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface PrincipalSummary {
  id: number;
  canonical_email: string;
  display_name: string;
  flags: string[];
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
              p.canonical_email.toLowerCase().includes(needle) ||
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

    const result = await apiGet<{ items: PrincipalSummary[]; next: string | null }>(
      `/api/v1/principals?after=0&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('principals.error.loadFailed');
      this.status = 'error';
      return;
    }

    const items = result.data.items ?? [];
    this.items = items;
    this.hasMore = items.length === PAGE_LIMIT;
    const lastItem = items[items.length - 1];
    if (lastItem !== undefined) {
      this.cursor = String(lastItem.id);
    }
    this.status = 'ready';
  }

  async loadMore(): Promise<void> {
    if (!this.hasMore || this.status === 'loading') return;
    this.status = 'loading';

    const result = await apiGet<{ items: PrincipalSummary[]; next: string | null }>(
      `/api/v1/principals?after=${this.cursor}&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('principals.error.loadMoreFailed');
      this.status = 'ready';
      return;
    }

    const items = result.data.items ?? [];
    this.items = [...this.items, ...items];
    this.hasMore = items.length === PAGE_LIMIT;
    const lastItem = items[items.length - 1];
    if (lastItem !== undefined) {
      this.cursor = String(lastItem.id);
    }
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    await this.load();
  }

  async create(payload: CreatePrincipalPayload): Promise<{ ok: boolean; errorMessage: string | null; id?: number }> {
    const result = await apiPost<{ id: number }>('/api/v1/principals', payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('principals.error.createFailed') };
    }
    // Reload the full list so the new item appears.
    await this.load();
    return { ok: true, errorMessage: null, id: result.data?.id };
  }
}

export const principals = new PrincipalsState();
