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
 * The OAuth start helper POSTs with X-CSRF-Token (via client.post()), reads
 * the provider authorization URL from the JSON response body, and navigates
 * window.location.href to it so the browser completes the OAuth redirect
 * chain.
 */

import { get, post, put, del } from './client';

/** Security mode for the external SMTP connection. */
export type SubmitSecurity = 'implicit_tls' | 'starttls' | 'none';

/** Auth method for the external SMTP connection. */
export type SubmitAuthMethod = 'password' | 'oauth2';

/** Per-identity submission state as returned by the server. */
export type SubmissionState = 'ok' | 'auth-failed' | 'unreachable';

/**
 * GET /api/v1/identities/{id}/submission response body.
 * No credential material is ever returned here (REQ-MAIL-SUBMIT-08).
 *
 * The server always includes `available_oauth_providers` and
 * `domain_authoritative` regardless of whether `configured` is true or
 * false (re #73, re #74).
 */
export interface SubmissionStatus {
  configured: boolean;
  submit_host?: string;
  submit_port?: number;
  submit_security?: SubmitSecurity;
  submit_auth_method?: SubmitAuthMethod;
  state?: SubmissionState;
  /**
   * Sorted list of OAuth provider ids that are fully configured on this
   * server (non-empty ClientSecret). The Suite renders one OAuth sign-in
   * button per entry. Empty when no OAuth providers are configured (re #73).
   */
  available_oauth_providers: string[];
  /**
   * True when the identity's email domain is authoritative on this server.
   * False means the domain is external: DKIM signing and DMARC alignment
   * are unavailable, so external SMTP submission is required (re #74).
   */
  domain_authoritative: boolean;
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
 * Start an OAuth flow for the given provider.
 *
 * POST /api/v1/identities/{id}/submission/oauth/start?provider=<provider>
 *
 * The server returns the provider's authorization URL as JSON
 * (`{"auth_url":"https://..."}`) when Accept: application/json is sent.
 * The client POSTs with the X-CSRF-Token header (via client.post(), the same
 * mechanism used by all other Suite mutations) then navigates the top-level
 * frame to the returned URL so the OAuth redirect chain continues in the
 * browser (REQ-MAIL-SUBMIT-02, REQ-AUTH-EXT-SUBMIT-04, REQ-AUTH-CSRF).
 *
 * A native form submit cannot carry custom headers, so the previous
 * form-submit approach could not satisfy the server's CSRF middleware.
 *
 * If the server returns 503 (provider not configured by the operator), the
 * Promise rejects with an ApiError (status 503); the caller surfaces an
 * inline error.
 */
export async function startOAuth(
  identityId: string,
  provider: OAuthProvider,
): Promise<void> {
  const url = `/api/v1/identities/${identityId}/submission/oauth/start?provider=${encodeURIComponent(provider)}`;
  // post() from client.ts sends Accept: application/json and the
  // X-CSRF-Token header from the herold_public_csrf cookie, satisfying
  // the server's CSRF middleware for cookie-authenticated callers.
  const { auth_url } = await post<{ auth_url: string }>(url);
  window.location.href = auth_url;
}
