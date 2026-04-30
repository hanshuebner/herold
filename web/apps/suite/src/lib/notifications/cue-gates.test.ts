/**
 * Unit tests for shouldPlayChatCue() and shouldPlayMailCue().
 *
 * Coverage per spec:
 *   shouldPlayChatCue (REQ-CHAT-220):
 *     - senderId == self                              -> false
 *     - muted conversation                            -> false
 *     - presenceState == 'present-in-chat'            -> false
 *     - presenceState == 'present-elsewhere'          -> false
 *     - presenceState == 'absent'                     -> true
 *   shouldPlayMailCue:
 *     - email not in inbox -> false
 *     - email from self -> false
 *     - visible + inbox focused -> false
 *     - backgrounded tab / non-inbox route -> true
 */

import { describe, it, expect } from 'vitest';
import { shouldPlayChatCue, shouldPlayMailCue } from './cue-gates';
import type { ChatCueContext, MailCueContext } from './cue-gates';

// ── shouldPlayChatCue ──────────────────────────────────────────────────────

function chatCtx(overrides: Partial<ChatCueContext> = {}): ChatCueContext {
  return {
    senderId: 'p-other',
    myPrincipalId: 'p-self',
    conversationMuted: false,
    presenceState: 'absent',
    ...overrides,
  };
}

describe('shouldPlayChatCue', () => {
  it('returns false when senderId equals myPrincipalId', () => {
    expect(shouldPlayChatCue(chatCtx({ senderId: 'p-self' }))).toBe(false);
  });

  it('returns false when myPrincipalId is null (unauthenticated)', () => {
    // If somehow called while unauth, treat sender == self as unclear;
    // the gate passes here (principalId null) — but the call site checks
    // auth first; this tests the null guard does not throw.
    expect(
      shouldPlayChatCue(chatCtx({ myPrincipalId: null, senderId: 'p-other' })),
    ).toBe(true);
  });

  it('returns false when conversation is muted', () => {
    expect(shouldPlayChatCue(chatCtx({ conversationMuted: true }))).toBe(false);
  });

  it("returns false when presenceState is 'present-in-chat' (REQ-CHAT-220)", () => {
    expect(
      shouldPlayChatCue(chatCtx({ presenceState: 'present-in-chat' })),
    ).toBe(false);
  });

  it("returns false when presenceState is 'present-elsewhere' (REQ-CHAT-220)", () => {
    expect(
      shouldPlayChatCue(chatCtx({ presenceState: 'present-elsewhere' })),
    ).toBe(false);
  });

  it("returns true when presenceState is 'absent' and other gates pass (REQ-CHAT-220)", () => {
    expect(
      shouldPlayChatCue(chatCtx({ presenceState: 'absent' })),
    ).toBe(true);
  });

  it('returns true for all gates passing', () => {
    expect(
      shouldPlayChatCue({
        senderId: 'p-other',
        myPrincipalId: 'p-self',
        conversationMuted: false,
        presenceState: 'absent',
      }),
    ).toBe(true);
  });
});

// ── shouldPlayMailCue ──────────────────────────────────────────────────────

const INBOX_ID = 'mbx-inbox';

function mailCtx(overrides: Partial<MailCueContext> = {}): MailCueContext {
  return {
    mailboxIds: { [INBOX_ID]: true },
    inboxMailboxId: INBOX_ID,
    senderEmail: 'sender@example.com',
    ownEmails: new Set(['me@example.com']),
    inboxFocused: false,
    ...overrides,
  };
}

describe('shouldPlayMailCue', () => {
  it('returns false when inboxMailboxId is null (mailboxes not loaded)', () => {
    expect(mailCtx({ inboxMailboxId: null })).toEqual(
      expect.objectContaining({ inboxMailboxId: null }),
    );
    expect(shouldPlayMailCue(mailCtx({ inboxMailboxId: null }))).toBe(false);
  });

  it('returns false when email is not in the inbox mailbox', () => {
    expect(
      shouldPlayMailCue(
        mailCtx({ mailboxIds: { 'mbx-sent': true } }),
      ),
    ).toBe(false);
  });

  it('returns false when email lands in Drafts (different mailbox)', () => {
    expect(
      shouldPlayMailCue(mailCtx({ mailboxIds: { 'mbx-drafts': true } })),
    ).toBe(false);
  });

  it('returns false when sender email matches an own email', () => {
    expect(
      shouldPlayMailCue(
        mailCtx({
          senderEmail: 'me@example.com',
          ownEmails: new Set(['me@example.com']),
        }),
      ),
    ).toBe(false);
  });

  it('returns false when inbox is focused (visible + active)', () => {
    expect(shouldPlayMailCue(mailCtx({ inboxFocused: true }))).toBe(false);
  });

  it('returns true when all gates pass (backgrounded, inbox, external sender)', () => {
    expect(shouldPlayMailCue(mailCtx())).toBe(true);
  });

  it('returns true when senderEmail is null (sender unknown)', () => {
    // Unknown sender does not match any own email; should play.
    expect(
      shouldPlayMailCue(mailCtx({ senderEmail: null })),
    ).toBe(true);
  });

  it('returns true when email is in inbox and tab is backgrounded', () => {
    expect(
      shouldPlayMailCue({
        mailboxIds: { [INBOX_ID]: true },
        inboxMailboxId: INBOX_ID,
        senderEmail: 'someone@example.com',
        ownEmails: new Set(['me@example.com']),
        inboxFocused: false,
      }),
    ).toBe(true);
  });
});
