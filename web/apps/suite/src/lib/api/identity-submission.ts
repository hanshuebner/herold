/**
 * Typed wrappers over the five external-submission REST endpoints
 * (REQ-MAIL-SUBMIT-01..08, REQ-AUTH-EXT-SUBMIT-04).
 *
 * Endpoints:
 *   GET    /api/v1/identities/{id}/submission
 *   PUT    /api/v1/identities/{id}/submission
 *   DELETE /api/v1/identities/{id}/submission
 *   POST   /api/v1/identities/{id}/submission/oauth/start
 *
 * Credentials never appear in any response shape (REQ-MAIL-SUBMIT-08).
 * The OAuth start helper redirects the browser via window.location.href
 * so the server's 302 carries the user to the provider's auth page.
 */

import { get, put, del, ApiError } from './client';

/** Security mode for the external SMTP connection. */
export type SubmitSecurity = 'implicit_tls' | 'starttls' | 'none';

/** Auth method for the external SMTP connection. */
export type SubmitAuthMethod = 'password' | 'oauth2';

/** Per-identity submission state as returned by the server. */
export type SubmissionState = 'ok' | 'auth-failed' | 'unreachable';

/**
 * GET /api/v1/identities/{id}/submission response body.
 * No credential material is ever returned here (REQ-MAIL-SUBMIT-08).
 */
export interface SubmissionStatus {
  configured: boolean;
  submit_host?: string;
  submit_port?: number;
  submit_security?: SubmitSecurity;
  submit_auth_method?: SubmitAuthMethod;
  state?: SubmissionState;
}

/**
 * Body for PUT /api/v1/identities/{id}/submission.
 *
 * For `password` mode, supply `password`.
 * For `oauth2` mode, supply the `oauth` object.
 * The server runs `extsubmit.Submitter.Probe` before persisting;
 * a 422 is returned on probe failure with a ProblemDetail body
 * carrying `type: "external_submission_probe_failed"`,
 * `category: "auth-failed" | "unreachable" | "permanent" | "transient"`,
 * and `diagnostic: <text>`.
 */
export interface SubmissionPutBody {
  auth_method: SubmitAuthMethod;
  host: string;
  port: number;
  security: SubmitSecurity;
  password?: string;
  oauth?: {
    access_token: string;
    refresh_token: string;
    expires_at: string;
    token_endpoint: string;
  };
}

/**
 * Problem detail body for a 422 probe failure.
 * The type field is `external_submission_probe_failed`.
 */
export interface ProbeProblemDetail {
  type: string;
  category: 'auth-failed' | 'unreachable' | 'permanent' | 'transient';
  diagnostic: string;
}

/** Known OAuth providers accepted by the server. */
export type OAuthProvider = 'gmail' | 'm365';

/**
 * GET /api/v1/identities/{id}/submission
 * Returns the submission status for the given identity.
 */
export function getSubmission(identityId: string): Promise<SubmissionStatus> {
  return get<SubmissionStatus>(`/api/v1/identities/${identityId}/submission`);
}

/**
 * PUT /api/v1/identities/{id}/submission
 * Set or replace the external submission config.
 * Throws ApiError with status 422 and a ProbeProblemDetail body on probe failure.
 */
export function putSubmission(
  identityId: string,
  body: SubmissionPutBody,
): Promise<void> {
  return put<void>(`/api/v1/identities/${identityId}/submission`, body);
}

/**
 * DELETE /api/v1/identities/{id}/submission
 * Remove the submission config. Subsequent submissions for this
 * identity revert to herold's outbound queue.
 */
export function deleteSubmission(identityId: string): Promise<void> {
  return del<void>(`/api/v1/identities/${identityId}/submission`);
}

/**
 * Start an OAuth flow for the given provider by redirecting the browser to the
 * server's start endpoint. The server returns 302 to the provider's auth URL;
 * the browser follows it; the provider redirects back to the server's callback
 * which persists the tokens and redirects to the suite settings page.
 *
 * POST /api/v1/identities/{id}/submission/oauth/start?provider=<provider>
 *
 * The suite never holds OAuth client credentials (REQ-MAIL-SUBMIT-02);
 * the redirect is browser-level so the auth URL is never visible to JS.
 *
 * If the server returns 503 (provider not configured by the operator), the
 * Promise rejects with an ApiError (status 503); the caller surfaces an
 * inline error.
 */
export async function startOAuth(
  identityId: string,
  provider: OAuthProvider,
): Promise<void> {
  // Use a form POST so the browser follows the server's 302 natively,
  // carrying the session cookie with it. A fetch() call would intercept
  // the redirect and lose the cookie attachment semantics for the OAuth
  // provider leg.
  //
  // We submit a hidden form to POST the endpoint, which lets the server
  // issue a 302 that the browser follows. The session cookie attaches
  // automatically (same-origin, credentials: include).
  const url = `/api/v1/identities/${identityId}/submission/oauth/start?provider=${encodeURIComponent(provider)}`;

  // Read the CSRF token from the cookie (same logic as client.ts).
  const csrfToken = readCsrfToken();

  const form = document.createElement('form');
  form.method = 'POST';
  form.action = url;

  if (csrfToken) {
    const csrfInput = document.createElement('input');
    csrfInput.type = 'hidden';
    csrfInput.name = 'x-csrf-token';
    csrfInput.value = csrfToken;
    form.appendChild(csrfInput);
  }

  // Before submitting the form, do a preflight fetch to detect 503
  // (provider not configured) without leaving the page.
  // If the server returns 503, surface the error without a page navigation.
  const preflight = await fetch(url, {
    method: 'POST',
    credentials: 'include',
    headers: {
      Accept: 'application/json',
      'X-CSRF-Token': csrfToken,
      // Signal to the server that we want a JSON error instead of a
      // redirect on failure, so we can stay on the page.
      'X-Preflight': '1',
    },
    redirect: 'manual',
  });

  // A `redirect: 'manual'` fetch returns type 'opaqueredirect' on 302.
  // That means the server redirected — the OAuth flow is starting.
  if (preflight.type === 'opaqueredirect' || preflight.redirected || preflight.status === 0) {
    // Server is redirecting us; follow with a real browser navigation.
    document.body.appendChild(form);
    form.submit();
    return;
  }

  if (!preflight.ok) {
    let msg = `HTTP ${preflight.status}`;
    let detail: { type?: string; message?: string; error?: string } | null = null;
    try {
      detail = (await preflight.json()) as { type?: string; message?: string; error?: string };
      msg = detail?.message ?? detail?.error ?? msg;
    } catch {
      // ignore JSON parse error
    }
    throw new ApiError(preflight.status, msg, detail);
  }

  // 200 OK from preflight means the server accepted it without redirecting.
  // Fall through to a real form submit which will follow the redirect.
  document.body.appendChild(form);
  form.submit();
}

function readCsrfToken(): string {
  const pairs = document.cookie.split(';');
  for (const pair of pairs) {
    const [name, value] = pair.trim().split('=');
    if (name === 'herold_public_csrf' && value !== undefined) {
      return decodeURIComponent(value);
    }
  }
  return '';
}
