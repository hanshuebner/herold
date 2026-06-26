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
   * Raw header values for list detection (REQ-MAIL-191). The suite
   * fetches `header:List-ID:asText` to determine mailing-list mail.
   */
  'header:List-ID:asText'?: string | null;
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
  'header:List-ID:asText',
  'header:Face:asText',
  'header:X-Face:asText',
  'header:X-Herold-Recipient:asText',
  'internalizePending',
] as const;

/**
 * The plain-text body of an email, if any. Returns null when no text part
 * is present (rare; HTML-only emails happen but are usually accompanied
 * by a plain-text alternative).
 */
export function emailTextBody(email: Email): string | null {
  const part = email.textBody?.[0];
  if (!part?.partId) return null;
  return email.bodyValues?.[part.partId]?.value ?? null;
}

/**
 * The HTML body of an email, if any. The suite prefers HTML when both are
 * present (`docs/requirements/02-mail-basics.md` REQ-MAIL-02).
 */
export function emailHtmlBody(email: Email): string | null {
  const part = email.htmlBody?.[0];
  if (!part?.partId) return null;
  return email.bodyValues?.[part.partId]?.value ?? null;
}
