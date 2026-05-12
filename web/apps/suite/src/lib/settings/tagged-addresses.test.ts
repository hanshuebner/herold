/**
 * Pure-helper tests for Settings → Tagged addresses
 * (REQ-SET-TAG-01..21, server gate REQ-TAG-13 / REQ-TAG-83).
 *
 * Coverage:
 *  - normaliseSuffix: trim + lowercase.
 *  - validateSuffix: empty / overlong / whitespace / forbidden / OK.
 *  - validateLabelName: empty / overlong / OK.
 *  - actionLabelKey: exhaustive switch over the action enum.
 *  - composeTaggedAddress: well-formed and degenerate base addresses.
 *  - sortFilters / sortDismissals: identity email then suffix.
 */

import { describe, expect, it } from 'vitest';
import {
  actionLabelKey,
  composeTaggedAddress,
  FILTER_ACTIONS,
  normaliseSuffix,
  sortDismissals,
  sortFilters,
  validateLabelName,
  validateSuffix,
} from './tagged-addresses';

describe('normaliseSuffix', () => {
  it('lower-cases and trims', () => {
    expect(normaliseSuffix(' Amazon ')).toBe('amazon');
    expect(normaliseSuffix('SHOP-Aldi')).toBe('shop-aldi');
  });

  it('returns the empty string for whitespace-only input', () => {
    expect(normaliseSuffix('   ')).toBe('');
  });
});

describe('validateSuffix', () => {
  it('accepts a typical suffix', () => {
    expect(validateSuffix('amazon')).toBeNull();
    expect(validateSuffix('shop-aldi')).toBeNull();
    expect(validateSuffix('test+more')).toBeNull(); // server allows nested '+'
  });

  it('rejects empty (after normalisation)', () => {
    expect(validateSuffix('')).toBe(
      'settings.taggedAddresses.error.suffixEmpty',
    );
    expect(validateSuffix('   ')).toBe(
      'settings.taggedAddresses.error.suffixEmpty',
    );
  });

  it('rejects suffixes longer than 64 bytes', () => {
    expect(validateSuffix('a'.repeat(64))).toBeNull();
    expect(validateSuffix('a'.repeat(65))).toBe(
      'settings.taggedAddresses.error.suffixTooLong',
    );
    // Multi-byte UTF-8 must count bytes, not code units.
    // 'ä' is 2 bytes in UTF-8; 33 of them == 66 bytes > 64.
    expect(validateSuffix('ä'.repeat(33))).toBe(
      'settings.taggedAddresses.error.suffixTooLong',
    );
  });

  it('rejects whitespace inside the suffix', () => {
    expect(validateSuffix('a b')).toBe(
      'settings.taggedAddresses.error.suffixWhitespace',
    );
  });

  it('rejects @ in the suffix', () => {
    expect(validateSuffix('a@b')).toBe(
      'settings.taggedAddresses.error.suffixForbidden',
    );
  });

  it('rejects ASCII control bytes', () => {
    expect(validateSuffix('ab')).toBe(
      'settings.taggedAddresses.error.suffixForbidden',
    );
    expect(validateSuffix('ab')).toBe(
      'settings.taggedAddresses.error.suffixForbidden',
    );
  });

  it('lower-cases on input — mixed-case is accepted', () => {
    expect(validateSuffix('Amazon')).toBeNull();
    expect(validateSuffix('AMAZON')).toBeNull();
  });
});

describe('validateLabelName', () => {
  it('accepts a typical label', () => {
    expect(validateLabelName('Shopping')).toBeNull();
  });

  it('rejects empty / whitespace-only', () => {
    expect(validateLabelName('')).toBe(
      'settings.taggedAddresses.error.labelEmpty',
    );
    expect(validateLabelName('   ')).toBe(
      'settings.taggedAddresses.error.labelEmpty',
    );
  });

  it('rejects labels longer than 255 bytes', () => {
    expect(validateLabelName('a'.repeat(255))).toBeNull();
    expect(validateLabelName('a'.repeat(256))).toBe(
      'settings.taggedAddresses.error.labelTooLong',
    );
  });
});

describe('actionLabelKey', () => {
  it('is exhaustive over FILTER_ACTIONS', () => {
    // The switch is exhaustive at the type level; this test catches
    // a future addition that updates the type but forgets to extend
    // the helper. We assert that every action in the enumeration
    // produces a defined key.
    for (const action of FILTER_ACTIONS) {
      const key = actionLabelKey(action);
      expect(key.startsWith('settings.taggedAddresses.action.')).toBe(true);
    }
  });

  it('maps each action to a distinct key', () => {
    expect(actionLabelKey('label')).toBe('settings.taggedAddresses.action.label');
    expect(actionLabelKey('label_archive')).toBe(
      'settings.taggedAddresses.action.labelArchive',
    );
    expect(actionLabelKey('label_archive_read')).toBe(
      'settings.taggedAddresses.action.labelArchiveRead',
    );
  });
});

describe('composeTaggedAddress', () => {
  it('inserts the suffix between local-part and domain', () => {
    expect(composeTaggedAddress('alice@example.local', 'amazon')).toBe(
      'alice+amazon@example.local',
    );
  });

  it('falls back to +suffix when the base email is missing', () => {
    expect(composeTaggedAddress(null, 'amazon')).toBe('+amazon');
    expect(composeTaggedAddress('', 'amazon')).toBe('+amazon');
    expect(composeTaggedAddress(undefined, 'amazon')).toBe('+amazon');
  });

  it('falls back to +suffix when the base email is malformed', () => {
    expect(composeTaggedAddress('no-at-sign', 'amazon')).toBe('+amazon');
    expect(composeTaggedAddress('@example.local', 'amazon')).toBe('+amazon');
  });
});

describe('sortFilters', () => {
  const emails: Record<string, string> = {
    A: 'alice@example.local',
    B: 'bob@example.local',
    C: 'carol@example.local',
  };

  const emailFor = (id: string): string | null => emails[id] ?? null;

  it('sorts by identity email then by suffix', () => {
    const filters = [
      { id: '1', baseIdentityId: 'C', suffix: 'zebra' },
      { id: '2', baseIdentityId: 'A', suffix: 'beta' },
      { id: '3', baseIdentityId: 'A', suffix: 'alpha' },
      { id: '4', baseIdentityId: 'B', suffix: 'gamma' },
    ];
    const sorted = sortFilters(filters, emailFor);
    expect(sorted.map((f) => f.id)).toEqual(['3', '2', '4', '1']);
  });

  it('is stable when emails are missing — sort by suffix', () => {
    const filters = [
      { id: '1', baseIdentityId: 'X', suffix: 'zebra' },
      { id: '2', baseIdentityId: 'Y', suffix: 'alpha' },
    ];
    const sorted = sortFilters(filters, () => null);
    expect(sorted.map((f) => f.id)).toEqual(['2', '1']);
  });
});

describe('sortDismissals', () => {
  it('mirrors the filters sort', () => {
    const emails: Record<string, string> = {
      A: 'alice@example.local',
      B: 'bob@example.local',
    };
    const emailFor = (id: string): string | null => emails[id] ?? null;
    const dismissals = [
      { baseIdentityId: 'B', suffix: 'gamma' },
      { baseIdentityId: 'A', suffix: 'beta' },
      { baseIdentityId: 'A', suffix: 'alpha' },
    ];
    const sorted = sortDismissals(dismissals, emailFor);
    expect(sorted.map((d) => `${d.baseIdentityId}/${d.suffix}`)).toEqual([
      'A/alpha',
      'A/beta',
      'B/gamma',
    ]);
  });
});
