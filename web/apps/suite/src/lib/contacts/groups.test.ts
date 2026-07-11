/**
 * Unit tests for group membership patch helpers (groups.svelte.ts).
 *
 * Confirms wire-format behaviour documented in the round-trip test
 * (2026-07-11): members is Record<string, boolean> keyed by JMAP contact ID.
 */

import { describe, it, expect } from 'vitest';
import { _internals_forTest } from './groups.svelte';

const { buildAddMemberPatch, buildRemoveMemberPatch } = _internals_forTest;

// ── buildAddMemberPatch ───────────────────────────────────────────────────────

describe('buildAddMemberPatch', () => {
  it('adds a new member to an empty map', () => {
    expect(buildAddMemberPatch({}, 'c1')).toEqual({ c1: true });
  });

  it('adds a new member to an existing map', () => {
    expect(buildAddMemberPatch({ c1: true, c2: true }, 'c3')).toEqual({
      c1: true,
      c2: true,
      c3: true,
    });
  });

  it('is idempotent when the member is already present', () => {
    const result = buildAddMemberPatch({ c1: true }, 'c1');
    expect(result).toEqual({ c1: true });
  });

  it('does not mutate the original map', () => {
    const original = { c1: true };
    buildAddMemberPatch(original, 'c2');
    expect(original).toEqual({ c1: true });
  });
});

// ── buildRemoveMemberPatch ────────────────────────────────────────────────────

describe('buildRemoveMemberPatch', () => {
  it('removes the only member, returning an empty map', () => {
    expect(buildRemoveMemberPatch({ c1: true }, 'c1')).toEqual({});
  });

  it('removes one member from a multi-member map', () => {
    expect(buildRemoveMemberPatch({ c1: true, c2: true, c3: true }, 'c2')).toEqual({
      c1: true,
      c3: true,
    });
  });

  it('is a no-op when the member is not present', () => {
    expect(buildRemoveMemberPatch({ c1: true }, 'c99')).toEqual({ c1: true });
  });

  it('does not mutate the original map', () => {
    const original = { c1: true, c2: true };
    buildRemoveMemberPatch(original, 'c1');
    expect(original).toEqual({ c1: true, c2: true });
  });

  it('handles an empty input map', () => {
    expect(buildRemoveMemberPatch({}, 'c1')).toEqual({});
  });
});

// ── Scope filter logic (inline: group filtering uses groupIds set) ─────────────

describe('group scope filter', () => {
  it('filters group card IDs out of normal-view rows', () => {
    const groupIds = new Set(['g1', 'g2']);
    const rows = [
      { id: 'c1', displayName: 'Alice', secondary: '', photoBlobId: null },
      { id: 'g1', displayName: 'Friends', secondary: '', photoBlobId: null },
      { id: 'c2', displayName: 'Bob', secondary: '', photoBlobId: null },
      { id: 'g2', displayName: 'Work', secondary: '', photoBlobId: null },
    ];
    const visible = rows.filter((r) => !groupIds.has(r.id));
    expect(visible.map((r) => r.id)).toEqual(['c1', 'c2']);
  });

  it('shows all rows in group mode (no group filtering)', () => {
    const groupIds = new Set(['g1']);
    // In group mode activeGroupId !== null, so no filtering applies.
    const activeGroupId = 'g1';
    const memberRows = [
      { id: 'c1', displayName: 'Alice', secondary: '', photoBlobId: null },
      { id: 'c2', displayName: 'Bob', secondary: '', photoBlobId: null },
    ];
    const visible =
      activeGroupId !== null
        ? memberRows
        : memberRows.filter((r) => !groupIds.has(r.id));
    expect(visible.map((r) => r.id)).toEqual(['c1', 'c2']);
  });
});
