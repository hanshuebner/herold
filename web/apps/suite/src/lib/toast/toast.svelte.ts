/**
 * Toast / snackbar singleton per docs/requirements/11-optimistic-ui.md
 * REQ-OPT-14 (one toast at a time; new toast displaces the prior).
 *
 * Default timeout 5 s per REQ-SET-06; the user-configurable Undo window
 * setting wires into this when settings ship.
 */

export type ToastKind = 'info' | 'error';

export interface ToastSpec {
  /** The body text shown in the toast. */
  message: string;
  /**
   * Optional Undo handler — when set, an Undo button is rendered.
   * Clicking it dismisses the toast and invokes this callback.
   */
  undo?: () => void | Promise<void>;
  /** Default 5000 ms; 0 disables auto-dismiss (useful for errors). */
  timeoutMs?: number;
  /** Visual variant. Default 'info'. */
  kind?: ToastKind;
}

class ToastStore {
  current = $state<ToastSpec | null>(null);

  #timer: ReturnType<typeof setTimeout> | null = null;

  show(spec: ToastSpec): void {
    this.dismiss();
    this.current = spec;
    const timeout = spec.timeoutMs ?? 5000;
    if (timeout > 0) {
      this.#timer = setTimeout(() => this.dismiss(), timeout);
    }
  }

  dismiss(): void {
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    this.current = null;
  }

  async undo(): Promise<void> {
    const t = this.current;
    if (!t?.undo) return;
    const undoFn = t.undo;
    this.dismiss();
    try {
      await undoFn();
    } catch (err) {
      this.show({
        message: err instanceof Error ? err.message : 'Undo failed',
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }
}

export const toast = new ToastStore();
