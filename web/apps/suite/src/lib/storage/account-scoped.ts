/**
 * Account-scoped localStorage helpers.
 *
 * Keys follow the scheme `herold.suite.<username>.<name>`, where
 * `<username>` is the currently logged-in account's username (from
 * the JMAP session descriptor). The pre-auth namespace uses 'anon'
 * so reads that happen before the session is established do not
 * bleed across accounts.
 *
 * Modules that store per-account data (search history, avatar cache,
 * reaction-confirm flags) import from here rather than duplicating
 * the scoping logic. Settings uses its own internal `storageKey()`
 * with a compatible `herold.suite.settings.<username>.<name>` scheme
 * and is not migrated to avoid touching existing persisted user data.
 */

import { auth } from '../auth/auth.svelte';

/**
 * Return a localStorage key scoped to the currently logged-in account.
 * Falls back to 'anon' when no session is available so pre-auth reads
 * return only the anon namespace (which is normally empty).
 */
export function accountKey(name: string): string {
  const username = auth.session?.username ?? 'anon';
  return `herold.suite.${username}.${name}`;
}

/** Read a JSON value from the current account's namespace. */
export function readAccountJson<T>(name: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(accountKey(name));
    if (raw === null) return fallback;
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

/** Write a JSON value into the current account's namespace. */
export function writeAccountJson(name: string, value: unknown): void {
  try {
    localStorage.setItem(accountKey(name), JSON.stringify(value));
  } catch {
    // Quota / private mode — data just does not persist this session.
  }
}

/** Remove an item from the current account's namespace. */
export function removeAccountItem(name: string): void {
  try {
    localStorage.removeItem(accountKey(name));
  } catch {
    // ignore
  }
}
