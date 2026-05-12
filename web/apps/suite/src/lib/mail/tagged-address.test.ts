/**
 * Unit tests for the pure helpers behind the per-message tagged-address
 * banner (REQ-MAIL-12c).
 *
 * The helpers run without any DOM or store dependency so the visibility
 * decision can be reasoned about in isolation. The component-side test
 * (TaggedAddressBanner.test.ts) exercises the wiring against the
 * @testing-library/svelte renderer.
 */

import { describe, it, expect } from 'vitest';
import {
  bannerVisibility,
  capitalise,
  extractSuffix,
  findBaseIdentity,
  type DismissalRecord,
  type FilterRecord,
} from './tagged-address';
import type { Identity } from './types';

function makeIdentity(id: string, email: string): Identity {
  return {
    id,
    name: email,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

describe('extractSuffix', () => {
  it('returns null for an empty header', () => {
    expect(extractSuffix(null)).toBeNull();
    expect(extractSuffix(undefined)).toBeNull();
    expect(extractSuffix('')).toBeNull();
    expect(extractSuffix('  ')).toBeNull();
  });

  it('returns null when the local part has no plus', () => {
    expect(extractSuffix('alice@example.local')).toBeNull();
  });

  it('extracts a simple suffix', () => {
    expect(extractSuffix('alice+amazon@example.local')).toEqual({
      baseAddress: 'alice@example.local',
      suffix: 'amazon',
    });
  });

  it('lower-cases both the base address and the suffix (REQ-TAG-02)', () => {
    expect(extractSuffix('Alice+AMAZON@Example.LOCAL')).toEqual({
      baseAddress: 'alice@example.local',
      suffix: 'amazon',
    });
  });

  it('keeps additional plus characters inside the suffix verbatim', () => {
    // REQ-TAG-01: "Suffix bytes after the first + are taken verbatim".
    expect(extractSuffix('alice+shop+amazon@example.local')).toEqual({
      baseAddress: 'alice@example.local',
      suffix: 'shop+amazon',
    });
  });

  it('returns null for a trailing-plus edge case (REQ-TAG-03)', () => {
    expect(extractSuffix('alice+@example.local')).toBeNull();
  });

  it('returns null for a leading-plus local part (empty base)', () => {
    expect(extractSuffix('+tag@example.local')).toBeNull();
  });

  it('returns null when the @ is the last byte (no domain)', () => {
    expect(extractSuffix('alice+tag@')).toBeNull();
  });

  it('returns null when there is no @ at all', () => {
    expect(extractSuffix('alice+tag')).toBeNull();
  });
});

describe('findBaseIdentity', () => {
  const ids = [
    makeIdentity('id-1', 'alice@example.local'),
    makeIdentity('id-2', 'bob@example.local'),
  ];

  it('returns the matching identity case-insensitively', () => {
    expect(findBaseIdentity(ids, 'alice@example.local')?.id).toBe('id-1');
    expect(findBaseIdentity(ids, 'ALICE@example.local')?.id).toBe('id-1');
  });

  it('returns null when no identity matches', () => {
    expect(findBaseIdentity(ids, 'eve@example.local')).toBeNull();
  });

  it('returns null when the identity list is empty', () => {
    expect(findBaseIdentity([], 'alice@example.local')).toBeNull();
  });
});

describe('bannerVisibility', () => {
  const identities = [makeIdentity('id-1', 'alice@example.local')];

  it('hides when the capability is not advertised (REQ-MAIL-12c gate b)', () => {
    expect(
      bannerVisibility({
        capabilityAdvertised: false,
        headerValue: 'alice+amazon@example.local',
        identities,
        filters: [],
        dismissals: [],
      }),
    ).toBeNull();
  });

  it('hides when X-Herold-Recipient is absent (REQ-MAIL-12c gate a)', () => {
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: null,
        identities,
        filters: [],
        dismissals: [],
      }),
    ).toBeNull();
  });

  it('hides when X-Herold-Recipient carries no suffix (REQ-MAIL-12c gate a)', () => {
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'alice@example.local',
        identities,
        filters: [],
        dismissals: [],
      }),
    ).toBeNull();
  });

  it('hides when no identity owns the base address', () => {
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'unknown+tag@example.local',
        identities,
        filters: [],
        dismissals: [],
      }),
    ).toBeNull();
  });

  it('hides when a matching filter exists (REQ-MAIL-12c gate c)', () => {
    const filters: FilterRecord[] = [
      { id: 'f-1', baseIdentityId: 'id-1', suffix: 'amazon' },
    ];
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'alice+amazon@example.local',
        identities,
        filters,
        dismissals: [],
      }),
    ).toBeNull();
  });

  it('hides when a matching dismissal exists (REQ-MAIL-12c gate c)', () => {
    const dismissals: DismissalRecord[] = [
      { baseIdentityId: 'id-1', suffix: 'amazon' },
    ];
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'alice+amazon@example.local',
        identities,
        filters: [],
        dismissals,
      }),
    ).toBeNull();
  });

  it('matches the dismissal case-insensitively', () => {
    const dismissals: DismissalRecord[] = [
      { baseIdentityId: 'id-1', suffix: 'AMAZON' },
    ];
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'alice+amazon@example.local',
        identities,
        filters: [],
        dismissals,
      }),
    ).toBeNull();
  });

  it('shows the banner with the canonical (baseIdentity, suffix) pair when all gates pass', () => {
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'Alice+AMAZON@Example.LOCAL',
        identities,
        filters: [],
        dismissals: [],
      }),
    ).toEqual({
      baseIdentityId: 'id-1',
      baseAddress: 'alice@example.local',
      suffix: 'amazon',
    });
  });

  it('does not match across base identities (filter on identity-2 must not hide identity-1)', () => {
    const filters: FilterRecord[] = [
      { id: 'f-1', baseIdentityId: 'id-2', suffix: 'amazon' },
    ];
    expect(
      bannerVisibility({
        capabilityAdvertised: true,
        headerValue: 'alice+amazon@example.local',
        identities: [
          makeIdentity('id-1', 'alice@example.local'),
          makeIdentity('id-2', 'bob@example.local'),
        ],
        filters,
        dismissals: [],
      })?.baseIdentityId,
    ).toBe('id-1');
  });
});

describe('capitalise', () => {
  it('upper-cases the first character', () => {
    expect(capitalise('amazon')).toBe('Amazon');
  });

  it('leaves dashes and the rest of the string untouched', () => {
    expect(capitalise('aws-prod')).toBe('Aws-prod');
  });

  it('returns the empty string for empty input', () => {
    expect(capitalise('')).toBe('');
  });

  it('leaves an already-capitalised string alone', () => {
    expect(capitalise('Amazon')).toBe('Amazon');
  });
});
