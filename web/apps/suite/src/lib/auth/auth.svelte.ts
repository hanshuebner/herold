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
 */

import { jmap } from '../jmap/client';
import { UnauthenticatedError } from '../jmap/errors';
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

class Auth {
  status = $state<AuthStatus>('idle');
  errorMessage = $state<string | null>(null);
  session = $state<SessionResource | null>(null);
  /** True after a /api/v1/auth/login 401 with step_up_required; the LoginView
   *  uses this to reveal the TOTP-code field. */
  needsStepUp = $state(false);

  /**
   * Run the bootstrap once. Subsequent calls are idempotent unless
   * the previous attempt errored, in which case retry is allowed.
   */
  async bootstrap(): Promise<void> {
    if (this.status === 'bootstrapping' || this.status === 'ready') return;
    this.status = 'bootstrapping';
    this.errorMessage = null;
    try {
      const session = await jmap.bootstrap();
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

    const msg = `Unexpected response: HTTP ${response.status}`;
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
    this.session = null;
    this.status = 'unauthenticated';
  }
}

export const auth = new Auth();
