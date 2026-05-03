/**
 * Recipient hover-card singleton (REQ-MAIL-46).
 *
 * Owns the "which card is open" decision so only one is visible at a
 * time. Triggers (avatar, sender name, recipient row entry) call
 * `recipientHover.requestOpen({...})` on pointer-enter / focus and
 * `recipientHover.requestClose(...)` on pointer-leave / blur; the
 * singleton applies the hover-intent delays:
 *
 *   - 400 ms sustained hover before opening
 *   - 150 ms grace before closing once the pointer leaves both the
 *     trigger and the popover
 *   - keyboard focus opens immediately
 *   - moving to a new trigger cancels the previous card without
 *     re-paying the open delay
 *
 * The card itself reads the reactive `open` state and renders inside a
 * portal floated above page content.
 */

const OPEN_DELAY_MS = 400;
const CLOSE_DELAY_MS = 150;

export interface HoverRequest {
  /** The DOM element the card anchors to. */
  anchor: HTMLElement;
  /** The address being shown. Lower-cased internally. */
  email: string;
  /** Best display name observed at the call site (parsed header name). */
  capturedName: string | null;
  /** Optional Face / X-Face headers from the rendered message for tier-2. */
  messageHeaders?: { face?: string; xFace?: string };
}

export interface OpenCard {
  anchor: HTMLElement;
  email: string;
  capturedName: string | null;
  messageHeaders?: { face?: string; xFace?: string };
}

class RecipientHoverStore {
  /** Currently visible card; null when none is open. */
  open = $state<OpenCard | null>(null);

  /** Pending open / close timers. */
  #openTimer: ReturnType<typeof setTimeout> | null = null;
  #closeTimer: ReturnType<typeof setTimeout> | null = null;

  /** Last anchor a card was opened against. Used to skip the open delay
   *  when the user moves between adjacent triggers without leaving the
   *  hover-card surface entirely. */
  #lastOpenedAnchor: HTMLElement | null = null;

  /**
   * Request the card to open against `req.anchor`. When `immediate` is
   * true (keyboard focus) the card opens this tick; otherwise the open
   * is delayed by 400 ms unless another card is already visible — in
   * which case the new anchor takes over without re-paying the delay.
   */
  requestOpen(req: HoverRequest, opts?: { immediate?: boolean }): void {
    this.#cancelClose();
    if (this.open && this.open.anchor === req.anchor) {
      return;
    }
    if (opts?.immediate || this.open || this.#lastOpenedAnchor) {
      this.#cancelOpen();
      this.#commitOpen(req);
      return;
    }
    this.#cancelOpen();
    this.#openTimer = setTimeout(() => {
      this.#openTimer = null;
      this.#commitOpen(req);
    }, OPEN_DELAY_MS);
  }

  /**
   * Request the card to close after the 150 ms grace window. The grace
   * is cancelled by another `requestOpen` (e.g. the pointer moves into
   * the popover) or by `cancelClose()`.
   */
  requestClose(): void {
    if (this.#openTimer) {
      this.#cancelOpen();
      return;
    }
    this.#cancelClose();
    this.#closeTimer = setTimeout(() => {
      this.#closeTimer = null;
      this.open = null;
      this.#lastOpenedAnchor = null;
    }, CLOSE_DELAY_MS);
  }

  /** Keep the card visible — called on pointer-enter of the popover. */
  cancelClose(): void {
    this.#cancelClose();
  }

  /** Tear the card down immediately (Escape, popover blur, route change). */
  closeNow(): void {
    this.#cancelOpen();
    this.#cancelClose();
    this.open = null;
    this.#lastOpenedAnchor = null;
  }

  #commitOpen(req: HoverRequest): void {
    this.open = {
      anchor: req.anchor,
      email: req.email.toLowerCase().trim(),
      capturedName: req.capturedName,
      messageHeaders: req.messageHeaders,
    };
    this.#lastOpenedAnchor = req.anchor;
  }

  #cancelOpen(): void {
    if (this.#openTimer) {
      clearTimeout(this.#openTimer);
      this.#openTimer = null;
    }
  }

  #cancelClose(): void {
    if (this.#closeTimer) {
      clearTimeout(this.#closeTimer);
      this.#closeTimer = null;
    }
  }
}

export const recipientHover = new RecipientHoverStore();

/** Test surface — internals exposed only for unit tests. */
export const _internals_forTest = {
  OPEN_DELAY_MS,
  CLOSE_DELAY_MS,
};
