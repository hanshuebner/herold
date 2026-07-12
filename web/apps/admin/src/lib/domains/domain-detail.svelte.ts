/**
 * Domain detail state class.
 *
 * Composes the domain detail view from:
 *   - The domain record fetched from the list (GET /api/v1/domains, filtered).
 *   - Its alias list from GET /api/v1/aliases?domain={name}.
 *
 * There is no GET /api/v1/domains/{name} endpoint; the detail is synthesised
 * from the list response (see Phase 2 audit section 4).
 */

import { apiGet, apiPost, apiDelete } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface DomainRecord {
  name: string;
  local: boolean;
  created_at: string;
}

// AliasRecord's target is exactly one of target_principal_id (an
// internal principal) or target_address (an external forwarding
// target, re #181); the other is absent from the wire response
// (aliasDTO uses `omitempty` on both).
export interface AliasRecord {
  id: string;
  local: string;
  domain: string;
  target_principal_id?: number;
  target_address?: string;
  expires_at?: string | null;
  created_at: string;
}

export interface CreateAliasPayload {
  local: string;
  domain: string;
  target_principal_id?: number;
  target_address?: string;
  expires_at?: string;
}

export type DomainDetailStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

class DomainDetailState {
  status = $state<DomainDetailStatus>('idle');
  domain = $state<DomainRecord | null>(null);
  aliases = $state<AliasRecord[]>([]);
  errorMessage = $state<string | null>(null);

  async load(name: string): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.domain = null;
    this.aliases = [];

    // Fetch domain from list + aliases in parallel.
    const [domainsResult, aliasesResult] = await Promise.allSettled([
      apiGet<{ items: DomainRecord[]; next: string | null }>('/api/v1/domains?limit=200'),
      apiGet<{ items: AliasRecord[]; next: string | null }>(`/api/v1/aliases?domain=${encodeURIComponent(name)}`),
    ]);

    if (domainsResult.status === 'fulfilled' && domainsResult.value.ok && domainsResult.value.data) {
      const found = (domainsResult.value.data.items ?? []).find((d) => d.name === name);
      if (found) {
        this.domain = found;
      } else {
        this.errorMessage = t('domainDetail.error.notFound');
        this.status = 'error';
        return;
      }
    } else {
      this.errorMessage =
        domainsResult.status === 'fulfilled'
          ? (domainsResult.value.errorMessage ?? t('domainDetail.error.loadFailed'))
          : t('domainDetail.error.networkError');
      this.status = 'error';
      return;
    }

    if (aliasesResult.status === 'fulfilled' && aliasesResult.value.ok && aliasesResult.value.data) {
      this.aliases = aliasesResult.value.data.items ?? [];
    } else {
      this.aliases = [];
    }

    this.status = 'ready';
  }

  async loadAliases(name: string): Promise<void> {
    const result = await apiGet<{ items: AliasRecord[]; next: string | null }>(
      `/api/v1/aliases?domain=${encodeURIComponent(name)}`,
    );
    if (result.ok && result.data) {
      this.aliases = result.data.items ?? [];
    }
  }

  async createAlias(payload: CreateAliasPayload): Promise<OpResult> {
    const result = await apiPost<AliasRecord>('/api/v1/aliases', payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('domainDetail.error.createAliasFailed') };
    }
    // Refresh the alias list.
    if (this.domain) {
      await this.loadAliases(this.domain.name);
    }
    return { ok: true, errorMessage: null };
  }

  async deleteAlias(id: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/aliases/${id}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('domainDetail.error.deleteAliasFailed') };
    }
    this.aliases = this.aliases.filter((a) => a.id !== id);
    return { ok: true, errorMessage: null };
  }

  async deleteDomain(name: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/domains/${encodeURIComponent(name)}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('domainDetail.error.deleteDomainFailed') };
    }
    return { ok: true, errorMessage: null };
  }
}

export const domainDetail = new DomainDetailState();
