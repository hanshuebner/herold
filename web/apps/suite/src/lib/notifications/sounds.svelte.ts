/**
 * In-app notification sounds per REQ-PUSH-95..99 and REQ-SET-16.
 *
 * Lazily creates one Audio element per sound kind on first play().
 * Reuses the same element on rapid repeats by resetting currentTime.
 * play() Promise rejections are swallowed silently (autoplay policy).
 *
 * The master toggle is persisted in localStorage under
 * 'herold:sounds_enabled' per REQ-SET-16.
 */

export type SoundKind = 'call' | 'chat' | 'mail';

const STORAGE_KEY = 'herold:sounds_enabled';

const SOUND_PATHS: Record<SoundKind, string> = {
  call: '/sounds/sound-call.wav',
  chat: '/sounds/sound-chat.wav',
  mail: '/sounds/sound-mail.wav',
};

class NotificationSounds {
  /** Whether in-app sounds are enabled. Mirrors localStorage per REQ-SET-16. */
  enabled = $state(true);

  /** Lazily-created Audio elements keyed by SoundKind. */
  readonly #elements = new Map<SoundKind, HTMLAudioElement>();

  constructor() {
    // Defer hydration to hydrate() so tests can call it explicitly after
    // mocking localStorage / Audio.
  }

  /**
   * Read the persisted preference from localStorage.
   * Safe to call multiple times; subsequent calls re-read the stored value.
   */
  hydrate(): void {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      // Default true when absent; false only when stored as the string 'false'.
      this.enabled = raw !== 'false';
    } catch {
      // Private-browsing / quota — default to true.
      this.enabled = true;
    }
  }

  /** Update the toggle and persist it to localStorage. */
  setEnabled(value: boolean): void {
    this.enabled = value;
    try {
      localStorage.setItem(STORAGE_KEY, String(value));
    } catch {
      // Private-browsing / quota — state is in-memory only.
    }
  }

  /**
   * Play the sound for the given kind. Fire-and-forget.
   * Reuses the element across calls by resetting currentTime so rapid
   * repeats restart cleanly without creating new nodes.
   * Does nothing when enabled is false.
   */
  play(kind: SoundKind): void {
    if (!this.enabled) return;
    const el = this.#getOrCreate(kind);
    el.currentTime = 0;
    el.play().catch(() => {
      // Browser autoplay policy may block this; swallow silently per spec.
    });
  }

  /**
   * Stop the sound for the given kind, if it is currently playing.
   * Called by the IncomingCall flow when the user accepts or declines
   * per REQ-PUSH-99.
   */
  stop(kind: SoundKind): void {
    const el = this.#elements.get(kind);
    if (!el) return;
    el.pause();
    el.currentTime = 0;
  }

  #getOrCreate(kind: SoundKind): HTMLAudioElement {
    let el = this.#elements.get(kind);
    if (!el) {
      el = new Audio(SOUND_PATHS[kind]);
      this.#elements.set(kind, el);
    }
    return el;
  }
}

export const sounds = new NotificationSounds();

/** Exported for unit tests. */
export const _internals_forTest = { NotificationSounds, STORAGE_KEY };
