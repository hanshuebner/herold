/**
 * Auth state machine for the admin SPA.
 *
 * States:
 *   idle -> bootstrapping -> ready              (active elevation present)
 *                         -> step_up_pending    (authenticated, no elevation)
 *                         -> unauthenticated    (no valid session)
 *                         -> error              (server unreachable)
 *
 * Bootstrap probes GET /api/v1/auth/me (no elevation required) to confirm
 * session validity and read roles + elevation_expires_at. Based on those values:
 *   - No session (401) -> unauthenticated
 *   - Non-admin role   -> unauthenticated
 *   - Admin role, active elevation -> fetch server/status, then ready
 *   - Admin role, no elevation     -> step_up_pending (REQ-AS-21, re #79)
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
  | 'step_up_pending'
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

interface AuthMeResponse {
  principal_id: string;
  email: string;
  scopes: string[];
  /** Role memberships; 'admin' or 'superadmin' grants access to the admin SPA. */
  roles?: string[];
  /** RFC 3339 timestamp when current TOTP elevation expires, or null. */
  elevation_expires_at?: string | null;
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
  roles?: string[];
  elevation_expires_at?: string | null;
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
  /**
   * Role memberships for the current principal.
   * 'admin' or 'superadmin' grants access to the admin SPA.
   */
  roles = $state<string[]>([]);
  /**
   * When the current TOTP elevation expires, or null when no elevation is
   * active. Updated by updateElevation() after a successful step-up.
   */
  elevationExpiresAt = $state<Date | null>(null);

  // clientlog predicates read by the clientlog wrapper in main.ts.
  // Defaults: telemetry enabled, no live-tail. Updated after a successful
  // session probe or login when the server returns the clientlog block.
  clientlogTelemetryEnabled = $state(true);
  clientlogLivetailUntil = $state<number | null>(null);

  /** Parse an RFC 3339 elevation_expires_at into a Date or null. */
  #parseElevation(raw: string | null | undefined): Date | null {
    if (!raw) return null;
    const d = new Date(raw);
    return Number.isNaN(d.getTime()) ? null : d;
  }

  /** True when elevationExpiresAt is set and is in the future. */
  #hasActiveElevation(): boolean {
    const exp = this.elevationExpiresAt;
    return exp !== null && exp.getTime() > Date.now();
  }

  /**
   * Bootstrap the admin SPA session.
   *
   * Calls GET /api/v1/auth/me (no elevation required) to verify session
   * and read roles + elevation_expires_at. Based on those values:
   *   - No session (401) -> unauthenticated
   *   - Non-admin role   -> unauthenticated
   *   - Admin role, active elevation -> fetch server/status, then ready
   *   - Admin role, no elevation     -> step_up_pending (REQ-AS-21)
   *
   * Idempotent: subsequent calls while bootstrapping or ready are no-ops.
   */
  async bootstrap(): Promise<void> {
    if (this.status === 'bootstrapping' || this.status === 'ready') return;
    this.status = 'bootstrapping';
    this.errorMessage = null;
    try {
      const response = await fetch('/api/v1/auth/me', {
        method: 'GET',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });

      if (response.status === 401) {
        this.status = 'unauthenticated';
        return;
      }

      if (response.status !== 200) {
        this.status = 'unauthenticated';
        this.errorMessage = `Unexpected response: HTTP ${response.status}`;
        return;
      }

      const me = (await response.json()) as AuthMeResponse;
      const roles = me.roles ?? [];
      const isAdmin = roles.includes('admin') || roles.includes('superadmin');

      if (!isAdmin) {
        // Authenticated but not an admin principal — the admin SPA is not
        // accessible to end-user principals.
        this.status = 'unauthenticated';
        return;
      }

      this.roles = roles;
      this.elevationExpiresAt = this.#parseElevation(me.elevation_expires_at);
      this.principal = {
        id: me.principal_id,
        email: me.email,
        scopes: me.scopes,
      };

      if (this.#hasActiveElevation()) {
        // Elevation is live; fetch server/status for clientlog block.
        await this.#fetchServerStatus();
      } else {
        // Need to elevate before showing admin content (REQ-AS-21, re #79).
        this.status = 'step_up_pending';
      }
    } catch (err) {
      // Network error: treat as unauthenticated rather than crashing; the
      // login page will surface the connectivity problem on the next attempt.
      this.status = 'unauthenticated';
      this.errorMessage = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Fetch GET /api/v1/server/status (requires authAdmin) and apply
   * clientlog config. Transitions to 'ready' on success.
   * Called after bootstrap confirms active elevation, and after step-up.
   */
  async #fetchServerStatus(): Promise<void> {
    try {
      const response = await fetch('/api/v1/server/status', {
        method: 'GET',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
      if (response.status === 200) {
        const body = (await response.json()) as ServerStatusResponse;
        // Update principal from status response (may have fresher scopes).
        this.principal = {
          id: body.principal_id,
          email: body.email,
          scopes: body.scopes,
        };
        this._applyClientlogBlock(body.clientlog);
        this.status = 'ready';
        return;
      }
      // If server/status returns 403 after we thought elevation was active,
      // the elevation has just expired — fall back to step_up_pending.
      if (response.status === 403) {
        this.elevationExpiresAt = null;
        this.status = 'step_up_pending';
        return;
      }
      this.status = 'unauthenticated';
    } catch {
      this.status = 'unauthenticated';
    }
  }

  /**
   * Called by AdminStepUpModal after a successful POST /api/v1/auth/step-up.
   * Fetches server/status to confirm elevation and transitions to 'ready'.
   * REQ-AS-21, re #79.
   */
  async afterStepUp(): Promise<void> {
    await this.#fetchServerStatus();
    // If the status is now 'ready', navigate to dashboard if at root.
    if (this.status === 'ready') {
      const cur = router.current;
      if (!cur || cur === '/' || cur === '/login') {
        router.replace('/dashboard');
      }
    }
  }

  /**
   * Update elevationExpiresAt after a successful step-up response.
   * Called by the step-up store with the elevation_expires_at value from
   * the POST /api/v1/auth/step-up response (REQ-AS-23, re #79).
   */
  updateElevation(raw: string | null): void {
    this.elevationExpiresAt = this.#parseElevation(raw);
  }

  /**
   * Submit credentials to POST /api/v1/auth/login.
   * On success the server sets the session + CSRF cookies; we read the
   * principal from the response body and transition via bootstrap.
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
        this.roles = body.roles ?? [];
        this.elevationExpiresAt = this.#parseElevation(body.elevation_expires_at);
        this.principal = {
          id: body.principal_id,
          email: req.email,
          scopes: body.scopes,
        };
        // Re-bootstrap from idle to fetch server/status + clientlog.
        this.status = 'idle';
        await this.bootstrap();
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
    this.roles = [];
    this.elevationExpiresAt = null;
    this.status = 'unauthenticated';
    router.replace('/login');
  }

  /**
   * Called by client.ts when any /api/v1/ call returns 401.
   * Transitions to unauthenticated and routes to /login.
   */
  handleUnauthorized(): void {
    if (this.status === 'ready' || this.status === 'step_up_pending') {
      this.principal = null;
      this.roles = [];
      this.elevationExpiresAt = null;
      this.status = 'unauthenticated';
      router.replace('/login');
    }
  }

  /**
   * Apply the optional clientlog block from the session response.
   * Updates the predicates read by the clientlog wrapper (REQ-OPS-208/211).
   */
  _applyClientlogBlock(block: ClientlogBlock | undefined): void {
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
