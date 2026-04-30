/**
 * Tiered sender avatar resolver.
 *
 * Resolution order for any given sender email `e`:
 *
 *   1. Own identity avatar — if `e` matches one of the user's
 *      Identity.email values and that identity carries avatarBlobId,
 *      return the JMAP download URL.
 *   2. Email metadata (toggle-gated, `emailMetadataEnabled`):
 *      a. Face: header — decoded to a blob: URL.
 *      b. Gravatar — HEAD-checked; returned if the address is known.
 *   3. null — caller falls back to the initial-letter avatar.
 *
 * Results are cached in-memory (keyed by lowercased email) and
 * persisted to localStorage['herold:avatar:cache'] as
 * `{ [email]: { url: string | null, ts: number } }`. TTL is 24 h.
 * A null cached value means "no picture found"; that entry also
 * respects the TTL so that Gravatar profile additions become visible
 * within 24 h.
 *
 * The `emailMetadataEnabled` toggle is read from
 * localStorage['herold:avatar:emailMetadata'] (string "true"/"false");
 * default is true. The settings UI writes the same key.
 */

import { identityAvatarUrl } from './identity-avatar';
import {
  decodeFaceHeader,
  gravatarUrl,
  tryFetchGravatar,
} from './email-metadata-avatar';
import type { Identity } from './types';
import { jmap } from '../jmap/client';
import { Capability } from '../jmap/types';
import { auth } from '../auth/auth.svelte';

// ---------------------------------------------------------------------------
// localStorage keys & TTL
// ---------------------------------------------------------------------------

const CACHE_KEY = 'herold:avatar:cache';
const TOGGLE_KEY = 'herold:avatar:emailMetadata';
const TTL_MS = 24 * 60 * 60 * 1000; // 24 h

// ---------------------------------------------------------------------------
// Cache types
// ---------------------------------------------------------------------------

interface CacheEntry {
  url: string | null;
  ts: number;
}

type CacheMap = Record<string, CacheEntry>;

// ---------------------------------------------------------------------------
// Persistent cache helpers
// ---------------------------------------------------------------------------

function loadCache(): CacheMap {
  try {
    const raw = localStorage.getItem(CACHE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
      return {};
    return parsed as CacheMap;
  } catch {
    return {};
  }
}

function saveCache(cache: CacheMap): void {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify(cache));
  } catch {
    // Quota / private mode — cache just lives in-memory this session.
  }
}

function isExpired(entry: CacheEntry): boolean {
  return Date.now() - entry.ts > TTL_MS;
}

// ---------------------------------------------------------------------------
// Toggle helper
// ---------------------------------------------------------------------------

function readToggle(): boolean {
  try {
    const raw = localStorage.getItem(TOGGLE_KEY);
    if (raw === null) return true; // default on
    return raw !== 'false';
  } catch {
    return true;
  }
}

// ---------------------------------------------------------------------------
// Resolver singleton
// ---------------------------------------------------------------------------

/**
 * Per-email in-flight promise, to coalesce concurrent resolve calls
 * for the same address before the first one writes the cache.
 */
const inflight = new Map<string, Promise<string | null>>();

/** In-memory layer on top of the persistent cache for the current session. */
let memCache: CacheMap = loadCache();

/**
 * Resolve the best available avatar URL for a sender email.
 *
 * @param email           - the sender's email address (trimmed, lowercased internally).
 * @param ownIdentities   - the current user's identities from the mail store.
 * @param messageHeaders  - optional parsed headers from the email being rendered.
 * @returns               - a URL string for `<img src>` or null when no picture is
 *                          available.
 */
export async function resolve(
  email: string,
  ownIdentities: Identity[],
  messageHeaders?: { face?: string; xFace?: string },
): Promise<string | null> {
  const key = email.toLowerCase().trim();

  // ── 1. Own identity (not cached — always fast, no network) ──────────────
  const matchedIdentity = ownIdentities.find(
    (id) => id.email.toLowerCase().trim() === key,
  );
  if (matchedIdentity) {
    const url = identityAvatarUrl(matchedIdentity);
    if (url !== null) return url;
    // Identity matches but has no avatar blob — fall through to tier 2.
  }

  // ── Cache lookup (covers tiers 2+) ─────────────────────────────────────
  const cached = memCache[key];
  if (cached && !isExpired(cached)) {
    return cached.url;
  }

  // ── Coalesce concurrent resolves for the same email ─────────────────────
  const existing = inflight.get(key);
  if (existing) return existing;

  const promise = resolveUncached(key, messageHeaders);
  inflight.set(key, promise);
  promise.finally(() => inflight.delete(key));
  return promise;
}

async function resolveUncached(
  key: string,
  messageHeaders?: { face?: string; xFace?: string },
): Promise<string | null> {
  // ── 2. Hosted-principal lookup (REQ-MAIL-44 tier 2) ───────────────────────
  // Always tried before email-metadata fallback because it produces a
  // higher-quality, server-stored picture; it is also a cheap one-shot
  // JMAP round-trip whose negative is cached for 24 h.
  const hostedUrl = await lookupHostedPrincipalAvatar(key);
  if (hostedUrl) {
    writeCache(key, hostedUrl);
    return hostedUrl;
  }

  const emailMetadata = readToggle();

  if (emailMetadata) {
    // ── 3a. Face: header ───────────────────────────────────────────────────
    const faceHeader = messageHeaders?.face;
    if (faceHeader) {
      const blobUrl = decodeFaceHeader(faceHeader);
      if (blobUrl) {
        writeCache(key, blobUrl);
        // Note: blob: URLs are session-scoped and cannot be persisted across
        // page reloads, but writeCache will store the non-null sentinel so
        // we don't re-fetch Gravatar. On a reload the Face header will be
        // available again from the email object.
        return blobUrl;
      }
    }

    // ── 3b. Gravatar ─────────────────────────────────────────────────────
    const url = await gravatarUrl(key);
    const found = await tryFetchGravatar(url);
    if (found) {
      writeCache(key, url);
      return url;
    }
  }

  // ── 4. No picture available ──────────────────────────────────────────────
  writeCache(key, null);
  return null;
}

/**
 * Resolve a hosted principal's avatar via Principal/query + Principal/get,
 * batched in one JMAP round-trip. Returns the JMAP download URL with
 * `disposition=inline` when the address is a hosted principal that has set
 * an avatarBlobId, otherwise null.
 *
 * Called from `resolveUncached` when own-identity tier misses; failure
 * (network error, no chat capability, miss) returns null so the caller
 * falls through to the email-metadata tiers.
 */
async function lookupHostedPrincipalAvatar(
  email: string,
): Promise<string | null> {
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
          properties: ['id', 'email', 'avatarBlobId'],
        },
        [Capability.Core, Capability.HeroldChat],
      );
    });
    const getResp = responses.find(([name]) => name === 'Principal/get');
    if (!getResp || getResp[0] === 'error') return null;
    const result = getResp[1] as {
      list: Array<{
        id: string;
        email: string;
        avatarBlobId: string | null;
      }>;
    };
    const principal = result.list[0];
    if (!principal?.avatarBlobId) return null;
    return jmap.downloadUrl({
      accountId,
      blobId: principal.avatarBlobId,
      type: 'image/*',
      name: 'avatar',
      disposition: 'inline',
    });
  } catch {
    return null;
  }
}

function writeCache(key: string, url: string | null): void {
  memCache[key] = { url, ts: Date.now() };
  saveCache(memCache);
}

/**
 * Invalidate the in-memory cache entry for the given email.
 * The next resolve() call will re-run the lookup chain.
 * Exposed for testing.
 */
export function _invalidateCacheEntry(email: string): void {
  const key = email.toLowerCase().trim();
  delete memCache[key];
}

/**
 * Replace the in-memory cache wholesale. Used by tests to inject state.
 */
export function _setMemCache(cache: CacheMap): void {
  memCache = cache;
}

/**
 * Return a snapshot of the current in-memory cache. Used by tests.
 */
export function _getMemCache(): CacheMap {
  return memCache;
}

/**
 * Clear both the in-memory and persisted avatar caches.
 */
export function clearAvatarCache(): void {
  memCache = {};
  try {
    localStorage.removeItem(CACHE_KEY);
  } catch {
    // ignore
  }
}

// Export the toggle read/write helpers so the settings UI can use them
// directly without duplicating the key constant.
export { readToggle as avatarEmailMetadataEnabled };

export function setAvatarEmailMetadataEnabled(value: boolean): void {
  try {
    localStorage.setItem(TOGGLE_KEY, value ? 'true' : 'false');
  } catch {
    // ignore
  }
}
