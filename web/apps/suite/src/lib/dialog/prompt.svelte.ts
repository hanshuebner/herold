/**
 * In-app text-prompt store. Drop-in replacement for window.prompt so the
 * modal matches Suite styling and headless tests can drive it.
 *
 * prompt.ask({ title?, message?, label, defaultValue?, placeholder?,
 * confirmLabel?, cancelLabel?, kind? }) returns Promise<string | null>:
 * the trimmed value on confirm, null on cancel (or empty submit when
 * `allowEmpty` is unset). One prompt at a time -- a second ask() while
 * a prompt is pending resolves the previous one with null so the
 * promise does not leak.
 */

export type PromptKind = 'default' | 'danger';

export interface PromptRequest {
  /** Optional heading. */
  title?: string;
  /** Optional explanatory body, rendered above the input. */
  message?: string;
  /** Field label rendered alongside the input. */
  label: string;
  /** Pre-filled input value. */
  defaultValue?: string;
  /** Input placeholder. */
  placeholder?: string;
  confirmLabel?: string;
  cancelLabel?: string;
  /** When 'danger', the confirm button uses the destructive style. */
  kind?: PromptKind;
  /**
   * When true, allow submitting empty values (returns ''). Default
   * behaviour is to disable the confirm button while the input is
   * blank, so callers do not have to re-validate.
   */
  allowEmpty?: boolean;
}

interface PendingState extends PromptRequest {
  resolve: (value: string | null) => void;
}

class PromptStore {
  pending = $state<PendingState | null>(null);

  ask(req: PromptRequest): Promise<string | null> {
    if (this.pending) {
      this.pending.resolve(null);
      this.pending = null;
    }
    return new Promise<string | null>((resolve) => {
      this.pending = { ...req, resolve };
    });
  }

  decide(value: string | null): void {
    const cur = this.pending;
    if (!cur) return;
    this.pending = null;
    cur.resolve(value);
  }
}

export const prompt = new PromptStore();
