/**
 * Auth state machine for the suite-shell bootstrap.
 *
 * States flow:
 *   idle → bootstrapping → ready (success path)
 *                       → unauthenticated (401 from herold)
 *                       → error (any other failure)
 *
 * Per docs/architecture/01-system-overview.md § Bootstrap (resolved R1):
 * authentication is via an HTTP-only session cookie set by herold's login
 * surface. On 401 we redirect the browser to `/login?return=<current-url>`;
 * herold authenticates and redirects back.
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

class Auth {
  status = $state<AuthStatus>('idle');
  errorMessage = $state<string | null>(null);
  session = $state<SessionResource | null>(null);

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

  /** Redirect the browser to herold's login surface, preserving the return URL. */
  redirectToLogin(): void {
    const returnUrl = encodeURIComponent(
      window.location.pathname + window.location.search + window.location.hash,
    );
    window.location.assign(`/login?return=${returnUrl}`);
  }
}

export const auth = new Auth();
