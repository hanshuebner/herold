/**
 * Typed REST helper for the admin SPA.
 *
 * All requests go to the same-origin admin listener at /api/v1/...
 * with credentials:'include' so the herold_admin_session cookie attaches.
 *
 * Mutating verbs (POST, PATCH, DELETE) also send the X-CSRF-Token header
 * whose value is read from the herold_admin_csrf non-HttpOnly cookie
 * (issued by the session-create endpoint alongside the session cookie).
 *
 * On 401 from any /api/v1/ call the auth singleton transitions to
 * 'unauthenticated' and the router redirects to /login. We import auth
 * lazily to avoid a circular dependency at module init.
 */

export interface ApiResponse<T> {
  ok: boolean;
  status: number;
  data: T | null;
  errorMessage: string | null;
}

/** Parse the herold_admin_csrf cookie value from document.cookie. */
function readCsrfToken(): string {
  const pairs = document.cookie.split(';');
  for (const pair of pairs) {
    const [name, value] = pair.trim().split('=');
    if (name === 'herold_admin_csrf' && value !== undefined) {
      return decodeURIComponent(value);
    }
  }
  return '';
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };

  const isMutating = method === 'POST' || method === 'PATCH' || method === 'DELETE' || method === 'PUT';
  if (isMutating) {
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
    return {
      ok: false,
      status: 0,
      data: null,
      errorMessage: err instanceof Error ? err.message : 'Network error',
    };
  }

  if (response.status === 401) {
    // Lazy import to avoid circular dependency: auth imports router, client
    // is imported by auth -- but auth is not imported by client at module level.
    void import('../auth/auth.svelte').then(({ auth }) => {
      auth.handleUnauthorized();
    });
    return {
      ok: false,
      status: 401,
      data: null,
      errorMessage: 'Session expired. Please sign in again.',
    };
  }

  if (!response.ok) {
    let errorMessage: string | null = null;
    try {
      // The protoadmin REST surface emits RFC 7807 problem-json on errors
      // (Content-Type: application/problem+json; fields title, detail, type,
      // status, instance). Older / non-protoadmin endpoints may instead use
      // the {message, error} shape. Try the RFC 7807 fields first so the
      // operator sees a real reason ("store: not found") rather than the
      // useless "HTTP 404" fallback.
      const errBody = (await response.json()) as {
        title?: string;
        detail?: string;
        message?: string;
        error?: string;
      };
      const parts: string[] = [];
      if (errBody.title) parts.push(errBody.title);
      if (errBody.detail) parts.push(errBody.detail);
      if (parts.length === 0 && errBody.message) parts.push(errBody.message);
      if (parts.length === 0 && errBody.error) parts.push(errBody.error);
      errorMessage = parts.length > 0 ? parts.join(': ') : `HTTP ${response.status}`;
    } catch {
      errorMessage = `HTTP ${response.status}`;
    }
    return { ok: false, status: response.status, data: null, errorMessage };
  }

  if (response.status === 204) {
    return { ok: true, status: 204, data: null, errorMessage: null };
  }

  try {
    const data = (await response.json()) as T;
    return { ok: true, status: response.status, data, errorMessage: null };
  } catch {
    return {
      ok: false,
      status: response.status,
      data: null,
      errorMessage: 'Failed to parse response body',
    };
  }
}

export function apiGet<T>(path: string): Promise<ApiResponse<T>> {
  return request<T>('GET', path);
}

export function apiPost<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>('POST', path, body);
}

export function apiPatch<T>(path: string, body: unknown): Promise<ApiResponse<T>> {
  return request<T>('PATCH', path, body);
}

export function apiPut<T>(path: string, body: unknown): Promise<ApiResponse<T>> {
  return request<T>('PUT', path, body);
}

export function apiDelete<T>(path: string, body?: unknown): Promise<ApiResponse<T>> {
  return request<T>('DELETE', path, body);
}
