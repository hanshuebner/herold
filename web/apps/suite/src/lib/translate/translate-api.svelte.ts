/**
 * Translation API client for the on-demand translate feature (issue #84).
 *
 * All requests go to the same-origin herold backend at /api/v1/translate,
 * which proxies server-side to the operator-configured external service.
 * The browser never contacts the external service directly.
 *
 * Session-level availability: a 501 response with the translation_not_configured
 * problem type means the operator has not enabled translation. This state is
 * tracked as module-level $state so every component that reads it re-renders
 * automatically when the value changes.
 */

import { post, ApiError } from '../api/client';

/** Per-session availability flag. Set to false on 501; persists until page reload. */
let _available = $state(true);

/** Reactive accessor so components can observe availability without importing
 *  the mutable cell directly. */
export const translateFeature = {
  get available(): boolean {
    return _available;
  },
};

export interface TranslateRequest {
  text: string;
  target_lang: string;
  source_lang?: string;
}

export interface TranslateResponse {
  translated_text: string;
  detected_source_lang?: string;
}

export type TranslateErrorKind =
  | 'unavailable'
  | 'tooLong'
  | 'upstreamError'
  | 'networkError'
  | 'badRequest';

export interface TranslateResult {
  ok: true;
  data: TranslateResponse;
}

export interface TranslateFailure {
  ok: false;
  kind: TranslateErrorKind;
  message?: string;
}

/**
 * POST /api/v1/translate (same-origin) via the shared REST client.
 *
 * Uses client.post() so the herold_public_csrf cookie is automatically read
 * and sent as X-CSRF-Token — required for all cookie-authenticated POST
 * requests (REQ-AUTH-CSRF, re #84).
 *
 * On 501: marks the feature as permanently unavailable for this session and
 * returns { ok: false, kind: 'unavailable' }.
 * On 413: returns { ok: false, kind: 'tooLong' }.
 * On 502: returns { ok: false, kind: 'upstreamError' }.
 * On 400: returns { ok: false, kind: 'badRequest' }.
 * On network failure or other error: returns { ok: false, kind: 'networkError' }.
 */
export async function callTranslateApi(
  req: TranslateRequest,
): Promise<TranslateResult | TranslateFailure> {
  try {
    const data = await post<TranslateResponse>('/api/v1/translate', req);
    return { ok: true, data };
  } catch (err) {
    if (err instanceof ApiError) {
      switch (err.status) {
        case 501:
          _available = false;
          return { ok: false, kind: 'unavailable' };
        case 413:
          return { ok: false, kind: 'tooLong' };
        case 502:
          return { ok: false, kind: 'upstreamError' };
        case 400:
          return { ok: false, kind: 'badRequest' };
        default:
          return { ok: false, kind: 'networkError', message: `HTTP ${err.status}` };
      }
    }
    // UnauthenticatedError (401) and ForbiddenError (403) from client.post()
    // fire their own global callbacks (_onUnauthenticated) before throwing.
    // Surface them as networkError so TranslateBar shows its generic error UI.
    return { ok: false, kind: 'networkError', message: String(err) };
  }
}
