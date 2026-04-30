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
 * The default idle threshold is 120s (REQ-CHAT-183).
 *
 * "Window focused" is the AND of `document.hasFocus()` and
 * `document.visibilityState === 'visible'`. The first catches
 * OS-level app switches; the second catches inter-tab switches in
 * the same browser window where some platforms do NOT fire `blur`
 * on the window. We listen to `focus`, `blur`, and `visibilitychange`
 * AND re-poll on the periodic tick AND on every input event, because
 * each platform/browser combination has at least one path that drops
 * one of the three events under contenteditable focus. The lazy
 * re-poll inside the listeners is what keeps presence honest.
 *
 * Debug logging: set `localStorage['herold:chat-presence-debug'] = '1'`
 * (or call `presence.setDebug(true)` from the console) to log every
 * presence event and every windowFocused transition. Logs go to
 * console.log so they show up in the standard browser dev tools.
 */

const DEFAULT_IDLE_SECONDS = 120;

/**
 * Recompute pulse cadence: how often we bump a tick so $derived cells
 * that read presence re-evaluate against `Date.now() - lastInputAt`
 * AND we re-sync `windowFocused` from `document.hasFocus()` /
 * `document.visibilityState`. 5s is a compromise between detection
 * latency and timer wake budget; the 30s sound debounce in the store
 * has its own clock so the pulse cadence does not affect it.
 */
const TICK_INTERVAL_MS = 5_000;

const DEBUG_KEY = 'herold:chat-presence-debug';

export type PresenceState = 'present-in-chat' | 'present-elsewhere' | 'absent';

class ChatPresence {
  /**
   * True when the document has both keyboard focus AND the tab is
   * visible. Mutated by syncWindowFocused() in response to
   * focus/blur/visibilitychange events, every input event, and the
   * periodic tick.
   */
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
   * Tick bumped on a 5s interval. Read by stateFor() / #isIdle() so
   * $derived consumers re-evaluate on the idle-window edge without
   * having to wire their own timer.
   */
  #tick = $state(0);

  #debug = false;

  #unmount: (() => void) | null = null;

  /** Wire the global window/input listeners. Idempotent. */
  install(): void {
    if (this.#unmount || typeof window === 'undefined') return;

    try {
      this.#debug = localStorage.getItem(DEBUG_KEY) === '1';
    } catch {
      this.#debug = false;
    }

    // Initial sync; later updates flow through #syncWindowFocused.
    this.#syncWindowFocused('install');

    const onFocus = (): void => this.#syncWindowFocused('window-focus');
    const onBlur = (): void => this.#syncWindowFocused('window-blur');
    const onVisibility = (): void => {
      this.#syncWindowFocused(`visibility-${document.visibilityState}`);
    };
    const onInput = (): void => {
      this.lastInputAt = Date.now();
      // An input event proves the document has focus right now even
      // if some prior `focus` was dropped by the browser. Re-sync
      // explicitly rather than waiting for the next tick.
      this.#syncWindowFocused('input');
    };

    window.addEventListener('focus', onFocus);
    window.addEventListener('blur', onBlur);
    document.addEventListener('visibilitychange', onVisibility);
    document.addEventListener('keydown', onInput, { passive: true });
    document.addEventListener('mousemove', onInput, { passive: true });
    document.addEventListener('mousedown', onInput, { passive: true });
    document.addEventListener('touchstart', onInput, { passive: true });
    document.addEventListener('pointerdown', onInput, { passive: true });

    const interval = setInterval(() => {
      this.#tick += 1;
      this.#syncWindowFocused('tick');
    }, TICK_INTERVAL_MS);

    this.#unmount = (): void => {
      clearInterval(interval);
      window.removeEventListener('focus', onFocus);
      window.removeEventListener('blur', onBlur);
      document.removeEventListener('visibilitychange', onVisibility);
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

  /**
   * Toggle verbose presence logging. Persisted in localStorage so it
   * survives reloads. Useful when diagnosing why a message did or did
   * not auto-mark-read in production builds.
   */
  setDebug(enabled: boolean): void {
    this.#debug = enabled;
    try {
      if (enabled) localStorage.setItem(DEBUG_KEY, '1');
      else localStorage.removeItem(DEBUG_KEY);
    } catch {
      // private-mode / quota: in-memory only.
    }
    // eslint-disable-next-line no-console
    console.log(`[chat-presence] debug logging ${enabled ? 'on' : 'off'}`);
  }

  /** Mirror the compose-focus event from a ChatCompose component. */
  setComposeFocus(conversationId: string): void {
    this.composeFocusedId = conversationId;
    this.lastInputAt = Date.now();
    // A compose receiving DOM focus is also a strong signal the
    // document has focus right now; re-sync so any missed window
    // focus event does not strand windowFocused at false.
    this.#syncWindowFocused('compose-focus');
    if (this.#debug) {
      // eslint-disable-next-line no-console
      console.log('[chat-presence] compose-focus', { conversationId });
    }
  }

  /** Mirror the compose-blur event from a ChatCompose component. */
  clearComposeFocus(conversationId: string): void {
    if (this.composeFocusedId === conversationId) {
      this.composeFocusedId = null;
      if (this.#debug) {
        // eslint-disable-next-line no-console
        console.log('[chat-presence] compose-blur', { conversationId });
      }
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

  /**
   * Re-read `document.hasFocus()` and `document.visibilityState` and
   * push the AND result into `windowFocused`. Called from every event
   * listener and from the periodic tick so `windowFocused` always
   * reflects the actual platform state, even when one of the three
   * focus-event paths drops an event.
   */
  #syncWindowFocused(label: string): void {
    if (typeof document === 'undefined') return;
    const hasFocus = document.hasFocus();
    const visible = document.visibilityState === 'visible';
    const next = hasFocus && visible;
    if (next !== this.windowFocused) {
      this.windowFocused = next;
      if (this.#debug) {
        // eslint-disable-next-line no-console
        console.log('[chat-presence] windowFocused ->', next, {
          via: label,
          documentHasFocus: hasFocus,
          visibilityState: document.visibilityState,
          composeFocusedId: this.composeFocusedId,
        });
      }
    } else if (this.#debug) {
      // eslint-disable-next-line no-console
      console.log('[chat-presence] event', label, {
        windowFocused: next,
        documentHasFocus: hasFocus,
        visibilityState: document.visibilityState,
      });
    }
  }

  #isIdle(): boolean {
    void this.#tick;
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
