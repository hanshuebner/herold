/**
 * Thread-row drag-and-drop coordinator (REQ-UI-17, REQ-MAIL-54).
 *
 * The thread list owns drag *source* events; the sidebar owns *drop*
 * events. They cooperate through this module's shared state — the
 * dragged email ids and the currently-hovered mailbox row.
 *
 * The active mailbox is rejected as a drop target by spec; we resolve
 * `mail.listFolder` (a role name like `inbox` or a custom-mailbox id)
 * back to the current mailbox row's id so the comparison is uniform
 * regardless of route shape.
 *
 * dnd is desktop-only (REQ-UI-17, REQ-MOB-37 — touch breakpoints use
 * the move-to picker instead). The drag source attaches `draggable`
 * unconditionally; touch devices simply don't fire HTML5 drag events.
 */

import { mail } from './store.svelte';

interface DragInfo {
  /** Email ids being dragged. */
  ids: string[];
  /** Mailbox-row id currently under the pointer, or null. */
  hoveredMailboxId: string | null;
}

class ThreadDnd {
  current = $state<DragInfo | null>(null);

  /** Begin a drag with the given email ids. */
  begin(ids: string[]): void {
    if (ids.length === 0) {
      this.current = null;
      return;
    }
    this.current = { ids, hoveredMailboxId: null };
  }

  /** End the drag — called on dragend / drop / cancel. */
  end(): void {
    this.current = null;
  }

  /** Update which mailbox row the pointer is over. Pass null to clear. */
  setHovered(id: string | null): void {
    if (!this.current) return;
    this.current.hoveredMailboxId = id;
  }

  /** Resolve `mail.listFolder` (role or custom id) to a mailbox row id. */
  currentMailboxId(): string | null {
    const folder = mail.listFolder;
    // 'all' is the all-mail virtual view; nothing to resolve.
    if (folder === 'all') return null;
    // If folder matches a known mailbox id, use it directly.
    if (mail.mailboxes.has(folder)) return folder;
    // Otherwise treat folder as a role name and look up by role.
    for (const m of mail.mailboxes.values()) {
      if (m.role === folder) return m.id;
    }
    return null;
  }

  /**
   * True when a drop on `targetMailboxId` should fire. The current
   * mailbox view (REQ-UI-17) is *not* a valid target. Drafts and Sent
   * are never valid targets: Drafts contains partial messages managed
   * by the compose stack; Sent contains outbound copies that should not
   * be re-filed by drag-and-drop.
   */
  isValidTarget(targetMailboxId: string): boolean {
    if (!this.current || this.current.ids.length === 0) return false;
    // Drafts and Sent may not receive dropped threads.
    for (const m of mail.mailboxes.values()) {
      if (m.id === targetMailboxId && (m.role === 'drafts' || m.role === 'sent')) return false;
    }
    return this.currentMailboxId() !== targetMailboxId;
  }
}

export const threadDnd = new ThreadDnd();

/**
 * Compute the email ids the thread row should drag. Honours the active
 * multi-selection: when the dragged row's email is part of the current
 * selection, drag the entire selection; otherwise drag just that one.
 */
export function dragIdsForRow(emailId: string): string[] {
  const sel = mail.listSelectedIds;
  if (sel.has(emailId) && sel.size > 1) {
    return Array.from(sel);
  }
  return [emailId];
}
