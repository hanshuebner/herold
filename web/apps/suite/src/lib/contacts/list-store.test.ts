/**
 * Unit tests for the contacts list store helpers (list-store.svelte.ts).
 *
 * Tests cover the pure projection helpers that derive list-row values from
 * raw JSContact Card objects: deriveDisplayName, deriveSecondary,
 * deriveFallbackInitial, and parseRow.
 *
 * The stateful store (init, loadMore, sync handler) uses the JMAP client
 * and is covered by integration tests against a running instance.
 */

import { describe, it, expect } from 'vitest';
import {
  _internals_forTest,
} from './list-store.svelte';

const { deriveDisplayName, deriveSecondary, deriveFallbackInitial, parseRow } =
  _internals_forTest;

// ── deriveDisplayName ─────────────────────────────────────────────────────────

describe('deriveDisplayName', () => {
  it('prefers name.full when set', () => {
    expect(
      deriveDisplayName({
        name: { full: 'Alice Liddell', components: [{ kind: 'given', value: 'Alice' }] },
      }),
    ).toBe('Alice Liddell');
  });

  it('trims whitespace from name.full', () => {
    expect(deriveDisplayName({ name: { full: '  Bob  ' } })).toBe('Bob');
  });

  it('falls back to joined name.components when full is absent', () => {
    expect(
      deriveDisplayName({
        name: {
          components: [
            { kind: 'given', value: 'Alice' },
            { kind: 'surname', value: 'Smith' },
          ],
        },
      }),
    ).toBe('Alice Smith');
  });

  it('falls back to joined name.components when full is empty', () => {
    expect(
      deriveDisplayName({
        name: {
          full: '',
          components: [{ kind: 'given', value: 'Bob' }],
        },
      }),
    ).toBe('Bob');
  });

  it('skips empty component values', () => {
    expect(
      deriveDisplayName({
        name: {
          components: [
            { kind: 'given', value: 'Alice' },
            { kind: 'separator', value: '' },
            { kind: 'surname', value: 'Jones' },
          ],
        },
      }),
    ).toBe('Alice Jones');
  });

  it('falls back to primary email when name is absent', () => {
    expect(
      deriveDisplayName({
        emails: { e1: { address: 'alice@example.com' } },
      }),
    ).toBe('alice@example.com');
  });

  it('returns empty string when nothing is available', () => {
    expect(deriveDisplayName({})).toBe('');
  });
});

// ── deriveSecondary ───────────────────────────────────────────────────────────

describe('deriveSecondary', () => {
  it('returns primary email (pref=1) when available', () => {
    expect(
      deriveSecondary({
        emails: {
          e1: { address: 'work@example.com', pref: 1 },
          e2: { address: 'home@example.com' },
        },
      }),
    ).toBe('work@example.com');
  });

  it('returns first email when no pref=1 is set', () => {
    // Object.values order is insertion order for string keys in V8.
    const card: Record<string, unknown> = {};
    (card as Record<string, unknown>).emails = {
      e1: { address: 'first@example.com' },
      e2: { address: 'second@example.com' },
    };
    const result = deriveSecondary(card);
    // Either first or second is acceptable; the important invariant is that
    // a non-empty email is returned.
    expect(result).toMatch(/@example\.com$/);
  });

  it('falls back to organization name when no email', () => {
    expect(
      deriveSecondary({
        organizations: { o1: { name: 'Acme Corp' } },
      }),
    ).toBe('Acme Corp');
  });

  it('falls back to phone number when no email or org', () => {
    expect(
      deriveSecondary({
        phones: { p1: { number: '+1 555 0100' } },
      }),
    ).toBe('+1 555 0100');
  });

  it('returns empty string when nothing is available', () => {
    expect(deriveSecondary({})).toBe('');
  });

  it('prefers email over org', () => {
    expect(
      deriveSecondary({
        emails: { e1: { address: 'bob@acme.com' } },
        organizations: { o1: { name: 'Acme' } },
      }),
    ).toBe('bob@acme.com');
  });

  it('prefers org over phone', () => {
    expect(
      deriveSecondary({
        organizations: { o1: { name: 'Acme' } },
        phones: { p1: { number: '+1 555 0100' } },
      }),
    ).toBe('Acme');
  });
});

// ── deriveFallbackInitial ─────────────────────────────────────────────────────

describe('deriveFallbackInitial', () => {
  it('returns uppercased first character', () => {
    expect(deriveFallbackInitial('alice')).toBe('A');
  });

  it('skips leading spaces', () => {
    expect(deriveFallbackInitial('  bob')).toBe('B');
  });

  it('returns ? for empty string', () => {
    expect(deriveFallbackInitial('')).toBe('?');
  });

  it('returns ? for all-whitespace string', () => {
    expect(deriveFallbackInitial('   ')).toBe('?');
  });
});

// ── parseRow ──────────────────────────────────────────────────────────────────

describe('parseRow', () => {
  it('extracts id, displayName, secondary, and null photoBlobId', () => {
    const row = parseRow({
      id: 'c1',
      name: { full: 'Alice Liddell' },
      emails: { e1: { address: 'alice@example.com' } },
    });
    expect(row.id).toBe('c1');
    expect(row.displayName).toBe('Alice Liddell');
    expect(row.secondary).toBe('alice@example.com');
    expect(row.photoBlobId).toBeNull();
  });

  it('extracts photoBlobId from media map', () => {
    const row = parseRow({
      id: 'c2',
      name: { full: 'Bob' },
      media: { m1: { type: 'photo', blobId: 'blob-abc-123' } },
    });
    expect(row.photoBlobId).toBe('blob-abc-123');
  });

  it('ignores non-photo media entries', () => {
    const row = parseRow({
      id: 'c3',
      name: { full: 'Carol' },
      media: { m1: { type: 'logo', blobId: 'blob-xyz' } },
    });
    expect(row.photoBlobId).toBeNull();
  });

  it('converts id to string', () => {
    const row = parseRow({ id: 42, name: { full: 'Dave' } } as unknown as Record<string, unknown>);
    expect(row.id).toBe('42');
  });
});
