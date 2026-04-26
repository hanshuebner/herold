/**
 * JMAP request- and method-level errors, distinguished per RFC 8620 §3.6.
 */

import type { MethodError } from './types';

/**
 * 401 from the JMAP server. Redirect to `/login` (`docs/architecture/
 * 01-system-overview.md` § Bootstrap, resolved Q1 — same-origin session
 * cookie auth).
 */
export class UnauthenticatedError extends Error {
  override name = 'UnauthenticatedError';
  constructor() {
    super('Unauthenticated');
  }
}

/**
 * Transport-level failure (4xx other than 401, 5xx, network error).
 */
export class JmapTransportError extends Error {
  override name = 'JmapTransportError';
  constructor(
    message: string,
    readonly status: number | undefined,
  ) {
    super(message);
  }
}

/**
 * Method-level error (RFC 8620 §3.6.1) — one specific method-call in a
 * batch returned an `error` invocation. Other calls in the same batch may
 * have succeeded.
 */
export class JmapMethodError extends Error {
  override name = 'JmapMethodError';
  constructor(
    readonly callId: string,
    readonly methodError: MethodError,
  ) {
    super(`${methodError.type}: ${methodError.description ?? '(no description)'}`);
  }
}
