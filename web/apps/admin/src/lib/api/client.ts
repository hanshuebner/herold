/**
 * Typed REST helper for the admin SPA.
 *
 * All requests go to the same-origin public listener at /api/v1/...
 * with credentials:'include' so the herold_public_session cookie attaches.
 * Since the admin SPA is served at /admin/ on the public listener (re #58),
 * all web sessions share the public cookie set (REQ-AUTH-COOKIE-PATH).
 *
 * Mutating verbs (POST, PATCH, DELETE) also send the X-CSRF-Token header
 * whose value is read from the herold_public_csrf non-HttpOnly cookie
 * (issued by the session-create endpoint alongside the session cookie).
 *
 * On 401 from any /api/v1/ call the auth singleton transitions to
 * 'unauthenticated' and the router redirects to /login. We import auth
 * lazily to avoid a circular dependency at module init.
 *
 * On 403 with step_up_required:true the client calls the registered
 * _onStepUpRequired callback (set by step-up.svelte.ts at module init),
 * waits for elevation, then retries the request once (REQ-AS-20..23, re #79).
 */

import { t } from '../i18n/i18n.svelte';

export interface ApiResponse<T> {
  ok: boolean;
  status: number;
  data: T | null;
  errorMessage: string | null;
}

/** Parse the herold_public_csrf cookie value from document.cookie. */
export function readAdminCsrfToken(): string {
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
 * Optional async callback invoked when a 403 carries step_up_required:true.
 * Resolves when elevation is granted, rejects when the user cancels.
 * Registered by step-up.svelte.ts at module init (REQ-AS-20, re #79).
 */
let _onStepUpRequired: (() => Promise<void>) | null = null;

export function setAdminOnStepUpRequired(fn: () => Promise<void>): void {
  _onStepUpRequired = fn;
}

/**
 * Core request function.
 *
 * _retry is set to true on the automatic retry after step-up elevation so
 * that a second 403 does not trigger another modal — it returns an error.
 */
async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  _retry = false,
): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'application/json',
  };

  const isMutating = method === 'POST' || method === 'PATCH' || method === 'DELETE' || method === 'PUT';
  if (isMutating) {
    const token = readAdminCsrfToken();
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

  if (response.status === 403) {
    // Peek at the body to detect step_up_required (REQ-AS-20, re #79).
    type ForbiddenDetail = {
      step_up_required?: boolean;
      message?: string;
      title?: string;
      detail?: string;
    };
    let detail: ForbiddenDetail | null = null;
    try {
      detail = (await response.json()) as ForbiddenDetail;
    } catch {
      // ignore
    }

    if (detail?.step_up_required && !_retry && _onStepUpRequired) {
      try {
        await _onStepUpRequired();
        // Elevation granted; retry exactly once.
        return request<T>(method, path, body, true);
      } catch {
        return {
          ok: false,
          status: 403,
          data: null,
          errorMessage: 'Action cancelled — authentication required.',
        };
      }
    }

    const parts: string[] = [];
    if (detail?.title) parts.push(detail.title);
    if (detail?.detail) parts.push(detail.detail);
    if (parts.length === 0 && detail?.message) parts.push(detail.message);
    const errorMessage = parts.length > 0 ? parts.join(': ') : 'Insufficient permissions.';
    return { ok: false, status: 403, data: null, errorMessage };
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
      errorMessage: t('common.parseResponseFailed'),
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
