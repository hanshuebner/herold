/**
 * Duplicate detection and merge helpers for the contacts app.
 *
 * Detection (REQ-CONT-90):
 *   - Cluster contacts by shared email (case-insensitive exact match after
 *     normalisation).
 *   - Cluster by shared normalised phone (strip non-digits, compare) —
 *     but only when the number is shared by at most
 *     MAX_SHARED_PHONE_CONTACTS distinct contacts (see below).
 *   - Cluster by close display-name match (case/space-normalised plus a
 *     small edit-distance threshold).
 *
 * The candidate set is gathered cheaply via Contact/query text filter;
 * final clustering and deduplication are client-side only — no server
 * dedup method is needed (REQ-CONT-90).
 *
 * Generic phone number guard (re herold#223):
 *   A phone number carries no clustering signal once it is shared by more
 *   than MAX_SHARED_PHONE_CONTACTS distinct contacts — a company
 *   switchboard, a hotline, or a directory-assistance short code (e.g. the
 *   German "11833") is stored verbatim on many unrelated address-book
 *   entries, and global union-find would otherwise chain all of them into
 *   one "duplicate" cluster. Two contacts sharing one specific number is
 *   still the strongest possible signal that they are the same person
 *   recorded twice (the identical-number pair does not need a matching
 *   name — plenty of real duplicates have no name at all), so that case
 *   still clusters on phone alone. Once a third distinct contact turns up
 *   on the same number, the number is reclassified as generic and is
 *   dropped from phone-based clustering entirely: none of the contacts
 *   sharing it are unioned by that number, though they may still cluster
 *   via a matching email or a close/exact name (a name match survives
 *   independently of this guard).
 *
 *   This mirrors the server-side vCard re-import matcher
 *   (`findPhoneNameImportMatches` in
 *   internal/protojmap/contacts/vcard_transport.go, re herold#206), which
 *   never treats phone as a match key by itself and requires an exact name
 *   match alongside it. The two rules diverge only for the pair case: the
 *   import matcher's job is a fully automatic, unattended skip-on-reimport
 *   decision, so it always requires the extra name corroboration to avoid
 *   silently absorbing a card into the wrong contact. This client dedup
 *   view always puts a human in front of the result — an operator merges
 *   or dismisses each cluster explicitly — so a two-contact identical-
 *   number match can be surfaced for review without the name requirement,
 *   without ever reintroducing herold#223's many-way false-positive chain.
 *
 * Dismissed pairs (REQ-CONT-91):
 *   Remembered in localStorage per principal as sorted "id1:id2" tokens.
 *
 * Merge card builder (REQ-CONT-92, 93):
 *   buildMergedVM — union of selected multi-valued fields + chosen name/photo.
 *   buildMergeSetArgs — single Contact/set payload: update survivor + destroy
 *   others in one call.
 */

import type { JsCard } from './jscontact';
import { cardToVM, vmToPatch, nextKey, type ContactEditVM } from './jscontact';

// ── Types ─────────────────────────────────────────────────────────────────────

/** A contact as used by the dedup logic (id + extracted match fields). */
export interface ContactCandidate {
  id: string;
  displayName: string;
  emails: string[];     // normalised (lowercase trimmed)
  phones: string[];     // normalised (digits only)
  raw: Record<string, unknown>;
}

/** Reason(s) a cluster was formed. */
export type ClusterReason = 'email' | 'phone' | 'name';

/** A group of contacts detected as likely duplicates. */
export interface DuplicateCluster {
  contacts: ContactCandidate[];
  reasons: ClusterReason[];
}

/** User choices during the assisted merge. */
export interface MergeChoices {
  /** Index into cluster.contacts for the name to use. */
  nameContactIndex: number;
  /** Index into cluster.contacts for the photo to use, or -1 for no photo. */
  photoContactIndex: number;
  /** Flat list of (contactIndex, emailKey) pairs the user wants to keep. */
  selectedEmails: Array<{ contactIndex: number; key: string }>;
  /** Flat list of (contactIndex, phoneKey) pairs the user wants to keep. */
  selectedPhones: Array<{ contactIndex: number; key: string }>;
  /** Flat list of (contactIndex, addressKey) pairs the user wants to keep. */
  selectedAddresses: Array<{ contactIndex: number; key: string }>;
  /** Index into cluster.contacts that becomes the surviving card. */
  survivorIndex: number;
}

// ── Normalisation helpers ─────────────────────────────────────────────────────

/** Normalise an email address for comparison (lowercase trim). */
export function normalizeEmail(email: string): string {
  return email.toLowerCase().trim();
}

/** Normalise a phone number for comparison (digits only). */
export function normalizePhone(phone: string): string {
  return phone.replace(/\D/g, '');
}

/**
 * Normalise a display name for fuzzy comparison.
 * Lowercases, collapses whitespace, strips leading/trailing space.
 */
export function normalizeDisplayName(name: string): string {
  return name.toLowerCase().replace(/\s+/g, ' ').trim();
}

/**
 * Levenshtein edit distance between two strings.
 * Used for close-name detection with a small threshold.
 * O(m*n) — only called on reasonably short display names.
 */
export function editDistance(a: string, b: string): number {
  if (a === b) return 0;
  if (a.length === 0) return b.length;
  if (b.length === 0) return a.length;

  // Use two rows to save memory.
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  let curr = new Array<number>(b.length + 1);

  for (let i = 1; i <= a.length; i++) {
    curr[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      curr[j] = Math.min(
        (curr[j - 1] ?? 0) + 1,        // insertion
        (prev[j] ?? 0) + 1,             // deletion
        (prev[j - 1] ?? 0) + cost,      // substitution
      );
    }
    [prev, curr] = [curr, prev];
  }
  return prev[b.length] ?? 0;
}

/**
 * Maximum edit distance threshold for "close name" matching.
 * Shorter names use a proportionally tighter threshold.
 */
function nameThreshold(name: string): number {
  const len = name.length;
  if (len <= 3) return 0;   // short names must match exactly
  if (len <= 6) return 1;
  if (len <= 12) return 2;
  return 3;
}

/** True when two normalised display names are "close enough" to flag. */
export function namesAreClose(a: string, b: string): boolean {
  if (!a || !b) return false;
  if (a === b) return true;
  const threshold = Math.min(nameThreshold(a), nameThreshold(b));
  return editDistance(a, b) <= threshold;
}

// ── Candidate extraction ──────────────────────────────────────────────────────

/**
 * Extract a ContactCandidate from a raw JSContact Card.
 * The raw card must have at minimum an `id` property.
 */
export function extractCandidate(raw: Record<string, unknown>): ContactCandidate {
  const id = String(raw.id ?? '');

  // Display name: name.full, then joined components.
  let displayName = '';
  const nameObj = raw.name as Record<string, unknown> | undefined;
  if (nameObj) {
    if (typeof nameObj.full === 'string' && nameObj.full.trim()) {
      displayName = nameObj.full.trim();
    } else if (Array.isArray(nameObj.components)) {
      const parts: string[] = [];
      for (const c of nameObj.components) {
        if (typeof c === 'object' && c !== null) {
          const v = (c as Record<string, unknown>).value;
          if (typeof v === 'string' && v.trim()) parts.push(v.trim());
        }
      }
      displayName = parts.join(' ');
    }
  }

  // Emails: all addresses from the emails map, normalised.
  const emails: string[] = [];
  const emailMap = raw.emails as Record<string, unknown> | undefined;
  if (emailMap) {
    for (const v of Object.values(emailMap)) {
      if (typeof v === 'object' && v !== null) {
        const addr = (v as Record<string, unknown>).address;
        if (typeof addr === 'string' && addr.trim()) {
          emails.push(normalizeEmail(addr));
        }
      }
    }
  }

  // Phones: all numbers from the phones map, normalised.
  const phones: string[] = [];
  const phoneMap = raw.phones as Record<string, unknown> | undefined;
  if (phoneMap) {
    for (const v of Object.values(phoneMap)) {
      if (typeof v === 'object' && v !== null) {
        const num = (v as Record<string, unknown>).number;
        if (typeof num === 'string' && num.trim()) {
          const n = normalizePhone(num);
          if (n.length >= 4) phones.push(n); // skip very short "numbers"
        }
      }
    }
  }

  return { id, displayName, emails, phones, raw };
}

// ── Clustering ────────────────────────────────────────────────────────────────

/**
 * Maximum number of distinct contacts that may share one normalised phone
 * number before that number is treated as generic and excluded from
 * phone-based clustering (re herold#223). Two contacts sharing a number is
 * kept as a clustering signal — the strongest available evidence of an
 * accidental duplicate entry. Three or more distinct contacts sharing the
 * same number is far more likely a reused institutional number (switchboard,
 * hotline, directory-assistance short code) than three separate accidental
 * copies of one person, so at that point the number stops unioning anyone
 * on its own; genuine duplicates within such a group still cluster via a
 * matching email or name.
 */
export const MAX_SHARED_PHONE_CONTACTS = 2;

/**
 * Cluster a list of candidates into duplicate groups.
 *
 * Algorithm:
 * 1. Build adjacency pairs from shared emails, shared phones (excluding
 *    generic numbers shared by more than MAX_SHARED_PHONE_CONTACTS distinct
 *    contacts, re herold#223), and close names.
 * 2. Union-find merges overlapping pairs into clusters.
 * 3. Return clusters with 2+ members; filter out singletons.
 *
 * REQ-CONT-90.
 */
export function clusterDuplicates(
  candidates: ContactCandidate[],
  dismissedPairs: Set<string>,
): DuplicateCluster[] {
  if (candidates.length < 2) return [];

  // Union-Find data structure.
  const parent = new Map<string, string>();
  const rankMap = new Map<string, number>();
  const reasons = new Map<string, Set<ClusterReason>>();

  function find(id: string): string {
    if (!parent.has(id)) parent.set(id, id);
    let root = parent.get(id)!;
    while (root !== parent.get(root)) {
      const grandparent = parent.get(parent.get(root)!)!;
      parent.set(root, grandparent);
      root = grandparent;
    }
    return root;
  }

  function union(a: string, b: string, reason: ClusterReason): void {
    const ra = find(a);
    const rb = find(b);
    if (ra === rb) {
      // Already in same cluster — just record the reason.
      if (!reasons.has(ra)) reasons.set(ra, new Set());
      reasons.get(ra)!.add(reason);
      return;
    }
    const rankA = rankMap.get(ra) ?? 0;
    const rankB = rankMap.get(rb) ?? 0;
    let newRoot: string;
    if (rankA >= rankB) {
      parent.set(rb, ra);
      if (rankA === rankB) rankMap.set(ra, rankA + 1);
      newRoot = ra;
    } else {
      parent.set(ra, rb);
      newRoot = rb;
    }
    // Merge reasons.
    const rr = new Set<ClusterReason>();
    reasons.get(ra)?.forEach((r) => rr.add(r));
    reasons.get(rb)?.forEach((r) => rr.add(r));
    rr.add(reason);
    reasons.set(newRoot, rr);
  }

  function pairKey(id1: string, id2: string): string {
    return [id1, id2].sort().join(':');
  }

  function isDismissed(id1: string, id2: string): boolean {
    return dismissedPairs.has(pairKey(id1, id2));
  }

  // Build email -> candidates[] index.
  const emailIndex = new Map<string, string[]>();
  for (const c of candidates) {
    for (const email of c.emails) {
      if (!emailIndex.has(email)) emailIndex.set(email, []);
      emailIndex.get(email)!.push(c.id);
    }
  }

  // Build phone -> candidates[] index.
  const phoneIndex = new Map<string, string[]>();
  for (const c of candidates) {
    for (const phone of c.phones) {
      if (!phoneIndex.has(phone)) phoneIndex.set(phone, []);
      phoneIndex.get(phone)!.push(c.id);
    }
  }

  // Email pairs.
  for (const ids of emailIndex.values()) {
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const a = ids[i]!;
        const b = ids[j]!;
        if (!isDismissed(a, b)) union(a, b, 'email');
      }
    }
  }

  // Phone pairs. A number shared by more than MAX_SHARED_PHONE_CONTACTS
  // distinct contacts is generic and unions no one on its own (re
  // herold#223) — skip the whole index entry rather than just capping the
  // pair count, so no contact in an oversized group is unioned via this
  // number, directly or transitively.
  for (const ids of phoneIndex.values()) {
    if (ids.length > MAX_SHARED_PHONE_CONTACTS) continue;
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const a = ids[i]!;
        const b = ids[j]!;
        if (!isDismissed(a, b)) union(a, b, 'phone');
      }
    }
  }

  // Name pairs (O(n^2) — acceptable for typical address book sizes).
  for (let i = 0; i < candidates.length; i++) {
    const a = candidates[i]!;
    const normA = normalizeDisplayName(a.displayName);
    if (!normA) continue;
    for (let j = i + 1; j < candidates.length; j++) {
      const b = candidates[j]!;
      if (isDismissed(a.id, b.id)) continue;
      const normB = normalizeDisplayName(b.displayName);
      if (!normB) continue;
      if (namesAreClose(normA, normB)) {
        union(a.id, b.id, 'name');
      }
    }
  }

  // Collect clusters: group candidates by their root.
  const clusterMap = new Map<string, ContactCandidate[]>();
  for (const c of candidates) {
    const root = find(c.id);
    if (!clusterMap.has(root)) clusterMap.set(root, []);
    clusterMap.get(root)!.push(c);
  }

  // Return clusters with 2+ members, with collected reasons.
  const result: DuplicateCluster[] = [];
  for (const [root, members] of clusterMap.entries()) {
    if (members.length < 2) continue;
    const clusterReasons = reasons.get(root) ?? new Set<ClusterReason>();
    result.push({
      contacts: members,
      reasons: Array.from(clusterReasons),
    });
  }
  return result;
}

// ── Dismissed pairs (REQ-CONT-91) ─────────────────────────────────────────────

function dismissedStorageKey(principalId: string): string {
  return `herold:contacts:dismissed-pairs:${principalId}`;
}

/** Load dismissed pair IDs from localStorage for the given principal. */
export function loadDismissedPairs(principalId: string): Set<string> {
  try {
    const raw = localStorage.getItem(dismissedStorageKey(principalId));
    if (!raw) return new Set();
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((x): x is string => typeof x === 'string'));
  } catch {
    return new Set();
  }
}

/**
 * Persist a new dismissed pair (sorted by ID) to localStorage.
 * Returns the updated set.
 */
export function dismissPair(
  principalId: string,
  id1: string,
  id2: string,
): Set<string> {
  const key = [id1, id2].sort().join(':');
  const current = loadDismissedPairs(principalId);
  current.add(key);
  try {
    localStorage.setItem(dismissedStorageKey(principalId), JSON.stringify(Array.from(current)));
  } catch {
    // localStorage may be unavailable (private browsing with storage blocked).
  }
  return current;
}

// ── Merge card builder (REQ-CONT-92, 93) ──────────────────────────────────────

/**
 * Build a merged ContactEditVM from a cluster and user choices.
 *
 * Rules:
 * - Name: taken from choices.nameContactIndex.
 * - Photo: taken from choices.photoContactIndex (or null if -1).
 *   NOTE: media is handled separately (not part of ContactEditVM), so the
 *   caller must set raw.media on the survivor card patch.
 * - Emails: union of selected entries; re-keyed to avoid collisions.
 * - Phones: union of selected entries; re-keyed.
 * - Addresses: union of selected entries; re-keyed.
 * - Orgs, titles, notes, links, nicknames, anniversaries: union across all
 *   contacts (no per-field exclusion for these — they are always included
 *   unless both contacts have the same value).
 *
 * The rawCard of the resulting VM is set to the survivor's rawCard so that
 * vmToPatch produces a minimal patch that preserves unknown properties.
 */
export function buildMergedVM(
  cluster: DuplicateCluster,
  choices: MergeChoices,
): ContactEditVM {
  const survivorRaw = cluster.contacts[choices.survivorIndex]?.raw ?? {};
  const nameSource = cluster.contacts[choices.nameContactIndex];
  const nameVM = nameSource
    ? cardToVM(nameSource.raw).name
    : cardToVM(survivorRaw).name;

  // ── Emails: union of selected ─────────────────────────────────────────────
  const emails = choices.selectedEmails.map(({ contactIndex, key }) => {
    const vm = cardToVM(cluster.contacts[contactIndex]?.raw ?? {});
    const found = vm.emails.find((e) => e.key === key);
    return found ? { ...found, key: nextKey() } : null;
  }).filter((e): e is NonNullable<typeof e> => e !== null);

  // Ensure at most one preferred.
  const prefCount = emails.filter((e) => e.pref).length;
  if (prefCount > 1) {
    let firstPref = true;
    for (const e of emails) {
      if (e.pref) {
        if (!firstPref) e.pref = false;
        firstPref = false;
      }
    }
  }

  // ── Phones: union of selected ─────────────────────────────────────────────
  const phones = choices.selectedPhones.map(({ contactIndex, key }) => {
    const vm = cardToVM(cluster.contacts[contactIndex]?.raw ?? {});
    const found = vm.phones.find((p) => p.key === key);
    return found ? { ...found, key: nextKey() } : null;
  }).filter((p): p is NonNullable<typeof p> => p !== null);

  {
    const prefCount = phones.filter((p) => p.pref).length;
    if (prefCount > 1) {
      let firstPref = true;
      for (const p of phones) {
        if (p.pref) { if (!firstPref) p.pref = false; firstPref = false; }
      }
    }
  }

  // ── Addresses: union of selected ──────────────────────────────────────────
  const addresses = choices.selectedAddresses.map(({ contactIndex, key }) => {
    const vm = cardToVM(cluster.contacts[contactIndex]?.raw ?? {});
    const found = vm.addresses.find((a) => a.key === key);
    return found ? { ...found, key: nextKey() } : null;
  }).filter((a): a is NonNullable<typeof a> => a !== null);

  {
    const prefCount = addresses.filter((a) => a.pref).length;
    if (prefCount > 1) {
      let firstPref = true;
      for (const a of addresses) {
        if (a.pref) { if (!firstPref) a.pref = false; firstPref = false; }
      }
    }
  }

  // ── Union of unary/repeatable fields across all contacts ──────────────────
  // These are always merged completely (user cannot individually deselect
  // these at this stage — the merge keeps all unique values).
  const orgs: ContactEditVM['orgs'] = [];
  const titles: ContactEditVM['titles'] = [];
  const nicknames: ContactEditVM['nicknames'] = [];
  const links: ContactEditVM['links'] = [];
  const notes: ContactEditVM['notes'] = [];
  const anniversaries: ContactEditVM['anniversaries'] = [];

  // Dedup by string-serialised value to avoid exact duplicates.
  const seenOrgs = new Set<string>();
  const seenTitles = new Set<string>();
  const seenNicks = new Set<string>();
  const seenLinks = new Set<string>();
  const seenNotes = new Set<string>();
  const seenAnns = new Set<string>();

  for (const contact of cluster.contacts) {
    const vm = cardToVM(contact.raw);

    for (const o of vm.orgs) {
      const sig = JSON.stringify({ name: o.name, units: o.units });
      if (!seenOrgs.has(sig)) { seenOrgs.add(sig); orgs.push({ ...o, key: nextKey() }); }
    }
    for (const tt of vm.titles) {
      const sig = JSON.stringify({ name: tt.name, kind: tt.kind });
      if (!seenTitles.has(sig)) { seenTitles.add(sig); titles.push({ ...tt, key: nextKey() }); }
    }
    for (const n of vm.nicknames) {
      if (!seenNicks.has(n.name)) { seenNicks.add(n.name); nicknames.push({ ...n, key: nextKey() }); }
    }
    for (const l of vm.links) {
      if (!seenLinks.has(l.uri)) { seenLinks.add(l.uri); links.push({ ...l, key: nextKey() }); }
    }
    for (const nt of vm.notes) {
      if (!seenNotes.has(nt.note)) { seenNotes.add(nt.note); notes.push({ ...nt, key: nextKey() }); }
    }
    for (const a of vm.anniversaries) {
      const sig = `${a.kind}:${a.year}:${a.month}:${a.day}`;
      if (!seenAnns.has(sig)) { seenAnns.add(sig); anniversaries.push({ ...a, key: nextKey() }); }
    }
  }

  return {
    id: cluster.contacts[choices.survivorIndex]?.id ?? null,
    kind: cardToVM(survivorRaw).kind,
    name: nameVM,
    emails,
    phones,
    addresses,
    orgs,
    titles,
    nicknames,
    links,
    notes,
    anniversaries,
    rawCard: survivorRaw,
  };
}

/**
 * Build the Contact/set arguments for an atomic merge (REQ-CONT-93).
 *
 * Returns { update, destroy } suitable for the Contact/set call.
 * The survivor card is updated to the merged Card (via merge patch from
 * its original raw card); all other cluster members are destroyed.
 *
 * Partial failure leaves all pre-merge contacts intact because /set
 * is atomic — a notUpdated or notDestroyed error means nothing changed.
 */
export function buildMergeSetArgs(
  cluster: DuplicateCluster,
  choices: MergeChoices,
  mergedVM: ContactEditVM,
): {
  update: Record<string, Record<string, unknown>>;
  destroy: string[];
} {
  const survivorId = cluster.contacts[choices.survivorIndex]?.id;
  if (!survivorId) throw new Error('buildMergeSetArgs: survivorIndex is out of range');

  const destroyIds = cluster.contacts
    .filter((c, i) => i !== choices.survivorIndex)
    .map((c) => c.id);

  // Build patch from survivor's original card to the merged VM.
  const survivorRaw = cluster.contacts[choices.survivorIndex]!.raw;
  const { patch } = vmToPatch(survivorRaw, mergedVM);

  // Handle photo separately: insert into the patch if the photo changed.
  const photoCard = choices.photoContactIndex >= 0
    ? cluster.contacts[choices.photoContactIndex]?.raw
    : null;

  const newMedia = photoCard
    ? (photoCard.media as Record<string, unknown> | undefined)
    : null;
  const oldMedia = survivorRaw.media as Record<string, unknown> | undefined;

  const mediaChanged = JSON.stringify(newMedia ?? null) !== JSON.stringify(oldMedia ?? null);
  if (mediaChanged) {
    patch.media = newMedia ?? null;
  }

  return {
    update: { [survivorId]: patch },
    destroy: destroyIds,
  };
}

// ── Default merge choices (REQ-CONT-92) ────────────────────────────────────────

/**
 * Build the default MergeChoices for a cluster — all fields included, first
 * contact as name/photo/survivor. The user can adjust before committing.
 */
export function defaultMergeChoices(cluster: DuplicateCluster): MergeChoices {
  // Find the first contact that has a photo to use as default photo source.
  let photoContactIndex = -1;
  for (let i = 0; i < cluster.contacts.length; i++) {
    const raw = cluster.contacts[i]!.raw;
    const media = raw.media as Record<string, unknown> | undefined;
    if (media) {
      for (const v of Object.values(media)) {
        if (typeof v === 'object' && v !== null && (v as Record<string, unknown>).kind === 'photo') {
          photoContactIndex = i;
          break;
        }
      }
    }
    if (photoContactIndex >= 0) break;
  }

  const selectedEmails: MergeChoices['selectedEmails'] = [];
  const selectedPhones: MergeChoices['selectedPhones'] = [];
  const selectedAddresses: MergeChoices['selectedAddresses'] = [];

  // Track seen normalised values to avoid selecting duplicates.
  const seenEmails = new Set<string>();
  const seenPhones = new Set<string>();
  const seenAddresses = new Set<string>();

  for (let ci = 0; ci < cluster.contacts.length; ci++) {
    const vm = cardToVM(cluster.contacts[ci]!.raw);
    for (const e of vm.emails) {
      const norm = normalizeEmail(e.address);
      if (norm && !seenEmails.has(norm)) {
        seenEmails.add(norm);
        selectedEmails.push({ contactIndex: ci, key: e.key });
      }
    }
    for (const p of vm.phones) {
      const norm = normalizePhone(p.number);
      if (norm.length >= 4 && !seenPhones.has(norm)) {
        seenPhones.add(norm);
        selectedPhones.push({ contactIndex: ci, key: p.key });
      }
    }
    for (const a of vm.addresses) {
      // Use the street+locality as a rough dedup key.
      const sig = [a.street, a.locality, a.postcode].join('|').toLowerCase().trim();
      if (sig && !seenAddresses.has(sig)) {
        seenAddresses.add(sig);
        selectedAddresses.push({ contactIndex: ci, key: a.key });
      }
    }
  }

  return {
    nameContactIndex: 0,
    photoContactIndex,
    selectedEmails,
    selectedPhones,
    selectedAddresses,
    survivorIndex: 0,
  };
}

// ── Test surface ──────────────────────────────────────────────────────────────

export const _internals_forTest = {
  MAX_SHARED_PHONE_CONTACTS,
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
};
