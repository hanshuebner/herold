/**
 * Label-picker state. Mirrors movePicker but stays open across multiple
 * checkbox toggles -- labels are multi-select. Confirm or Escape closes
 * the dialog. Used by the per-message and bulk Label actions
 * (issue #16, REQ-LBL-10..13).
 */
class LabelPicker {
  isOpen = $state(false);
  /** Single-email mode target, null when bulk mode is active. */
  emailId = $state<string | null>(null);
  /** Bulk-mode targets, empty when single mode is active. */
  bulkIds = $state<string[]>([]);

  open(emailId: string): void {
    this.emailId = emailId;
    this.bulkIds = [];
    this.isOpen = true;
  }

  openBulk(ids: string[]): void {
    this.emailId = null;
    this.bulkIds = ids.slice();
    this.isOpen = true;
  }

  get isBulk(): boolean {
    return this.bulkIds.length > 0;
  }

  close(): void {
    this.isOpen = false;
    this.emailId = null;
    this.bulkIds = [];
  }
}

export const labelPicker = new LabelPicker();
