/**
 * Language detection helpers for the on-demand translate feature.
 *
 * franc-min returns ISO 639-3 codes; the translate API and i18n locale
 * both use ISO 639-1 (two-letter) codes. This module maps between them
 * and wraps franc so callers never import it directly.
 */

import { franc } from 'franc-min';

/**
 * Minimum body length in characters before we attempt language detection.
 * franc on very short bodies is unreliable; below this threshold we return
 * null so the Translate affordance is not shown.
 */
export const MIN_DETECT_LENGTH = 100;

/**
 * ISO 639-3 -> ISO 639-1 mapping covering the languages most likely to
 * appear in herold-hosted mail. Entries outside this set remain untranslatable
 * via the affordance (franc returns an unknown code; we yield null).
 */
const ISO3_TO_1: Readonly<Record<string, string>> = {
  eng: 'en',
  deu: 'de',
  fra: 'fr',
  spa: 'es',
  ita: 'it',
  por: 'pt',
  nld: 'nl',
  rus: 'ru',
  pol: 'pl',
  ces: 'cs',
  slk: 'sk',
  swe: 'sv',
  dan: 'da',
  nor: 'no',
  nob: 'no',
  nno: 'no',
  fin: 'fi',
  hun: 'hu',
  ron: 'ro',
  bul: 'bg',
  hrv: 'hr',
  ell: 'el',
  tur: 'tr',
  jpn: 'ja',
  zho: 'zh',
  cmn: 'zh',
  kor: 'ko',
  ara: 'ar',
  heb: 'he',
  ukr: 'uk',
  cat: 'ca',
};

/**
 * Map an ISO 639-3 code (as returned by franc) to an ISO 639-1 code.
 * Returns null when the code is not in the table or is 'und' (undetermined).
 */
export function iso3ToIso1(code: string): string | null {
  if (!code || code === 'und') return null;
  return ISO3_TO_1[code] ?? null;
}

/**
 * Detect the ISO 639-1 language code for the given plain text.
 *
 * Returns null when:
 *   - the text is shorter than MIN_DETECT_LENGTH (franc is unreliable on short samples)
 *   - franc returns 'und' (indeterminate)
 *   - the detected ISO 639-3 code has no ISO 639-1 entry in our table
 */
export function detectLanguage(text: string): string | null {
  const stripped = text.trim();
  if (stripped.length < MIN_DETECT_LENGTH) return null;
  const code3 = franc(stripped);
  return iso3ToIso1(code3);
}
