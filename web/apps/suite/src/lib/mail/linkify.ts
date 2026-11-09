/**
 * Shared URL detection for message bodies. The HTML path (sanitize.ts)
 * and the plain-text path (TextBody.svelte, print-message.ts) both use
 * these helpers so detection and trimming behave identically.
 */

/** A run of body text: either literal text or a detected link. */
export type TextSegment =
  | { kind: 'text'; text: string }
  | { kind: 'link'; text: string; href: string };

// URL/mailto pattern. The match is greedy up to whitespace and
// angle-bracket / quote terminators only; closing punctuation like ")"
// or "." stays in the match and is shed by trimTrailingPunctuation in a
// balance-aware way.
//
// - "https?://" requires the scheme prefix.
// - bare "www." paths are linkified with an implicit https:// prefix.
// - "mailto:..." addresses linkify as mailto links.
// - bare addresses (foo@example.com) are not linkified, to avoid false
//   positives in plain-text quoting ("On X wrote: alice@example.com said").
// - other schemes (javascript:, data:, ...) never match.
export const LINKIFY_RE = /(?:https?:\/\/|www\.)[^\s<>"'`]+|mailto:[^\s<>"'`]+/gi;

/**
 * Strip trailing sentence punctuation from a matched URL. Closing
 * brackets are dropped when unmatched and kept when they balance an
 * opening bracket inside the URL ("https://x/wiki/Foo_(bar)").
 */
export function trimTrailingPunctuation(url: string): { kept: string; dropped: string } {
  let kept = url;
  let dropped = '';
  for (;;) {
    const last = kept.charAt(kept.length - 1);
    if (last === '') break;
    if ('.,;:!?>'.includes(last)) {
      dropped = last + dropped;
      kept = kept.slice(0, -1);
      continue;
    }
    if (last === ')' || last === ']' || last === '}') {
      const open = last === ')' ? '(' : last === ']' ? '[' : '{';
      const opens = kept.split(open).length - 1;
      const closes = kept.split(last).length - 1;
      if (closes > opens) {
        dropped = last + dropped;
        kept = kept.slice(0, -1);
        continue;
      }
    }
    break;
  }
  return { kept, dropped };
}

const LINK_BODY_RE = /^(?:https?:\/\/|www\.|mailto:)./i;

/**
 * True when a trimmed match still has a scheme/www prefix followed by at
 * least one more character. A bare "www" or "https://" left over after
 * trimming stays text.
 */
export function isLinkable(kept: string): boolean {
  return LINK_BODY_RE.test(kept);
}

/** The href for a trimmed match: bare "www." gains an https:// prefix. */
export function linkHref(kept: string): string {
  return kept.toLowerCase().startsWith('www.') ? 'https://' + kept : kept;
}

/**
 * Split text into text and link segments. Concatenating every
 * segment's `text` reproduces the input exactly.
 */
export function splitLinks(text: string): TextSegment[] {
  const out: TextSegment[] = [];
  const re = new RegExp(LINKIFY_RE.source, LINKIFY_RE.flags);
  let last = 0;
  const pushText = (s: string) => {
    if (!s) return;
    const prev = out[out.length - 1];
    if (prev && prev.kind === 'text') prev.text += s;
    else out.push({ kind: 'text', text: s });
  };
  for (;;) {
    const m = re.exec(text);
    if (!m) break;
    const { kept, dropped } = trimTrailingPunctuation(m[0]);
    pushText(text.slice(last, m.index));
    if (isLinkable(kept)) {
      out.push({ kind: 'link', text: kept, href: linkHref(kept) });
      pushText(dropped);
    } else {
      pushText(m[0]);
    }
    last = m.index + m[0].length;
  }
  pushText(text.slice(last));
  return out;
}
