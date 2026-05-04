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
 *
 * filterAsync() extends filter() with an async Directory/search query
 * when the server advertises the directory-autocomplete capability
 * (https://netzhansa.com/jmap/directory-autocomplete). Directory results
 * are appended after Contacts and SeenAddress entries within each tier,
 * deduplicated by lowercased email (Contacts win > SeenAddress > Directory).
 */

import { jmap, strict } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { seenAddresses, type SeenAddress } from './seen-addresses.svelte';
import { hasDirectoryAutocomplete } from '../auth/capabilities';

export interface ContactSuggestion {
  /** JMAP Contact id (string), SeenAddress id prefixed with 'sa:', or Directory id prefixed with 'dir:'. */
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
    return this.#fetch();
  }

  /**
   * Force a full reload regardless of current status. Call this after a
   * contact is created / updated / destroyed so the suggestions cache
   * reflects the server state (re #75).
   */
  async reload(): Promise<void> {
    if (this.status === 'loading') return; // already in-flight
    this.status = 'idle';
    return this.#fetch();
  }

  async #fetch(): Promise<void> {
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
    return mergeFilter(this.suggestions, seenAddresses.entries, [], query, limit);
  }

  /**
   * Async version of filter() that also queries Directory/search when the
   * server advertises the directory-autocomplete capability. The result merges
   * all three sources with priority: Contacts > SeenAddress > Directory.
   *
   * Returns [] immediately (synchronously via Promise.resolve) when:
   *   - the query is empty or shorter than 2 characters (no directory call)
   *
   * Directory results are deduped against Contacts and SeenAddress by
   * lowercased email.
   */
  async filterAsync(query: string, limit = 8): Promise<ContactSuggestion[]> {
    const contactsAndSeen = this.filter(query, limit);
    const dirResults = await searchDirectory(query, limit);
    if (dirResults.length === 0) return contactsAndSeen;
    return mergeFilter(this.suggestions, seenAddresses.entries, dirResults, query, limit);
  }

  #accountId(): string | null {
    return auth.session?.primaryAccounts[Capability.Contacts] ?? null;
  }
}

export const contacts = new Contacts();

/**
 * Query the server's Directory/search method with the given prefix.
 *
 * Returns [] when:
 *   - the directory-autocomplete capability is not advertised
 *   - prefix is empty or shorter than 2 characters
 *   - the JMAP call fails for any reason (soft-fail)
 *
 * Soft-fail: logs to console but does not surface a toast.
 */
export async function searchDirectory(
  prefix: string,
  limit = 8,
): Promise<ContactSuggestion[]> {
  if (!hasDirectoryAutocomplete()) return [];
  if (prefix.length < 2) return [];

  const accountId = directoryAccountId();
  if (!accountId) return [];

  try {
    const { responses } = await jmap.batch((b) => {
      b.call(
        'Directory/search',
        { accountId, textPrefix: prefix, limit },
        [Capability.HeroldDirectoryAutocomplete],
      );
    });
    const resp = responses[0];
    if (!resp || resp[0] === 'error') return [];
    const args = resp[1] as {
      accountId: string;
      items: Array<{ id: string; email: string; displayName?: string }>;
    };
    const items = args.items ?? [];
    return items.slice(0, limit).map((item) => ({
      id: `dir:${item.id}`,
      name: item.displayName ?? '',
      email: item.email,
    }));
  } catch (err) {
    console.error('Directory/search failed', err);
    return [];
  }
}

/**
 * Merge JMAP Contact suggestions with SeenAddress entries and (optionally)
 * Directory results, returning a filtered, sorted, deduped list capped at
 * `limit`.
 *
 * Priority for deduplication by lowercased email:
 *   1. JMAP Contacts (highest)
 *   2. SeenAddress entries
 *   3. Directory results
 *
 * Sort tiers:
 *   0 — query matches start of name/displayName
 *   1 — query matches start of email local-part
 *   2 — any other substring match
 *
 * Within each tier:
 *   - Contact entries ordered alphabetically by name.
 *   - SeenAddress entries ordered by lastUsedAt desc.
 *   - Directory entries ordered alphabetically by displayName.
 *   - Contacts precede SeenAddress which precede Directory within the same tier.
 */
export function mergeFilter(
  contacts: ContactSuggestion[],
  seen: SeenAddress[],
  directory: ContactSuggestion[],
  query: string,
  limit = 8,
): ContactSuggestion[] {
  const q = query.trim().toLowerCase();

  // Build a set of lowercased emails already covered by JMAP Contacts so we
  // can suppress matching SeenAddress and Directory entries.
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

  // Build dedup set covering contacts + seen, to suppress directory duplicates.
  const seenEmails = new Set(seenSuggestions.map((s) => s.email.toLowerCase()));
  const coveredEmails = new Set([...contactEmails, ...seenEmails]);

  // Deduplicate directory results against contacts and seen-address entries.
  const dirSuggestions: ContactSuggestion[] = directory.filter(
    (d) => !coveredEmails.has(d.email.toLowerCase()),
  );

  if (!q) {
    // Empty query: contacts first, then seen, then directory, proportional.
    const allContacts = contacts.slice(0, limit);
    const rem1 = limit - allContacts.length;
    const allSeen = rem1 > 0 ? seenSuggestions.slice(0, rem1) : [];
    const rem2 = limit - allContacts.length - allSeen.length;
    const allDir = rem2 > 0 ? dirSuggestions.slice(0, rem2) : [];
    return [...allContacts, ...allSeen, ...allDir];
  }

  // Source tag: 0 = contact, 1 = seen, 2 = directory (used for inter-source ordering).
  type Scored = {
    item: ContactSuggestion;
    tier: number;
    source: number;
    sortKey: string;
    date: string;
  };

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
    scored.push({ item: c, tier: t, source: 0, sortKey: c.name.toLowerCase(), date: '' });
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
      source: 1,
      sortKey: s.name.toLowerCase(),
      date: dateBySeenId.get(s.id) ?? '',
    });
  }

  for (const d of dirSuggestions) {
    const t = tier(d);
    if (t < 0) continue;
    scored.push({ item: d, tier: t, source: 2, sortKey: d.name.toLowerCase(), date: '' });
  }

  // Sort: tier asc, then source asc (contacts < seen < directory), then within source:
  //   - SeenAddress entries (source 1) by date desc
  //   - Contact and Directory entries by name asc
  scored.sort((a, b) => {
    if (a.tier !== b.tier) return a.tier - b.tier;
    if (a.source !== b.source) return a.source - b.source;
    if (a.source === 1) {
      // Both seen: more recent first.
      return b.date.localeCompare(a.date);
    }
    // Both contacts or both directory: alphabetical.
    return a.sortKey.localeCompare(b.sortKey);
  });

  return scored.slice(0, limit).map((s) => s.item);
}

// ── Test surface ───────────────────────────────────────────────────────────

export const _internals_forTest = { mergeFilter, searchDirectory };

/**
 * Resolve the accountId to use for Directory/search. Mirrors the chat
 * store's principal-account resolution: prefer the directory-autocomplete
 * primaryAccount if the server sets one, otherwise fall back to the mail
 * account (the Mail capability is the standard home for address data).
 */
function directoryAccountId(): string | null {
  const session = auth.session;
  if (!session) return null;
  return (
    session.primaryAccounts[Capability.HeroldDirectoryAutocomplete] ??
    session.primaryAccounts[Capability.Mail] ??
    null
  );
}

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
