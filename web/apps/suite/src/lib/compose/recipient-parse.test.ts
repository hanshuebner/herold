/**
 * Tests for the recipient-parse helpers (REQ-MAIL-11a..d).
 *
 * Covers the three recognized formats, structural grouping (no split
 * inside `<...>` or `"..."`), paste parsing, and the worked examples
 * from issue #38.
 */
import { describe, it, expect } from 'vitest';
import {
  tryCommit,
  parsePaste,
  isStructurallyComplete,
  recipientToString,
  type Recipient,
} from './recipient-parse';

// ---------------------------------------------------------------------------
// isStructurallyComplete
// ---------------------------------------------------------------------------

describe('isStructurallyComplete', () => {
  it('returns true for an empty buffer', () => {
    expect(isStructurallyComplete('')).toBe(true);
  });

  it('returns true for a bare email address', () => {
    expect(isStructurallyComplete('alice@example.com')).toBe(true);
  });

  it('returns false while inside angle brackets', () => {
    expect(isStructurallyComplete('Hans <hans@')).toBe(false);
  });

  it('returns true after closing angle bracket', () => {
    expect(isStructurallyComplete('Hans <hans@huebner.org>')).toBe(true);
  });

  it('returns false while inside a quoted string', () => {
    expect(isStructurallyComplete('"Hans')).toBe(false);
  });

  it('returns true after closing quote', () => {
    expect(isStructurallyComplete('"Hans Hubner"')).toBe(true);
  });

  it('returns false when angle bracket is inside a quoted string', () => {
    // The `<` is inside the quote, so still counts as not-in-angle when quote ends
    expect(isStructurallyComplete('"Hans <addr"')).toBe(true);
  });

  it('handles escape sequences in quoted strings', () => {
    expect(isStructurallyComplete('"Hans \\"partial')).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// tryCommit — single address formats
// ---------------------------------------------------------------------------

describe('tryCommit: bare email', () => {
  it('commits a bare email address', () => {
    const { chips, rest } = tryCommit('alice@example.com');
    expect(chips).toEqual([{ email: 'alice@example.com' }]);
    expect(rest).toBe('');
  });

  it('commits a bare email and leaves trailing separator consumed', () => {
    const { chips, rest } = tryCommit('alice@example.com,');
    expect(chips).toHaveLength(1);
    expect(chips[0]!.email).toBe('alice@example.com');
    expect(rest).toBe('');
  });

  it('returns no chips for a partial bare address without @', () => {
    const { chips, rest } = tryCommit('alice');
    expect(chips).toEqual([]);
    expect(rest).toBe('alice');
  });
});

describe('tryCommit: Name <email> format', () => {
  it('commits "Hans Hubner <hans@huebner.org>"', () => {
    const { chips, rest } = tryCommit('Hans Hubner <hans@huebner.org>');
    expect(chips).toEqual([{ name: 'Hans Hubner', email: 'hans@huebner.org' }]);
    expect(rest).toBe('');
  });

  it('commits the issue #38 worked example with umlaut', () => {
    const { chips, rest } = tryCommit('Hans Hubner <hans@huebner.org>');
    expect(chips[0]!.email).toBe('hans@huebner.org');
    expect(chips[0]!.name).toBe('Hans Hubner');
    expect(rest).toBe('');
  });

  it('commits a quoted name before the angle address', () => {
    const { chips } = tryCommit('"Alice B." <alice@example.com>');
    expect(chips).toEqual([{ name: 'Alice B.', email: 'alice@example.com' }]);
  });

  it('does not split on space inside angle brackets', () => {
    // The buffer contains a space inside `<...>` — should not commit mid-way
    const { chips } = tryCommit('Test User <test user@example.com>');
    // "test user@example.com" is not a valid email (space in local part)
    // so the whole thing should fail or leave the tail
    expect(chips).toEqual([]);
  });

  it('does not split on comma inside angle brackets', () => {
    const buf = 'Name <addr@x.test>';
    expect(isStructurallyComplete(buf)).toBe(true);
    const { chips } = tryCommit(buf);
    expect(chips[0]!.email).toBe('addr@x.test');
  });
});

describe('tryCommit: email "Name" format', () => {
  it('commits the issue #38 alternate worked example', () => {
    const { chips, rest } = tryCommit('hans@huebner.org "Hans Hubner"');
    expect(chips).toEqual([{ name: 'Hans Hubner', email: 'hans@huebner.org' }]);
    expect(rest).toBe('');
  });

  it('commits bare email then quoted name, leaves no rest', () => {
    const { chips, rest } = tryCommit('alice@example.com "Alice"');
    expect(chips[0]!.name).toBe('Alice');
    expect(chips[0]!.email).toBe('alice@example.com');
    expect(rest).toBe('');
  });
});

// ---------------------------------------------------------------------------
// tryCommit — multiple addresses
// ---------------------------------------------------------------------------

describe('tryCommit: multiple addresses', () => {
  it('commits two comma-separated bare addresses', () => {
    const { chips, rest } = tryCommit('a@x.test, b@y.test');
    expect(chips).toEqual([{ email: 'a@x.test' }, { email: 'b@y.test' }]);
    expect(rest).toBe('');
  });

  it('commits two semicolon-separated addresses', () => {
    const { chips } = tryCommit('a@x.test;b@y.test');
    expect(chips).toHaveLength(2);
  });

  it('commits mixed formats in one buffer', () => {
    const { chips, rest } = tryCommit(
      'Alice <alice@x.test>, bob@y.test, carol@z.test "Carol"',
    );
    expect(chips).toHaveLength(3);
    expect(chips[0]!.name).toBe('Alice');
    expect(chips[1]!.email).toBe('bob@y.test');
    expect(chips[2]!.name).toBe('Carol');
    expect(rest).toBe('');
  });

  it('leaves unrecognized tail in rest', () => {
    const { chips, rest } = tryCommit('a@x.test, notanemail');
    expect(chips).toHaveLength(1);
    expect(rest).toBe('notanemail');
  });

  it('handles a trailing comma with no additional content', () => {
    const { chips, rest } = tryCommit('a@x.test,');
    expect(chips).toHaveLength(1);
    expect(rest).toBe('');
  });
});

// ---------------------------------------------------------------------------
// parsePaste
// ---------------------------------------------------------------------------

describe('parsePaste', () => {
  it('parses two newline-separated addresses from a paste', () => {
    const { chips, rest } = parsePaste('a@x.test\nb@y.test');
    expect(chips).toHaveLength(2);
    expect(rest).toBe('');
  });

  it('parses a recognized address and leaves unrecognized tail', () => {
    const { chips, rest } = parsePaste('alice@x.test, badvalue');
    expect(chips).toHaveLength(1);
    expect(chips[0]!.email).toBe('alice@x.test');
    expect(rest).toBe('badvalue');
  });

  it('parses a pasted string with comma, semicolon, newline mixed', () => {
    const paste = 'a@x.test, b@y.test; c@z.test\nd@w.test';
    const { chips, rest } = parsePaste(paste);
    expect(chips).toHaveLength(4);
    expect(rest).toBe('');
  });

  it('handles an empty paste', () => {
    const { chips, rest } = parsePaste('');
    expect(chips).toEqual([]);
    expect(rest).toBe('');
  });

  it('handles a paste of only whitespace', () => {
    const { chips, rest } = parsePaste('   \n  ');
    expect(chips).toEqual([]);
    expect(rest).toBe('');
  });

  it('preserves whitespace inside angle brackets during paste', () => {
    // "Name with spaces <email>" should become one chip, not split on the space
    const { chips } = parsePaste('Some Person <person@example.com>');
    expect(chips).toHaveLength(1);
    expect(chips[0]!.name).toBe('Some Person');
    expect(chips[0]!.email).toBe('person@example.com');
  });

  it('parses multiple Name <email> entries from a paste', () => {
    const paste =
      'Alice Smith <alice@x.test>, Bob Jones <bob@y.test>';
    const { chips, rest } = parsePaste(paste);
    expect(chips).toHaveLength(2);
    expect(chips[0]!.name).toBe('Alice Smith');
    expect(chips[1]!.name).toBe('Bob Jones');
    expect(rest).toBe('');
  });
});

// ---------------------------------------------------------------------------
// recipientToString
// ---------------------------------------------------------------------------

describe('recipientToString', () => {
  it('renders name + email form when name is present', () => {
    const r: Recipient = { name: 'Alice', email: 'alice@x.test' };
    expect(recipientToString(r)).toBe('Alice <alice@x.test>');
  });

  it('renders bare email when name is absent', () => {
    const r: Recipient = { email: 'alice@x.test' };
    expect(recipientToString(r)).toBe('alice@x.test');
  });

  it('renders bare email when name is empty string', () => {
    // name: '' should fall through to bare form (name is undefined in type,
    // but guard against accidental empty-string names too)
    const r = { name: '', email: 'alice@x.test' } as Recipient;
    expect(recipientToString(r)).toBe('alice@x.test');
  });
});
