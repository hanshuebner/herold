/**
 * Principal detail state class.
 *
 * Fetches and manages state for a single principal's detail page, including
 * their profile, API keys, and OIDC links. TOTP status is inferred from the
 * principal flags (FLAG_TOTP_ENABLED bit).
 *
 * Each mutating operation returns {ok, errorMessage} so the view can surface
 * inline errors without navigating away.
 */

import { apiGet, apiPost, apiPatch, apiPut, apiDelete } from '../api/client';
import { FLAG_TOTP_ENABLED } from './principals.svelte';

export interface PrincipalDetail {
  id: string;
  email: string;
  display_name: string;
  flags: number;
  quota_bytes: number;
  created_at: string;
}

export interface APIKey {
  id: string;
  principal_id: string;
  name: string;
  scopes: string[];
  created_at: string;
  last_used_at?: string;
}

export interface OIDCLink {
  provider_id: string;
  provider_name?: string;
  subject: string;
  linked_at: string;
}

export interface TOTPStatus {
  enabled: boolean;
  provisioning_uri?: string;
}

export type DetailStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

export interface CreateAPIKeyResult extends OpResult {
  plaintext?: string;
  key?: APIKey;
}

export interface TOTPEnrollResult extends OpResult {
  provisioning_uri?: string;
}

class PrincipalDetailState {
  status = $state<DetailStatus>('idle');
  principal = $state<PrincipalDetail | null>(null);
  apiKeys = $state<APIKey[]>([]);
  oidcLinks = $state<OIDCLink[]>([]);
  errorMessage = $state<string | null>(null);

  totpEnabled = $derived(
    this.principal !== null
      ? (this.principal.flags & FLAG_TOTP_ENABLED) !== 0
      : false,
  );

  async load(id: string): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;

    const [principalResult, keysResult, oidcResult] = await Promise.allSettled([
      apiGet<PrincipalDetail>(`/api/v1/principals/${id}`),
      apiGet<APIKey[]>(`/api/v1/principals/${id}/api-keys`),
      apiGet<OIDCLink[]>(`/api/v1/principals/${id}/oidc-links`),
    ]);

    if (principalResult.status === 'fulfilled' && principalResult.value.ok) {
      this.principal = principalResult.value.data;
    } else {
      const msg =
        principalResult.status === 'fulfilled'
          ? (principalResult.value.errorMessage ?? 'Failed to load principal')
          : 'Network error';
      this.errorMessage = msg;
      this.status = 'error';
      return;
    }

    if (keysResult.status === 'fulfilled' && keysResult.value.ok && keysResult.value.data) {
      this.apiKeys = keysResult.value.data;
    } else {
      this.apiKeys = [];
    }

    if (oidcResult.status === 'fulfilled' && oidcResult.value.ok && oidcResult.value.data) {
      this.oidcLinks = oidcResult.value.data;
    } else {
      this.oidcLinks = [];
    }

    this.status = 'ready';
  }

  async updateProfile(id: string, patch: { display_name?: string; quota_bytes?: number; flags?: number }): Promise<OpResult> {
    const result = await apiPatch<PrincipalDetail>(`/api/v1/principals/${id}`, patch);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Update failed' };
    }
    if (result.data) {
      this.principal = result.data;
    }
    return { ok: true, errorMessage: null };
  }

  async changePassword(id: string, payload: { current_password?: string; new_password: string }): Promise<OpResult> {
    const result = await apiPut<unknown>(`/api/v1/principals/${id}/password`, payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Password change failed' };
    }
    return { ok: true, errorMessage: null };
  }

  async enrollTOTP(id: string): Promise<TOTPEnrollResult> {
    const result = await apiPost<{ provisioning_uri: string; secret?: string }>(`/api/v1/principals/${id}/totp/enroll`);
    if (!result.ok || !result.data) {
      return { ok: false, errorMessage: result.errorMessage ?? 'TOTP enroll failed' };
    }
    return { ok: true, errorMessage: null, provisioning_uri: result.data.provisioning_uri };
  }

  async confirmTOTP(id: string, code: string): Promise<OpResult> {
    const result = await apiPost<unknown>(`/api/v1/principals/${id}/totp/confirm`, { code });
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'TOTP confirm failed' };
    }
    // Refresh principal to update flags.
    await this.load(id);
    return { ok: true, errorMessage: null };
  }

  async disableTOTP(id: string, current_password: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/principals/${id}/totp`, { current_password });
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'TOTP disable failed' };
    }
    // Refresh principal to update flags.
    await this.load(id);
    return { ok: true, errorMessage: null };
  }

  async createAPIKey(id: string, payload: { label: string; scopes: string[] }): Promise<CreateAPIKeyResult> {
    const result = await apiPost<{ id: string; key: string; name: string; scopes: string[]; created_at: string }>(`/api/v1/principals/${id}/api-keys`, {
      name: payload.label,
      scopes: payload.scopes,
    });
    if (!result.ok || !result.data) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Key creation failed' };
    }
    // Refresh keys list.
    const keysResult = await apiGet<APIKey[]>(`/api/v1/principals/${id}/api-keys`);
    if (keysResult.ok && keysResult.data) {
      this.apiKeys = keysResult.data;
    }
    return {
      ok: true,
      errorMessage: null,
      plaintext: result.data.key,
      key: {
        id: result.data.id,
        principal_id: id,
        name: result.data.name,
        scopes: result.data.scopes ?? [],
        created_at: result.data.created_at,
      },
    };
  }

  async revokeAPIKey(principalId: string, keyId: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/api-keys/${keyId}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Revoke failed' };
    }
    this.apiKeys = this.apiKeys.filter((k) => k.id !== keyId);
    return { ok: true, errorMessage: null };
  }

  async beginOIDCLink(id: string, providerId: string): Promise<{ ok: boolean; auth_url?: string; errorMessage: string | null }> {
    const result = await apiPost<{ auth_url: string; state: string }>(`/api/v1/principals/${id}/oidc-links/begin`, { provider_id: providerId });
    if (!result.ok || !result.data) {
      return { ok: false, errorMessage: result.errorMessage ?? 'OIDC link begin failed' };
    }
    return { ok: true, auth_url: result.data.auth_url, errorMessage: null };
  }

  async unlinkOIDC(id: string, provider: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/principals/${id}/oidc-links/${provider}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Unlink failed' };
    }
    this.oidcLinks = this.oidcLinks.filter((l) => l.provider_id !== provider);
    return { ok: true, errorMessage: null };
  }

  async deletePrincipal(id: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/principals/${id}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? 'Delete failed' };
    }
    return { ok: true, errorMessage: null };
  }
}

export const principalDetail = new PrincipalDetailState();
