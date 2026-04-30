/**
 * Pure-TS helpers for email-metadata-based avatar lookup.
 *
 * Three helpers:
 *   gravatarUrl(email)        — builds a Gravatar URL using SHA-256 hash.
 *   decodeFaceHeader(b64)     — decodes a Face: header value to a blob: URL.
 *   tryFetchGravatar(url)     — HEAD-fetches a Gravatar URL; true on 200.
 *
 * These helpers are intentionally side-effect-free (except
 * tryFetchGravatar which issues a network request and
 * decodeFaceHeader which calls URL.createObjectURL).
 * The caller (avatar-resolver.svelte.ts) owns caching and
 * toggle-gating.
 */

/**
 * Build a Gravatar URL for the given email address.
 *
 * Uses SHA-256 (the modern preference; Gravatar also accepts MD5).
 * `d=404` makes Gravatar return HTTP 404 for unknown addresses
 * rather than a default placeholder image, allowing the caller
 * to detect "no picture" and fall through to the initials tier.
 */
export async function gravatarUrl(email: string): Promise<string> {
  const normalised = email.toLowerCase().trim();
  const hash = await sha256Hex(normalised);
  return `https://www.gravatar.com/avatar/${hash}?s=128&d=404`;
}

/**
 * Decode a Face: header value (base64-encoded PNG/JPEG) to a blob: URL.
 *
 * The caller is responsible for revoking the returned URL with
 * URL.revokeObjectURL when the avatar is no longer needed.
 *
 * Returns null when the base64 string is empty or cannot be decoded.
 */
export function decodeFaceHeader(b64: string): string | null {
  const stripped = b64.trim();
  if (!stripped) return null;
  try {
    const binary = atob(stripped);
    const bytes = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) {
      bytes[i] = binary.charCodeAt(i);
    }
    // Detect JPEG (FF D8) vs PNG (89 50 4E 47).
    let mime = 'image/png';
    if (bytes[0] === 0xff && bytes[1] === 0xd8) {
      mime = 'image/jpeg';
    }
    const blob = new Blob([bytes], { type: mime });
    return URL.createObjectURL(blob);
  } catch {
    return null;
  }
}

/**
 * Issue a HEAD request to the Gravatar URL.
 *
 * Returns true when the server responds with HTTP 200 (the address
 * has a custom picture). Returns false on HTTP 404 (unknown address)
 * or any network / CORS error.
 *
 * Gravatar supports HEAD requests; the response is small and carries
 * no body, so this is cheap even for cold cache misses.
 */
export async function tryFetchGravatar(url: string): Promise<boolean> {
  try {
    const resp = await fetch(url, { method: 'HEAD' });
    return resp.ok;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

async function sha256Hex(text: string): Promise<string> {
  const encoder = new TextEncoder();
  const data = encoder.encode(text);
  const hashBuffer = await crypto.subtle.digest('SHA-256', data);
  const hashArray = Array.from(new Uint8Array(hashBuffer));
  return hashArray.map((b) => b.toString(16).padStart(2, '0')).join('');
}
