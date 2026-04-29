/**
 * Pure gating helpers that decide whether an in-app audio cue should fire.
 *
 * Kept separate from the sounds singleton so they are easy to unit-test
 * without DOM or Audio mocks.
 *
 * Quiet-hours gate (REQ-PUSH-97) is not yet implemented; a TODO is
 * left below for when the quiet-hours store lands.
 */

import type { Message } from '../chat/types';
import type { Email } from '../mail/types';

/** Context required to evaluate the chat-cue gate. */
export interface ChatCueContext {
  /** The senderId on the incoming message. */
  senderId: string;
  /** The authenticated principal's own id. */
  myPrincipalId: string | null;
  /** Whether the conversation is muted. */
  conversationMuted: boolean;
  /**
   * Whether this conversation is the one currently in focus.
   * True when document is visible AND (the chat route shows this id
   * OR an overlay window for this id is open and expanded).
   */
  conversationFocused: boolean;
}

/**
 * Return true when an incoming chat message should trigger an audio cue.
 *
 * Gates (all must pass):
 *   - message is not from the authenticated user (not an echo of own send)
 *   - conversation is not muted
 *   - focus gate: not (visible AND the conversation is the active one)
 *
 * TODO: also suppress when quiet hours are active per REQ-PUSH-97 once
 * the quiet-hours store lands.
 */
export function shouldPlayChatCue(ctx: ChatCueContext): boolean {
  if (ctx.myPrincipalId !== null && ctx.senderId === ctx.myPrincipalId) {
    return false;
  }
  if (ctx.conversationMuted) {
    return false;
  }
  if (ctx.conversationFocused) {
    return false;
  }
  return true;
}

/** Context required to evaluate the mail-cue gate. */
export interface MailCueContext {
  /**
   * The mailboxIds map of the incoming email. Keys are mailbox ids;
   * values are always true (JMAP sparse-set encoding).
   */
  mailboxIds: Record<string, true>;
  /** The id of the user's inbox mailbox, or null if not yet loaded. */
  inboxMailboxId: string | null;
  /**
   * The email address of the sender (from[0].email).
   * Null when from is absent or empty (malformed / system mail).
   */
  senderEmail: string | null;
  /** The set of email addresses the user can send as (from Identity list). */
  ownEmails: Set<string>;
  /**
   * Whether the user is currently looking at the inbox.
   * True when document is visible AND the active route is /mail (inbox).
   */
  inboxFocused: boolean;
}

/**
 * Return true when a freshly-arrived email should trigger an audio cue.
 *
 * Gates (all must pass):
 *   - email is in the inbox mailbox
 *   - email is not from the user themselves
 *   - focus gate: not (visible AND the inbox is the active view)
 *
 * TODO: also suppress when quiet hours are active per REQ-PUSH-97 once
 * the quiet-hours store lands.
 */
export function shouldPlayMailCue(ctx: MailCueContext): boolean {
  if (!ctx.inboxMailboxId) return false;
  if (!ctx.mailboxIds[ctx.inboxMailboxId]) return false;
  if (ctx.senderEmail !== null && ctx.ownEmails.has(ctx.senderEmail)) {
    return false;
  }
  if (ctx.inboxFocused) {
    return false;
  }
  return true;
}
