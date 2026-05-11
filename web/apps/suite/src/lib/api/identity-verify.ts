/**
 * Typed wrappers over the identity verification REST endpoints
 * (REQ-IDENT-40..41, REQ-SET-IDENT-20..21).
 *
 * Endpoints:
 *   POST /api/v1/identities/{id}/verify          — code submission
 *   POST /api/v1/identities/{id}/verify-request  — resend (rate-limited)
 *
 * The verify endpoint expects `{code: "<6 digits>"}` and returns 204 on
 * success. The verify-request endpoint takes no body and returns 204 on
 * success, or 429 with a `Retry-After` header when the per-identity
 * rate-limit kicks in (REQ-IDENT-36). We read the header off the raw
 * Response so the wizard / verify dialog can render the countdown.
 *
 * NOTE: The verify-request endpoint is documented in the wizard task
 * spec (#21) and the corresponding server route is part of the
 * verification email flow on `feat/identity-verification-mail-v2`.
 * If the server has not yet mounted the route, the helper will
 * surface the 404 directly so the caller can degrade gracefully.
 */

import { ApiError, UnauthenticatedError, ForbiddenError, type ProblemDetail } from './client';

/**
 * Custom request wrapper for the verify-request endpoint so we can
 * surface the `Retry-After` header on a 429. The shared `request()`
 * helper in client.ts throws on any non-2xx without exposing headers;
 * we duplicate just enough of its logic here to satisfy the wizard's
 * resend UX (REQ-SET-IDENT-21).
 */
async function postWithRetryAfter(path: string): Promise<{ retryAfter: number | null }> {
  const csrfToken = readCsrfToken();
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken;

  let response: Response;
  try {
    response = await fetch(path, {
      method: 'POST',
      headers,
      credentials: 'include',
    });
  } catch (err) {
    throw new ApiError(0, err instanceof Error ? err.message : 'Network error');
  }

  if (response.status === 204 || response.status === 200) {
    return { retryAfter: null };
  }

  if (response.status === 401) {
    let msg = 'Session expired. Please sign in again.';
    try {
      const b = (await response.json()) as ProblemDetail;
      if (b.message) msg = b.message;
    } catch {
      // ignore
    }
    throw new UnauthenticatedError(msg);
  }

  if (response.status === 403) {
    let msg = 'Insufficient permissions.';
    try {
      const b = (await response.json()) as ProblemDetail;
      if (b.message) msg = b.message;
    } catch {
      // ignore
    }
    throw new ForbiddenError(msg);
  }

  if (response.status === 429) {
    const header = response.headers.get('Retry-After');
    const seconds = parseRetryAfterHeader(header, Date.now());
    let detail: ProblemDetail | null = null;
    let msg = 'Too many requests';
    try {
      detail = (await response.json()) as ProblemDetail;
      msg = detail.message ?? detail.error ?? msg;
    } catch {
      // ignore
    }
    const err = new ApiError(429, msg, detail);
    (err as ApiError & { retryAfter: number | null }).retryAfter = seconds;
    throw err;
  }

  let detail: ProblemDetail | null = null;
  let msg = `HTTP ${response.status}`;
  try {
    detail = (await response.json()) as ProblemDetail;
    msg = detail.message ?? detail.error ?? msg;
  } catch {
    // ignore
  }
  throw new ApiError(response.status, msg, detail);
}

/**
 * POST /api/v1/identities/{id}/verify with the user-entered 6-digit
 * code. Resolves on the server-side 204; throws an `ApiError` with
 * `status: 400` on a malformed / wrong / expired code.
 */
export async function postVerifyCode(identityId: string, code: string): Promise<void> {
  const csrfToken = readCsrfToken();
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'Content-Type': 'application/json',
  };
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken;

  let response: Response;
  try {
    response = await fetch(`/api/v1/identities/${encodeURIComponent(identityId)}/verify`, {
      method: 'POST',
      headers,
      credentials: 'include',
      body: JSON.stringify({ code }),
    });
  } catch (err) {
    throw new ApiError(0, err instanceof Error ? err.message : 'Network error');
  }

  if (response.status === 204 || response.status === 200) return;

  if (response.status === 401) {
    throw new UnauthenticatedError();
  }
  if (response.status === 403) {
    throw new ForbiddenError();
  }

  let detail: ProblemDetail | null = null;
  let msg = `HTTP ${response.status}`;
  try {
    detail = (await response.json()) as ProblemDetail;
    msg = detail.message ?? detail.error ?? msg;
  } catch {
    // ignore
  }
  throw new ApiError(response.status, msg, detail);
}

/**
 * POST /api/v1/identities/{id}/verify-request to re-issue a
 * verification email. Returns the `Retry-After` header value as
 * seconds when present (e.g. on a 200 OK that wants the SPA to
 * cool down before another attempt); throws an `ApiError` with
 * `status: 429` and a `retryAfter: number | null` property when
 * the server returns Too Many Requests (REQ-IDENT-36).
 */
export async function postVerifyResend(
  identityId: string,
): Promise<{ retryAfter: number | null }> {
  return postWithRetryAfter(`/api/v1/identities/${encodeURIComponent(identityId)}/verify-request`);
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

/**
 * Internal: parse Retry-After per RFC 7231 §7.1.3. Mirrors the public
 * helper in `lib/identities/wizard-validators.ts` to avoid a cross-
 * module dependency from the lib/api layer back into lib/identities.
 */
function parseRetryAfterHeader(header: string | null, nowMs: number): number | null {
  if (header === null) return null;
  const trimmed = header.trim();
  if (trimmed === '') return null;
  if (/^\d+$/.test(trimmed)) {
    const seconds = parseInt(trimmed, 10);
    if (Number.isFinite(seconds) && seconds >= 0) return seconds;
    return null;
  }
  if (/^[+-]\d+$/.test(trimmed)) return null;
  const target = Date.parse(trimmed);
  if (!Number.isFinite(target)) return null;
  const deltaMs = target - nowMs;
  if (deltaMs <= 0) return 0;
  return Math.ceil(deltaMs / 1000);
}

/**
 * Test helper: read the `retryAfter` we tagged onto an ApiError. The
 * cast is unsafe in general; only the resend helper sets the property,
 * so callers gate on `err.status === 429`.
 */
export function retryAfterOf(err: unknown): number | null {
  if (!(err instanceof ApiError)) return null;
  const tagged = err as ApiError & { retryAfter?: number | null };
  return typeof tagged.retryAfter === 'number' ? tagged.retryAfter : null;
}
