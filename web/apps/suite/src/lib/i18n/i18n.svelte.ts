/**
 * Tiny in-app i18n.
 *
 * The store exposes a reactive `locale` ($state), plus a translation
 * function `t(key, params?)` that resolves keys against the active
 * dictionary and falls back to the English string. A simple `{name}`
 * placeholder replacement lets us interpolate counts and strings
 * without pulling in a full ICU formatter -- pluralisation goes
 * through the `count` parameter and a per-string `n_` / `n_one`
 * sibling key when needed.
 *
 * Locale persistence is handled by the settings store (lives next to
 * theme); this module just surfaces a reactive cell that the settings
 * store writes into. Detection: settings.hydrate() seeds from
 * localStorage; if absent, we sniff `navigator.language` once and pick
 * 'de' for any de-* tag, English otherwise.
 */

import { en } from './en';
import { de } from './de';

export type Locale = 'en' | 'de';
export const LOCALES: readonly Locale[] = ['en', 'de'];

export type Dict = Readonly<Record<string, string>>;

const DICTS: Readonly<Record<Locale, Dict>> = {
  en,
  de,
};

class I18n {
  /** Current active locale. Wired up by the settings store. */
  locale = $state<Locale>('en');

  /** Resolve a translation key. Falls back to English, then the key. */
  t(key: string, params?: Record<string, string | number>): string {
    const active = DICTS[this.locale] as Readonly<Record<string, string>>;
    const fallback = en as Readonly<Record<string, string>>;
    const raw = active[key] ?? fallback[key] ?? key;
    if (!params) return raw;
    return raw.replace(/\{(\w+)\}/g, (_match: string, name: string) => {
      const value = params[name];
      return value === undefined ? `{${name}}` : String(value);
    });
  }
}

export const i18n = new I18n();

/**
 * Bound shortcut: components import `t` directly and Svelte's
 * reactivity on `i18n.locale` re-renders them when locale changes.
 */
export function t(key: string, params?: Record<string, string | number>): string {
  return i18n.t(key, params);
}

/**
 * Best-effort detection from the browser. Used when no setting is
 * persisted yet. Returns `'de'` for German tags (`de`, `de-DE`,
 * `de-CH`, ...), English otherwise.
 */
export function detectLocale(): Locale {
  try {
    const lang = (typeof navigator !== 'undefined' ? navigator.language : '') || '';
    if (lang.toLowerCase().startsWith('de')) return 'de';
  } catch {
    // headless-test envs may not expose navigator; default to English.
  }
  return 'en';
}

/**
 * BCP-47 tag for Intl-based date / time / number formatting that
 * matches the active locale. Both supported locales pin to a
 * 24-hour, day-month-year region (issue #23: English default would
 * follow the browser to en-US otherwise -- 12h am/pm,
 * mm/dd/yyyy -- which the spec rules out).
 */
export function localeTag(): string {
  return i18n.locale === 'de' ? 'de-DE' : 'en-GB';
}
