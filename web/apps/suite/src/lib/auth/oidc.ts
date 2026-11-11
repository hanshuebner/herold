/**
 * External OpenID Connect sign-in and account linking (REQ-AUTH-50..58).
 *
 * Both flows leave the SPA: the server returns the provider's
 * authorization URL, the browser navigates there, and the provider
 * redirects back to GET /api/v1/oidc/callback, which completes the flow
 * server-side and redirects into the Suite:
 *
 *   sign-in success  ->  /                      (session cookie set)
 *   sign-in failure  ->  /?oidc_error=<reason>
 *   link outcome     ->  /#/settings/security?oidc_link=<outcome>
 *
 * Matching is by the provider's `sub`, never by email, so only a provider
 * identity linked to an account (or auto-provisioned by the operator's
 * choice) signs in.
 */

import { get, post } from '../api/client';

/** One provider offered on the sign-in page. */
export interface SignInProvider {
  id: string;
  name: string;
}

/** One link between the signed-in principal and a provider identity. */
export interface OidcLink {
  provider_id: string;
  subject: string;
  email_at_provider?: string;
  linked_at: string;
}

/** Providers the sign-in page offers. Never throws: on any failure the
 *  sign-in page simply shows no provider buttons. */
export async function listSignInProviders(): Promise<SignInProvider[]> {
  try {
    const res = await get<{ items?: SignInProvider[] }>('/api/v1/auth/oidc/providers');
    return res?.items ?? [];
  } catch {
    return [];
  }
}

/** Starts a sign-in and returns the provider URL to navigate to. */
export async function beginSignIn(providerId: string): Promise<string> {
  const res = await post<{ auth_url: string }>('/api/v1/auth/oidc/begin', {
    provider_id: providerId,
  });
  return res.auth_url;
}

/** Starts linking the signed-in principal and returns the provider URL. */
export async function beginLink(principalId: string, providerId: string): Promise<string> {
  const res = await post<{ auth_url: string }>(
    `/api/v1/principals/${principalId}/oidc-links/begin`,
    { provider_id: providerId },
  );
  return res.auth_url;
}

/** Sign-in failure reasons the callback reports via ?oidc_error=. */
const SIGN_IN_ERRORS = new Set(['not_linked', 'expired', 'browser_mismatch', 'cancelled', 'failed']);

/** i18n key for a sign-in failure reason; unknown reasons map to 'failed'. */
export function signInErrorKey(reason: string): string {
  return `login.oidc.error.${SIGN_IN_ERRORS.has(reason) ? reason : 'failed'}`;
}

/** Link outcomes the callback reports via the oidc_link hash parameter. */
const LINK_OUTCOMES = new Set([
  'linked',
  'already_linked',
  'expired',
  'browser_mismatch',
  'cancelled',
  'failed',
]);

/** i18n key for a link outcome; unknown outcomes map to 'failed'. */
export function linkOutcomeKey(outcome: string): string {
  return `settings.security.oidc.outcome.${LINK_OUTCOMES.has(outcome) ? outcome : 'failed'}`;
}

/**
 * Reads and removes ?oidc_error= from the page URL so a reload does not
 * show the message again. Returns the reason, or null when absent.
 */
export function takeSignInError(loc: Location = window.location, hist: History = window.history): string | null {
  const params = new URLSearchParams(loc.search);
  const reason = params.get('oidc_error');
  if (reason === null) return null;
  params.delete('oidc_error');
  const qs = params.toString();
  hist.replaceState(hist.state, '', loc.pathname + (qs ? `?${qs}` : '') + loc.hash);
  return reason;
}
