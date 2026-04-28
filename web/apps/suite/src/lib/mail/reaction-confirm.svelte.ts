/**
 * Cross-server reaction confirmation state per REQ-MAIL-191.
 *
 * When a user reacts to a mailing-list message (List-ID header present)
 * with more than 5 recipients, we surface a one-time confirmation
 * explaining that the reaction will propagate to N people. The "don't
 * ask again" decision is stored in localStorage, keyed by list-id.
 *
 * The store holds the pending confirmation context; the component reads
 * it and calls `confirm()` or `cancel()` in response.
 */

const STORAGE_KEY_PREFIX = 'herold.suite.reaction-confirm.';

/** Persist that the user has agreed to react to list `listId` without asking again. */
function saveListConfirmed(listId: string): void {
  try {
    localStorage.setItem(STORAGE_KEY_PREFIX + listId, '1');
  } catch {
    // Quota / private mode — OK; next reaction will ask again.
  }
}

/** Return true if the user has already confirmed reactions for this list. */
function isListConfirmed(listId: string): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY_PREFIX + listId) === '1';
  } catch {
    return false;
  }
}

/**
 * Count total explicit recipients (to + cc fields) by inspecting the
 * recipient summary already available on the Email object. The value
 * is passed in by the caller since the Email type has `to` and `cc`
 * arrays.
 */
export function recipientCount(to: number, cc: number): number {
  return to + cc;
}

export interface ConfirmContext {
  emailId: string;
  emoji: string;
  listId: string;
  totalRecipients: number;
  onConfirm: (dontAskAgain: boolean) => void;
  onCancel: () => void;
}

class ReactionConfirmStore {
  pending = $state<ConfirmContext | null>(null);

  /**
   * Decide whether a confirmation modal is needed before dispatching a
   * reaction. Returns `false` if the reaction can proceed immediately
   * (no list context, few recipients, or the user already said "don't
   * ask again"). Returns `true` when the modal was opened — the caller
   * should wait for `onConfirm`/`onCancel` callbacks.
   */
  needsConfirm(args: {
    listId: string | null | undefined;
    totalRecipients: number;
    emailId: string;
    emoji: string;
    onProceed: () => void;
    onAbort: () => void;
  }): boolean {
    const { listId, totalRecipients, emailId, emoji, onProceed, onAbort } = args;
    if (!listId || totalRecipients <= 5) return false;
    if (isListConfirmed(listId)) return false;

    this.pending = {
      emailId,
      emoji,
      listId,
      totalRecipients,
      onConfirm: (dontAskAgain: boolean) => {
        if (dontAskAgain) saveListConfirmed(listId);
        this.pending = null;
        onProceed();
      },
      onCancel: () => {
        this.pending = null;
        onAbort();
      },
    };
    return true;
  }
}

export const reactionConfirm = new ReactionConfirmStore();

/** Exported for unit tests only. */
export const _internals_forTest = {
  saveListConfirmed,
  isListConfirmed,
  recipientCount,
};
