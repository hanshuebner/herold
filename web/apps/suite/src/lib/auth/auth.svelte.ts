/**
 * Auth state machine for suite-shell bootstrap.
 *
 * States flow:
 *   idle -> bootstrapping -> ready          (session cookie already valid)
 *                         -> unauthenticated (401 from herold; renders LoginView inline)
 *                         -> error          (any other failure)
 *
 * Per the phase-3c-ii migration: authentication is via the JSON endpoint
 * POST /api/v1/auth/login on the public listener. On bootstrap-detected 401
 * the state transitions to 'unauthenticated' and the Suite renders its own
 * LoginView; no redirect to /login occurs.
 *
 * The session cookie (herold_session) is set by the server and is HttpOnly;
 * the Suite never reads or stores any auth token.
 *
 * Phase 4 adds principal_id to the auth state. The value is a string because
 * the wire type is uint64 and JS Number loses precision past 2^53. It is only
 * used for URL construction (e.g. /api/v1/principals/{pid}), never for
 * arithmetic.
 *
 * Phase 5 (re #79) adds roles and elevationExpiresAt for TOTP step-up gating.
 * The admin entry point is gated on roles (not scopes); elevation expiry drives
 * the countdown chip (REQ-AS-24) and the step-up modal pre-check (REQ-AS-21).
 */

import { jmap, setJmapOnUnauthenticated } from '../jmap/client';
import { UnauthenticatedError } from '../jmap/errors';
import { get, setOnUnauthenticated } from '../api/client';
import type { SessionResource } from '../jmap/types';

export type AuthStatus =
  | 'idle'
  | 'bootstrapping'
  | 'ready'
  | 'unauthenticated'
  | 'error';

interface LoginRequest {
  email: string;
  password: string;
  totp_code?: string;
}

interface LoginErrorResponse {
  step_up_required?: boolean;
  message?: string;
}

/** Shape of POST /api/v1/auth/login and GET /api/v1/auth/me response bodies. */
interface AuthMeResponse {
  /** uint64 as a JSON number. Parse to string immediately to avoid precision loss. */
  principal_id: number;
  email: string;
  scopes: string[];
  /**
   * Role memberships for the principal. Used to gate admin-visible UI
   * (REQ-AS-26, re #79): the admin entry requires 'admin' or 'superadmin'
   * in roles, not the 'admin' scope (which is no longer carried in the
   * session cookie per REQ-AUTH-SCOPE-01).
   */
  roles?: string[];
  /**
   * RFC 3339 UTC timestamp at which the current TOTP elevation expires, or
   * null when no elevation is active. Drives the countdown chip (REQ-AS-24)
   * and the pre-navigation check for admin routes (REQ-AS-21).
   */
  elevation_expires_at?: string | null;
  /**
   * Absolute session deadline as an RFC 3339 UTC timestamp. Kept for
   * backward-compat with old server builds; the SPA now prefers
   * session_idle_deadline (REQ-AUTH-75, re #77).
   */
  session_expires_at?: string;
  /**
   * Rolling idle deadline as an RFC 3339 UTC timestamp. The SPA arms a
   * setTimeout against this so the LoginView appears the moment the idle
   * window closes, instead of only on the user's next interaction. Replaces
   * the prior use of session_expires_at (absolute) which was useless in the
   * idle-only session model (REQ-AUTH-75, REQ-AS-11, re #77). Optional
   * because older server builds omit it; when absent the SPA falls back to
   * session_expires_at, then to the reactive 401 handler.
   */
  session_idle_deadline?: string;
}

/** Problem type URI suffix for a session_expired 401 (REQ-AUTH-76, re #77). */
const SESSION_EXPIRED_TYPE = 'https://netzhansa.com/problems/session_expired';
/** Problem type URI suffix for a session_revoked 401 (REQ-AUTH-76, re #80). */
const SESSION_REVOKED_TYPE = 'https://netzhansa.com/problems/session_revoked';

class Auth {
  status = $state<AuthStatus>('idle');
  errorMessage = $state<string | null>(null);
  session = $state<SessionResource | null>(null);
  /**
   * The current principal's ID as a decimal string. Populated by login()
   * (from the response body) and by loadMe() (from GET /api/v1/auth/me).
   * Null until bootstrap completes successfully.
   */
  principalId = $state<string | null>(null);
  /**
   * Scopes granted to the current principal. Populated by loadMe() and
   * by login(). Empty array until bootstrap completes or when unauthenticated.
   */
  scopes = $state<string[]>([]);
  /**
   * Role memberships for the current principal. Used by AppSwitcherMenu to
   * gate the admin entry point on 'admin' or 'superadmin' role membership
   * rather than on scopes (REQ-AS-26, re #79).
   * Empty array until bootstrap completes or when unauthenticated.
   */
  roles = $state<string[]>([]);
  /**
   * When the current TOTP elevation expires, or null when no elevation is
   * active. Populated from login and loadMe responses. Drives the countdown
   * chip (REQ-AS-24) and the pre-navigation check for admin routes (REQ-AS-21).
   */
  elevationExpiresAt = $state<Date | null>(null);
  /** True after a /api/v1/auth/login 401 with step_up_required; the LoginView
   *  uses this to reveal the TOTP-code field. */
  needsStepUp = $state(false);

  /**
   * Rolling idle deadline reported by the server (from the login response
   * or from GET /auth/me). The SPA schedules a setTimeout at this deadline
   * to proactively transition to forced-login state before the next request
   * fails. Null when the server build omits session_idle_deadline — in that
   * case the SPA falls back to session_expires_at, then to the reactive 401
   * handler (REQ-AUTH-75, REQ-AS-11, re #77).
   */
  sessionIdleDeadline = $state<Date | null>(null);

  /**
   * Reason for the most recent forced-login transition, or null when no
   * forced-login has occurred this session or after successful re-login.
   * 'expired' — idle window closed (timer or first 401 with session_expired).
   * 'revoked' — session was tombstoned remotely (session_revoked, re #80).
   * null — initial load / explicit logout / no forced-login this session.
   * Used by LoginView to render the appropriate context message (REQ-AS-13).
   */
  forcedLoginReason = $state<'expired' | 'revoked' | null>(null);

  /**
   * Handle for the pending expiry timer. setTimeout returns `number` in the
   * browser and `Timeout` in node; both clearTimeout overloads accept either,
   * so we widen to unknown rather than picking one.
   */
  #expiryTimer: ReturnType<typeof setTimeout> | null = null;

  /**
   * Arm a setTimeout that calls signalUnauthenticated('expired') exactly
   * when the idle deadline passes. Idempotent: calling twice cancels the
   * previous timer. If the deadline is already in the past the transition
   * fires immediately. A null deadline just clears any pending timer (used
   * on logout and when no deadline is available from the server).
   *
   * The timer fires proactively so the forced-login overlay appears without
   * waiting for the user's next network interaction (REQ-AS-11, re #77).
   *
   * setTimeout's effective maximum is 2^31-1 ms (~24.8 days); one week is
   * comfortably below that.
   */
  #scheduleIdleExpiry(deadline: Date | null): void {
    if (this.#expiryTimer !== null) {
      clearTimeout(this.#expiryTimer);
      this.#expiryTimer = null;
    }
    if (deadline === null) {
      return;
    }
    // Always go through setTimeout so the signal is deferred to the next
    // event-loop tick. A synchronous call here would fire during bootstrap()
    // or login() and be overridden by the subsequent `status = 'ready'`
    // assignment. Past deadlines use delay 0 (fires immediately after the
    // current call stack unwinds) and are also caught by the visibilitychange
    // listener for tabs returning from background (REQ-AS-11, re #77).
    const delayMs = Math.max(0, deadline.getTime() - Date.now());
    this.#expiryTimer = setTimeout(() => {
      this.#expiryTimer = null;
      this.signalUnauthenticated('expired');
    }, delayMs);
  }

  /** Parse an RFC 3339 elevation_expires_at into a Date or null. */
  #parseElevation(raw: string | null | undefined): Date | null {
    if (!raw) return null;
    const d = new Date(raw);
    return Number.isNaN(d.getTime()) ? null : d;
  }

  /**
   * Resolve the current principal's ID from GET /api/v1/auth/me.
   *
   * This is a non-throwing helper: on failure (e.g. 401 race between
   * bootstrap and this call) the error is silently swallowed and
   * principalId remains null. The caller is responsible for checking.
   */
  async loadMe(): Promise<void> {
    try {
      const me = await get<AuthMeResponse>('/api/v1/auth/me');
      // Stringify immediately to avoid JS Number precision loss for large
      // uint64 values (> 2^53 rounds to the wrong integer).
      this.principalId = String(me.principal_id);
      this.scopes = me.scopes ?? [];
      this.roles = me.roles ?? [];
      this.elevationExpiresAt = this.#parseElevation(me.elevation_expires_at);
      // Prefer the rolling idle deadline; fall back to the absolute one
      // for backward-compat with old server builds (re #77).
      this.#applyIdleDeadline(me.session_idle_deadline ?? me.session_expires_at);
    } catch {
      // Non-fatal: security forms degrade gracefully when principalId is null.
    }
  }

  /**
   * Parse an optional RFC 3339 timestamp from the server, arm (or clear)
   * the idle-expiry timer, and re-evaluate immediately if the deadline is
   * already past (handles returning from a long background tab).
   * Invalid or missing timestamps clear any pending timer and leave
   * sessionIdleDeadline at null so the reactive 401 path takes over.
   */
  #applyIdleDeadline(raw: string | undefined): void {
    if (!raw) {
      this.sessionIdleDeadline = null;
      this.#scheduleIdleExpiry(null);
      return;
    }
    const parsed = new Date(raw);
    if (Number.isNaN(parsed.getTime())) {
      this.sessionIdleDeadline = null;
      this.#scheduleIdleExpiry(null);
      return;
    }
    this.sessionIdleDeadline = parsed;
    this.#scheduleIdleExpiry(parsed);
  }

  /**
   * Run the bootstrap once. Subsequent calls are idempotent unless
   * the previous attempt errored, in which case retry is allowed.
   *
   * The JMAP session fetch and the /auth/me fetch run in parallel;
   * neither blocks the other and both are awaited before transitioning
   * to 'ready'.
   */
  async bootstrap(): Promise<void> {
    if (this.status === 'bootstrapping' || this.status === 'ready') return;
    this.status = 'bootstrapping';
    this.errorMessage = null;
    try {
      // Fire both requests in parallel. jmap.bootstrap() throws
      // UnauthenticatedError on 401 and controls the state machine.
      // loadMe() is non-throwing and populates principalId as a side
      // effect.
      const [session] = await Promise.all([jmap.bootstrap(), this.loadMe()]);
      this.session = session;
      this.status = 'ready';
    } catch (err) {
      if (err instanceof UnauthenticatedError) {
        this.status = 'unauthenticated';
      } else {
        this.status = 'error';
        this.errorMessage = err instanceof Error ? err.message : String(err);
      }
    }
  }

  /**
   * Submit credentials to POST /api/v1/auth/login.
   *
   * On 200: re-runs bootstrap() so the JMAP session descriptor is fetched
   * and the status transitions to 'ready'.
   *
   * On 401 with step_up_required: sets needsStepUp and throws so the
   * LoginView can reveal the TOTP-code field.
   *
   * On any other error: sets errorMessage and throws.
   */
  async login(args: {
    email: string;
    password: string;
    totpCode?: string;
  }): Promise<void> {
    this.errorMessage = null;

    const req: LoginRequest = {
      email: args.email,
      password: args.password,
      totp_code: args.totpCode || undefined,
    };

    let response: Response;
    try {
      response = await fetch('/api/v1/auth/login', {
        method: 'POST',
        credentials: 'include',
        headers: {
          'Content-Type': 'application/json',
          Accept: 'application/json',
        },
        body: JSON.stringify(req),
      });
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Network error';
      this.errorMessage = msg;
      throw new Error(msg);
    }

    if (response.status === 200) {
      this.needsStepUp = false;
      // Capture principal_id, scopes, roles, elevation, and session idle
      // deadline from the login response body. The idle-expiry timer arms
      // immediately so the forced-login overlay appears without waiting for
      // a 401 (REQ-AS-11). Roles are needed immediately for the admin link
      // (REQ-AS-26, re #79).
      try {
        const body = (await response.json()) as AuthMeResponse;
        this.principalId = String(body.principal_id);
        this.scopes = body.scopes ?? [];
        this.roles = body.roles ?? [];
        this.elevationExpiresAt = this.#parseElevation(body.elevation_expires_at);
        // Prefer rolling idle deadline; fall back to absolute for old servers.
        this.#applyIdleDeadline(body.session_idle_deadline ?? body.session_expires_at);
      } catch {
        // Ignore parse errors; loadMe() in bootstrap() will populate it.
      }
      // Clear any prior forced-login reason on successful re-login.
      this.forcedLoginReason = null;
      // Reset per-account store state before bootstrapping under the new
      // account. The session may still refer to the previous account at
      // this point, which is intentional: account-scoped localStorage
      // helpers use it to clear the departing account's keys.
      callAccountResets();
      // Re-bootstrap so the JMAP session descriptor reflects the new auth state.
      // Reset status to allow bootstrap() to re-run.
      this.status = 'idle';
      await this.bootstrap();
      return;
    }

    if (response.status === 401) {
      let stepUpRequired = false;
      let errorMessage = 'Invalid email or password.';
      try {
        const body = (await response.json()) as LoginErrorResponse;
        if (body.step_up_required) {
          stepUpRequired = true;
          errorMessage = 'Enter your two-factor authentication code to continue.';
        } else if (body.message) {
          errorMessage = body.message;
        }
      } catch {
        // ignore JSON parse error; use defaults above
      }
      if (stepUpRequired) {
        this.needsStepUp = true;
      }
      this.errorMessage = errorMessage;
      throw new Error(errorMessage);
    }

    let msg: string;
    if (response.status === 400) {
      try {
        const body = (await response.json()) as { message?: string };
        msg = body.message ?? 'Invalid request. Please check your e-mail address and password.';
      } catch {
        msg = 'Invalid request. Please check your e-mail address and password.';
      }
    } else {
      msg = `Unexpected response: HTTP ${response.status}`;
    }
    this.errorMessage = msg;
    throw new Error(msg);
  }

  /**
   * POST /api/v1/auth/logout to clear the server-side session, then
   * transition to 'unauthenticated' regardless of server response.
   */
  async logout(): Promise<void> {
    try {
      await fetch('/api/v1/auth/logout', {
        method: 'POST',
        credentials: 'include',
        headers: { Accept: 'application/json' },
      });
    } catch {
      // Swallow network errors: we log out locally regardless.
    }
    // Reset per-account store state while the session is still set so
    // account-scoped localStorage keys resolve to the departing account.
    callAccountResets();
    this.session = null;
    this.principalId = null;
    this.scopes = [];
    this.roles = [];
    this.elevationExpiresAt = null;
    this.sessionIdleDeadline = null;
    this.forcedLoginReason = null;
    this.#scheduleIdleExpiry(null);
    this.status = 'unauthenticated';
  }

  /**
   * Tell the auth state machine that the session was rejected by the
   * server, or that the local idle timer fired. Sets `forcedLoginReason`
   * so the LoginView can present the correct context message (REQ-AS-13).
   *
   * reason:
   *   'expired' — idle timer fired proactively, or typed 401 session_expired.
   *   'revoked' — typed 401 session_revoked (remote revocation, re #80).
   *   null      — initial bootstrap 401 or untyped error; no context message.
   *
   * Idempotent: repeated calls while already unauthenticated are no-ops.
   */
  signalUnauthenticated(reason: 'expired' | 'revoked' | null = null): void {
    if (this.status === 'unauthenticated') return;
    // Reset per-account store state while the session is still set so
    // account-scoped localStorage keys resolve to the departing account.
    callAccountResets();
    this.session = null;
    this.principalId = null;
    this.scopes = [];
    this.roles = [];
    this.elevationExpiresAt = null;
    this.sessionIdleDeadline = null;
    this.forcedLoginReason = reason;
    this.#scheduleIdleExpiry(null);
    this.status = 'unauthenticated';
  }

  /**
   * Update elevation_expires_at after a successful TOTP step-up
   * (POST /api/v1/auth/step-up response). Called by the step-up store
   * (step-up.svelte.ts) after receiving a 200 from the step-up endpoint.
   * Triggers UI updates: countdown chip (REQ-AS-24) and admin-link
   * visibility (REQ-AS-26, re #79).
   */
  updateElevation(raw: string | null): void {
    this.elevationExpiresAt = this.#parseElevation(raw);
  }

  /**
   * Re-fetch the JMAP session descriptor without disturbing the auth
   * state machine. Used by stores that want to refresh derived
   * capability values (e.g. the per-principal internalize-pending count
   * in REQ-EXTIMG-BG-32) when a server-side push event implies the
   * descriptor's contents may have changed.
   *
   * Best-effort: 401 transitions to unauthenticated via the existing
   * unauth signal; any other failure is silently swallowed because the
   * existing session descriptor remains a valid fallback.
   */
  async refreshSession(): Promise<void> {
    if (this.status !== 'ready') return;
    try {
      const session = await jmap.bootstrap();
      this.session = session;
    } catch (err) {
      if (err instanceof UnauthenticatedError) {
        this.signalUnauthenticated(null);
        return;
      }
      // Non-fatal: keep using the existing session descriptor.
    }
  }
}

export const auth = new Auth();

// Wire both HTTP clients so that any 401 received after bootstrap
// automatically transitions the auth state machine to 'unauthenticated',
// causing AuthGate to replace the application shell with LoginView.
// Registered here once at module init to avoid circular imports (the clients
// cannot import auth; auth imports the clients).
setOnUnauthenticated((problemType) => {
  // Map the RFC 7807 type URI to the forced-login reason (REQ-AS-13, re #77).
  let reason: 'expired' | 'revoked' | null = null;
  if (problemType === SESSION_EXPIRED_TYPE) reason = 'expired';
  else if (problemType === SESSION_REVOKED_TYPE) reason = 'revoked';
  auth.signalUnauthenticated(reason);
});
setJmapOnUnauthenticated(() => auth.signalUnauthenticated(null));

// Proactive idle-expiry check on tab visibility change (REQ-AS-11, re #77).
// When the tab returns from background after the idle deadline has passed,
// transition to forced-login state immediately rather than waiting for the
// next network request to fail with a 401.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && auth.status === 'ready') {
      const deadline = auth.sessionIdleDeadline;
      if (deadline !== null && Date.now() >= deadline.getTime()) {
        auth.signalUnauthenticated('expired');
      }
    }
  });
}

// ── Account-change reset callbacks ────────────────────────────────────────
//
// Stores that hold per-account in-memory state (mail, chat, contacts,
// avatar cache) register a reset function here. Auth calls every
// registered callback when the active account changes — on explicit
// logout, on session expiry (signalUnauthenticated), and at the start of
// a new login — so that a freshly-signed-in account always sees a clean
// cache rather than the previous account's data.
//
// Callbacks are registered by the store modules at their own module-init
// boundary (bottom of each *.svelte.ts file), which avoids circular imports
// (auth cannot import the stores; the stores import auth).
const accountResetCallbacks: Array<() => void> = [];

/**
 * Register a function to be called whenever the active account changes.
 * Call from store module init (outside any class) to avoid circular imports.
 */
export function registerAccountResetCallback(cb: () => void): void {
  accountResetCallbacks.push(cb);
}

function callAccountResets(): void {
  for (const cb of accountResetCallbacks) cb();
}
