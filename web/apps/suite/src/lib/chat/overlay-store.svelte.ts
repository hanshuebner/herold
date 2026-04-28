/**
 * Chat overlay window state — open windows, minimise state.
 *
 * Tracks up to MAX_WINDOWS overlay chat windows anchored to the
 * bottom-right of the viewport.  Each window shows one conversation.
 * Opening a fourth evicts the oldest (LIFO-with-age).
 *
 * Two overlay windows for the same conversation cannot be open
 * concurrently: openWindow() is a no-op (modulo focus) when the
 * conversation is already in the stack.
 *
 * REQ-CHAT-20..27 (compose), REQ-CHAT-40 (read receipts), REQ-CHAT-52
 * (typing auto-expire) all apply inside the overlay windows because
 * they reuse MessageList and ChatCompose unchanged.
 */

const MAX_WINDOWS = 3;

export interface OverlayWindow {
  /** Stable key for Svelte #key / aria purposes. */
  key: string;
  conversationId: string;
  /** false = expanded, true = title-bar only. */
  minimized: boolean;
  /** Monotonic open timestamp used for eviction ordering (oldest first). */
  openedAt: number;
}

class ChatOverlayStore {
  windows = $state<OverlayWindow[]>([]);
  #seq = 0;

  /**
   * Open a conversation in an overlay.  No-op (deduped) when already open.
   * If MAX_WINDOWS are already open, the oldest (by openedAt) is closed first.
   */
  openWindow(conversationId: string): void {
    // Dedup: if already open, un-minimize and bring to the end of the stack.
    const existing = this.windows.find((w) => w.conversationId === conversationId);
    if (existing) {
      this.windows = [
        ...this.windows.filter((w) => w.conversationId !== conversationId),
        { ...existing, minimized: false },
      ];
      return;
    }

    let next = [...this.windows];

    // Evict oldest if at capacity.
    if (next.length >= MAX_WINDOWS) {
      const oldest = next.reduce((a, b) => (a.openedAt <= b.openedAt ? a : b));
      next = next.filter((w) => w.key !== oldest.key);
    }

    next.push({
      key: `ow-${++this.#seq}`,
      conversationId,
      minimized: false,
      openedAt: Date.now(),
    });

    this.windows = next;
  }

  closeWindow(key: string): void {
    this.windows = this.windows.filter((w) => w.key !== key);
  }

  minimizeWindow(key: string): void {
    this.windows = this.windows.map((w) =>
      w.key === key ? { ...w, minimized: true } : w,
    );
  }

  expandWindow(key: string): void {
    this.windows = this.windows.map((w) =>
      w.key === key ? { ...w, minimized: false } : w,
    );
  }

  toggleMinimize(key: string): void {
    this.windows = this.windows.map((w) =>
      w.key === key ? { ...w, minimized: !w.minimized } : w,
    );
  }

  isOpen(conversationId: string): boolean {
    return this.windows.some((w) => w.conversationId === conversationId);
  }
}

export const chatOverlay = new ChatOverlayStore();

/** Exported for unit tests only. */
export const _internals_forTest = { ChatOverlayStore, MAX_WINDOWS };
