/**
 * @herold/manual — in-app and standalone manual viewer.
 *
 * Public API:
 *   - Manual (default export) — top-level Svelte 5 component
 *   - ManualBundle, ManualChapter, OutlineEntry — bundle JSON types
 *   - defaultT — identity-with-fallback translation helper
 *   - markdocConfig, allowedTags — Markdoc schema (used by bundler)
 *   - tokenize — minimal code tokenizer
 */
export { default } from './components/Manual.svelte';

export type { ManualBundle, ManualChapter, OutlineEntry } from './markdoc/bundle.js';
export { markdocConfig, allowedTags } from './markdoc/schema.js';
export { defaultT, defaultStrings } from './chrome/strings.js';
export { tokenize } from './markdoc/tokenize.js';
export type { Token } from './markdoc/tokenize.js';
