/**
 * Category picker singleton state — Wave 3.13.
 *
 * Opened from the `m` shortcut in MailView, the toolbar "Move to category"
 * button, and the per-message context menu. The picker is always thread-
 * granular by default (REQ-CAT-22); per-message mode is set to false when
 * opening from an individual message's action menu.
 */

class CategoryPicker {
  isOpen = $state(false);
  /** The email id to patch. When thread-granular all emails in the thread
   *  with this email's threadId will be updated. */
  emailId = $state<string | null>(null);
  /** When true, the pick applies to every email in the thread (REQ-CAT-22). */
  threadGranular = $state(true);

  /** Open for a thread (default — REQ-CAT-22). */
  open(emailId: string): void {
    this.emailId = emailId;
    this.threadGranular = true;
    this.isOpen = true;
  }

  /** Open for a single message only (per-message override). */
  openSingle(emailId: string): void {
    this.emailId = emailId;
    this.threadGranular = false;
    this.isOpen = true;
  }

  close(): void {
    this.isOpen = false;
    this.emailId = null;
    this.threadGranular = true;
  }
}

export const categoryPicker = new CategoryPicker();
