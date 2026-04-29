/**
 * Contacts store — caches the principal's address book for use by the
 * compose-window autocomplete. Each entry is a flat
 * {name, email, contactId} record so the suggestion list can be filtered
 * cheaply without re-walking the JSContact JSON on every keystroke.
 *
 * Phase 1: load all contacts once on first compose (Contact/get with
 * ids: null), reload on AddressBook/Contact state-change events.
 *
 * filter() returns the union of JMAP Contacts and SeenAddress entries
 * (REQ-MAIL-11). Dedup is by canonical lowercased email; JMAP Contacts
 * win when the same email appears in both sources. Results are capped at 8.
 * Sort order (per tier): name-prefix matches first, then email-local-part
 * prefix, then any substring match; within each tier SeenAddress entries
 * sort by lastUsedAt desc, Contacts sort alphabetically by name.
 */

import { jmap, strict } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { seenAddresses, type SeenAddress } from './seen-addresses.svelte';

export interface ContactSuggestion {
  /** JMAP Contact id (string) or SeenAddress id prefixed with 'sa:'. */
  id: string;
  /** Display name — JSContact name.full || derived from name components. */
  name: string;
  /** Single email address. A contact with N emails produces N suggestions. */
  email: string;
}

class Contacts {
  /** Status: idle / loading / ready / error. */
  status = $state<'idle' | 'loading' | 'ready' | 'error'>('idle');
  /** Flattened (name, email) suggestions. */
  suggestions = $state<ContactSuggestion[]>([]);

  /** Idempotent: no-op when ready / loading. */
  async load(): Promise<void> {
    if (this.status === 'loading' || this.status === 'ready') return;
    const accountId = this.#accountId();
    if (!accountId) return;
    this.status = 'loading';
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Contact/get',
          { accountId, ids: null },
          [Capability.Contacts],
        );
      });
      strict(responses);
      const args = responses[0]![1] as { list: unknown[] };
      const flat: ContactSuggestion[] = [];
      for (const card of args.list ?? []) {
        if (typeof card !== 'object' || card === null) continue;
        const obj = card as Record<string, unknown>;
        const id = String(obj.id ?? '');
        if (!id) continue;
        const name = extractName(obj);
        for (const email of extractEmails(obj)) {
          flat.push({ id, name, email });
        }
      }
      this.suggestions = flat;
      this.status = 'ready';
    } catch {
      // Soft-fail: contact autocomplete is a nice-to-have. The compose
      // form still works without it.
      this.status = 'error';
      this.suggestions = [];
    }
  }

  /**
   * Filter the union of JMAP Contacts and SeenAddress entries by
   * case-insensitive match across name and email; cap at `limit`.
   *
   * Merge rules (REQ-MAIL-11, REQ-MAIL-11l):
   *   - Dedup by canonical lowercased email: when the same email appears in
   *     both sources the JMAP Contact wins and the SeenAddress is suppressed.
   *   - Sort tiers (per source, within each tier):
   *       1. Query matches the START of displayName / name.
   *       2. Query matches the START of the email local-part.
   *       3. Any other substring match.
   *     Within each tier SeenAddress entries sort by lastUsedAt desc;
   *     Contact entries sort alphabetically by name.
   *   - When query is empty, return Contacts first then SeenAddress entries,
   *     each capped proportionally to fill the limit.
   */
  filter(query: string, limit = 8): ContactSuggestion[] {
    return mergeFilter(this.suggestions, seenAddresses.entries, query, limit);
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }
}

export const contacts = new Contacts();

/**
 * Merge JMAP Contact suggestions with SeenAddress entries and return a
 * filtered, sorted, deduped list capped at `limit`.
 *
 * Sort tiers:
 *   0 — query matches start of name/displayName
 *   1 — query matches start of email local-part
 *   2 — any other substring match
 *
 * Within each tier: SeenAddress entries ordered by lastUsedAt desc;
 * Contact entries ordered alphabetically by name.
 */
export function mergeFilter(
  contacts: ContactSuggestion[],
  seen: SeenAddress[],
  query: string,
  limit = 8,
): ContactSuggestion[] {
  const q = query.trim().toLowerCase();

  // Build a set of lowercased emails already covered by JMAP Contacts so we
  // can suppress matching SeenAddress entries (dedup by email, Contacts win).
  const contactEmails = new Set(contacts.map((c) => c.email.toLowerCase()));

  // Convert SeenAddress entries to ContactSuggestion shape with a namespaced
  // id so callers can distinguish the source if needed.
  const seenSuggestions: ContactSuggestion[] = seen
    .filter((sa) => !contactEmails.has(sa.email.toLowerCase()))
    .map((sa) => ({
      id: `sa:${sa.id}`,
      name: sa.displayName,
      email: sa.email,
    }));

  if (!q) {
    // Empty query: contacts first, then seen entries, each slice proportional.
    const allContacts = contacts.slice(0, limit);
    const remaining = limit - allContacts.length;
    const allSeen = remaining > 0 ? seenSuggestions.slice(0, remaining) : [];
    return [...allContacts, ...allSeen];
  }

  type Scored = { item: ContactSuggestion; tier: number; sortKey: string; date: string };

  function tier(item: ContactSuggestion): number {
    const nameLower = item.name.toLowerCase();
    const emailLower = item.email.toLowerCase();
    const localPart = emailLower.split('@')[0] ?? emailLower;
    if (nameLower.startsWith(q)) return 0;
    if (localPart.startsWith(q)) return 1;
    if (nameLower.includes(q) || emailLower.includes(q)) return 2;
    return -1; // no match
  }

  const scored: Scored[] = [];

  for (const c of contacts) {
    const t = tier(c);
    if (t < 0) continue;
    scored.push({ item: c, tier: t, sortKey: c.name.toLowerCase(), date: '' });
  }

  // Capture lastUsedAt for SeenAddress entries via the original seen array
  // so we can sort within each tier by recency.
  const dateBySeenId = new Map(seen.map((sa) => [`sa:${sa.id}`, sa.lastUsedAt]));

  for (const s of seenSuggestions) {
    const t = tier(s);
    if (t < 0) continue;
    scored.push({
      item: s,
      tier: t,
      sortKey: s.name.toLowerCase(),
      date: dateBySeenId.get(s.id) ?? '',
    });
  }

  // Sort: tier asc, then within tier:
  //   - SeenAddress entries (date non-empty) by date desc
  //   - Contact entries by name asc
  scored.sort((a, b) => {
    if (a.tier !== b.tier) return a.tier - b.tier;
    const aIsSeen = a.date !== '';
    const bIsSeen = b.date !== '';
    if (aIsSeen && bIsSeen) {
      // Both seen: more recent first.
      return b.date.localeCompare(a.date);
    }
    if (!aIsSeen && !bIsSeen) {
      // Both contacts: alphabetical.
      return a.sortKey.localeCompare(b.sortKey);
    }
    // Mixed: contact before seen within the same tier (contacts are primary).
    return aIsSeen ? 1 : -1;
  });

  return scored.slice(0, limit).map((s) => s.item);
}

// ── Test surface ───────────────────────────────────────────────────────────

export const _internals_forTest = { mergeFilter };

/**
 * Extract a display name from a JSContact card — prefer name.full,
 * otherwise concatenate ordered name.components, otherwise fall back
 * to the empty string.
 */
function extractName(card: Record<string, unknown>): string {
  const nameObj = card.name as Record<string, unknown> | undefined;
  if (nameObj && typeof nameObj.full === 'string' && nameObj.full.trim()) {
    return nameObj.full.trim();
  }
  if (nameObj && Array.isArray(nameObj.components)) {
    const parts: string[] = [];
    for (const c of nameObj.components) {
      if (typeof c === 'object' && c !== null) {
        const v = (c as Record<string, unknown>).value;
        if (typeof v === 'string') parts.push(v);
      }
    }
    return parts.join(' ').trim();
  }
  return '';
}

/**
 * Extract every email address from a JSContact card. Emails are stored
 * as a map keyed by the client's opaque id; values have an `address`
 * field (RFC 9553 §2.5.2).
 */
function extractEmails(card: Record<string, unknown>): string[] {
  const map = card.emails as Record<string, unknown> | undefined;
  if (!map) return [];
  const out: string[] = [];
  for (const v of Object.values(map)) {
    if (typeof v === 'object' && v !== null) {
      const addr = (v as Record<string, unknown>).address;
      if (typeof addr === 'string' && addr.includes('@')) out.push(addr);
    }
  }
  return out;
}
