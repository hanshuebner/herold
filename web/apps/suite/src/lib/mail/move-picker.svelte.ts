/**
 * Move-to-mailbox picker state. Opened with the `v` keybinding from a
 * focused list row or the per-message Move button. The active emailId
 * lives here so a single overlay component can serve every entry point;
 * the overlay closes on submit / cancel.
 */
import type { Mailbox } from './types';

class MovePicker {
  isOpen = $state(false);
  /** Single-email mode target, null when bulk mode is active. */
  emailId = $state<string | null>(null);
  /** Bulk-mode targets, empty when single mode is active. */
  bulkIds = $state<string[]>([]);

  /** Open the picker for a single email. */
  open(emailId: string): void {
    this.emailId = emailId;
    this.bulkIds = [];
    this.isOpen = true;
  }

  /** Open the picker for a bulk move targeting many emails. */
  openBulk(ids: string[]): void {
    this.emailId = null;
    this.bulkIds = ids;
    this.isOpen = true;
  }

  /** True when the picker is operating in bulk-move mode. */
  get isBulk(): boolean {
    return this.bulkIds.length > 0;
  }

  close(): void {
    this.isOpen = false;
    this.emailId = null;
    this.bulkIds = [];
  }
}

export const movePicker = new MovePicker();

/** Roled mailboxes float to the top in this order. */
const ROLE_ORDER = ['inbox', 'archive', 'sent', 'drafts', 'trash'];

/**
 * Compute the candidate target mailboxes for a move:
 * 1. exclude every mailbox the email is already in;
 * 2. sort roled mailboxes (inbox / archive / sent / drafts / trash) first
 *    in that fixed order, then user mailboxes alphabetically by name.
 */
export function computeMoveCandidates(
  all: Iterable<Mailbox>,
  currentMailboxIds: ReadonlySet<string>,
): Mailbox[] {
  const out = [...all].filter((m) => !currentMailboxIds.has(m.id));
  out.sort((a, b) => {
    const ar = a.role ? ROLE_ORDER.indexOf(a.role) : -1;
    const br = b.role ? ROLE_ORDER.indexOf(b.role) : -1;
    if (ar !== -1 && br !== -1) return ar - br;
    if (ar !== -1) return -1;
    if (br !== -1) return 1;
    return a.name.localeCompare(b.name);
  });
  return out;
}

/**
 * Case-insensitive substring filter over mailbox names. Empty filter
 * returns the input unchanged.
 */
export function filterMailboxesByName(items: Mailbox[], filter: string): Mailbox[] {
  const f = filter.trim().toLowerCase();
  if (!f) return items;
  return items.filter((m) => m.name.toLowerCase().includes(f));
}

export const _internals_forTest = {
  computeMoveCandidates,
  filterMailboxesByName,
};
