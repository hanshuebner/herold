import type { Email } from './types';

/**
 * REQ-UI-20 (corrected): returns the ids of **all** unread messages in
 * `emails`, or a single-element array containing the last message id if all
 * messages are read.
 *
 * Expanding every unread message is correct in all cases:
 *
 * - Single unread at the end (common new-message case): returns that one id —
 *   same visible result as before.
 * - Multiple consecutive unread starting in the middle ("Ab hier als
 *   ungelesen markieren" case): all unread messages from the anchor onward are
 *   returned, so the user sees every unread message expanded on reopen. The
 *   previous fix (f480a40a) only expanded the first unread, leaving the rest
 *   collapsed.
 * - All messages read: returns the last message id so the thread is not blank.
 */
export function pickInitialExpanded(emails: Email[]): string[] {
  if (emails.length === 0) return [];
  const unread = emails.filter((e) => e && !e.keywords.$seen).map((e) => e.id);
  if (unread.length > 0) return unread;
  return [emails[emails.length - 1]!.id];
}
