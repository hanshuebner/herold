/**
 * Settings store for the admin SPA, mirroring
 * `web/apps/suite/src/lib/settings/settings.svelte.ts` (re #180).
 *
 * Currently holds only the locale preference. It lives in `localStorage`,
 * keyed by the signed-in principal's email so a shared browser profile
 * used by two operators keeps their locale choices separate.
 *
 * Locale is a reactive `$state` cell (via the i18n singleton). Mutating it
 * goes through `setLocale`, which writes back to localStorage on the same
 * tick.
 */

import { auth } from '../auth/auth.svelte';
import { i18n, detectLocale, LOCALES, type Locale } from '../i18n/i18n.svelte';

const KEYS = {
  locale: 'locale',
} as const;

function storageKey(name: string): string {
  // Scope per-principal so two operators on the same browser don't
  // clobber each other's locale choice. The pre-auth namespace uses
  // 'anon' so defaults can be read/written before the session resolves.
  const email = auth.principal?.email ?? 'anon';
  return `herold.admin.settings.${email}.${name}`;
}

function readJson<T>(name: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(storageKey(name));
    if (raw === null) return fallback;
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

function writeJson(name: string, value: unknown): void {
  try {
    localStorage.setItem(storageKey(name), JSON.stringify(value));
  } catch {
    // Quota exceeded / private mode — settings just won't persist this run.
  }
}

class Settings {
  /** Read every setting from localStorage. Idempotent. */
  hydrate(): void {
    const persistedLocale = readJson<Locale | null>(KEYS.locale, null);
    const resolvedLocale =
      persistedLocale && LOCALES.includes(persistedLocale)
        ? persistedLocale
        : detectLocale();
    i18n.locale = resolvedLocale;
  }

  // ── Locale ────────────────────────────────────────────────────────────
  /**
   * Locale lives on the i18n singleton -- this getter is a thin
   * forwarder so callers can use the familiar settings.* shape.
   */
  get locale(): Locale {
    return i18n.locale;
  }
  setLocale(value: Locale): void {
    i18n.locale = value;
    writeJson(KEYS.locale, value);
  }
}

export const settings = new Settings();
