/**
 * Unit tests for the franc -> ISO 639-1 language mapping helpers.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { iso3ToIso1, detectLanguage, MIN_DETECT_LENGTH } from './lang-map';

// franc-min is mocked so tests are deterministic and don't depend on the
// actual statistical model.
vi.mock('franc-min', () => ({
  franc: vi.fn(),
}));

import { franc } from 'franc-min';
const francMock = vi.mocked(franc);

describe('iso3ToIso1', () => {
  it('maps common language codes', () => {
    expect(iso3ToIso1('eng')).toBe('en');
    expect(iso3ToIso1('deu')).toBe('de');
    expect(iso3ToIso1('fra')).toBe('fr');
    expect(iso3ToIso1('spa')).toBe('es');
    expect(iso3ToIso1('ita')).toBe('it');
    expect(iso3ToIso1('por')).toBe('pt');
    expect(iso3ToIso1('nld')).toBe('nl');
    expect(iso3ToIso1('rus')).toBe('ru');
    expect(iso3ToIso1('pol')).toBe('pl');
    expect(iso3ToIso1('ces')).toBe('cs');
    expect(iso3ToIso1('swe')).toBe('sv');
    expect(iso3ToIso1('dan')).toBe('da');
    expect(iso3ToIso1('nor')).toBe('no');
    expect(iso3ToIso1('nob')).toBe('no');
    expect(iso3ToIso1('fin')).toBe('fi');
    expect(iso3ToIso1('jpn')).toBe('ja');
    expect(iso3ToIso1('zho')).toBe('zh');
    expect(iso3ToIso1('cmn')).toBe('zh');
    expect(iso3ToIso1('kor')).toBe('ko');
    expect(iso3ToIso1('ara')).toBe('ar');
    expect(iso3ToIso1('tur')).toBe('tr');
  });

  it('returns null for undetermined code', () => {
    expect(iso3ToIso1('und')).toBeNull();
  });

  it('returns null for empty string', () => {
    expect(iso3ToIso1('')).toBeNull();
  });

  it('returns null for unknown code', () => {
    expect(iso3ToIso1('xyz')).toBeNull();
  });
});

describe('detectLanguage', () => {
  beforeEach(() => {
    francMock.mockReset();
  });

  it('returns null when text is shorter than MIN_DETECT_LENGTH', () => {
    const short = 'a'.repeat(MIN_DETECT_LENGTH - 1);
    const result = detectLanguage(short);
    expect(result).toBeNull();
    // franc should not be called for short text
    expect(francMock).not.toHaveBeenCalled();
  });

  it('returns ISO 639-1 code when franc detects a known language', () => {
    francMock.mockReturnValue('deu');
    const longText = 'a'.repeat(MIN_DETECT_LENGTH);
    expect(detectLanguage(longText)).toBe('de');
    expect(francMock).toHaveBeenCalledWith(longText);
  });

  it('returns null when franc returns und', () => {
    francMock.mockReturnValue('und');
    const longText = 'a'.repeat(MIN_DETECT_LENGTH);
    expect(detectLanguage(longText)).toBeNull();
  });

  it('returns null when franc returns an unmapped code', () => {
    francMock.mockReturnValue('xyz');
    const longText = 'a'.repeat(MIN_DETECT_LENGTH);
    expect(detectLanguage(longText)).toBeNull();
  });

  it('trims the text before checking length', () => {
    // A string that is long enough after trimming but short with padding
    // counts only the trimmed content.
    const padded = '  ' + 'a'.repeat(MIN_DETECT_LENGTH - 4) + '  ';
    // The trimmed length is MIN_DETECT_LENGTH - 4, which is still short
    detectLanguage(padded);
    // Since MIN_DETECT_LENGTH - 4 < MIN_DETECT_LENGTH, franc should NOT run
    expect(francMock).not.toHaveBeenCalled();
  });

  it('detects English correctly', () => {
    francMock.mockReturnValue('eng');
    const longText = 'b'.repeat(MIN_DETECT_LENGTH);
    expect(detectLanguage(longText)).toBe('en');
  });
});
