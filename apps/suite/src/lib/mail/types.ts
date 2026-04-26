/**
 * RFC 8621 (JMAP for Mail) datatypes — only the properties tabard's
 * inbox / thread / compose flows actually read or write.
 *
 * Cross-reference: docs/requirements/01-data-model.md.
 */

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
  /** Tabard custom property per `notes/server-contract.md` § Mailbox colour. Optional. */
  color?: string | null;
}

export interface Address {
  name: string | null;
  email: string;
}

/**
 * `Email` properties tabard requests for the inbox-list rendering shape.
 * Full property set per RFC 8621 §4 is broader; we read what we need and
 * ask herold to project just those.
 */
export interface Email {
  id: string;
  threadId: string;
  mailboxIds: Record<string, true>;
  keywords: Record<string, true | undefined>;
  from: Address[] | null;
  to: Address[] | null;
  subject: string | null;
  preview: string;
  receivedAt: string;
  hasAttachment: boolean;
}

/** The properties projection tabard requests by default for list rendering. */
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
