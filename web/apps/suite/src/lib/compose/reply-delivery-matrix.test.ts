/**
 * End-to-end delivery-shape matrix for the reply-identity flow (re #280).
 *
 * `reply-identity.ts` / `compose.svelte.ts` has absorbed fixes across #146,
 * #164, #166, #254 (open) and #280, every one unit-tested at the pure-
 * function level with hand-built option objects, never as a single test
 * driving a real header block through both `selectReplyIdentity` (From) and
 * `localAliasesForCc` (Cc) together. That gap is why #280 shipped a fix that
 * addressed a different bug than the one reported (see git history on this
 * file): the unit tests exercised same-domain aliases exhaustively but no
 * row ever modelled a genuinely IMAP-imported, cross-domain forwarded
 * message — the actual shape of the reported thread.
 *
 * This file is the missing contract test. Each row is built from a raw
 * RFC 822 header block (parsed by the small helpers below, not hand-
 * assigned `to`/`header:*` fields) so the test exercises address/header
 * extraction, not just the selection algorithm. Every row asserts BOTH the
 * resulting From identity and the resulting Cc list, and the From address
 * is asserted to never also appear in Cc.
 */

import { describe, it, expect } from 'vitest';
import { selectReplyIdentity, localAliasesForCc } from './reply-identity';
import type { Address, Email, Identity } from '../mail/types';

// ── Raw-header parsing helpers ──────────────────────────────────────
//
// Deliberately minimal (no folding, no quoted-string commas) — enough to
// turn a literal header block into the same `to`/`from`/`header:X:asText`
// shape the JMAP layer hands the reply-identity code, so fixtures are
// transcribed from real headers rather than pre-split into fields.

function parseAddressList(raw: string): Address[] {
  return raw.split(',').map((entry) => {
    const trimmed = entry.trim();
    const angleMatch = trimmed.match(/^(.*)<([^>]+)>$/);
    if (angleMatch) {
      const name = angleMatch[1]!.trim().replace(/^"|"$/g, '') || null;
      return { name, email: angleMatch[2]!.trim() };
    }
    return { name: null, email: trimmed };
  });
}

function parseHeaderBlock(raw: string): Map<string, string> {
  const headers = new Map<string, string>();
  for (const line of raw.split('\n')) {
    if (!line.trim()) continue;
    const colon = line.indexOf(':');
    if (colon < 0) continue;
    const key = line.slice(0, colon).trim();
    const value = line.slice(colon + 1).trim();
    headers.set(key.toLowerCase(), value);
  }
  return headers;
}

/**
 * Build an `Email` fixture from a raw header block. `To`/`Cc`/`From` become
 * address arrays; every other header is projected as `header:<Name>:asText`
 * matching the properties `EMAIL_BODY_PROPERTIES` requests (see types.ts).
 */
function emailFromHeaders(rawHeaders: string, overrides: Partial<Email> = {}): Email {
  const headers = parseHeaderBlock(rawHeaders);
  const base: Email = {
    id: 'e1',
    threadId: 't1',
    mailboxIds: {},
    keywords: {},
    from: headers.has('from') ? parseAddressList(headers.get('from')!) : null,
    to: headers.has('to') ? parseAddressList(headers.get('to')!) : null,
    cc: headers.has('cc') ? parseAddressList(headers.get('cc')!) : null,
    subject: null,
    preview: '',
    receivedAt: '2026-05-10T00:00:00Z',
    hasAttachment: false,
    blobId: 'blob-1',
  };
  const projected: Record<string, string> = {};
  for (const [key, value] of headers) {
    if (key === 'from' || key === 'to' || key === 'cc') continue;
    // Re-derive the canonical header casing for the well-known headers this
    // suite projects (types.ts EMAIL_BODY_PROPERTIES); anything else keeps
    // its as-written casing.
    const canonical: Record<string, string> = {
      'x-herold-recipient': 'X-Herold-Recipient',
      'delivered-to': 'Delivered-To',
      'x-original-to': 'X-Original-To',
    };
    const name = canonical[key] ?? key;
    projected[`header:${name}:asText`] = value;
  }
  return { ...base, ...projected, ...overrides };
}

function makeIdentity(email: string, overrides: Partial<Identity> = {}): Identity {
  return {
    id: `id-${email}`,
    name: email.split('@')[0]!,
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: false,
    ...overrides,
  };
}

// The account's actual registered-identity set for the classic-computing.de
// family, per the production `jmap_identities` row the verifier queried for
// #280: exactly one row, verified, no `.org` identity of any kind.
const VORSITZ = makeIdentity('vorsitz@classic-computing.de', {
  verifiedAt: '2026-01-01T00:00:00Z',
});

interface MatrixRow {
  name: string;
  headers: string;
  identities: Identity[];
  expectFromEmail: string;
  expectCc: string[];
}

const MATRIX: MatrixRow[] = [
  {
    name: 'direct delivery to a registered identity (X-Herold-Recipient exact match)',
    headers: `From: correspondent@elsewhere.test
To: vorsitz@classic-computing.de
X-Herold-Recipient: vorsitz@classic-computing.de`,
    identities: [VORSITZ],
    expectFromEmail: 'vorsitz@classic-computing.de',
    expectCc: [],
  },
  {
    name: 'same-domain alias, X-Herold-Recipient present (#146/#164)',
    headers: `From: correspondent@elsewhere.test
To: info@classic-computing.de
X-Herold-Recipient: info@classic-computing.de`,
    identities: [VORSITZ],
    expectFromEmail: 'vorsitz@classic-computing.de',
    expectCc: ['info@classic-computing.de'],
  },
  {
    name: 'same-domain alias, visible To only, no X-Herold-Recipient (older / imported mail)',
    headers: `From: correspondent@elsewhere.test
To: info@classic-computing.de`,
    identities: [VORSITZ],
    expectFromEmail: 'vorsitz@classic-computing.de',
    expectCc: ['info@classic-computing.de'],
  },
  {
    name: 'CROSS-DOMAIN alias forward via Delivered-To/X-Original-To, no X-Herold-Recipient (#280, IMAP-imported thread t2583)',
    // Verbatim production header block for issue #280's thread t2583.
    headers: `From: Verein Contact <chair@classic-computing.org>
To: Verein zum Erhalt klassischer Computer <info@classic-computing.org>
Delivered-To: vorsitz@classic-computing.de
X-Original-To: info@classic-computing.org`,
    identities: [VORSITZ],
    expectFromEmail: 'vorsitz@classic-computing.de',
    expectCc: ['info@classic-computing.org'],
  },
  {
    name: 'IMAP-imported message with no herold-specific headers at all, To is the identity directly',
    headers: `From: correspondent@elsewhere.test
To: vorsitz@classic-computing.de`,
    identities: [VORSITZ],
    expectFromEmail: 'vorsitz@classic-computing.de',
    expectCc: [],
  },
];

describe('reply delivery-shape matrix (From + Cc together, re #280)', () => {
  for (const row of MATRIX) {
    it(row.name, () => {
      const parent = emailFromHeaders(row.headers);
      const identity = selectReplyIdentity(parent, row.identities, VORSITZ);
      const cc = localAliasesForCc(parent, row.identities, /* ownMessage */ false);

      expect(identity.email).toBe(row.expectFromEmail);
      expect(cc).toEqual(row.expectCc);

      // Guard: the selected From address must never also appear in Cc —
      // getting this wrong ships a self-CC on every reply.
      expect(cc).not.toContain(identity.email);
    });
  }
});
