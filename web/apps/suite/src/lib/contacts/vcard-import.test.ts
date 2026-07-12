/**
 * Unit tests for vcard-import.ts — import/export request building and
 * import-summary categorisation.
 *
 * REQ-CONT-80..83 acceptance surface.
 */

import { describe, it, expect } from 'vitest';
import {
  buildImportArgs,
  buildExportArgs,
  parseImportSummary,
  type ImportCardResult,
  type ImportFieldDiff,
} from './vcard-import';

// ── buildImportArgs ────────────────────────────────────────────────────────

describe('buildImportArgs', () => {
  it('produces minimal args without addressBookId', () => {
    const args = buildImportArgs('account1', 'blob123');
    expect(args).toEqual({ accountId: 'account1', blobId: 'blob123' });
    expect(Object.prototype.hasOwnProperty.call(args, 'addressBookId')).toBe(
      false,
    );
  });

  it('includes addressBookId when supplied', () => {
    const args = buildImportArgs('account1', 'blob123', 'book42');
    expect(args).toEqual({
      accountId: 'account1',
      blobId: 'blob123',
      addressBookId: 'book42',
    });
  });

  it('does not include addressBookId when empty string', () => {
    // Empty string is falsy; should not include the key.
    const args = buildImportArgs('account1', 'blob123', '');
    expect(Object.prototype.hasOwnProperty.call(args, 'addressBookId')).toBe(
      false,
    );
  });
});

// ── buildExportArgs ────────────────────────────────────────────────────────

describe('buildExportArgs', () => {
  it('produces minimal args with no options', () => {
    const args = buildExportArgs('account1');
    expect(args).toEqual({ accountId: 'account1' });
  });

  it('includes ids when supplied', () => {
    const args = buildExportArgs('account1', { ids: ['id1', 'id2'] });
    expect(args).toEqual({ accountId: 'account1', ids: ['id1', 'id2'] });
  });

  it('includes addressBookId when supplied', () => {
    const args = buildExportArgs('account1', { addressBookId: 'book7' });
    expect(args).toEqual({ accountId: 'account1', addressBookId: 'book7' });
  });

  it('includes fetchPhotos when supplied', () => {
    const args = buildExportArgs('account1', { fetchPhotos: true });
    expect(args).toEqual({ accountId: 'account1', fetchPhotos: true });
  });

  it('includes all options together', () => {
    const args = buildExportArgs('account1', {
      ids: ['id1'],
      addressBookId: 'book7',
      fetchPhotos: false,
    });
    expect(args).toEqual({
      accountId: 'account1',
      ids: ['id1'],
      addressBookId: 'book7',
      fetchPhotos: false,
    });
  });

  it('does not include undefined options as keys', () => {
    const args = buildExportArgs('account1', {});
    expect(Object.prototype.hasOwnProperty.call(args, 'ids')).toBe(false);
    expect(Object.prototype.hasOwnProperty.call(args, 'addressBookId')).toBe(
      false,
    );
    expect(Object.prototype.hasOwnProperty.call(args, 'fetchPhotos')).toBe(
      false,
    );
  });
});

// ── parseImportSummary ─────────────────────────────────────────────────────

describe('parseImportSummary', () => {
  it('returns empty lists for empty results', () => {
    const s = parseImportSummary([]);
    expect(s.created).toHaveLength(0);
    expect(s.skipped).toHaveLength(0);
    expect(s.conflicts).toHaveLength(0);
    expect(s.failed).toHaveLength(0);
    expect(s.withDuplicates).toHaveLength(0);
  });

  const allCreated: ImportCardResult[] = [
    { index: 0, result: 'created', id: 'c1' },
    { index: 1, result: 'created', id: 'c2' },
  ];

  it('classifies all-created results', () => {
    const s = parseImportSummary(allCreated);
    expect(s.created).toHaveLength(2);
    expect(s.failed).toHaveLength(0);
    expect(s.withDuplicates).toHaveLength(0);
  });

  it('classifies failed results with reasons', () => {
    const results: ImportCardResult[] = [
      { index: 0, result: 'created', id: 'c1' },
      { index: 1, result: 'failed', reason: 'invalid vCard: missing FN' },
      { index: 2, result: 'failed', reason: 'parse error at line 7' },
    ];
    const s = parseImportSummary(results);
    expect(s.created).toHaveLength(1);
    expect(s.failed).toHaveLength(2);
    expect(s.failed[0]!.reason).toBe('invalid vCard: missing FN');
    expect(s.failed[1]!.reason).toBe('parse error at line 7');
  });

  it('classifies cards with duplicateCandidates into withDuplicates', () => {
    const results: ImportCardResult[] = [
      { index: 0, result: 'created', id: 'c1', duplicateCandidates: ['old1'] },
      { index: 1, result: 'created', id: 'c2' },
      { index: 2, result: 'failed', reason: 'bad', duplicateCandidates: ['old2', 'old3'] },
    ];
    const s = parseImportSummary(results);
    expect(s.created).toHaveLength(2);
    expect(s.failed).toHaveLength(1);
    expect(s.withDuplicates).toHaveLength(2);
    expect(s.withDuplicates[0]!.index).toBe(0);
    expect(s.withDuplicates[1]!.index).toBe(2);
  });

  it('treats empty duplicateCandidates array as no duplicates', () => {
    const results: ImportCardResult[] = [
      { index: 0, result: 'created', id: 'c1', duplicateCandidates: [] },
    ];
    const s = parseImportSummary(results);
    expect(s.withDuplicates).toHaveLength(0);
  });

  it('does not double-count a created card with duplicates', () => {
    // A card in both created and withDuplicates is correct — they are separate lists.
    const results: ImportCardResult[] = [
      { index: 0, result: 'created', id: 'c1', duplicateCandidates: ['old1'] },
    ];
    const s = parseImportSummary(results);
    expect(s.created).toHaveLength(1);
    expect(s.withDuplicates).toHaveLength(1);
    // Same object reference in both lists.
    expect(s.created[0]).toBe(s.withDuplicates[0]);
  });

  it('classifies skipped results by matchedId/matchedName (re #206)', () => {
    const results: ImportCardResult[] = [
      { index: 0, result: 'created', id: 'c1' },
      {
        index: 1,
        result: 'skipped',
        matchedId: 'c2',
        matchedName: 'Alice Existing',
      },
    ];
    const s = parseImportSummary(results);
    expect(s.created).toHaveLength(1);
    expect(s.skipped).toHaveLength(1);
    expect(s.skipped[0]!.matchedId).toBe('c2');
    expect(s.skipped[0]!.matchedName).toBe('Alice Existing');
    expect(s.conflicts).toHaveLength(0);
  });

  it('classifies conflict results with matchedName and diff (re #206)', () => {
    const diff: ImportFieldDiff[] = [
      { field: 'name', existing: '"Original"', incoming: '"Changed"' },
    ];
    const results: ImportCardResult[] = [
      {
        index: 0,
        result: 'conflict',
        matchedId: 'c1',
        matchedName: 'Original',
        diff,
      },
    ];
    const s = parseImportSummary(results);
    expect(s.conflicts).toHaveLength(1);
    expect(s.conflicts[0]!.matchedName).toBe('Original');
    expect(s.conflicts[0]!.diff).toEqual(diff);
    expect(s.created).toHaveLength(0);
    expect(s.skipped).toHaveLength(0);
  });

  it('preserves index ordering within each sub-list', () => {
    const results: ImportCardResult[] = [
      { index: 3, result: 'failed', reason: 'a' },
      { index: 1, result: 'created', id: 'c1' },
      { index: 2, result: 'created', id: 'c2' },
      { index: 0, result: 'failed', reason: 'b' },
    ];
    const s = parseImportSummary(results);
    // Order matches insertion order (filter preserves it).
    expect(s.failed.map((r) => r.index)).toEqual([3, 0]);
    expect(s.created.map((r) => r.index)).toEqual([1, 2]);
  });
});
