/**
 * ProseMirror input and paste rules for auto-linking URLs in chat compose.
 *
 * Covers two cases:
 *   1. Typed URL: when the user types a URL followed by a space or Enter,
 *      the preceding token is wrapped in the link mark.
 *   2. Pasted plain text that is itself a bare URL: the entire pasted text
 *      is wrapped in the link mark.
 *
 * Only http:// and https:// schemes are accepted; all other schemes are
 * left as plain text.
 */

import { inputRules, InputRule } from 'prosemirror-inputrules';
import type { Plugin } from 'prosemirror-state';
import type { Schema } from 'prosemirror-model';

/** Matches a trailing http/https URL followed by a space (the trigger char). */
const URL_RE = /(https?:\/\/[^\s]+)\s$/;

/**
 * Build an InputRule that detects a space typed after a URL and wraps the
 * preceding token in the link mark. The space that triggered the rule is
 * preserved after the link.
 */
function urlInputRule(schema: Schema): InputRule {
  return new InputRule(URL_RE, (state, match, start, end) => {
    const linkMark = schema.marks.link;
    if (!linkMark) return null;
    const url = match[1];
    if (!url) return null;

    // `start` is the beginning of the match; `end` is after the space.
    // We want to wrap [start, end - 1] (the URL without the trailing space)
    // in the link mark, then insert a plain space after it.
    const urlEnd = end - 1; // exclude the trailing space from the mark range
    const mark = linkMark.create({ href: url });
    const { tr } = state;

    // Replace the matched range with the URL text (link mark) + a plain space.
    tr.replaceWith(start, end, [
      schema.text(url, [mark]),
      schema.text(' '),
    ]);

    return tr;
  });
}

/** Returns the inputRules plugin for URL auto-linking. */
export function urlInputRulesPlugin(schema: Schema): Plugin {
  return inputRules({ rules: [urlInputRule(schema)] });
}

/**
 * A custom paste handler intended for use as a ProseMirror handlePaste hook.
 * When the clipboard contains plain text that is a bare http/https URL
 * (nothing else), it is inserted as a link mark node rather than plain text.
 *
 * Returns true if it handled the paste (preventing default ProseMirror
 * handling), false otherwise.
 */
export function handleUrlPaste(
  view: import('prosemirror-view').EditorView,
  event: ClipboardEvent,
): boolean {
  const text = event.clipboardData?.getData('text/plain')?.trim();
  if (!text) return false;
  if (!/^https?:\/\/[^\s]+$/.test(text)) return false;

  const { state, dispatch } = view;
  const linkMark = state.schema.marks.link;
  if (!linkMark) return false;

  const mark = linkMark.create({ href: text });
  const node = state.schema.text(text, [mark]);
  const tr = state.tr.replaceSelectionWith(node, false);
  dispatch(tr);
  return true;
}
