/**
 * Stacked-compose orchestration per REQ-UI-04 (max 3 concurrent compose
 * windows).  The active compose is the existing singleton at
 * `compose.svelte.ts`; this module owns the minimized "tray" — up to two
 * additional snapshots that the user has stowed.  Combined cap: 3.
 *
 * Behaviour:
 *   - Open a new compose while one is active → the active compose's
 *     content snapshots into the tray and the new one takes over.
 *   - Minimize button on the modal → same: snapshot, tray-push, close.
 *   - Click a tray chip → if a different compose is currently active,
 *     it snapshots first; then the clicked snapshot restores into the
 *     active singleton.
 *   - Cap reached (tray full + active compose has content) → refuse,
 *     surface a toast.
 *
 * Snapshots survive across reply / forward openers; they persist as
 * long as the page lives (no localStorage round-trip yet).
 */

import { compose, type ComposeAttachment } from './compose.svelte';
import { toast } from '../toast/toast.svelte';

const STACK_CAP = 2; // active compose makes 3.

export interface ComposeSnapshot {
  /** Stable per-snapshot key for the tray chip's #key. */
  key: string;
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
  ccBccVisible: boolean;
  editingDraftId: string | null;
  replyContext: typeof compose.replyContext;
  attachments: ComposeAttachment[];
  /** Wall-clock createdAt so tray chips can be sorted oldest-first. */
  createdAt: number;
}

class ComposeStack {
  /** Minimized compose snapshots, oldest first. */
  minimized = $state<ComposeSnapshot[]>([]);
  #seq = 0;

  /** True when the maximum (active + minimized) has been reached. */
  get atCapacity(): boolean {
    return this.minimized.length >= STACK_CAP && compose.isOpen;
  }

  /**
   * Snapshot the active compose into the tray and close the modal.
   * No-op when nothing is open.  Returns true if a snapshot was made.
   */
  minimizeCurrent(): boolean {
    if (!compose.isOpen) return false;
    const snap: ComposeSnapshot = {
      key: `mc-${++this.#seq}`,
      to: compose.to,
      cc: compose.cc,
      bcc: compose.bcc,
      subject: compose.subject,
      body: compose.body,
      ccBccVisible: compose.ccBccVisible,
      editingDraftId: compose.editingDraftId,
      replyContext: { ...compose.replyContext },
      attachments: [...compose.attachments],
      createdAt: Date.now(),
    };
    this.minimized = [...this.minimized, snap];
    compose.close();
    return true;
  }

  /**
   * Restore a snapshot into the active compose, removing it from the
   * tray.  When another compose is already active, that one's content
   * is snapshotted first so it isn't lost.
   */
  restore(key: string): void {
    const idx = this.minimized.findIndex((s) => s.key === key);
    if (idx < 0) return;
    const snap = this.minimized[idx]!;
    if (compose.isOpen) {
      // Swap: snapshot the current, then restore the picked one.
      this.minimizeCurrent();
    }
    this.minimized = this.minimized.filter((s) => s.key !== key);
    compose.openWith({
      to: snap.to,
      cc: snap.cc,
      bcc: snap.bcc,
      subject: snap.subject,
      body: snap.body,
      replyContext: snap.replyContext,
      draftId: snap.editingDraftId,
      skipHook: true,
    });
    // Re-attach the persisted attachment list — openWith resets it.
    compose.attachments = snap.attachments;
    compose.ccBccVisible = snap.ccBccVisible;
  }

  /** Drop a snapshot without restoring. */
  discard(key: string): void {
    this.minimized = this.minimized.filter((s) => s.key !== key);
  }

  /**
   * Hook called by the keyboard `c` opener and the reply / forward
   * paths.  When the active compose has content, auto-minimize it
   * before letting the new opener take over.  Returns true if the
   * caller may proceed; false when the cap is hit.
   */
  beforeOpenNew(): boolean {
    if (!compose.isOpen) return true;
    if (!compose.hasContent) {
      // The active compose is empty — just close it without snapshotting.
      compose.close();
      return true;
    }
    if (this.minimized.length >= STACK_CAP) {
      toast.show({
        message: `You already have ${STACK_CAP + 1} compose windows. Close one first.`,
        kind: 'error',
        timeoutMs: 6000,
      });
      return false;
    }
    this.minimizeCurrent();
    return true;
  }

  /**
   * Build a short label for a tray chip — subject when present,
   * otherwise the first recipient, otherwise "(empty)".
   */
  static chipLabel(s: ComposeSnapshot): string {
    if (s.subject.trim()) return s.subject;
    const firstAddr = s.to.split(',')[0]?.trim();
    if (firstAddr) return firstAddr;
    return '(empty)';
  }
}

export const composeStack = new ComposeStack();

export const _internals_forTest = { ComposeStack };
