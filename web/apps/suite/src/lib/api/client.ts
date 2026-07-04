/**
 * Typed REST helper for the Suite SPA.
 *
 * All requests go to the same-origin public listener at /api/v1/...
 * with credentials:'include' so the herold_public_session cookie attaches.
 *
 * Mutating verbs (POST, PUT, PATCH, DELETE) also send the X-CSRF-Token
 * header whose value is read from the herold_public_csrf non-HttpOnly
 * cookie (issued by the session-create endpoint alongside the session
 * cookie, REQ-AUTH-CSRF).
 *
 * Error hierarchy:
 *   UnauthenticatedError   -- 401: session expired or never established.
 *   ForbiddenError         -- 403: session valid but insufficient scope.
 *   StepUpCancelledError   -- 403 step_up_required, user cancelled the modal.
 *   ApiError               -- other non-2xx: carries status + RFC 7807 body.
 *
 * 403 step_up_required handling (REQ-AS-20..23, re #79):
 *   When a 403 carries step_up_required:true the client calls the registered
 *   _onStepUpRequired callback (set by step-up.svelte.ts at module init).
 *   The callback presents the TOTP modal and resolves when elevation is
 *   granted, or rejects when the user cancels. On resolve the original
 *   request is retried once (_retry flag prevents infinite loops). On reject
 *   a StepUpCancelledError propagates to the caller.
 *
 * Debug-ring logging (re #124):
 *   Every failed API call (network error, 401, 403, or any non-2xx) is
 *   recorded as an 'error'-level entry in the device debug ring via
 *   appendEvent. The payload carries method, path, status, and the
 *   problem-detail fields title/category/diagnostic when present.
 *   Credentials and request bodies are never logged.
 */

import { appendEvent } from '../debug-ring/debug-ring';

/** RFC 7807 problem detail body, as returned by herold. */
export interface ProblemDetail {
  /** The problem type URI, e.g. "https://netzhansa.com/problems/session_expired". */
  type?: string;
  /** RFC 7807 standard human-readable explanation of the error. */
  detail?: string;
  error?: string;
  message?: string;
  /** Present on 403 when the server requires TOTP elevation (REQ-AUTH-74). */
  step_up_required?: boolean;
  /** Present on 400 from the step-up endpoint when TOTP is not enrolled. */
  enroll_required?: boolean;
  /** Scope the elevation applies to: "admin" or "self-service". */
  elevation_scope?: string;
  [key: string]: unknown;
}

export class UnauthenticatedError extends Error {
  readonly status = 401;
  /**
   * The RFC 7807 type URI from the problem detail body, when present.
   * Known values: "https://netzhansa.com/problems/session_expired",
   * "https://netzhansa.com/problems/session_revoked".
   * Null when the server did not emit a typed problem (generic 401).
   */
  readonly problemType: string | null;
  constructor(message = 'Session expired. Please sign in again.', problemType: string | null = null) {
    super(message);
    this.name = 'UnauthenticatedError';
    this.problemType = problemType;
  }
}

export class ForbiddenError extends Error {
  readonly status = 403;
  constructor(message = 'Insufficient permissions.') {
    super(message);
    this.name = 'ForbiddenError';
  }
}

/**
 * Thrown when a 403 step_up_required was intercepted and the user cancelled
 * the TOTP modal rather than completing elevation (REQ-AS-22).
 * Callers that catch generic errors will receive the human-readable message
 * "Action cancelled — authentication required." Callers that need to suppress
 * their own error UI can check instanceof StepUpCancelledError.
 */
export class StepUpCancelledError extends Error {
  readonly status = 403;
  constructor() {
    super('Action cancelled — authentication required.');
    this.name = 'StepUpCancelledError';
  }
}

export class ApiError extends Error {
  readonly status: number;
  readonly detail: ProblemDetail | null;
  constructor(status: number, message: string, detail: ProblemDetail | null = null) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.detail = detail;
  }
}

/**
 * Optional callback invoked whenever the REST client receives a 401.
 * The problemType argument carries the RFC 7807 type URI from the
 * problem detail body, or null for a generic (untyped) 401.
 * Register once at application boot (auth.svelte.ts) so the auth state
 * machine can transition to 'unauthenticated' without a circular import.
 */
let _onUnauthenticated: ((problemType: string | null) => void) | null = null;

export function setOnUnauthenticated(fn: (problemType: string | null) => void): void {
  _onUnauthenticated = fn;
}

/**
 * Optional async callback invoked whenever the REST client receives a 403
 * with step_up_required:true. The callback presents the TOTP modal and
 * returns a Promise<void> that resolves when elevation is granted, or
 * rejects with StepUpCancelledError when the user cancels.
 *
 * Register once at application boot (step-up.svelte.ts) to avoid a circular
 * import: step-up imports auth; auth imports client; client cannot import
 * step-up. The callback pattern breaks the cycle (REQ-AS-20..23, re #79).
 */
let _onStepUpRequired: (() => Promise<void>) | null = null;

export function setOnStepUpRequired(fn: () => Promise<void>): void {
  _onStepUpRequired = fn;
}

/**
 * Invoke the registered unauthenticated callback, if any.
 * Used by fetch wrappers (e.g. identity-verify.ts) that handle
 * their own response parsing but still need to escalate 401s to
 * the global auth state machine (REQ-AS-10, re #77).
 */
export function notifyUnauthenticated(problemType: string | null = null): void {
  _onUnauthenticated?.(problemType);
}

/** Parse the herold_public_csrf cookie value from document.cookie. */
export function readCsrfToken(): string {
  const pairs = document.cookie.split(';');
  for (const pair of pairs) {
    const [name, value] = pair.trim().split('=');
    if (name === 'herold_public_csrf' && value !== undefined) {
      return decodeURIComponent(value);
    }
  }
  return '';
}

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

/**
 * Core request function. The _retry flag is set to true on the automatic
 * retry that follows a successful step-up elevation so that a second 403
 * does not trigger another modal (it throws ForbiddenError instead).
 */
async function request<T>(method: string, path: string, body?: unknown, _retry = false): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
  };

  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  if (MUTATING.has(method)) {
    const token = readCsrfToken();
    if (token) {
      headers['X-CSRF-Token'] = token;
    }
  }

  let response: Response;
  try {
    response = await fetch(path, {
      method,
      headers,
      credentials: 'include',
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (err) {
    const networkMsg = err instanceof Error ? err.message : 'Network error';
    void appendEvent('page', 'error', 'api.error', { method, path, status: 0, title: networkMsg });
    throw new ApiError(0, networkMsg);
  }

  if (response.status === 401) {
    let msg = 'Session expired. Please sign in again.';
    let problemType: string | null = null;
    try {
      const b = (await response.json()) as ProblemDetail;
      if (b.message) msg = b.message;
      if (typeof b.type === 'string' && b.type !== 'about:blank') {
        problemType = b.type;
      }
    } catch {
      // ignore
    }
    void appendEvent('page', 'error', 'api.error', {
      method,
      path,
      status: 401,
      ...(problemType ? { type: problemType } : {}),
    });
    _onUnauthenticated?.(problemType);
    throw new UnauthenticatedError(msg, problemType);
  }

  if (response.status === 403) {
    let detail: ProblemDetail | null = null;
    try {
      detail = (await response.json()) as ProblemDetail;
    } catch {
      // ignore
    }

    // Step-up required: present modal and retry once (REQ-AS-20..23, re #79).
    if (detail?.step_up_required && !_retry && _onStepUpRequired) {
      try {
        await _onStepUpRequired();
      } catch {
        // User cancelled the modal.
        void appendEvent('page', 'error', 'api.error', { method, path, status: 403, title: 'step-up cancelled' });
        throw new StepUpCancelledError();
      }
      // Elevation granted; retry the original request exactly once.
      return request<T>(method, path, body, true);
    }

    const msg = detail?.message ?? 'Insufficient permissions.';
    void appendEvent('page', 'error', 'api.error', { method, path, status: 403 });
    throw new ForbiddenError(msg);
  }

  if (!response.ok) {
    let detail: ProblemDetail | null = null;
    let msg = `HTTP ${response.status}`;
    try {
      detail = (await response.json()) as ProblemDetail;
      msg = detail.detail ?? detail.message ?? detail.error ?? msg;
    } catch {
      // ignore
    }
    void appendEvent('page', 'error', 'api.error', {
      method,
      path,
      status: response.status,
      ...(detail?.['title'] ? { title: String(detail['title']) } : {}),
      ...(detail?.['category'] ? { category: String(detail['category']) } : {}),
      ...(detail?.['diagnostic'] ? { diagnostic: String(detail['diagnostic']) } : {}),
    });
    throw new ApiError(response.status, msg, detail);
  }

  // 204 No Content and other void responses.
  if (response.status === 204 || response.headers.get('content-length') === '0') {
    return undefined as unknown as T;
  }

  // Check content-type before trying to parse as JSON.
  const ct = response.headers.get('content-type') ?? '';
  if (!ct.includes('application/json')) {
    return undefined as unknown as T;
  }

  try {
    return (await response.json()) as T;
  } catch {
    throw new ApiError(response.status, 'Failed to parse response body');
  }
}

/** GET /api/v1/<path> and return parsed JSON as T. */
export function get<T>(path: string): Promise<T> {
  return request<T>('GET', path);
}

/** POST /api/v1/<path> with optional body, return parsed JSON as T. */
export function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('POST', path, body);
}

/** PUT /api/v1/<path> with body, return parsed JSON as T. */
export function put<T>(path: string, body: unknown): Promise<T> {
  return request<T>('PUT', path, body);
}

/** PATCH /api/v1/<path> with body, return parsed JSON as T. */
export function patch<T>(path: string, body: unknown): Promise<T> {
  return request<T>('PATCH', path, body);
}

/** DELETE /api/v1/<path> with optional body. Use T = void for 204. */
export function del<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('DELETE', path, body);
}
