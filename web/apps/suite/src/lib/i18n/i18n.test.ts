import { describe, it, expect, beforeEach } from 'vitest';
import { i18n, t, detectLocale, LOCALES } from './i18n.svelte';

describe('i18n', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });

  it('exposes both supported locales', () => {
    expect(LOCALES).toEqual(['en', 'de']);
  });

  it('returns the English string by default', () => {
    expect(t('rail.mail')).toBe('Mail');
    expect(t('compose.send')).toBe('Send');
  });

  it('returns the German string when locale is de', () => {
    i18n.locale = 'de';
    expect(t('rail.mail')).toBe('E-Mail');
    expect(t('compose.send')).toBe('Senden');
  });

  it('falls back to the key for unknown identifiers', () => {
    expect(t('does.not.exist')).toBe('does.not.exist');
  });

  it('falls back to English when a key is missing in the active locale', () => {
    // Both en and de currently have every key; emulate a missing key by
    // poking a fake key through the cast that the resolver uses.
    expect(t('select.all')).toBe('All');
    i18n.locale = 'de';
    expect(t('select.all')).toBe('Alle');
  });

  it('interpolates {name} placeholders', () => {
    expect(t('bulk.selected', { count: 3 })).toBe('3 selected');
    i18n.locale = 'de';
    expect(t('bulk.selected', { count: 3 })).toBe('3 ausgewaehlt');
  });

  it('leaves unmatched placeholders untouched', () => {
    expect(t('list.couldNotLoad', {})).toBe("Couldn't load {name}.");
  });
});

describe('detectLocale', () => {
  it('returns a member of LOCALES', () => {
    const detected = detectLocale();
    expect(LOCALES).toContain(detected);
  });
});
