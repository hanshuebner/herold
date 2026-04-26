/**
 * Single document-level keyboard dispatcher per
 * docs/architecture/05-keyboard-engine.md.
 *
 * Layered keymap: a stack of binding maps; topmost layer wins. The bottom
 * layer is the global (always-on) map; views push their own layer in
 * $effect and pop on cleanup so per-view bindings shadow global ones for
 * the duration the view is mounted.
 *
 * Two-key sequences with `g` prefix (REQ-KEY-03): a 1000ms timeout buffer.
 *
 * Focus carve-outs (REQ-KEY-02): when the active element is an
 * <input>/<textarea>/contenteditable, single-key shortcuts don't fire.
 * Always-pass-through: Escape; Mod+Enter (the compose-send chord).
 */

export type KeyboardAction = (event: KeyboardEvent) => void;

export interface Binding {
  /**
   * Canonical key string. Modifiers in fixed order: `Mod+`, `Alt+`, `Shift+`.
   * Single-key letters are lowercase ('e', 'j'); shifted single letters are
   * uppercase ('R'); special keys use their `KeyboardEvent.key` value
   * ('Enter', 'Escape', '/'). Two-key sequences are space-separated:
   * `'g i'`. The `Mod+` prefix matches either Cmd (macOS) or Ctrl (others).
   */
  key: string;
  action: KeyboardAction;
  /** Optional human-readable description for the help overlay. */
  description?: string;
}

class Engine {
  /** Stack of layers; index 0 is the global layer. */
  #layers: Map<string, Binding>[] = [new Map()];
  #gPrefixActive = false;
  #gPrefixTimer: ReturnType<typeof setTimeout> | null = null;
  #attached = false;

  /** Attach the document keydown listener. Idempotent. */
  init(): void {
    if (this.#attached) return;
    document.addEventListener('keydown', (e) => this.#handle(e));
    this.#attached = true;
  }

  /** Add to the global (bottom) layer. Returns an unregister function. */
  registerGlobal(binding: Binding): () => void {
    const global = this.#layers[0]!;
    global.set(binding.key, binding);
    return () => {
      const cur = global.get(binding.key);
      if (cur === binding) global.delete(binding.key);
    };
  }

  /**
   * Push a layer of bindings (e.g. for an active view). Returns a pop
   * function. Inside Svelte 5, call this from `$effect` and return the
   * pop function as the cleanup.
   */
  pushLayer(bindings: Binding[]): () => void {
    const layer = new Map<string, Binding>();
    for (const b of bindings) layer.set(b.key, b);
    this.#layers.push(layer);
    return () => {
      const idx = this.#layers.indexOf(layer);
      if (idx >= 0) this.#layers.splice(idx, 1);
    };
  }

  /** Iterate all currently-active bindings, top-of-stack first. */
  *activeBindings(): IterableIterator<Binding> {
    const seen = new Set<string>();
    for (let i = this.#layers.length - 1; i >= 0; i--) {
      for (const [key, binding] of this.#layers[i]!) {
        if (seen.has(key)) continue;
        seen.add(key);
        yield binding;
      }
    }
  }

  #handle(e: KeyboardEvent): void {
    if (this.#shouldSkipForFocus(e)) return;

    // Two-key sequence: previous key was 'g'; this is the second key.
    if (this.#gPrefixActive) {
      const seqKey = `g ${e.key}`;
      this.#clearGPrefix();
      const binding = this.#resolve(seqKey);
      if (binding) {
        e.preventDefault();
        binding.action(e);
      }
      return;
    }

    // Plain 'g' starts a sequence (only when no modifiers — `Cmd+G` shouldn't
    // trigger the prefix).
    if (e.key === 'g' && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) {
      this.#gPrefixActive = true;
      this.#gPrefixTimer = setTimeout(() => this.#clearGPrefix(), 1000);
      return;
    }

    const key = this.#canonicalize(e);
    if (!key) return;
    const binding = this.#resolve(key);
    if (binding) {
      e.preventDefault();
      binding.action(e);
    }
  }

  #shouldSkipForFocus(e: KeyboardEvent): boolean {
    const target = e.target;
    if (!(target instanceof HTMLElement)) return false;
    const tag = target.tagName;
    const isField = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
    const isEditable = target.isContentEditable;
    if (!isField && !isEditable) return false;
    // Carve-outs always pass through.
    if (e.key === 'Escape') return false;
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) return false;
    return true;
  }

  #canonicalize(e: KeyboardEvent): string | null {
    // Don't treat lone modifier keydowns as bindings.
    if (e.key === 'Shift' || e.key === 'Control' || e.key === 'Meta' || e.key === 'Alt') {
      return null;
    }
    const parts: string[] = [];
    if (e.metaKey || e.ctrlKey) parts.push('Mod');
    if (e.altKey) parts.push('Alt');
    // For special keys (multi-char e.key), shift becomes an explicit modifier.
    // For single-char keys, shift is folded into the key value already
    // ('R' vs 'r', '?' vs '/').
    if (e.shiftKey && e.key.length > 1) parts.push('Shift');
    parts.push(e.key);
    return parts.join('+');
  }

  #resolve(key: string): Binding | undefined {
    for (let i = this.#layers.length - 1; i >= 0; i--) {
      const b = this.#layers[i]!.get(key);
      if (b) return b;
    }
    return undefined;
  }

  #clearGPrefix(): void {
    if (this.#gPrefixTimer !== null) {
      clearTimeout(this.#gPrefixTimer);
      this.#gPrefixTimer = null;
    }
    this.#gPrefixActive = false;
  }
}

export const keyboard = new Engine();
