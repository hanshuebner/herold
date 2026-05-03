/**
 * Default English strings for the manual viewer.
 *
 * The Manual component accepts a `t` prop of type `(key: string) => string`.
 * If no `t` is provided the component falls back to this map; keys not found
 * in the map are returned as-is (identity fallback) so missing keys are
 * visible rather than silent.
 */
export const defaultStrings: Record<string, string> = {
  'manual.toc.label': 'Table of contents',
  'manual.onthispage.label': 'On this page',
  'manual.search.placeholder': 'Search topics...',
  'manual.search.label': 'Search manual',
  'manual.search.noResults': 'No matching topics.',
  'manual.callout.info': 'Note',
  'manual.callout.warning': 'Warning',
  'manual.callout.caution': 'Caution',
  'manual.req.label': 'Requirement',
  'manual.code.copy': 'Copy',
  'manual.code.copied': 'Copied',
};

/**
 * Returns the default English string for a key, falling back to the key
 * itself when no entry exists.  Consumers wire a real i18n function via the
 * `t` prop instead of calling this directly.
 */
export function defaultT(key: string): string {
  return defaultStrings[key] ?? key;
}
