import type { Email } from './types';

/**
 * REQ-UI-20 (corrected): returns the id of the **first** unread message in
 * `emails`, or the last message if all are read.
 *
 * Scanning forward (index 0 first) is correct in all cases:
 *
 * - Single unread at the end (common new-message case): forward scan lands on
 *   it just as the previous backward scan did — no regression.
 * - Multiple consecutive unread starting in the middle ("Ab hier als
 *   ungelesen markieren" case): forward scan returns the anchor message, which
 *   is where the user expected to resume reading. The old backward scan
 *   returned the last message, skipping the unread messages in between.
 * - All messages read: falls through to the last-message fallback.
 */
export function pickInitialExpanded(emails: Email[]): string | null {
  if (emails.length === 0) return null;
  for (let i = 0; i < emails.length; i++) {
    const e = emails[i];
    if (e && !e.keywords.$seen) return e.id;
  }
  return emails[emails.length - 1]?.id ?? null;
}
