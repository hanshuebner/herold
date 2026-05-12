/**
 * In-app label create/edit dialog store.
 *
 * labelDialog.open({ title, defaultName?, defaultColor?, confirmLabel? })
 * returns Promise<{ name: string; color: string } | null>: the name and
 * colour on confirm, null on cancel.  One dialog at a time; a second open()
 * while a dialog is pending resolves the previous one with null.
 */

export interface LabelDialogRequest {
  /** Dialog heading. */
  title: string;
  /** Pre-filled label name. */
  defaultName?: string;
  /** Pre-filled colour (#RRGGBB). */
  defaultColor?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /**
   * When true (the historical edit-mode default) AND `defaultName` is
   * supplied, the confirm button stays disabled until the user changes
   * the name or colour from the pre-filled values. When false, the
   * confirm button is enabled as soon as the name is non-empty —
   * matches the "suggested default the user MAY edit" semantics
   * required by REQ-MAIL-12c's tagged-address banner flow. Defaults to
   * true (undefined treated as true) for backward compatibility with
   * the settings rename / edit callsites.
   */
  requireDirty?: boolean;
}

export interface LabelDialogResult {
  name: string;
  color: string;
}

interface PendingState extends LabelDialogRequest {
  resolve: (value: LabelDialogResult | null) => void;
}

class LabelDialogStore {
  pending = $state<PendingState | null>(null);

  open(req: LabelDialogRequest): Promise<LabelDialogResult | null> {
    if (this.pending) {
      this.pending.resolve(null);
      this.pending = null;
    }
    return new Promise<LabelDialogResult | null>((resolve) => {
      this.pending = { ...req, resolve };
    });
  }

  decide(value: LabelDialogResult | null): void {
    const cur = this.pending;
    if (!cur) return;
    this.pending = null;
    cur.resolve(value);
  }
}

export const labelDialog = new LabelDialogStore();
