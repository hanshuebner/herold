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
 * POST /api/v1/translate (same-origin).
 *
 * On 501: marks the feature as permanently unavailable for this session and
 * returns { ok: false, kind: 'unavailable' }.
 * On 413: returns { ok: false, kind: 'tooLong' }.
 * On 502: returns { ok: false, kind: 'upstreamError' }.
 * On 400: returns { ok: false, kind: 'badRequest' }.
 * On network failure: returns { ok: false, kind: 'networkError' }.
 */
export async function callTranslateApi(
  req: TranslateRequest,
): Promise<TranslateResult | TranslateFailure> {
  try {
    const res = await fetch('/api/v1/translate', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    });

    if (res.status === 501) {
      _available = false;
      return { ok: false, kind: 'unavailable' };
    }

    if (res.status === 413) {
      return { ok: false, kind: 'tooLong' };
    }

    if (res.status === 502) {
      return { ok: false, kind: 'upstreamError' };
    }

    if (res.status === 400) {
      return { ok: false, kind: 'badRequest' };
    }

    if (!res.ok) {
      return { ok: false, kind: 'networkError', message: `HTTP ${res.status}` };
    }

    const data = (await res.json()) as TranslateResponse;
    return { ok: true, data };
  } catch (err) {
    return { ok: false, kind: 'networkError', message: String(err) };
  }
}
