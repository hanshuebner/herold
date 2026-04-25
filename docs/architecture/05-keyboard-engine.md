# 05 — Keyboard engine

A single dispatcher for all shortcuts. Bindings live in `../requirements/10-keyboard.md`; this doc is how they actually fire.

## Single dispatcher

One `keydown` listener at the document level. It:

1. Reads the active element. If it's an `<input>`, `<textarea>`, or `contenteditable` element, it skips dispatch except for two carve-outs:
   - `Escape` always dispatches (so users can blur fields and close pickers).
   - `Ctrl+Enter` always dispatches (so compose can fire Send from within the body editor).
2. Builds the binding key:
   - Single letter, lowercase: `e`, `j`, `r`, …
   - Shifted character: `Shift+R`, `#`, `?` (note `?` is `Shift+/` on US layouts; we map both forms to `?`).
   - Modified: `Ctrl+Enter`, `Cmd+Enter`.
   - Special: `Enter`, `Escape`, arrow keys.
3. Looks up the binding in the active keymap (see "Keymap stack" below).
4. If found, calls `event.preventDefault()` and dispatches the action.

## Two-key sequences

For `g…` navigation (`g i`, `g s`, `g d`, etc.):

- The first `g` doesn't fire an action. It transitions the dispatcher into "expecting second key" state and starts a 1000 ms timer (`../requirements/10-keyboard.md` REQ-KEY-03).
- The next keydown:
  - If it forms a known sequence, fire that action; clear the state.
  - If it doesn't, clear the state without firing. The user typed `g` then something else — it's not an error, just an abandoned sequence.
- Timer expiry clears the state with no action.

Only `g` is a sequence prefix in v1. Adding more (`*` for selection prefixes, etc.) means parameterising the prefix table; not v1.

## Keymap stack

The dispatcher consults a stack of keymaps, top-down:

1. **Picker keymap** (if a picker is open). Label picker, snooze picker, search field. The picker registers its own bindings: `Enter`, `Escape`, `↑`/`↓`, character keys for filtering. While the picker is open, only its keymap fires; the global keymap is dormant.
2. **View keymap** (if a view-specific binding overrides global). Empty in v1; hook for things like a thread-view-only `Tab` to next message.
3. **Global keymap** — the bindings in `../requirements/10-keyboard.md`.

## Picker pattern

Pickers (label, snooze, From-identity) are opened by a global binding (`l`, `b`). Opening a picker:

1. Stores the previously focused element so it can be restored on close.
2. Renders the picker, focusing its filter input.
3. Pushes its keymap onto the stack.
4. On close (`Escape`, click outside, or selection confirmed): pops the keymap, restores focus.

A picker's filter input is one of the input-focus carve-outs: typing in the picker filter shouldn't fire global single-key bindings, but `Enter` (confirm), `Escape` (dismiss), and arrow keys (navigate options) must work. The picker keymap supersedes both the global and the input-focus rules.

## Help overlay

`?` opens an overlay listing every binding active in the current keymap context, grouped by area (Navigation, Mail, Search, Compose, …). The overlay is itself a picker (registers its own keymap that traps `Escape` → close, ignores everything else). When closed, the prior keymap stack is restored unchanged.

## Capture keys we don't own

Browser-reserved chords (`Ctrl+T`, `Ctrl+W`, `Ctrl+L`, `Cmd+R`, etc.) are never captured. The dispatcher returns early for any keydown with an `event.metaKey` or `event.ctrlKey` modifier outside our explicit binding list (`Ctrl+Enter`, `Cmd+Enter`). Browser back/forward (`Alt+←` / `Cmd+[`) are likewise untouched.

## Customisation

Per `../requirements/10-keyboard.md` REQ-KEY-04, P0 bindings are remappable. The settings panel writes overrides to `localStorage`; the dispatcher loads them on bootstrap and merges them into the global keymap. Conflicts (a remap that collides with a sequence prefix or a built-in browser chord) are rejected at save time with an inline error.
