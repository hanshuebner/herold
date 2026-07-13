/**
 * Client-local "unsubscribed-from" set (REQ-UNS-50). Recorded after a
 * successful one-click unsubscribe POST so a future badge/filter
 * suggestion could consult it; the set itself has no v1 management UI.
 * Stored per-account in localStorage, keyed by the sender's From
 * address (lower-cased for case-insensitive lookups).
 */

import { readAccountJson, writeAccountJson } from '../storage/account-scoped';

const STORAGE_KEY = 'unsubscribed-from';

function normalize(email: string): string {
  return email.trim().toLowerCase();
}

/** Record that the user successfully unsubscribed from `email`. */
export function recordUnsubscribed(email: string): void {
  const key = normalize(email);
  if (!key) return;
  const set = readAccountJson<Record<string, true>>(STORAGE_KEY, {});
  set[key] = true;
  writeAccountJson(STORAGE_KEY, set);
}

/** True when the user has previously unsubscribed from `email`. */
export function isUnsubscribedFrom(email: string): boolean {
  const key = normalize(email);
  if (!key) return false;
  const set = readAccountJson<Record<string, true>>(STORAGE_KEY, {});
  return set[key] === true;
}
