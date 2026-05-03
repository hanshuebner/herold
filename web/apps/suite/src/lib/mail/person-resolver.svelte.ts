/**
 * Recipient hover-card resolver (REQ-MAIL-46 / REQ-MAIL-46a..f).
 *
 * Given an email address, returns the payload the hover card needs:
 *   - displayName     — best name observed for this person
 *   - avatarUrl       — same chain as REQ-MAIL-44 (own identity →
 *                       hosted principal → email metadata fallback)
 *   - phones          — phone numbers from the matching JMAP Contact or
 *                       hosted Principal record
 *   - contactId       — JMAP Contact id when the address is saved as a
 *                       contact in the user's primary address book; null
 *                       otherwise (drives the corner add/edit icon and
 *                       the "View detailed view" link)
 *   - principalId     — Herold Principal id when the address is a hosted
 *                       user on this server; gates the chat / call /
 *                       calendar secondary actions
 *
 * Cache: results are persisted in the same `localStorage['herold:avatar:cache']`
 * entry the avatar lookup writes (REQ-MAIL-46f). `peekPerson(email)` returns
 * the cached payload synchronously so the card renders fully populated on
 * the very first paint after a re-open; `resolvePerson(...)` re-runs the
 * lookup chain and writes back any fresher fields.
 */

import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';
import { contacts } from '../contacts/store.svelte';
import {
  resolve as resolveAvatar,
  readCacheEntry,
  writeCacheEntryFields,
  type CachedPhone,
} from './avatar-resolver.svelte';
import type { Identity } from './types';

export interface PersonRecord {
  /** Canonical lower-cased email address. */
  email: string;
  /** Best display name; falls back to the email's local-part when empty. */
  displayName: string;
  /** Avatar URL or null when the resolver chain found nothing. */
  avatarUrl: string | null;
  /** Phone numbers from the matched principal or contact. */
  phones: CachedPhone[];
  /** JMAP Contact id when this address is a saved contact. */
  contactId: string | null;
  /** Herold Principal id when this address is a hosted user on this server. */
  principalId: string | null;
}

/**
 * Synchronously read whatever is cached for an email so the hover card
 * can render immediately on open. Returns null when there is no cached
 * entry; partial entries (avatar-only or principal-only) are still
 * returned so the card shows what it has and waits for the async
 * re-validation to fill the rest.
 */
export function peekPerson(
  email: string,
  capturedName?: string | null,
): PersonRecord | null {
  const key = email.toLowerCase().trim();
  if (!key) return null;
  const entry = readCacheEntry(key);
  if (!entry) return null;
  return {
    email: key,
    displayName: pickName(entry.name, capturedName, key),
    avatarUrl: entry.url ?? null,
    phones: entry.phones ?? [],
    contactId: entry.contactId ?? null,
    principalId: entry.principalId ?? null,
  };
}

/**
 * Run the full resolution chain for an email and return the populated
 * record. Each stage that succeeds writes back into the shared cache
 * entry so the next peek is up to date.
 *
 * @param email          - the address to resolve.
 * @param ownIdentities  - the user's identities, for the own-identity tier.
 * @param capturedName   - the name parsed from the message header, used as
 *                         a fallback when no contact / principal is matched.
 * @param messageHeaders - Face / X-Face headers from the rendered message.
 */
export async function resolvePerson(
  email: string,
  ownIdentities: Identity[] = [],
  capturedName?: string | null,
  messageHeaders?: { face?: string; xFace?: string },
): Promise<PersonRecord> {
  const key = email.toLowerCase().trim();

  // Avatar URL — reuse the existing tiered resolver. It writes the URL
  // into the cache entry as a side-effect.
  const avatarUrl = await resolveAvatar(key, ownIdentities, messageHeaders);

  // Hosted-principal lookup — phones + principalId. Independent of the
  // avatar lookup so we can extract phones even when no avatar is set.
  const principal = await lookupPrincipal(key);

  // JMAP Contact lookup — phones + contactId. Driven off the in-memory
  // contacts store; it loads on first compose so a fresh page may not
  // have it yet, in which case we kick off a load and proceed.
  if (contacts.status === 'idle') {
    void contacts.load();
  }
  const contactMatch = findContact(key);

  // Phone numbers: prefer the contact's phones (the user's curated list)
  // over the principal's phones; merge the principal's entries that the
  // contact does not already have so we surface every known number.
  const phones = mergePhones(
    contactMatch?.phones ?? [],
    principal?.phones ?? [],
  );

  // Display name: contact > principal > captured name > local-part.
  const displayName = pickName(
    contactMatch?.name ?? principal?.displayName,
    capturedName,
    key,
  );

  // Persist the extended fields for the next peek.
  writeCacheEntryFields(key, {
    name: displayName,
    phones,
    contactId: contactMatch?.id ?? null,
    principalId: principal?.id ?? null,
  });

  return {
    email: key,
    displayName,
    avatarUrl,
    phones,
    contactId: contactMatch?.id ?? null,
    principalId: principal?.id ?? null,
  };
}

interface PrincipalMatch {
  id: string;
  displayName?: string;
  phones?: CachedPhone[];
}

async function lookupPrincipal(email: string): Promise<PrincipalMatch | null> {
  const session = auth.session;
  if (!session) return null;
  const accountId =
    session.primaryAccounts[Capability.HeroldChat] ??
    session.primaryAccounts[Capability.Core] ??
    null;
  if (!accountId) return null;

  try {
    const { responses } = await jmap.batch((b) => {
      const q = b.call(
        'Principal/query',
        { accountId, filter: { emailExact: email } },
        [Capability.Core, Capability.HeroldChat],
      );
      b.call(
        'Principal/get',
        {
          accountId,
          '#ids': q.ref('/ids'),
          properties: [
            'id',
            'email',
            'displayName',
            'avatarBlobId',
            'phones',
          ],
        },
        [Capability.Core, Capability.HeroldChat],
      );
    });
    const getResp = responses.find(([name]) => name === 'Principal/get');
    if (!getResp || getResp[0] === 'error') return null;
    const result = getResp[1] as {
      list: Array<{
        id: string;
        displayName?: string;
        phones?: Array<{ type?: string; number?: string }>;
      }>;
    };
    const principal = result.list[0];
    if (!principal) return null;
    return {
      id: principal.id,
      displayName: principal.displayName,
      phones: normalisePhones(principal.phones),
    };
  } catch {
    return null;
  }
}

interface ContactMatch {
  id: string;
  name: string;
  phones: CachedPhone[];
}

/**
 * Pure helper: scan the loaded contacts store for an entry whose email
 * matches `key`. Returns null when none found. The contacts store
 * flattens each address-book entry into per-email rows, so a single
 * match is enough.
 */
export function findContact(key: string): ContactMatch | null {
  const lc = key.toLowerCase();
  const match = contacts.suggestions.find((s) => s.email.toLowerCase() === lc);
  if (!match) return null;
  // Phones are not currently mirrored into the suggestions cache, so we
  // surface the contact's id and name only. A future iteration can pull
  // phones from the full Contact record.
  return { id: match.id, name: match.name, phones: [] };
}

function normalisePhones(
  raw: Array<{ type?: string; number?: string }> | undefined,
): CachedPhone[] {
  if (!raw) return [];
  const out: CachedPhone[] = [];
  for (const p of raw) {
    if (typeof p?.number !== 'string' || p.number.trim() === '') continue;
    out.push({ type: (p.type ?? '').trim(), number: p.number.trim() });
  }
  return out;
}

/** Merge two phone lists by number, preferring `primary`'s entries. */
function mergePhones(primary: CachedPhone[], extra: CachedPhone[]): CachedPhone[] {
  const seen = new Set<string>();
  const out: CachedPhone[] = [];
  for (const list of [primary, extra]) {
    for (const p of list) {
      const norm = p.number.replace(/\s+/g, '');
      if (seen.has(norm)) continue;
      seen.add(norm);
      out.push(p);
    }
  }
  return out;
}

/**
 * Pick the best name to render. The first non-empty entry from the
 * names list wins; when all are empty we fall back to the email's
 * local-part. The email is always supplied last and never returned
 * verbatim — only its local-part may be used as a fallback.
 */
function pickName(
  name1: string | null | undefined,
  name2: string | null | undefined,
  email: string,
): string {
  for (const c of [name1, name2]) {
    if (typeof c === 'string') {
      const trimmed = c.trim();
      if (trimmed) return trimmed;
    }
  }
  const at = email.indexOf('@');
  return at > 0 ? email.slice(0, at) : email;
}

// ── Test surface ─────────────────────────────────────────────────────────

export const _internals_forTest = {
  pickName,
  mergePhones,
  normalisePhones,
};
