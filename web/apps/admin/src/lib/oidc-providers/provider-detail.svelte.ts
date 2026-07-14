/**
 * OIDC provider detail state: the claim-to-grant mapping surface (epic
 * #188, REQ-AC-60..70) for a single provider.
 *
 * Composes three independent server resources under one provider id:
 *   - The provider row itself (GET /api/v1/oidc/providers/{id}), whose
 *     authz_trusted flag is flipped via
 *     PUT /api/v1/oidc/providers/{id}/authz-trusted (superadmin-only,
 *     REQ-AC-66).
 *   - Its authorization-claim allowlist
 *     (GET/POST .../claim-allowlist, DELETE .../claim-allowlist/{claim}).
 *   - Its claim-mapping rules
 *     (GET/POST .../claim-mapping-rules, DELETE .../claim-mapping-rules/{id}).
 *     Each rule DTO carries `orphaned` / `author_authority_valid`
 *     (REQ-AC-68) computed live by the server on every list call.
 *
 * Every mutation method re-fetches the affected resource from the server
 * afterwards rather than writing an optimistic local copy, so the
 * displayed state always matches the server's audited truth (the ticket's
 * explicit requirement) -- in particular authz_trusted and the rule
 * orphaned/author_authority_valid flags are facts the server computes,
 * not values the client can safely predict.
 */

import { apiGet, apiPost, apiPut, apiDelete } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface OIDCProviderRecord {
  id: string;
  name: string;
  issuer: string;
  client_id: string;
  scopes: string[];
  auto_provision: boolean;
  auto_provision_domain?: string;
  authz_trusted: boolean;
  created_at: string;
}

export interface ClaimAllowlistEntry {
  claim: string;
}

export interface ClaimMappingRule {
  id: number;
  provider: string;
  claim: string;
  match_value: string;
  resource_kind: string;
  resource_id: string;
  level: string;
  created_by?: number;
  orphaned: boolean;
  author_authority_valid: boolean;
  created_at: string;
}

export interface CreateClaimMappingRulePayload {
  claim: string;
  match_value: string;
  resource_kind: string;
  resource_id: string;
  level: string;
}

export type ProviderDetailStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

class ProviderDetailState {
  status = $state<ProviderDetailStatus>('idle');
  provider = $state<OIDCProviderRecord | null>(null);
  allowlist = $state<ClaimAllowlistEntry[]>([]);
  rules = $state<ClaimMappingRule[]>([]);
  errorMessage = $state<string | null>(null);

  async load(id: string): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.provider = null;
    this.allowlist = [];
    this.rules = [];

    const [providerResult, allowlistResult, rulesResult] = await Promise.allSettled([
      apiGet<OIDCProviderRecord>(`/api/v1/oidc/providers/${encodeURIComponent(id)}`),
      apiGet<{ items: ClaimAllowlistEntry[]; next: string | null }>(
        `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-allowlist`,
      ),
      apiGet<{ items: ClaimMappingRule[]; next: string | null }>(
        `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-mapping-rules`,
      ),
    ]);

    if (providerResult.status === 'fulfilled' && providerResult.value.ok && providerResult.value.data) {
      this.provider = providerResult.value.data;
    } else {
      this.errorMessage =
        providerResult.status === 'fulfilled'
          ? (providerResult.value.errorMessage ?? t('oidcProviderDetail.error.loadFailed'))
          : t('oidcProviderDetail.error.networkError');
      this.status = 'error';
      return;
    }

    this.allowlist =
      allowlistResult.status === 'fulfilled' && allowlistResult.value.ok && allowlistResult.value.data
        ? (allowlistResult.value.data.items ?? [])
        : [];
    this.rules =
      rulesResult.status === 'fulfilled' && rulesResult.value.ok && rulesResult.value.data
        ? (rulesResult.value.data.items ?? [])
        : [];

    this.status = 'ready';
  }

  async reloadAllowlist(id: string): Promise<void> {
    const result = await apiGet<{ items: ClaimAllowlistEntry[]; next: string | null }>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-allowlist`,
    );
    if (result.ok && result.data) {
      this.allowlist = result.data.items ?? [];
    }
  }

  async reloadRules(id: string): Promise<void> {
    const result = await apiGet<{ items: ClaimMappingRule[]; next: string | null }>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-mapping-rules`,
    );
    if (result.ok && result.data) {
      this.rules = result.data.items ?? [];
    }
  }

  /**
   * Flip authz_trusted (REQ-AC-66, superadmin-only server-side). Re-fetches
   * the provider row afterwards -- the PUT response already carries the new
   * value, but the follow-up GET is the same "trust the server" pattern
   * every other mutation here uses rather than trusting the PUT response
   * alone.
   */
  async setAuthzTrusted(id: string, trusted: boolean): Promise<OpResult> {
    const result = await apiPut<OIDCProviderRecord>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/authz-trusted`,
      { authz_trusted: trusted },
    );
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('oidcProviderDetail.error.setTrustedFailed') };
    }
    const fresh = await apiGet<OIDCProviderRecord>(`/api/v1/oidc/providers/${encodeURIComponent(id)}`);
    if (fresh.ok && fresh.data) {
      this.provider = fresh.data;
    } else if (result.data) {
      this.provider = result.data;
    }
    return { ok: true, errorMessage: null };
  }

  async addAllowlistClaim(id: string, claim: string): Promise<OpResult> {
    const result = await apiPost<ClaimAllowlistEntry>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-allowlist`,
      { claim },
    );
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('oidcProviderDetail.error.addClaimFailed') };
    }
    await this.reloadAllowlist(id);
    return { ok: true, errorMessage: null };
  }

  async deleteAllowlistClaim(id: string, claim: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-allowlist/${encodeURIComponent(claim)}`,
    );
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('oidcProviderDetail.error.removeClaimFailed') };
    }
    await this.reloadAllowlist(id);
    return { ok: true, errorMessage: null };
  }

  async createRule(id: string, payload: CreateClaimMappingRulePayload): Promise<OpResult> {
    const result = await apiPost<ClaimMappingRule>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-mapping-rules`,
      payload,
    );
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('oidcProviderDetail.error.createRuleFailed') };
    }
    await this.reloadRules(id);
    return { ok: true, errorMessage: null };
  }

  async deleteRule(id: string, ruleId: number): Promise<OpResult> {
    const result = await apiDelete<unknown>(
      `/api/v1/oidc/providers/${encodeURIComponent(id)}/claim-mapping-rules/${ruleId}`,
    );
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('oidcProviderDetail.error.deleteRuleFailed') };
    }
    await this.reloadRules(id);
    return { ok: true, errorMessage: null };
  }
}

export const providerDetail = new ProviderDetailState();
