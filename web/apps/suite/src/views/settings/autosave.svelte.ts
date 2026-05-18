/**
 * Autosave coordinator for the identity editor (Item 7).
 *
 * The identity editor no longer has per-section Save buttons: name /
 * display name / signature / reply-to / bcc autosave on blur. This
 * helper owns the small state machine each autosaving field shares —
 * `idle -> saving -> saved -> idle`, or `saving -> error` — and the
 * "Saved" auto-clear timer so the indicator fades back to nothing.
 *
 * Kept as a `.svelte.ts` module so the `$state` cell is reactive when
 * the component reads `controller.state`.
 */

import type { SaveState } from './SaveStatus.svelte';

const SAVED_VISIBLE_MS = 1800;

export class AutosaveController {
  state = $state<SaveState>('idle');
  errorMessage = $state<string | null>(null);

  #clearTimer: ReturnType<typeof setTimeout> | null = null;

  /**
   * Run `fn` as an autosave. No-ops when `fn` is a write that is not
   * needed — callers should gate on dirtiness before calling. Surfaces
   * `saving` while in flight, then `saved` (auto-clearing) or `error`.
   * Returns true on success, false on failure, so callers can decide
   * whether to commit their local "saved" snapshot.
   */
  async run(fn: () => Promise<void>): Promise<boolean> {
    this.#cancelClear();
    this.state = 'saving';
    this.errorMessage = null;
    try {
      await fn();
      this.state = 'saved';
      this.#clearTimer = setTimeout(() => {
        this.state = 'idle';
        this.#clearTimer = null;
      }, SAVED_VISIBLE_MS);
      return true;
    } catch (err) {
      this.state = 'error';
      this.errorMessage = err instanceof Error ? err.message : String(err);
      return false;
    }
  }

  /** Clear any pending "Saved" auto-clear timer. */
  #cancelClear(): void {
    if (this.#clearTimer) {
      clearTimeout(this.#clearTimer);
      this.#clearTimer = null;
    }
  }

  /** Tear down timers — call from the owning component's onDestroy / cleanup. */
  dispose(): void {
    this.#cancelClear();
  }
}
