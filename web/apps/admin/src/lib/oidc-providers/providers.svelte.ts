/**
 * OIDC providers list state.
 *
 * Loads GET /api/v1/oidc/providers (returns {items: oidcProviderDTO[],
 * next: null} -- the admin REST surface does not paginate this list, since
 * a deployment realistically configures a handful of providers).
 */

import { apiGet } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface OIDCProviderSummary {
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

export type OIDCProvidersStatus = 'idle' | 'loading' | 'ready' | 'error';

class OIDCProvidersState {
  status = $state<OIDCProvidersStatus>('idle');
  items = $state<OIDCProviderSummary[]>([]);
  errorMessage = $state<string | null>(null);

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;

    const result = await apiGet<{ items: OIDCProviderSummary[]; next: string | null }>(
      '/api/v1/oidc/providers',
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('oidcProviders.error.loadFailed');
      this.status = 'error';
      return;
    }

    this.items = result.data.items ?? [];
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    await this.load();
  }
}

export const oidcProviders = new OIDCProvidersState();
