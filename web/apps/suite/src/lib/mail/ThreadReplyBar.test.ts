/**
 * Unit tests for ThreadReplyBar's Reply-All visibility gate (re #163).
 *
 * hasMultipleRecipients must show the button IFF
 * compose.computeActualReplyAllCc(target, ...) — the exact function
 * compose.openReplyAll uses to build the real Cc, branching on
 * isFromSelf(target) between computeReplyAllCc (received message) and
 * computeOwnMessageReplyAllCc (own-sent message) — is non-empty. The
 * gate imports and calls the real function rather than approximating
 * its filter, so it cannot drift out of sync with what clicking Reply
 * All actually produces, in either branch:
 *
 *  - Received message: To/Cc minus own identities minus the sender.
 *  - Own-sent message: Cc minus own identities. To is NOT part of the
 *    Cc computation for this branch (it becomes the new message's To),
 *    so an own-sent message with an external To and no/self-only Cc
 *    must NOT show Reply All (the regression the coordinator caught:
 *    the first version of this fix unioned To+Cc unconditionally and
 *    still showed the button in that case).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ThreadReplyBar from './ThreadReplyBar.svelte';
import { buildSelfEmailSet } from './identity-match';
import type { Email, Identity } from './types';

// ── hoisted fixtures ───────────────────────────────────────────────────────────

const { mailMock, composeMock } = vi.hoisted(() => {
  const mailMock = {
    identities: new Map<string, Identity>(),
  };
  const composeMock = {
    isOpen: false,
    inlineMode: false,
    openReply: vi.fn().mockResolvedValue(undefined),
    openReplyAll: vi.fn(),
    openForward: vi.fn(),
  };
  return { mailMock, composeMock };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));

// Mock only the `compose` singleton (its network/store side effects); keep
// the real computeActualReplyAllCc (and the pure helpers it depends on) so
// the gate is exercised against the exact same code path openReplyAll uses.
vi.mock('../compose/compose.svelte', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../compose/compose.svelte')>();
  return { ...actual, compose: composeMock };
});

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));
vi.mock('../icons/ReplyIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/ReplyAllIcon.svelte', () => ({ default: () => null }));
vi.mock('../icons/ForwardIcon.svelte', () => ({ default: () => null }));

import { computeActualReplyAllCc } from '../compose/compose.svelte';

// ── helpers ────────────────────────────────────────────────────────────────────

function makeIdentity(email: string): Identity {
  return {
    id: `id-${email}`,
    name: '',
    email,
    replyTo: null,
    bcc: null,
    textSignature: '',
    htmlSignature: '',
    mayDelete: true,
  };
}

function makeEmail(overrides: Partial<Email>): Email {
  return {
    id: 'e-1',
    threadId: 'tid-1',
    mailboxIds: { 'mbx-inbox': true },
    keywords: {},
    from: [{ name: 'Sender', email: 'sender@example.test' }],
    to: null,
    cc: null,
    subject: 'Test',
    preview: 'preview',
    receivedAt: '2026-01-01T00:00:00Z',
    hasAttachment: false,
    attachments: [],
    reactions: null,
    snoozedUntil: null,
    blobId: 'blob-1',
    'header:List-ID:asText': null,
    ...overrides,
  };
}

const ALICE = 'alice@example.local';

/**
 * Render ThreadReplyBar for `email` and assert the Reply-All button's
 * presence matches computeActualReplyAllCc(email, ...) — the gate-vs-
 * click consistency contract. Also returns the computed Cc so callers
 * can assert on its contents (e.g. that it contains the expected
 * external address, not just that it is non-empty).
 */
function renderAndCheckGateConsistency(email: Email): ReturnType<typeof computeActualReplyAllCc> {
  const identities = Array.from(mailMock.identities.values());
  const selfEmails = buildSelfEmailSet(identities);
  const actualCc = computeActualReplyAllCc(email, selfEmails, identities);

  render(ThreadReplyBar, { props: { target: email } });

  const button = screen.queryByRole('button', { name: 'msg.replyAll' });
  if (actualCc.length > 0) {
    expect(button).toBeInTheDocument();
  } else {
    expect(button).not.toBeInTheDocument();
  }
  return actualCc;
}

describe('ThreadReplyBar Reply-All visibility (re #163)', () => {
  beforeEach(() => {
    mailMock.identities = new Map([[`id-${ALICE}`, makeIdentity(ALICE)]]);
    composeMock.isOpen = false;
    composeMock.inlineMode = false;
  });

  describe('received message (From is not the signed-in identity)', () => {
    it("suppresses Reply All when every extra To/Cc entry is the user's own identity", () => {
      const email = makeEmail({
        to: [{ name: 'Alice', email: ALICE }],
        cc: [{ name: 'Alice', email: ALICE }],
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc).toHaveLength(0);
    });

    it('shows Reply All when at least one Cc entry is outside the identity set', () => {
      const email = makeEmail({
        to: [{ name: 'Alice', email: ALICE }],
        cc: [{ name: 'External', email: 'external@example.test' }],
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc.map((a) => a.email)).toEqual(['external@example.test']);
    });

    it('shows Reply All when To has an external second recipient and no Cc', () => {
      const email = makeEmail({
        to: [
          { name: 'Alice', email: ALICE },
          { name: 'External', email: 'external@example.test' },
        ],
        cc: null,
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc.map((a) => a.email)).toEqual(['external@example.test']);
    });

    it('suppresses Reply All when To has only the single own-identity recipient and no Cc', () => {
      const email = makeEmail({ to: [{ name: 'Alice', email: ALICE }], cc: null });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc).toHaveLength(0);
    });
  });

  describe('own-sent message (From IS the signed-in identity)', () => {
    // computeOwnMessageReplyAllCc only ever draws from parent.cc — the To
    // list becomes the new message's To, never its Cc — so these cases
    // hinge entirely on cc, not to.

    it('suppresses Reply All for an external To with no Cc (re #163 regression)', () => {
      const email = makeEmail({
        from: [{ name: 'Alice', email: ALICE }],
        to: [{ name: 'External', email: 'external@example.test' }],
        cc: null,
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc).toHaveLength(0);
    });

    it('suppresses Reply All for an external To with a self-only Cc', () => {
      const email = makeEmail({
        from: [{ name: 'Alice', email: ALICE }],
        to: [{ name: 'External', email: 'external@example.test' }],
        cc: [{ name: 'Alice', email: ALICE }],
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc).toHaveLength(0);
    });

    it('shows Reply All for an external To with an external Cc', () => {
      const email = makeEmail({
        from: [{ name: 'Alice', email: ALICE }],
        to: [{ name: 'External', email: 'external@example.test' }],
        cc: [{ name: 'Bob', email: 'bob@example.test' }],
      });
      const cc = renderAndCheckGateConsistency(email);
      expect(cc.map((a) => a.email)).toEqual(['bob@example.test']);
    });

    it('does not add parent.to local-domain alias to Cc on own-sent reply-all (re #146)', () => {
      // The user (alice@example.local) sent a message To: info@example.local
      // (a local alias, not a registered identity). On own-sent reply-all,
      // parent.to becomes the new reply's To field — info@ must NOT also
      // appear in Cc, even though it shares the identity domain.
      mailMock.identities = new Map([[`id-${ALICE}`, makeIdentity(ALICE)]]);
      const email = makeEmail({
        from: [{ name: 'Alice', email: ALICE }],
        to: [{ name: null, email: 'info@example.local' }],
        cc: null,
      });
      const cc = renderAndCheckGateConsistency(email);
      // computeOwnMessageReplyAllCc: draws from parent.cc (null) → empty.
      // localAliasesForCc: ownMessage=true → skips parent.to → no aliases.
      expect(cc).toHaveLength(0);
    });
  });

  describe('upstream-alias case (re #146)', () => {
    // X-Herold-Recipient is the resolved mailbox identity; the original alias
    // survives only in the visible To header.
    it('shows Reply All and includes the local-domain To alias when X-Herold-Recipient is the resolved registered identity', () => {
      // info@example.local is a local alias (not registered); alice@example.local
      // is the resolved identity herold actually received the message at.
      const email = makeEmail({
        from: [{ name: 'Sender', email: 'sender@external.test' }],
        to: [{ name: null, email: 'info@example.local' }],
        'header:X-Herold-Recipient:asText': ALICE,
      });
      const cc = renderAndCheckGateConsistency(email);
      // computeReplyAllCc: info@ is in To, not a self-address → included.
      // localAliasesForCc: X-Herold-Recipient is registered → source 1 no-ops;
      //   To has info@example.local on identity domain → source 2 finds it.
      //   But it's already in cc from computeReplyAllCc → deduplicated.
      // Net: cc contains info@example.local (exactly once).
      expect(cc.map((a) => a.email)).toEqual(['info@example.local']);
    });
  });
});
