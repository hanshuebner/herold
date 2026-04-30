/**
 * Local-user presence state for chat (REQ-CHAT-180..184).
 *
 * Per requirement, the state vocabulary is per-conversation and
 * computed entirely client-side from browser signals — the server is
 * not involved.
 *
 *   present-in-chat    -- window has focus AND DOM focus is on the
 *                         compose surface for THIS conversation
 *                         (REQ-CHAT-181)
 *   present-elsewhere  -- window has focus AND last input observed
 *                         within the idle window AND not present-in-chat
 *                         (REQ-CHAT-182)
 *   absent             -- window not focused OR last input older than
 *                         the idle threshold (REQ-CHAT-183)
 *
 * The default idle threshold is 120s (REQ-CHAT-183). Operator config
 * may override; the override path is wired through capabilities at
 * boot via setIdleThresholdSeconds().
 */

const DEFAULT_IDLE_SECONDS = 120;

/**
 * Recompute pulse cadence: how often we bump a tick so $derived cells
 * that read presence re-evaluate against `Date.now() - lastInputAt`.
 * 10s is fine-grained enough for the 120s idle window; the 30s sound
 * debounce in the store has its own clock so the pulse cadence does
 * not affect it.
 */
const TICK_INTERVAL_MS = 10_000;

export type PresenceState = 'present-in-chat' | 'present-elsewhere' | 'absent';

class ChatPresence {
  /** True when the OS-level browser window has keyboard focus. */
  windowFocused = $state(true);

  /** Wall-clock timestamp of the last observed input event. */
  lastInputAt = $state(Date.now());

  /**
   * The id of the conversation whose ChatCompose surface currently
   * holds DOM focus, or null when no compose has focus. Only one
   * conversation can be in this slot at a time (REQ-CHAT-184).
   */
  composeFocusedId = $state<string | null>(null);

  /** Idle threshold in seconds; defaults to 120 per REQ-CHAT-183. */
  idleThresholdSeconds = $state(DEFAULT_IDLE_SECONDS);

  /**
   * Tick bumped on a 10s interval. Read by stateFor() so $derived
   * cells re-fire on the idle-window edge without the consumer
   * having to wire its own timer.
   */
  #tick = $state(0);

  #unmount: (() => void) | null = null;

  /** Wire the global window/input listeners. Called once at boot. */
  install(): void {
    if (this.#unmount || typeof window === 'undefined') return;

    this.windowFocused = typeof document !== 'undefined' && document.hasFocus();

    const onFocus = (): void => {
      this.windowFocused = true;
      this.lastInputAt = Date.now();
    };
    const onBlur = (): void => {
      this.windowFocused = false;
    };
    const onInput = (): void => {
      this.lastInputAt = Date.now();
    };
    const interval = setInterval(() => {
      this.#tick += 1;
    }, TICK_INTERVAL_MS);

    window.addEventListener('focus', onFocus);
    window.addEventListener('blur', onBlur);
    document.addEventListener('keydown', onInput, { passive: true });
    document.addEventListener('mousemove', onInput, { passive: true });
    document.addEventListener('mousedown', onInput, { passive: true });
    document.addEventListener('touchstart', onInput, { passive: true });
    document.addEventListener('pointerdown', onInput, { passive: true });

    this.#unmount = (): void => {
      clearInterval(interval);
      window.removeEventListener('focus', onFocus);
      window.removeEventListener('blur', onBlur);
      document.removeEventListener('keydown', onInput);
      document.removeEventListener('mousemove', onInput);
      document.removeEventListener('mousedown', onInput);
      document.removeEventListener('touchstart', onInput);
      document.removeEventListener('pointerdown', onInput);
    };
  }

  /** Tear down listeners. Tests use this; production never calls it. */
  uninstall(): void {
    this.#unmount?.();
    this.#unmount = null;
  }

  /** Mirror the compose-focus event from a ChatCompose component. */
  setComposeFocus(conversationId: string): void {
    this.composeFocusedId = conversationId;
    this.lastInputAt = Date.now();
  }

  /** Mirror the compose-blur event from a ChatCompose component. */
  clearComposeFocus(conversationId: string): void {
    if (this.composeFocusedId === conversationId) {
      this.composeFocusedId = null;
    }
  }

  /**
   * Compute the local user's presence state for a given conversation.
   * Reads `#tick` so $derived consumers re-evaluate on the idle-window
   * edge.
   */
  stateFor(conversationId: string): PresenceState {
    void this.#tick;
    if (this.windowFocused && this.composeFocusedId === conversationId) {
      return 'present-in-chat';
    }
    if (this.windowFocused && !this.#isIdle()) {
      return 'present-elsewhere';
    }
    return 'absent';
  }

  #isIdle(): boolean {
    const now = Date.now();
    return now - this.lastInputAt > this.idleThresholdSeconds * 1000;
  }
}

export const presence = new ChatPresence();

/** Test-only: reset the singleton's mutable state between cases. */
export function _resetForTest(): void {
  presence.uninstall();
  presence.windowFocused = true;
  presence.lastInputAt = Date.now();
  presence.composeFocusedId = null;
  presence.idleThresholdSeconds = DEFAULT_IDLE_SECONDS;
}
