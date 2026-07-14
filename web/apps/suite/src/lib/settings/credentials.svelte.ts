/**
 * Unified active-credentials store (issue #224).
 *
 * Backs the Settings "Active credentials" panel against the server's
 * unified enumeration endpoint (internal/protoadmin/credentials.go):
 *
 *   GET    /api/v1/auth/credentials              list the caller's own credentials
 *   DELETE /api/v1/auth/credentials/{kind}/{id}   revoke one credential
 *
 * The endpoint composes three underlying credential kinds for the calling
 * principal -- web sessions, device tokens, and OAuth2 native-client grants
 * (see the server doc comment for the exact mapping) -- into one list, each
 * entry carrying a `kind` discriminator. Revocation cascades server-side
 * (an oauth2_grant revoke tears down its whole refresh-token family; a
 * session revoke clears cookies) so `revoke()` never mutates `items`
 * optimistically -- it always re-fetches the list after the DELETE
 * succeeds, keeping the displayed state identical to the audited server
 * truth rather than a client-side guess about cascade effects.
 */

import { get, del, UnauthenticatedError } from '../api/client';

export type CredentialKind = 'session' | 'device_token' | 'oauth2_grant';

/** Mirrors internal/protoadmin/credentials.go's credentialDTO. */
export interface CredentialDTO {
  kind: CredentialKind;
  id: string;
  label?: string;
  client_id?: string;
  created_at: string;
  last_used_at?: string;
  expires_at?: string;
  last_seen_ip?: string;
  user_agent?: string;
  is_current: boolean;
}

interface PageDTO<T> {
  items: T[];
  next: string | null;
}

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

class CredentialsStore {
  items = $state<CredentialDTO[]>([]);
  status = $state<LoadStatus>('idle');
  errorMessage = $state<string | null>(null);

  get loading(): boolean {
    return this.status === 'loading' || this.status === 'idle';
  }

  /**
   * Fetch the caller's credentials from the unified endpoint. A 401 is
   * left to the shared client's global `_onUnauthenticated` callback
   * (registered once by auth.svelte.ts, same as every other REST call
   * in the suite) -- it has already transitioned the auth state machine
   * to forced-login by the time the exception reaches this catch block,
   * so no error state is set here; the settings view unmounts under the
   * forced-login overlay.
   */
  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    try {
      const result = await get<PageDTO<CredentialDTO>>('/api/v1/auth/credentials');
      this.items = result.items ?? [];
      this.status = 'ready';
    } catch (err) {
      if (err instanceof UnauthenticatedError) {
        return;
      }
      this.errorMessage = err instanceof Error ? err.message : String(err);
      this.status = 'error';
    }
  }

  /**
   * Revoke one credential, then re-fetch the list. Revoking the caller's
   * own current session clears their cookies server-side, so the
   * follow-up load() legitimately 401s -- handled the same way load()
   * always handles a 401 (see above).
   */
  async revoke(kind: CredentialKind, id: string): Promise<void> {
    await del<void>(`/api/v1/auth/credentials/${kind}/${encodeURIComponent(id)}`);
    await this.load();
  }

  /** The credential row for the session the caller is currently viewing from. */
  get current(): CredentialDTO | undefined {
    return this.items.find((c) => c.is_current);
  }
}

export const credentials = new CredentialsStore();
