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
  // Body properties — populated when the thread reader fetches them.
  bodyValues?: Record<string, EmailBodyValue>;
  htmlBody?: EmailBodyPart[];
  textBody?: EmailBodyPart[];
  attachments?: EmailBodyPart[];
  // Threading-relevant headers for reply / forward.
  messageId?: string[] | null;
  inReplyTo?: string[] | null;
  references?: string[] | null;
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
  'bodyValues',
  'htmlBody',
  'textBody',
  'attachments',
  'messageId',
  'inReplyTo',
  'references',
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
