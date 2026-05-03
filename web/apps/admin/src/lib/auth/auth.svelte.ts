/**
 * Auth state machine for the admin SPA.
 *
 * States:
 *   idle -> bootstrapping -> ready         (session cookie already valid)
 *                         -> unauthenticated (no valid session)
 *                         -> error          (server unreachable)
 *
 * Bootstrap probes GET /api/v1/server/status with credentials:'include'.
 * A 200 means the admin session cookie is present and valid. A 401 means
 * the user must log in.
 *
 * Login posts to POST /api/v1/auth/login (landed by http-api-implementor).
 * The server issues a herold_admin_session cookie plus a herold_admin_csrf
 * non-HttpOnly cookie; the client reads the CSRF token via document.cookie
 * and sends X-CSRF-Token on every mutating request (see src/lib/api/client.ts).
 *
 * On 401 from any /api/v1/ call (surfaced by client.ts via handleUnauthorized),
 * the SPA transitions to 'unauthenticated' and routes to /login.
 */

import { router } from '../router/router.svelte';

export type AuthStatus =
  | 'idle'
  | 'bootstrapping'
  | 'unauthenticated'
  | 'ready';

export interface Principal {
  id: string;
  email: string;
  scopes: string[];
}

interface ClientlogBlock {
  telemetry_enabled?: boolean;
  livetail_until?: string | null;
}

interface ServerStatusResponse {
  principal_id: string;
  email: string;
  scopes: string[];
  // clientlog block added by task #7's protoadmin extension (REQ-OPS-208/211).
  // Present once the server-side extension ships; absent on older servers.
  clientlog?: ClientlogBlock;
}

interface LoginRequest {
  email: string;
  password: string;
  totp_code?: string;
}

interface LoginResponse {
  principal_id: string;
  scopes: string[];
}

interface LoginErrorResponse {
  step_up_required?: boolean;
  message?: string;
}

export interface LoginResult {
  ok: boolean;
  stepUpRequired: boolean;
  errorMessage: string | null;
}

class Auth {
  status = $state<AuthStatus>('idle');
  principal = $state<Principal | null>(null);
  errorMessage = $state<string | null>(null);

  // clientlog predicates read by the clientlog wrapper in main.ts.
  // Defaults: telemetry enabled, no live-tail. Updated after a successful
  // session probe or login when the server returns the clientlog block.
  clientlogTelemetryEnabled = $state(true);
  clientlogLivetailUntil = $state<number | null>(null);

  /**
   * Probe the session by hitting GET /api/v1/server/status.
   * A 200 means the herold_admin_session cookie is valid.
   * A 401 means the user must authenticate.
   * Idempotent: subsequent calls while bootstrapping or ready are no-ops.
   */
  async bootstrap(): Promise<void> {
    if (this.status === 'bootstrapping' || this.status === 'ready') return;
    this.status = 'bootstrapping';
    this.errorMessage = null;
    try {
      const response = await fetch('/api/v1/server/status', {
        method: 'GET',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
      if (response.status === 200) {
        const body = (await response.json()) as ServerStatusResponse;
        this.principal = {
          id: body.principal_id,
          email: body.email,
          scopes: body.scopes,
        };
        this._applyClientlogBlock(body.clientlog);
        this.status = 'ready';
        return;
      }
      // 401 or any other non-200: force login.
      this.status = 'unauthenticated';
    } catch (err) {
      // Network error: treat as unauthenticated rather than crashing; the
      // login page will surface the connectivity problem on the next attempt.
      this.status = 'unauthenticated';
      this.errorMessage = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Submit credentials to POST /api/v1/auth/login.
   * On success the server sets the session + CSRF cookies; we read the
   * principal from the response body and transition to 'ready'.
   * On TOTP step-up required the result carries stepUpRequired: true.
   */
  async login(req: LoginRequest): Promise<LoginResult> {
    this.errorMessage = null;
    try {
      const response = await fetch('/api/v1/auth/login', {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
        },
        body: JSON.stringify(req),
      });

      if (response.status === 200) {
        const body = (await response.json()) as LoginResponse;
        this.principal = {
          id: body.principal_id,
          email: req.email,
          scopes: body.scopes,
        };
        this.status = 'ready';
        router.replace('/dashboard');
        return { ok: true, stepUpRequired: false, errorMessage: null };
      }


      if (response.status === 401) {
        let stepUpRequired = false;
        let errorMessage = 'Invalid email or password.';
        try {
          const body = (await response.json()) as LoginErrorResponse;
          if (body.step_up_required) {
            stepUpRequired = true;
            errorMessage = 'Two-factor authentication code required.';
          } else if (body.message) {
            errorMessage = body.message;
          }
        } catch {
          // ignore parse error; use defaults above
        }
        return { ok: false, stepUpRequired, errorMessage };
      }

      return {
        ok: false,
        stepUpRequired: false,
        errorMessage: `Unexpected response: HTTP ${response.status}`,
      };
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : 'Network error';
      return { ok: false, stepUpRequired: false, errorMessage };
    }
  }

  /**
   * POST /api/v1/auth/logout to clear the server-side session, then
   * transition to 'unauthenticated' and route to /login regardless of
   * server response.
   */
  async logout(): Promise<void> {
    try {
      await fetch('/api/v1/auth/logout', {
        method: 'POST',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
    } catch {
      // Swallow network errors: we are logging out regardless.
    }
    this.principal = null;
    this.status = 'unauthenticated';
    router.replace('/login');
  }

  /**
   * Called by client.ts when any /api/v1/ call returns 401.
   * Transitions to unauthenticated and routes to /login.
   */
  handleUnauthorized(): void {
    if (this.status === 'ready') {
      this.principal = null;
      this.status = 'unauthenticated';
      router.replace('/login');
    }
  }

  /**
   * Apply the optional clientlog block from the session response.
   * Updates the predicates read by the clientlog wrapper (REQ-OPS-208/211).
   */
  private _applyClientlogBlock(block: ClientlogBlock | undefined): void {
    if (!block) return;
    if (typeof block.telemetry_enabled === 'boolean') {
      this.clientlogTelemetryEnabled = block.telemetry_enabled;
    }
    if (block.livetail_until) {
      const ms = new Date(block.livetail_until).getTime();
      this.clientlogLivetailUntil = isNaN(ms) ? null : ms;
    } else {
      this.clientlogLivetailUntil = null;
    }
  }
}

export const auth = new Auth();
