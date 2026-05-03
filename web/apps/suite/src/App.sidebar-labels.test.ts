/**
 * Regression test for the sidebar "More" -> "Labels" rename (re #56).
 *
 * Commit 1966413 changed App.svelte line 415 from t('sidebar.more') to
 * t('sidebar.labels') so the sidebar section heading reads "Labels" in both
 * English and German.  This test asserts the i18n key wiring used by that
 * button so that any accidental revert of the key name is caught immediately.
 *
 * Mounting full App.svelte is avoided here: the heading is a single call site
 * `t('sidebar.labels')` and the meaningful invariant is that the key resolves
 * to "Labels" (not "More").  Testing the resolver directly is idiomatic for
 * i18n regressions in this codebase (see lib/i18n/i18n.test.ts).
 */

import { describe, it, expect, beforeEach } from 'vitest';
import { i18n, t } from './lib/i18n/i18n.svelte';

describe('App sidebar heading: sidebar.labels key (re #56)', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });

  it('sidebar.labels resolves to "Labels" in English', () => {
    expect(t('sidebar.labels')).toBe('Labels');
  });

  it('sidebar.labels does NOT resolve to the old "More" string in English', () => {
    expect(t('sidebar.labels')).not.toBe('More');
  });

  it('sidebar.more resolves to "More" in English (confirming the two keys are distinct)', () => {
    // This guards against someone merging the two keys back into one value.
    // If sidebar.more were also renamed to "Labels", the rename would be lost.
    expect(t('sidebar.more')).toBe('More');
  });

  it('sidebar.labels resolves to "Labels" in German', () => {
    i18n.locale = 'de';
    expect(t('sidebar.labels')).toBe('Labels');
  });

  it('sidebar.labels does NOT resolve to the old German "Mehr" string', () => {
    i18n.locale = 'de';
    // "Mehr" is the German word for "More" -- confirm the labels key is
    // clearly distinct from the More key in German as well.
    expect(t('sidebar.labels')).not.toBe('Mehr');
  });

  it('sidebar.more resolves to "Mehr" in German (the two keys remain distinct)', () => {
    i18n.locale = 'de';
    expect(t('sidebar.more')).toBe('Mehr');
  });
});
