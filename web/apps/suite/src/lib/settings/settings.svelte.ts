/**
 * Settings store per docs/requirements/20-settings.md.
 *
 * Local-only preferences (theme, image-load default, undo window, swipe
 * mapping, coach toggle, per-sender image allow-list) live in
 * `localStorage`, keyed by username so a single browser profile shared
 * across two accounts keeps them separate. Server-side settings
 * (identity, signature, vacation responder) live on the JMAP server and
 * are read through `mail.identities` / future stores.
 *
 * Each setting is a reactive `$state` cell. Mutating it goes through a
 * setter that writes the JSON-encoded value back to localStorage on the
 * same tick — small enough that we don't bother batching.
 *
 * Theme is applied to `<html data-theme>` from a single $effect created
 * in App.svelte (we set the attribute directly so per-tab activation
 * doesn't depend on `mail` being warm).
 */

import { auth } from '../auth/auth.svelte';
import { i18n, detectLocale, LOCALES, type Locale } from '../i18n/i18n.svelte';

export type ThemeChoice = 'system' | 'light' | 'dark';
export type ImageLoadDefault = 'never' | 'per-sender' | 'always';
export type ComposerEnterMode = 'paragraph' | 'linebreak';
export type SwipeAction =
  | 'archive'
  | 'snooze'
  | 'delete'
  | 'mark_read'
  | 'label'
  | 'none';

const KEYS = {
  theme: 'theme',
  locale: 'locale',
  imageLoadDefault: 'imageLoadDefault',
  imageAllowList: 'imageAllowList',
  undoWindowSec: 'undoWindowSec',
  coachEnabled: 'coachEnabled',
  swipeLeft: 'swipeLeft',
  swipeRight: 'swipeRight',
  desktopNotifEnabled: 'desktopNotifEnabled',
  composerEnterMode: 'composerEnterMode',
} as const;

const DEFAULTS = {
  theme: 'system' as ThemeChoice,
  imageLoadDefault: 'never' as ImageLoadDefault,
  imageAllowList: [] as string[],
  undoWindowSec: 5,
  coachEnabled: true,
  swipeLeft: 'archive' as SwipeAction,
  swipeRight: 'snooze' as SwipeAction,
  desktopNotifEnabled: false,
  composerEnterMode: 'paragraph' as ComposerEnterMode,
};

const UNDO_WINDOW_MIN = 0;
const UNDO_WINDOW_MAX = 30;

function storageKey(name: string): string {
  // Scope per-account so two accounts on the same browser don't clobber
  // each other. The pre-auth namespace uses 'anon' so we can read /
  // write defaults before the session resolves.
  const username = auth.session?.username ?? 'anon';
  return `herold.suite.settings.${username}.${name}`;
}

function readJson<T>(name: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(storageKey(name));
    if (raw === null) return fallback;
    return JSON.parse(raw) as T;
  } catch {
    return fallback;
  }
}

function writeJson(name: string, value: unknown): void {
  try {
    localStorage.setItem(storageKey(name), JSON.stringify(value));
  } catch {
    // Quota exceeded / private mode — settings just won't persist this run.
  }
}

class Settings {
  // Backing $state cells. Initial values come from defaults; we hydrate
  // from localStorage in `hydrate()` once the auth session is resolved
  // (so the storage key is the right account-scoped one).
  #theme = $state<ThemeChoice>(DEFAULTS.theme);
  #imageLoadDefault = $state<ImageLoadDefault>(DEFAULTS.imageLoadDefault);
  #imageAllowList = $state<string[]>([...DEFAULTS.imageAllowList]);
  #undoWindowSec = $state<number>(DEFAULTS.undoWindowSec);
  #coachEnabled = $state<boolean>(DEFAULTS.coachEnabled);
  #swipeLeft = $state<SwipeAction>(DEFAULTS.swipeLeft);
  #swipeRight = $state<SwipeAction>(DEFAULTS.swipeRight);
  #desktopNotifEnabled = $state<boolean>(DEFAULTS.desktopNotifEnabled);
  #composerEnterMode = $state<ComposerEnterMode>(DEFAULTS.composerEnterMode);

  /** Read every setting from localStorage. Idempotent. */
  hydrate(): void {
    this.#theme = readJson<ThemeChoice>(KEYS.theme, DEFAULTS.theme);
    const persistedLocale = readJson<Locale | null>(KEYS.locale, null);
    const resolvedLocale =
      persistedLocale && LOCALES.includes(persistedLocale)
        ? persistedLocale
        : detectLocale();
    i18n.locale = resolvedLocale;
    this.#imageLoadDefault = readJson<ImageLoadDefault>(
      KEYS.imageLoadDefault,
      DEFAULTS.imageLoadDefault,
    );
    this.#imageAllowList = readJson<string[]>(
      KEYS.imageAllowList,
      [...DEFAULTS.imageAllowList],
    );
    this.#undoWindowSec = clampUndo(
      readJson<number>(KEYS.undoWindowSec, DEFAULTS.undoWindowSec),
    );
    this.#coachEnabled = readJson<boolean>(
      KEYS.coachEnabled,
      DEFAULTS.coachEnabled,
    );
    this.#swipeLeft = readJson<SwipeAction>(KEYS.swipeLeft, DEFAULTS.swipeLeft);
    this.#swipeRight = readJson<SwipeAction>(
      KEYS.swipeRight,
      DEFAULTS.swipeRight,
    );
    this.#desktopNotifEnabled = readJson<boolean>(
      KEYS.desktopNotifEnabled,
      DEFAULTS.desktopNotifEnabled,
    );
    this.#composerEnterMode = readJson<ComposerEnterMode>(
      KEYS.composerEnterMode,
      DEFAULTS.composerEnterMode,
    );
  }

  // ── Theme ──────────────────────────────────────────────────────────────
  get theme(): ThemeChoice {
    return this.#theme;
  }
  setTheme(value: ThemeChoice): void {
    this.#theme = value;
    writeJson(KEYS.theme, value);
  }

  // ── Locale ────────────────────────────────────────────────────────────
  /**
   * Locale lives on the i18n singleton -- this getter is a thin
   * forwarder so SettingsView can use the familiar settings.* shape.
   */
  get locale(): Locale {
    return i18n.locale;
  }
  setLocale(value: Locale): void {
    i18n.locale = value;
    writeJson(KEYS.locale, value);
  }

  // ── Image-load default ────────────────────────────────────────────────
  get imageLoadDefault(): ImageLoadDefault {
    return this.#imageLoadDefault;
  }
  setImageLoadDefault(value: ImageLoadDefault): void {
    this.#imageLoadDefault = value;
    writeJson(KEYS.imageLoadDefault, value);
  }

  /** Per-sender allow-list for external images (REQ-SET-05). */
  get imageAllowList(): readonly string[] {
    return this.#imageAllowList;
  }

  /** True when this sender's images should be auto-loaded. */
  isImageAllowed(senderEmail: string | null | undefined): boolean {
    if (this.#imageLoadDefault === 'always') return true;
    // The per-sender allow-list is an explicit "I trust this sender"
    // override. It fires in BOTH 'never' and 'per-sender' modes so the
    // "Always from X" button persists as the user expects (issue
    // #101: clicking the button stored the entry but the early-out at
    // 'never' mode never consulted it). The pre-#101 code only honoured
    // the allow-list in 'per-sender' mode, which left the button as a
    // no-op for the default-mode user — no UX hint that they also had
    // to flip the global setting first.
    const lower = senderEmail?.toLowerCase();
    if (lower && this.#imageAllowList.includes(lower)) return true;
    if (this.#imageLoadDefault === 'never') return false;
    // per-sender mode: only the allow-list grants access (already
    // checked above; falling through means no match).
    return false;
  }

  addImageAllowedSender(senderEmail: string): void {
    const lower = senderEmail.trim().toLowerCase();
    if (!lower || this.#imageAllowList.includes(lower)) return;
    this.#imageAllowList = [...this.#imageAllowList, lower];
    writeJson(KEYS.imageAllowList, this.#imageAllowList);
  }

  removeImageAllowedSender(senderEmail: string): void {
    const lower = senderEmail.trim().toLowerCase();
    const next = this.#imageAllowList.filter((s) => s !== lower);
    if (next.length === this.#imageAllowList.length) return;
    this.#imageAllowList = next;
    writeJson(KEYS.imageAllowList, next);
  }

  // ── Undo window ────────────────────────────────────────────────────────
  get undoWindowSec(): number {
    return this.#undoWindowSec;
  }
  setUndoWindowSec(value: number): void {
    const clamped = clampUndo(value);
    this.#undoWindowSec = clamped;
    writeJson(KEYS.undoWindowSec, clamped);
  }

  // ── Coach ──────────────────────────────────────────────────────────────
  get coachEnabled(): boolean {
    return this.#coachEnabled;
  }
  setCoachEnabled(value: boolean): void {
    this.#coachEnabled = value;
    writeJson(KEYS.coachEnabled, value);
  }

  // ── Swipe ──────────────────────────────────────────────────────────────
  get swipeLeft(): SwipeAction {
    return this.#swipeLeft;
  }
  setSwipeLeft(value: SwipeAction): void {
    this.#swipeLeft = value;
    writeJson(KEYS.swipeLeft, value);
  }
  get swipeRight(): SwipeAction {
    return this.#swipeRight;
  }
  setSwipeRight(value: SwipeAction): void {
    this.#swipeRight = value;
    writeJson(KEYS.swipeRight, value);
  }

  // ── Desktop notifications ──────────────────────────────────────────────
  /** Show an OS-level notification for new inbox mail while the tab is open (re #23). */
  get desktopNotifEnabled(): boolean {
    return this.#desktopNotifEnabled;
  }
  setDesktopNotifEnabled(value: boolean): void {
    this.#desktopNotifEnabled = value;
    writeJson(KEYS.desktopNotifEnabled, value);
  }

  // ── Composer Enter mode ───────────────────────────────────────────────
  get composerEnterMode(): ComposerEnterMode {
    return this.#composerEnterMode;
  }
  setComposerEnterMode(value: ComposerEnterMode): void {
    this.#composerEnterMode = value;
    writeJson(KEYS.composerEnterMode, value);
  }
}

function clampUndo(n: number): number {
  if (!Number.isFinite(n)) return DEFAULTS.undoWindowSec;
  if (n < UNDO_WINDOW_MIN) return UNDO_WINDOW_MIN;
  if (n > UNDO_WINDOW_MAX) return UNDO_WINDOW_MAX;
  return Math.round(n);
}

export const settings = new Settings();

/**
 * Apply the current theme choice to `<html data-theme>`. Called from a
 * single $effect in App.svelte; runs again whenever the setting changes.
 */
export function applyTheme(choice: ThemeChoice): void {
  document.documentElement.dataset.theme = choice;
}
