/**
 * In-app confirm dialog store. Replaces window.confirm so the modal
 * matches the Suite styling and so headless tests can drive it.
 *
 * confirm.ask({ message, confirmLabel?, cancelLabel?, danger? }) returns
 * a promise that resolves to true when the user confirms and false
 * otherwise. One dialog at a time; a second ask() while a prompt is
 * pending rejects the previous one with cancel = false so the promise
 * does not leak.
 */

export type ConfirmKind = 'default' | 'danger';

export interface ConfirmRequest {
  message: string;
  /** Optional title above the message. */
  title?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** When 'danger', the confirm button uses the destructive style. */
  kind?: ConfirmKind;
}

interface PendingState extends ConfirmRequest {
  resolve: (ok: boolean) => void;
}

class ConfirmStore {
  pending = $state<PendingState | null>(null);

  ask(req: ConfirmRequest): Promise<boolean> {
    // Cancel any prior pending dialog so its promise resolves rather
    // than getting stranded.
    if (this.pending) {
      this.pending.resolve(false);
      this.pending = null;
    }
    return new Promise<boolean>((resolve) => {
      this.pending = { ...req, resolve };
    });
  }

  decide(ok: boolean): void {
    const cur = this.pending;
    if (!cur) return;
    this.pending = null;
    cur.resolve(ok);
  }
}

export const confirm = new ConfirmStore();
