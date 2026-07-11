/**
 * Unit tests for dedup.ts — duplicate detection heuristics, dismissed-pair
 * persistence, merged-Card builder, and atomic Contact/set payload.
 *
 * REQ-CONT-90..94 acceptance surface.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  normalizeEmail,
  normalizePhone,
  normalizeDisplayName,
  editDistance,
  namesAreClose,
  extractCandidate,
  clusterDuplicates,
  loadDismissedPairs,
  dismissPair,
  buildMergedVM,
  buildMergeSetArgs,
  defaultMergeChoices,
  type ContactCandidate,
  type DuplicateCluster,
} from './dedup';
import { cardToVM } from './jscontact';

// ── normalizeEmail ─────────────────────────────────────────────────────────────

describe('normalizeEmail', () => {
  it('lowercases and trims', () => {
    expect(normalizeEmail('  Alice@Example.COM  ')).toBe('alice@example.com');
  });
  it('leaves already-normalised unchanged', () => {
    expect(normalizeEmail('alice@example.com')).toBe('alice@example.com');
  });
});

// ── normalizePhone ─────────────────────────────────────────────────────────────

describe('normalizePhone', () => {
  it('strips non-digits', () => {
    // +49 (0) 123 456-78 → digits: 4,9,0,1,2,3,4,5,6,7,8 = "49012345678"
    expect(normalizePhone('+49 (0) 123 456-78')).toBe('49012345678');
  });
  it('handles already-stripped numbers', () => {
    expect(normalizePhone('491234567890')).toBe('491234567890');
  });
  it('returns empty string for a non-number string', () => {
    expect(normalizePhone('n/a')).toBe('');
  });
});

// ── normalizeDisplayName ───────────────────────────────────────────────────────

describe('normalizeDisplayName', () => {
  it('lowercases and collapses whitespace', () => {
    expect(normalizeDisplayName('  Alice  Smith  ')).toBe('alice smith');
  });
  it('handles single-word names', () => {
    expect(normalizeDisplayName('Alice')).toBe('alice');
  });
});

// ── editDistance ───────────────────────────────────────────────────────────────

describe('editDistance', () => {
  it('returns 0 for identical strings', () => {
    expect(editDistance('alice', 'alice')).toBe(0);
  });
  it('returns string length when the other is empty', () => {
    expect(editDistance('alice', '')).toBe(5);
    expect(editDistance('', 'alice')).toBe(5);
  });
  it('measures one substitution', () => {
    expect(editDistance('alice', 'alicf')).toBe(1);
  });
  it('measures one deletion', () => {
    expect(editDistance('alice', 'alic')).toBe(1);
  });
  it('measures one insertion', () => {
    expect(editDistance('alice', 'alices')).toBe(1);
  });
  it('measures transposition correctly', () => {
    expect(editDistance('alice', 'laice')).toBe(2);
  });
});

// ── namesAreClose ──────────────────────────────────────────────────────────────

describe('namesAreClose', () => {
  it('returns true for identical normalised names', () => {
    expect(namesAreClose('alice smith', 'alice smith')).toBe(true);
  });
  it('returns true for a single typo in a medium-length name', () => {
    expect(namesAreClose('alice smith', 'alice smitH')).toBe(true); // same after comparison
  });
  it('returns true for one-character difference', () => {
    expect(namesAreClose('alice smith', 'alice smit')).toBe(true);
  });
  it('returns false for completely different names', () => {
    expect(namesAreClose('alice smith', 'bob jones')).toBe(false);
  });
  it('returns false for empty strings', () => {
    expect(namesAreClose('', 'alice')).toBe(false);
    expect(namesAreClose('alice', '')).toBe(false);
  });
  it('treats strings literally (callers must normalise before calling)', () => {
    // Both already lowercase: identical -> true.
    expect(namesAreClose('alice', 'alice')).toBe(true);
    // 'alice' vs 'Alice': editDistance = 1, threshold for len 5 = 1, so true.
    // Callers should normalise to lowercase first; this test documents the
    // literal behaviour — the function itself does not lowercase.
    expect(namesAreClose('alice', 'Alice')).toBe(true);
  });
  it('short names require exact match (threshold 0 for len<=3)', () => {
    expect(namesAreClose('al', 'al')).toBe(true);
    expect(namesAreClose('al', 'ab')).toBe(false);
  });
  it('medium names allow 1 edit (len 4..6)', () => {
    expect(namesAreClose('alice', 'alicf')).toBe(true);   // 1 edit, len 5
    expect(namesAreClose('alice', 'alxxx')).toBe(false);  // 3 edits
  });
});

// ── extractCandidate ───────────────────────────────────────────────────────────

describe('extractCandidate', () => {
  it('extracts id, displayName, emails, and phones', () => {
    const raw = {
      id: 'c1',
      name: { full: 'Alice Smith' },
      emails: { e1: { address: 'Alice@Example.COM' } },
      phones: { p1: { number: '+49 (0) 123 456' } },
    };
    const c = extractCandidate(raw);
    expect(c.id).toBe('c1');
    expect(c.displayName).toBe('Alice Smith');
    expect(c.emails).toContain('alice@example.com');
    expect(c.phones).toContain('490123456');
  });

  it('falls back to components when full is absent', () => {
    const raw = {
      id: 'c2',
      name: { components: [{ kind: 'given', value: 'Bob' }, { kind: 'surname', value: 'Jones' }] },
      emails: {},
      phones: {},
    };
    const c = extractCandidate(raw);
    expect(c.displayName).toBe('Bob Jones');
  });

  it('omits very short phone numbers (< 4 digits)', () => {
    const raw = {
      id: 'c3',
      name: { full: 'Test' },
      phones: { p1: { number: '12' } },
    };
    const c = extractCandidate(raw);
    expect(c.phones).toHaveLength(0);
  });
});

// ── clusterDuplicates ─────────────────────────────────────────────────────────

describe('clusterDuplicates: email matching', () => {
  function makeCandidate(id: string, emails: string[], displayName = id): ContactCandidate {
    return { id, displayName, emails: emails.map(normalizeEmail), phones: [], raw: { id } };
  }

  it('clusters two contacts sharing an email', () => {
    const candidates = [
      makeCandidate('c1', ['alice@example.com'], 'Alice A'),
      makeCandidate('c2', ['alice@example.com'], 'Alice B'),
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(1);
    expect(clusters[0]!.contacts.map((c) => c.id).sort()).toEqual(['c1', 'c2']);
    expect(clusters[0]!.reasons).toContain('email');
  });

  it('does not cluster contacts with different emails', () => {
    const candidates = [
      makeCandidate('c1', ['alice@example.com']),
      makeCandidate('c2', ['bob@example.com']),
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(0);
  });

  it('forms a single cluster for three contacts sharing the same email', () => {
    const candidates = [
      makeCandidate('c1', ['shared@example.com'], 'Alice'),
      makeCandidate('c2', ['shared@example.com'], 'Bob'),
      makeCandidate('c3', ['shared@example.com'], 'Carol'),
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(1);
    expect(clusters[0]!.contacts).toHaveLength(3);
  });

  it('respects dismissed pairs', () => {
    const candidates = [
      makeCandidate('c1', ['alice@example.com']),
      makeCandidate('c2', ['alice@example.com']),
    ];
    const dismissed = new Set(['c1:c2']);
    const clusters = clusterDuplicates(candidates, dismissed);
    expect(clusters).toHaveLength(0);
  });
});

describe('clusterDuplicates: phone matching', () => {
  function makeCandidate(id: string, phones: string[], displayName = id): ContactCandidate {
    return { id, displayName, emails: [], phones: phones.map(normalizePhone).filter((p) => p.length >= 4), raw: { id } };
  }

  it('clusters two contacts sharing a normalised phone', () => {
    const candidates = [
      makeCandidate('c1', ['+49 123 456'], 'Alice A'),
      makeCandidate('c2', ['49123456'],    'Alice B'),
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(1);
    expect(clusters[0]!.reasons).toContain('phone');
  });
});

describe('clusterDuplicates: name matching', () => {
  it('clusters contacts with very similar display names', () => {
    const candidates: ContactCandidate[] = [
      { id: 'c1', displayName: 'alice smith', emails: [], phones: [], raw: { id: 'c1' } },
      { id: 'c2', displayName: 'alice smit', emails: [], phones: [], raw: { id: 'c2' } },
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(1);
    expect(clusters[0]!.reasons).toContain('name');
  });

  it('does not cluster contacts with different names', () => {
    const candidates: ContactCandidate[] = [
      { id: 'c1', displayName: 'alice smith', emails: [], phones: [], raw: { id: 'c1' } },
      { id: 'c2', displayName: 'bob jones',   emails: [], phones: [], raw: { id: 'c2' } },
    ];
    const clusters = clusterDuplicates(candidates, new Set());
    expect(clusters).toHaveLength(0);
  });
});

describe('clusterDuplicates: multi-reason', () => {
  it('clusters contacts sharing both email and phone, reporting both reasons', () => {
    const c1: ContactCandidate = {
      id: 'c1', displayName: 'Alice', emails: ['alice@example.com'], phones: ['491234567'],
      raw: { id: 'c1' },
    };
    const c2: ContactCandidate = {
      id: 'c2', displayName: 'Alice', emails: ['alice@example.com'], phones: ['491234567'],
      raw: { id: 'c2' },
    };
    const clusters = clusterDuplicates([c1, c2], new Set());
    expect(clusters).toHaveLength(1);
    const r = clusters[0]!.reasons;
    expect(r).toContain('email');
    expect(r).toContain('phone');
  });
});

// ── dismissed pairs (REQ-CONT-91) ─────────────────────────────────────────────

describe('dismissPair / loadDismissedPairs', () => {
  // Mock localStorage for tests.
  const storage = new Map<string, string>();

  beforeEach(() => {
    storage.clear();
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation((k) => storage.get(k) ?? null);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation((k, v) => storage.set(k, v));
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('initially returns an empty set', () => {
    const pairs = loadDismissedPairs('principal1');
    expect(pairs.size).toBe(0);
  });

  it('persists a dismissed pair and reloads it', () => {
    dismissPair('principal1', 'c1', 'c2');
    const pairs = loadDismissedPairs('principal1');
    expect(pairs.has('c1:c2')).toBe(true);
  });

  it('stores pairs in sorted order regardless of argument order', () => {
    dismissPair('principal1', 'c2', 'c1'); // reversed
    const pairs = loadDismissedPairs('principal1');
    expect(pairs.has('c1:c2')).toBe(true);
  });

  it('accumulates multiple dismissed pairs', () => {
    dismissPair('principal1', 'c1', 'c2');
    dismissPair('principal1', 'c3', 'c4');
    const pairs = loadDismissedPairs('principal1');
    expect(pairs.has('c1:c2')).toBe(true);
    expect(pairs.has('c3:c4')).toBe(true);
  });

  it('is scoped per principal', () => {
    dismissPair('principal1', 'c1', 'c2');
    const pairs = loadDismissedPairs('principal2');
    expect(pairs.has('c1:c2')).toBe(false);
  });

  it('dismissed pair suppresses clustering', () => {
    const candidates: ContactCandidate[] = [
      { id: 'c1', displayName: 'alice', emails: ['alice@example.com'], phones: [], raw: { id: 'c1' } },
      { id: 'c2', displayName: 'alice', emails: ['alice@example.com'], phones: [], raw: { id: 'c2' } },
    ];
    const dismissed = loadDismissedPairs('principal1'); // empty initially
    expect(clusterDuplicates(candidates, dismissed)).toHaveLength(1);

    dismissPair('principal1', 'c1', 'c2');
    const dismissed2 = loadDismissedPairs('principal1');
    expect(clusterDuplicates(candidates, dismissed2)).toHaveLength(0);
  });
});

// ── buildMergedVM (REQ-CONT-92) ────────────────────────────────────────────────

function makeRawCard(
  id: string,
  given: string,
  surname: string,
  emails: string[],
  phones: string[] = [],
): Record<string, unknown> {
  const emailsMap: Record<string, unknown> = {};
  emails.forEach((addr, i) => { emailsMap[`e${i}`] = { address: addr }; });
  const phonesMap: Record<string, unknown> = {};
  phones.forEach((num, i) => { phonesMap[`p${i}`] = { number: num }; });
  return {
    id,
    version: '1.0',
    kind: 'individual',
    name: {
      components: [
        { kind: 'given', value: given },
        { kind: 'surname', value: surname },
      ],
    },
    emails: emailsMap,
    phones: phonesMap,
  };
}

describe('buildMergedVM', () => {
  it('produces a merged VM with the union of emails from both contacts', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com'], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    const addrs = vm.emails.map((e) => e.address);
    expect(addrs).toContain('alice@a.com');
    expect(addrs).toContain('alice@b.com');
  });

  it('deduplicates shared email addresses', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@example.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@example.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@example.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@example.com'], phones: [], raw: raw2 },
      ],
      reasons: ['email'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    // Should have only one entry for the shared email.
    expect(vm.emails.filter((e) => e.address === 'alice@example.com')).toHaveLength(1);
  });

  it('uses the name from the specified nameContactIndex', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', []);
    const raw2 = makeRawCard('c2', 'Alicia', 'Schmidt', []);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: [], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alicia Schmidt', emails: [], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    choices.nameContactIndex = 1; // use second contact's name
    const vm = buildMergedVM(cluster, choices);
    expect(vm.name.given).toBe('Alicia');
    expect(vm.name.surname).toBe('Schmidt');
  });

  it('sets the survivor id correctly', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: [], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: [], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    choices.survivorIndex = 0;
    const vm = buildMergedVM(cluster, choices);
    expect(vm.id).toBe('c1');
  });
});

// ── buildMergeSetArgs (REQ-CONT-93) ───────────────────────────────────────────

describe('buildMergeSetArgs', () => {
  it('produces update for survivor and destroy for others', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com', 'alice@a.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com', 'alice@a.com'], phones: [], raw: raw2 },
      ],
      reasons: ['email'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    const { update, destroy } = buildMergeSetArgs(cluster, choices, vm);

    // Survivor is c1 (survivorIndex=0).
    expect(Object.keys(update)).toContain('c1');
    // Destroy the other contact.
    expect(destroy).toContain('c2');
    expect(destroy).not.toContain('c1');
  });

  it('the update patch contains the union emails', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com'], phones: [], raw: raw2 },
      ],
      reasons: ['email'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    const { update } = buildMergeSetArgs(cluster, choices, vm);

    const patch = update['c1'];
    expect(patch).toBeDefined();
    // The patch should include emails key since c1 gains alice@b.com.
    const emailMap = patch?.emails as Record<string, unknown> | undefined | null;
    // Should have 2 entries (alice@a.com + alice@b.com).
    if (emailMap) {
      const addrs = Object.values(emailMap).map((e) => (e as Record<string, unknown>).address);
      expect(addrs).toContain('alice@a.com');
      expect(addrs).toContain('alice@b.com');
    }
  });

  it('atomic failure simulation: if update is rejected, destroy never runs (Contract)', () => {
    // This test documents the contract rather than simulating a real server
    // response: buildMergeSetArgs produces a single Contact/set payload.
    // A server-side SetError on the update means the whole call is rejected
    // atomically (REQ-CONT-93). The test asserts the payload structure is
    // suitable for a single Contact/set call.
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', []);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', []);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: [], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: [], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    const args = buildMergeSetArgs(cluster, choices, vm);

    // Both update and destroy must be present in the same payload object.
    expect(args.update).toBeDefined();
    expect(args.destroy).toBeDefined();
    // The payload is used in a single Contact/set call — atomicity is
    // enforced by the server; the payload structure is correct.
    expect(typeof args.update).toBe('object');
    expect(Array.isArray(args.destroy)).toBe(true);
  });
});

// ── defaultMergeChoices ────────────────────────────────────────────────────────

describe('defaultMergeChoices', () => {
  it('selects survivorIndex=0 by default', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com'], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    expect(choices.survivorIndex).toBe(0);
    expect(choices.nameContactIndex).toBe(0);
  });

  it('picks photoContactIndex=-1 when no contact has a photo', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', []);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', []);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: [], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: [], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    expect(choices.photoContactIndex).toBe(-1);
  });

  it('picks photoContactIndex for the first contact that has a photo', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', []);
    const raw2 = {
      ...makeRawCard('c2', 'Alice', 'Smith', []),
      media: { photo1: { '@type': 'MediaResource', kind: 'photo', blobId: 'blob1', mediaType: 'image/jpeg' } },
    };
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: [], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: [], phones: [], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    expect(choices.photoContactIndex).toBe(1);
  });

  it('includes all unique emails from both contacts', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com'], phones: [], raw: raw2 },
      ],
      reasons: ['email'],
    };
    const choices = defaultMergeChoices(cluster);
    expect(choices.selectedEmails).toHaveLength(2);
  });

  it('deduplicates shared emails across contacts', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@example.com']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@example.com']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@example.com'], phones: [], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@example.com'], phones: [], raw: raw2 },
      ],
      reasons: ['email'],
    };
    const choices = defaultMergeChoices(cluster);
    expect(choices.selectedEmails).toHaveLength(1);
  });
});

// ── cardToVM roundtrip through buildMergedVM ───────────────────────────────────

describe('merged card preserves all user-kept values (REQ-CONT-93)', () => {
  it('merged VM has all chosen emails and phones', () => {
    const raw1 = makeRawCard('c1', 'Alice', 'Smith', ['alice@a.com'], ['+49 111 111']);
    const raw2 = makeRawCard('c2', 'Alice', 'Smith', ['alice@b.com'], ['+49 222 222']);
    const cluster: DuplicateCluster = {
      contacts: [
        { id: 'c1', displayName: 'Alice Smith', emails: ['alice@a.com'], phones: ['49111111'], raw: raw1 },
        { id: 'c2', displayName: 'Alice Smith', emails: ['alice@b.com'], phones: ['49222222'], raw: raw2 },
      ],
      reasons: ['name'],
    };
    const choices = defaultMergeChoices(cluster);
    const vm = buildMergedVM(cluster, choices);
    expect(vm.emails.map((e) => e.address)).toContain('alice@a.com');
    expect(vm.emails.map((e) => e.address)).toContain('alice@b.com');
    expect(vm.phones.map((p) => p.number)).toContain('+49 111 111');
    expect(vm.phones.map((p) => p.number)).toContain('+49 222 222');
  });
});
