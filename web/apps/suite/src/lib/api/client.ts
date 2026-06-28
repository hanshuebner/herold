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
 *   UnauthenticatedError -- 401: session expired or never established.
 *   ForbiddenError       -- 403: session valid but insufficient scope.
 *   ApiError             -- other non-2xx: carries status + RFC 7807 body.
 */

/** RFC 7807 problem detail body, as returned by herold. */
export interface ProblemDetail {
  /** The problem type URI, e.g. "https://netzhansa.com/problems/session_expired". */
  type?: string;
  error?: string;
  message?: string;
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
 * Invoke the registered unauthenticated callback, if any.
 * Used by fetch wrappers (e.g. identity-verify.ts) that handle
 * their own response parsing but still need to escalate 401s to
 * the global auth state machine (REQ-AS-10, re #77).
 */
export function notifyUnauthenticated(problemType: string | null = null): void {
  _onUnauthenticated?.(problemType);
}

/** Parse the herold_public_csrf cookie value from document.cookie. */
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

const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
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
    throw new ApiError(0, err instanceof Error ? err.message : 'Network error');
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
    _onUnauthenticated?.(problemType);
    throw new UnauthenticatedError(msg, problemType);
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

  if (!response.ok) {
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
