/**
 * Keyboard shortcut engine for the admin SPA.
 *
 * Mirrors the pattern from web/apps/suite/src/lib/keyboard/engine.svelte.ts
 * but without the coach integration (that is mail/suite-specific).
 *
 * The global (always-on) layer is the only layer used in the admin SPA at
 * this stage. Per-view layering can be added as admin pages grow in
 * complexity.
 *
 * Two-key sequences with `g` prefix (e.g. `g d`, `g h`): a 1000ms timeout
 * buffer. Pressing `g` starts the sequence; the next key completes it. The
 * buffer is cleared on timeout or when the second key is consumed.
 *
 * Focus carve-outs: when the active element is an <input>/<textarea>/
 * contenteditable, single-key shortcuts do not fire. Escape and Mod+Enter
 * always pass through.
 */

export type KeyboardAction = (event: KeyboardEvent) => void;

export interface Binding {
  /**
   * Canonical key string. Modifiers in fixed order: Mod+, Alt+, Shift+.
   * Single-key letters are lowercase ('e', 'j'); shifted single letters are
   * uppercase ('R'); special keys use their KeyboardEvent.key value
   * ('Enter', 'Escape', '/'). Two-key sequences are space-separated: 'g i'.
   * The Mod+ prefix matches either Cmd (macOS) or Ctrl (others).
   */
  key: string;
  action: KeyboardAction;
  /** Optional human-readable description for future help overlay. */
  description?: string;
}

class Engine {
  #global = new Map<string, Binding>();
  #attached = false;
  #gPrefixActive = false;
  #gPrefixTimer: ReturnType<typeof setTimeout> | null = null;

  /** Attach the document keydown listener. Idempotent. */
  init(): void {
    if (this.#attached) return;
    document.addEventListener('keydown', (e) => this.#handle(e));
    this.#attached = true;
  }

  /**
   * Register a binding in the global layer. Returns an unregister function.
   * Suitable for use in Svelte 5 $effect with the returned function as cleanup.
   */
  registerGlobal(binding: Binding): () => void {
    this.#global.set(binding.key, binding);
    return () => {
      const cur = this.#global.get(binding.key);
      if (cur === binding) this.#global.delete(binding.key);
    };
  }

  #handle(e: KeyboardEvent): void {
    if (this.#shouldSkipForFocus(e)) return;

    // Two-key sequence: previous key was 'g'; this is the second key.
    if (this.#gPrefixActive) {
      const seqKey = `g ${e.key}`;
      this.#clearGPrefix();
      const binding = this.#global.get(seqKey);
      if (binding) {
        e.preventDefault();
        binding.action(e);
      }
      return;
    }

    // Plain 'g' (no modifiers) starts a two-key prefix sequence.
    if (e.key === 'g' && !e.metaKey && !e.ctrlKey && !e.altKey) {
      this.#gPrefixActive = true;
      this.#gPrefixTimer = setTimeout(() => {
        this.#gPrefixActive = false;
        this.#gPrefixTimer = null;
      }, 1000);
      e.preventDefault();
      return;
    }

    const key = this.#canonicalize(e);
    if (!key) return;
    const binding = this.#global.get(key);
    if (binding) {
      e.preventDefault();
      binding.action(e);
    }
  }

  #clearGPrefix(): void {
    this.#gPrefixActive = false;
    if (this.#gPrefixTimer !== null) {
      clearTimeout(this.#gPrefixTimer);
      this.#gPrefixTimer = null;
    }
  }

  #shouldSkipForFocus(e: KeyboardEvent): boolean {
    const target = e.target;
    if (!(target instanceof HTMLElement)) return false;
    const tag = target.tagName;
    const isField = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
    const isEditable = target.isContentEditable;
    if (!isField && !isEditable) return false;
    if (e.key === 'Escape') return false;
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) return false;
    return true;
  }

  #canonicalize(e: KeyboardEvent): string | null {
    if (e.key === 'Shift' || e.key === 'Control' || e.key === 'Meta' || e.key === 'Alt') {
      return null;
    }
    const parts: string[] = [];
    if (e.metaKey || e.ctrlKey) parts.push('Mod');
    if (e.altKey) parts.push('Alt');
    if (e.shiftKey && e.key.length > 1) parts.push('Shift');
    parts.push(e.key);
    return parts.join('+');
  }
}

export const keyboard = new Engine();
