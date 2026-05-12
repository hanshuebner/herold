/**
 * Typed wrappers over the tagged-address REST surface (REQ-TAG-50,
 * REQ-TAG-60..62).
 *
 * Endpoints:
 *   POST   /api/v1/tagged-address-dismissals
 *   GET    /api/v1/tagged-address-dismissals
 *   DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}
 *   POST   /api/v1/tagged-address-filters/{id}/convert-to-sieve
 *
 * Dismissals do NOT appear on the JMAP wire (REQ-TAG-71) — they are a
 * private signalling mechanism for the SPA banner gate (REQ-MAIL-12c).
 * The convert-to-sieve operation is exposed alongside so the Settings
 * surface (#30) and any future per-filter overflow menu can reuse the
 * same module.
 */

import { get, post, del } from './client';

/** Wire-form dismissal row returned by GET / echoed back by POST. */
export interface TaggedAddressDismissal {
  base_identity_id: string;
  suffix: string;
  dismissed_at: string;
}

/** GET / list response envelope. */
export interface TaggedAddressDismissalList {
  dismissals: TaggedAddressDismissal[];
}

/** POST body for creating a dismissal. */
export interface CreateDismissalBody {
  base_identity_id: string;
  suffix: string;
}

/**
 * POST /api/v1/tagged-address-dismissals
 * Idempotent: returns 201 on first create, 200 on duplicate. Server
 * canonicalises the suffix to lower case (REQ-TAG-02).
 */
export function createDismissal(
  body: CreateDismissalBody,
): Promise<TaggedAddressDismissal> {
  return post<TaggedAddressDismissal>('/api/v1/tagged-address-dismissals', body);
}

/**
 * GET /api/v1/tagged-address-dismissals
 * Returns every dismissal owned by the authenticated caller.
 */
export function listDismissals(): Promise<TaggedAddressDismissalList> {
  return get<TaggedAddressDismissalList>('/api/v1/tagged-address-dismissals');
}

/**
 * DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}
 * Idempotent: 204 whether or not the row existed. Lets the user clear
 * their dismissal so the banner re-appears on the next message.
 */
export function deleteDismissal(
  baseIdentityId: string,
  suffix: string,
): Promise<void> {
  return del<void>(
    `/api/v1/tagged-address-dismissals/${encodeURIComponent(baseIdentityId)}/${encodeURIComponent(suffix)}`,
  );
}

/** Response body for the convert-to-sieve endpoint (REQ-TAG-50). */
export interface ConvertToSieveResponse {
  /** The Sieve text fragment the handler prepended (including the
   *  auto-merged `require [...]` line, REQ-TAG-50 step 3). */
  fragment: string;
  /** The complete user Sieve script after the merge — lets the SPA
   *  stamp it into the ManageSieve editor without a follow-up read. */
  script: string;
}

/**
 * POST /api/v1/tagged-address-filters/{id}/convert-to-sieve
 * One-way conversion (REQ-TAG-51): the managed filter row is replaced
 * by an equivalent Sieve fragment prepended to the principal's script.
 * Returns 200 OK with `{fragment, script}` on success.
 */
export function convertFilterToSieve(
  filterId: string,
): Promise<ConvertToSieveResponse> {
  return post<ConvertToSieveResponse>(
    `/api/v1/tagged-address-filters/${encodeURIComponent(filterId)}/convert-to-sieve`,
  );
}
