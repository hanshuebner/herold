/**
 * Pure parsing helpers for the RFC 2369 `List-*` header family and the
 * RFC 8058 one-click unsubscribe extension.
 *
 * Requirements: docs/design/web/requirements/16-mailing-lists.md
 * (REQ-LIST-01/02, REQ-LIST-20..22) and
 * docs/design/web/requirements/14-unsubscribe.md (REQ-UNS-01..23).
 *
 * Nothing here touches the network or the DOM -- these are string ->
 * data-shape transforms so they are exhaustively unit-testable.
 */

/** One URL extracted from an angle-bracketed `List-*` header value. */
export interface ListUrl {
  scheme: 'https' | 'http' | 'mailto';
  url: string;
}

/**
 * Extract every `<...>` bracketed URL from a raw header value, tagging
 * each with its scheme. Values with an unrecognised scheme (rare, e.g.
 * a bare `NO` or a non-URL token) are silently dropped -- callers treat
 * an empty result the same as an absent header.
 *
 * RFC 2369 headers may carry several alternatives separated by commas;
 * the regex scan finds all of them regardless of the separator, so it
 * tolerates both comma-separated and space-separated forms seen in the
 * wild.
 */
export function parseAngleBracketUrls(raw: string | null | undefined): ListUrl[] {
  if (!raw) return [];
  const out: ListUrl[] = [];
  const re = /<([^>]+)>/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(raw))) {
    const url = (m[1] ?? '').trim();
    if (/^https:/i.test(url)) out.push({ scheme: 'https', url });
    else if (/^http:/i.test(url)) out.push({ scheme: 'http', url });
    else if (/^mailto:/i.test(url)) out.push({ scheme: 'mailto', url });
  }
  return out;
}

/** The result of picking a single actionable URL from a `List-*` header. */
export type ListAction =
  | { kind: 'https'; url: string }
  | { kind: 'mailto'; url: string }
  /**
   * Header present but the only URL(s) advertised are cleartext `http:`.
   * REQ-UNS-04 / REQ-LIST-20: never auto-open these; the caller must
   * surface the "unencrypted link" message instead.
   */
  | { kind: 'http-only'; url: string };

/**
 * Pick the preferred action from a parsed URL list: HTTPS first, then
 * (optionally) mailto:, then a cleartext http: URL flagged as
 * 'http-only' so the caller can refuse to auto-open it. Returns null
 * when the header carried no recognised URL at all.
 */
export function pickPreferredAction(
  urls: ListUrl[],
  opts: { allowMailto: boolean },
): ListAction | null {
  const https = urls.find((u) => u.scheme === 'https');
  if (https) return { kind: 'https', url: https.url };
  if (opts.allowMailto) {
    const mailto = urls.find((u) => u.scheme === 'mailto');
    if (mailto) return { kind: 'mailto', url: mailto.url };
  }
  const http = urls.find((u) => u.scheme === 'http');
  if (http) return { kind: 'http-only', url: http.url };
  return null;
}

/** REQ-LIST-02: the list's display label plus its raw identifier. */
export interface ListIdInfo {
  /** The raw content of the angle brackets, e.g. "projectx-discuss.example.com". */
  id: string;
  /** Human-readable label -- the description part, or a fallback. */
  label: string;
}

/**
 * Parse the `List-ID` header per REQ-LIST-02:
 * `"Project X discuss" <projectx-discuss.example.com>` -> label
 * "Project X discuss", id "projectx-discuss.example.com". When no
 * quoted description is present, the label falls back to the local
 * part of the identifier (the segment before the first dot).
 *
 * Returns null when the header is absent/blank or carries no
 * angle-bracketed identifier at all (a malformed header is treated the
 * same as an absent one -- REQ-LIST-01 says presence is the signal).
 */
export function parseListId(raw: string | null | undefined): ListIdInfo | null {
  if (!raw) return null;
  const trimmed = raw.trim();
  if (!trimmed) return null;
  const m = trimmed.match(/^(?:"((?:[^"\\]|\\.)*)"\s*)?<([^>]+)>/);
  if (!m) return null;
  const id = (m[2] ?? '').trim();
  if (!id) return null;
  const quoted = m[1];
  const label = quoted
    ? quoted.replace(/\\(.)/g, '$1').trim()
    : id.split('.')[0] || id;
  return { id, label: label || id };
}

/**
 * Parse `List-Post` per REQ-LIST-22. Returns the bare mailto address,
 * or null when the header is absent, blank, or the literal `NO`
 * reserved by RFC 2369 SS3.4 for "no posting allowed".
 */
export function parseListPostAddress(raw: string | null | undefined): string | null {
  const trimmed = (raw ?? '').trim();
  if (!trimmed) return null;
  if (/^NO$/i.test(trimmed)) return null;
  const bracketed = trimmed.match(/<mailto:([^>]+)>/i);
  if (bracketed) return bracketed[1] ?? null;
  const bare = trimmed.match(/^mailto:([^\s<>,]+)/i);
  if (bare) return bare[1] ?? null;
  return null;
}

/**
 * REQ-UNS-02: true when `List-Unsubscribe-Post` carries the RFC 8058
 * one-click marker. The header's only defined value is the literal
 * string `List-Unsubscribe=One-Click`; match case-insensitively since
 * some senders vary casing.
 */
export function hasOneClickPost(raw: string | null | undefined): boolean {
  if (!raw) return false;
  return /list-unsubscribe\s*=\s*one-click/i.test(raw);
}

/** The chosen unsubscribe mechanism per the `14-unsubscribe.md` priority order. */
export type UnsubscribeMechanism =
  | { kind: 'one-click'; url: string }
  | { kind: 'https'; url: string }
  | { kind: 'mailto'; url: string }
  | { kind: 'http-only'; url: string };

/**
 * Decide which unsubscribe mechanism to offer, given the raw
 * `List-Unsubscribe` and `List-Unsubscribe-Post` header values.
 *
 * Priority (REQ-UNS "Action: choose mechanism"):
 *   1. RFC 8058 one-click -- List-Unsubscribe-Post: One-Click plus an
 *      HTTPS List-Unsubscribe URL (REQ-UNS-20).
 *   2. Plain HTTPS URL (REQ-UNS-21).
 *   3. mailto: (REQ-UNS-22).
 *   4. A cleartext http: URL is never auto-actioned (REQ-UNS-04); it is
 *      returned as 'http-only' purely so the caller can still show the
 *      affordance (REQ-UNS-03 -- presence of *a* mechanism, even an
 *      unusable one, is what gates the button) and surface the warning
 *      on click instead of silently hiding.
 *
 * Returns null only when `List-Unsubscribe` is absent/carries no
 * recognised URL at all -- REQ-UNS-03's "no fallback" case.
 */
export function chooseUnsubscribeMechanism(
  listUnsubscribe: string | null | undefined,
  listUnsubscribePost: string | null | undefined,
): UnsubscribeMechanism | null {
  const urls = parseAngleBracketUrls(listUnsubscribe);
  if (urls.length === 0) return null;
  const oneClick = hasOneClickPost(listUnsubscribePost);
  const https = urls.find((u) => u.scheme === 'https');
  if (oneClick && https) return { kind: 'one-click', url: https.url };
  if (https) return { kind: 'https', url: https.url };
  const mailto = urls.find((u) => u.scheme === 'mailto');
  if (mailto) return { kind: 'mailto', url: mailto.url };
  const http = urls.find((u) => u.scheme === 'http');
  if (http) return { kind: 'http-only', url: http.url };
  return null;
}

/** The fields a `mailto:` URI can prefill in a compose window (REQ-UNS-22). */
export interface MailtoFields {
  to: string;
  subject: string;
  body: string;
}

/**
 * Parse a `mailto:` URI into compose fields per REQ-UNS-22 / REQ-LIST-21.
 * Tolerant of malformed percent-encoding -- falls back to the raw
 * (un-decoded) text rather than throwing.
 */
export function parseMailtoUri(uri: string): MailtoFields {
  const stripped = uri.replace(/^mailto:/i, '');
  const qIdx = stripped.indexOf('?');
  const addrPart = qIdx === -1 ? stripped : stripped.slice(0, qIdx);
  const queryPart = qIdx === -1 ? '' : stripped.slice(qIdx + 1);
  const to = safeDecode(addrPart);
  const params = new URLSearchParams(queryPart);
  return {
    to,
    subject: params.get('subject') ?? '',
    body: params.get('body') ?? '',
  };
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}
