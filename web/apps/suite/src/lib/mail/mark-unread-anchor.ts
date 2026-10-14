import type { Email } from './types';

/**
 * Re #526: picks the anchor email id for the thread toolbar's "Mark
 * unread" action (REQ-UI-19b), which is rewired to `markUnreadFromHere`
 * (REQ-MAIL-133a) -- the same anchored, Undo-able operation the
 * per-message "Mark unread from here" kebab item already uses.
 *
 * The anchor is the first currently-expanded message in thread order
 * (the earliest message the user has actually seen on screen), so
 * `markUnreadFromHere` flips it and every later message, leaving
 * collapsed earlier messages untouched. When nothing is expanded, the
 * anchor falls back to the thread's last message.
 */
export function pickMarkUnreadAnchor(
  emails: readonly Email[],
  expandedIds: ReadonlySet<string>,
): string | null {
  for (const e of emails) {
    if (expandedIds.has(e.id)) return e.id;
  }
  return emails.length > 0 ? emails[emails.length - 1]!.id : null;
}
