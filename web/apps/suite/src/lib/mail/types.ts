/**
 * RFC 8621 (JMAP for Mail) datatypes — only the properties the suite's
 * inbox / thread / compose flows actually read or write.
 *
 * Cross-reference: docs/requirements/01-data-model.md.
 */

/**
 * RFC 8621 §6 — Identity. The set of From / Reply-To / Bcc / signatures
 * the user may legitimately send as.
 */
export interface Identity {
  id: string;
  name: string;
  email: string;
  replyTo: Address[] | null;
  bcc: Address[] | null;
  textSignature: string;
  htmlSignature: string;
  mayDelete: boolean;
  /** Herold extension: blob ID for the identity's avatar image. */
  avatarBlobId?: string | null;
  /** Herold extension: whether to embed an X-Face / Face header on outbound mail. */
  xFaceEnabled?: boolean;
  /**
   * Herold extension: ISO 8601 timestamp at which the user proved
   * ownership of the identity's email (REQ-IDENT-10). Drives the
   * verified-gate in compose's reply-identity match (REQ-MAIL-12a) and
   * the picker's disabled state for unverified identities (REQ-MAIL-12).
   *
   * Tri-state semantics, deliberately:
   *   - field absent (undefined)  → server has not yet shipped the
   *     property; the suite treats the identity as verified for
   *     legacy compatibility. Once the server emits it, the gate
   *     becomes effective without further suite changes.
   *   - explicit null             → server says "not verified"; the
   *     identity cannot be the result of the reply-identity match.
   *   - ISO timestamp string      → verified.
   */
  verifiedAt?: string | null;
  /**
   * Herold extension: ISO 8601 timestamp at which an
   * `Identity/set { create }` last issued a fresh verification token
   * that has not yet expired (REQ-IDENT-30..36 server-side,
   * REQ-SET-IDENT-02 in the suite). Used to distinguish the
   * "verification pending" chip (token live, awaiting click/code) from
   * the "unverified" chip (no live token).
   *
   * Tri-state semantics, same shape as verifiedAt:
   *   - field absent (undefined)  → server has not yet shipped the
   *     property; the suite treats the identity as "unverified" when
   *     verifiedAt is null and "verified" when verifiedAt is set.
   *   - explicit null             → server says "no live token";
   *     combined with a null verifiedAt this is the unverified state.
   *   - ISO timestamp string      → token live; combined with a null
   *     verifiedAt this is the verification-pending state.
   */
  verificationPendingSince?: string | null;
  /**
   * Herold extension: marks the user's default From identity
   * (REQ-SET-IDENT-04). Exactly one Identity per principal carries
   * `isDefault: true`; the suite enforces the singleton invariant
   * client-side via the `setDefaultIdentity` action which clears the
   * previous default in the same `Identity/set update` batch.
   *
   * When the field is absent the suite falls back to "first verified
   * identity in the list" per REQ-SET-02 for legacy compatibility
   * (the server-side extension lands separately under REQ-IDENT-70).
   */
  isDefault?: boolean | null;
}

export interface Mailbox {
  id: string;
  name: string;
  role: string | null;
  parentId: string | null;
  sortOrder: number;
  totalEmails: number;
  unreadEmails: number;
  totalThreads: number;
  unreadThreads: number;
  /** Suite custom property per `docs/design/web/notes/server-contract.md` § Mailbox colour. Optional. */
  color?: string | null;
}

export interface Address {
  name: string | null;
  email: string;
}

/**
 * RFC 8621 §3 — a Thread groups Emails. The `emailIds` are in the order
 * the server thinks they should be displayed (typically chronological,
 * oldest first).
 */
export interface Thread {
  id: string;
  emailIds: string[];
}

/**
 * RFC 8621 §4.1.4 — body part metadata. The suite reads partId, type, charset,
 * disposition, name; the rest of RFC 8621 §4.1.4 is kept on the wire but
 * not surfaced.
 */
export interface EmailBodyPart {
  partId: string | null;
  blobId: string | null;
  size: number;
  type: string;
  charset: string | null;
  disposition: string | null;
  name: string | null;
  cid: string | null;
  /**
   * Intrinsic image width in pixels, as decoded by the server bodymeta
   * worker (issue #47). Absent for older messages not yet re-indexed, or
   * for formats other than png/jpeg/gif. When present together with
   * `height`, the client uses the pair to set `aspect-ratio` on the
   * rendered `<img>` so the browser can reserve layout space before the
   * image bytes arrive.
   */
  width?: number;
  /**
   * Intrinsic image height in pixels. See `width` for semantics.
   */
  height?: number;
}

/**
 * RFC 8621 §4.1.4 — decoded body content for a leaf part. Returned via
 * `bodyValues` keyed by `partId` when `fetchHTMLBodyValues` /
 * `fetchTextBodyValues` is set on `Email/get`.
 */
export interface EmailBodyValue {
  value: string;
  isEncodingProblem: boolean;
  isTruncated: boolean;
}

/**
 * `Email` properties the suite reads. Sparse — populated incrementally:
 * the inbox list fetch sets the list-rendering subset, the thread
 * reader fetch adds bodyValues / htmlBody / textBody / to / cc / etc.
 */
export interface Email {
  id: string;
  threadId: string;
  mailboxIds: Record<string, true>;
  keywords: Record<string, true | undefined>;
  from: Address[] | null;
  to: Address[] | null;
  cc?: Address[] | null;
  bcc?: Address[] | null;
  replyTo?: Address[] | null;
  sender?: Address[] | null;
  subject: string | null;
  preview: string;
  receivedAt: string;
  sentAt?: string | null;
  hasAttachment: boolean;
  /**
   * JMAP Snooze extension wake-up deadline (RFC 8621 +
   * draft-ietf-jmap-snooze). null when the message is not snoozed.
   */
  snoozedUntil?: string | null;
  // Body properties — populated when the thread reader fetches them.
  bodyValues?: Record<string, EmailBodyValue>;
  htmlBody?: EmailBodyPart[];
  textBody?: EmailBodyPart[];
  attachments?: EmailBodyPart[];
  // Threading-relevant headers for reply / forward.
  messageId?: string[] | null;
  inReplyTo?: string[] | null;
  references?: string[] | null;
  /**
   * Email reactions extension property per REQ-MAIL-170. Shape:
   * `{ "<emoji>": ["<principal-id>", ...] }`. Sparse — emojis with no
   * current reactors are absent. Capability:
   * https://netzhansa.com/jmap/email-reactions
   */
  reactions?: Record<string, string[]> | null;
  /**
   * RFC 8621 §4.1.1 — the blob ID of the raw RFC 5322 message source.
   * Used by the "View original" action to open the undecoded wire bytes
   * in a new browser tab via the JMAP blob download endpoint.
   */
  blobId: string;
  /**
   * RFC 8621 §4.1.1 — total size of the message in octets. Used as a
   * tiebreaker when selecting the representative among same-Message-ID
   * copies: the externally-received copy accrues transit headers
   * (Received:, DKIM-Signature:, ARC-*, etc.) that the locally-submitted
   * Sent copy does not, so it is consistently larger and wins.
   * Optional because the property is only populated when callers include
   * `'size'` in the requested properties projection.
   */
  size?: number;
  /**
   * Raw header values for list detection (REQ-MAIL-191). The suite
   * fetches `header:List-ID:asText` to determine mailing-list mail.
   */
  'header:List-ID:asText'?: string | null;
  /**
   * RFC 2369 mailing-list headers (REQ-LIST-01) beyond List-ID: the
   * chip's popover actions (REQ-LIST-20..22). All optional/nullable --
   * absent when the sender did not set the header, which the chip
   * treats as "hide this action" rather than an error.
   */
  'header:List-Help:asText'?: string | null;
  'header:List-Subscribe:asText'?: string | null;
  'header:List-Post:asText'?: string | null;
  'header:List-Owner:asText'?: string | null;
  'header:List-Archive:asText'?: string | null;
  /**
   * RFC 2369 List-Unsubscribe and RFC 8058 List-Unsubscribe-Post
   * (REQ-UNS-01/02). Drives the thread-level Unsubscribe affordance in
   * docs/design/web/requirements/14-unsubscribe.md.
   */
  'header:List-Unsubscribe:asText'?: string | null;
  'header:List-Unsubscribe-Post:asText'?: string | null;
  /**
   * Face: header (base64-encoded PNG/JPEG avatar per the Face convention).
   * Fetched via `header:Face:asText` extension for the avatar resolver
   * tier-2 path.
   */
  'header:Face:asText'?: string | null;
  /**
   * X-Face: header (legacy monochrome avatar format). Fetched via
   * `header:X-Face:asText`. V1 of the resolver skips X-Face decoding
   * (the format requires a bespoke decoder); the field is fetched so the
   * resolver can signal "X-Face present" in future versions.
   */
  'header:X-Face:asText'?: string | null;
  /**
   * Synthetic per-recipient header injected by herold at Email/get
   * render time per REQ-FLOW-34. Carries the canonical RCPT TO that
   * produced this fan-out row, which the compose-side reply-identity
   * match consumes (REQ-MAIL-12a step 2). Absent on outbound messages
   * (REQ-FLOW-35) and on legacy / imported mail; the match degrades
   * to the To/Cc scan when missing.
   */
  'header:X-Herold-Recipient:asText'?: string | null;
  /**
   * `Delivered-To` / `X-Original-To` -- ordinary RFC 822 headers an
   * upstream MTA adds when it locally alias-expands the envelope
   * recipient before relaying to herold (e.g. Postfix virtual alias
   * delivery). Herold never strips, rewrites, or synthesises these; they
   * pass through verbatim from the stored raw message. The compose-side
   * reply-identity match consumes them as a fallback signal (REQ-MAIL-12a
   * step 3b) for cross-domain alias forwards where `X-Herold-Recipient`
   * (the literal envelope RCPT TO herold itself accepted, REQ-FLOW-33)
   * names a domain none of the account's identities own, but the
   * upstream MTA's `Delivered-To` names the identity the mail was
   * actually meant for. Absent on the common case (no upstream alias
   * hop) and on outbound/self-composed mail.
   */
  'header:Delivered-To:asText'?: string | null;
  'header:X-Original-To:asText'?: string | null;
  /**
   * Herold extension (REQ-EXTIMG-BG-20): true while the message body is
   * waiting for the background-internalize worker to rewrite its external
   * image references. Email/get serves placeholder data URIs in place of
   * external `<img src>` while this flag is set; the SPA surfaces a badge
   * (mailbox row) and a banner (thread reader) so the user understands why
   * the images have not loaded yet. Optional because the property is only
   * populated when callers include `internalizePending` in the requested
   * properties projection.
   */
  internalizePending?: boolean;
  /**
   * Herold extension (issue #162, REQ-EXTIMG-71/73): the number of
   * external images this message had that could not be internalized
   * and were permanently placeholdered in the stored body
   * (REQ-EXTIMG-BG-14). 0 or absent means every image internalized, or
   * the message never had external images. A message with a positive
   * count can be retried via `Email/retryImages`
   * (capability https://netzhansa.com/jmap/email-image-retry); the
   * origin URLs never reach the client. Optional because the property
   * is only populated when callers include `failedImageCount` in the
   * requested properties projection.
   */
  failedImageCount?: number;
}

/** The properties projection the suite requests for list rendering. */
export const EMAIL_LIST_PROPERTIES = [
  'id',
  'threadId',
  'mailboxIds',
  'keywords',
  'from',
  'to',
  'subject',
  'preview',
  'receivedAt',
  'hasAttachment',
  'snoozedUntil',
  'internalizePending',
  'failedImageCount',
] as const;

/** The properties projection the suite requests for thread / reading-pane rendering. */
export const EMAIL_BODY_PROPERTIES = [
  'id',
  'threadId',
  'mailboxIds',
  'keywords',
  'from',
  'to',
  'cc',
  'bcc',
  'replyTo',
  'sender',
  'subject',
  'preview',
  'receivedAt',
  'sentAt',
  'hasAttachment',
  'snoozedUntil',
  'bodyValues',
  'htmlBody',
  'textBody',
  'attachments',
  'messageId',
  'inReplyTo',
  'references',
  'reactions',
  'blobId',
  'size',
  'header:List-ID:asText',
  'header:List-Help:asText',
  'header:List-Subscribe:asText',
  'header:List-Post:asText',
  'header:List-Owner:asText',
  'header:List-Archive:asText',
  'header:List-Unsubscribe:asText',
  'header:List-Unsubscribe-Post:asText',
  'header:Face:asText',
  'header:X-Face:asText',
  'header:X-Herold-Recipient:asText',
  'header:Delivered-To:asText',
  'header:X-Original-To:asText',
  'internalizePending',
  'failedImageCount',
] as const;

/**
 * The plain-text body of an email, if any. RFC 8621 4.1.4 defines
 * `textBody` as the ORDERED LIST of parts to display for the plain-text
 * rendering; for the common case that list has exactly one entry, so this
 * returns that entry's value unchanged. When a multipart/mixed message
 * contributes more than one leaf to `textBody` (e.g. a forwarder's own
 * note ahead of the forwarded original, re #258), every part's value is
 * concatenated in order, separated by a blank line so the parts read as
 * distinct paragraphs rather than running together.
 *
 * Returns null when no text part is present (rare; HTML-only emails happen
 * but are usually accompanied by a plain-text alternative) or when none of
 * the listed parts have a resolved bodyValue.
 */
export function emailTextBody(email: Email): string | null {
  const parts = email.textBody;
  if (!parts || parts.length === 0) return null;
  const values: string[] = [];
  for (const part of parts) {
    if (!part.partId) continue;
    const value = email.bodyValues?.[part.partId]?.value;
    if (value === undefined) continue;
    values.push(value);
  }
  if (values.length === 0) return null;
  return values.join('\n\n');
}

/**
 * HTML-escapes a plain-text string for safe injection into an HTML
 * fragment. Used to render a `text/plain` part that RFC 8621 4.1.4 has
 * promoted into `htmlBody` (a leaf with no HTML alternative "at its
 * level", e.g. a forwarder's own note directly under multipart/mixed,
 * re #258) — the value is untrusted message content and must never be
 * interpreted as markup.
 */
function escapeHtmlText(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/**
 * The HTML body of an email, if any. The suite prefers HTML when both are
 * present (`docs/requirements/02-mail-basics.md` REQ-MAIL-02).
 *
 * RFC 8621 4.1.4 defines `htmlBody` as the ORDERED LIST of parts to
 * display as the HTML rendering, each rendered according to its own
 * type: a `text/html` part is injected as sanitized HTML; a `text/plain`
 * part that RFC 8621 has promoted into the list (a leaf with no HTML
 * sibling at its level) is HTML-escaped and wrapped in `<pre>` so it
 * renders as literal, whitespace-preserving text rather than markup
 * (re #258 — the forwarder's own note ahead of the forwarded original).
 * The rendered parts are concatenated in document order; for the common
 * single-part case this returns that part's value unchanged.
 *
 * Returns null — "no HTML rendering" — when `htmlBody` contains no
 * GENUINE `text/html` entry, even though the list is non-empty. RFC
 * 8621's symmetric-fill rule means a bare plain-text message (no HTML
 * part anywhere) gets its sole part promoted into `htmlBody` too, so a
 * naive "non-empty htmlBody means HTML exists" check would route every
 * plain-text-only message through this HTML rendering path and starve
 * `MessageAccordion`'s plain-text quote-collapse branch (re #258
 * follow-up: this silently disabled the "Show trimmed content" toggle,
 * issue #233, for every plain-text-only message). A message with at
 * least one real `text/html` entry (e.g. #258's [note, forwarded-html])
 * still renders every htmlBody entry as documented above.
 *
 * `overrides` lets a caller substitute the value for one or more partIds
 * — used by `MessageAccordion` to splice in the full (un-truncated) text
 * fetched via the blob-download recovery path (`html-body-full.ts`,
 * Forgejo #48) for whichever htmlBody part the server capped, without
 * disturbing the other parts' inline bodyValues.
 *
 * Also returns null when no html part is present or when none of the
 * listed parts have a resolved value (inline or overridden).
 */
export function emailHtmlBody(
  email: Email,
  overrides?: Record<string, string>,
): string | null {
  const parts = email.htmlBody;
  if (!parts || parts.length === 0) return null;
  const hasGenuineHtmlPart = parts.some((part) => part.type.toLowerCase() === 'text/html');
  if (!hasGenuineHtmlPart) return null;
  const rendered: string[] = [];
  for (const part of parts) {
    if (!part.partId) continue;
    const value = overrides?.[part.partId] ?? email.bodyValues?.[part.partId]?.value;
    if (value === undefined) continue;
    if (part.type.toLowerCase() === 'text/html') {
      rendered.push(value);
    } else {
      rendered.push(`<pre>${escapeHtmlText(value)}</pre>`);
    }
  }
  if (rendered.length === 0) return null;
  return rendered.join('');
}
