/**
 * Heuristics for separating an email body's "fresh" content from the
 * quoted reply history. Used by the thread reader to hide the prior
 * message by default and reveal it on demand.
 */

/**
 * A three-part view of a plain-text email body that preserves original
 * source order:
 *
 *   head      — always-visible content before the citation.
 *   collapsed — the single trailing citation, hidden behind "..." by default;
 *               empty when no qualifying citation is found. Includes a
 *               trailing signature block when one immediately follows the
 *               citation (re #234) — see rule 4 below.
 *   tail      — always empty. Retained as a field for the caller's render
 *               order (head → [... toggle] → tail); a signature that
 *               follows a collapsed citation folds into `collapsed`
 *               instead of appearing here (see rule 4).
 *
 * Rendered in source order: head → [... toggle] → tail
 */
export type BodySplit = {
  head: string;
  collapsed: string;
  tail: string;
};

/**
 * Splits a plain-text body into a head / collapsed / tail triple.
 *
 * Rules:
 *
 * 1. Collapse AT MOST ONE citation — the single contiguous trailing quoted
 *    region: an optional attribution line ("On … wrote:" / "Am … schrieb:" /
 *    "<Name> schrieb am … um …:") immediately preceding consecutive
 *    `>`-prefixed lines (any nesting depth), with blank lines allowed within
 *    the run.
 * 2. Collapse that citation ONLY IF everything that follows it to the end of
 *    the message is empty/whitespace or a signature block. A signature block
 *    begins at the first line matching `^-- ?$` and runs to the end.
 * 3. If the trailing quoted region is followed by any real (non-blank,
 *    non-signature) content, collapse NOTHING — the whole body is returned
 *    as `head` with `collapsed = ""`. This is the case when a quote appears
 *    at the top and new text follows: nothing is collapsed, everything is
 *    visible in order.
 * 4. A signature block that follows the collapsed citation folds away with
 *    it (re #234) rather than staying visible in `tail`: `findSigDelimiter`
 *    scans the WHOLE message for `^-- ?$`, so whenever both a citation and a
 *    signature are found, the signature is necessarily the trailing content
 *    of the message and immediately follows the citation — nothing else can
 *    sit between them, because `findTrailingCitation` only matches when the
 *    citation is already the last non-blank content before the signature
 *    delimiter. A message with a citation and NO signature still collapses
 *    normally with `tail` empty; a message with a signature and NO
 *    qualifying citation is unaffected (rule 3's early return keeps the
 *    signature visible in `head`, unfolded, since there is nothing to fold
 *    it with).
 * 5. Order is never changed: rendering head + collapsed + tail preserves the
 *    original source sequence.
 */
export function splitQuotedText(body: string): BodySplit {
  if (!body) return { head: '', collapsed: '', tail: '' };

  const lines = body.split(/\r?\n/);

  // ── Step 1: locate trailing signature ──────────────────────────────────
  const sigStart = findSigDelimiter(lines);
  const bodyLines = sigStart >= 0 ? lines.slice(0, sigStart) : lines;

  // ── Step 2: find the trailing citation within the body (before any
  // signature) ────────────────────────────────────────────────────────────
  const citationStart = findTrailingCitation(bodyLines);

  if (citationStart < 0) {
    // No qualifying citation: render the full body verbatim, in order —
    // including any signature, which has nothing to fold with (rule 4).
    return { head: body, collapsed: '', tail: '' };
  }

  // ── Step 3: build head; strip the trailing blank separator ─────────────
  const headLines = bodyLines.slice(0, citationStart);
  while (headLines.length > 0 && headLines[headLines.length - 1]!.trim() === '') {
    headLines.pop();
  }

  // ── Step 4: collapsed spans from the citation start to the TRUE end of
  // the message (not just the end of bodyLines) — see rule 4's doc comment
  // for why a signature here is always contiguous with the citation. ─────
  const collapsedLines = lines.slice(citationStart);
  while (
    collapsedLines.length > 0 &&
    collapsedLines[collapsedLines.length - 1]!.trim() === ''
  ) {
    collapsedLines.pop();
  }

  return {
    head: headLines.join('\n'),
    collapsed: collapsedLines.join('\n'),
    tail: '',
  };
}

// ── Shared helpers (also used by sanitize.ts's HTML-path quote collapse) ──

// English "On <date>, <sender> wrote:" (also emitted verbatim by
// compose.svelte.ts's formatReplyQuote, regardless of the active locale);
// German "Am <date> schrieb <sender>:"; Spanish "El <date> <sender>
// escribió:"; French "Le <date> <sender> a écrit:".
const LEAD_ATTRIBUTION_RE =
  /^(On\b[\s\S]+\bwrote\s*:|Am\b[\s\S]+schrieb\b[\s\S]*:|El\b[\s\S]+escribi[oó]\s*:|Le\b[\s\S]+a\s+[ée]crit\s*:)\s*$/i;

// German name-first attribution, as emitted by Thunderbird's German locale:
// "<Name> schrieb am <dd.mm.yy> um <hh:mm>:" -- the sender's name precedes
// "schrieb", so LEAD_ATTRIBUTION_RE's leading Am/On/El/Le anchor does not
// match it. Anchored on a literal date (d.m.y) and time (h:mm) immediately
// after "schrieb am" / "um" so ordinary prose that merely happens to
// contain the words "schrieb am ... um ..." is not mistaken for an
// auto-generated citation line -- that combination followed directly by a
// colon is not a sentence shape German prose produces on its own.
const NAME_FIRST_SCHRIEB_RE =
  /^.+\bschrieb am\s+\d{1,2}\.\d{1,2}\.\d{2,4}\s+um\s+\d{1,2}:\d{2}\s*:\s*$/i;

/**
 * True when `line` (a single, already-trimmed line or element text) is an
 * auto-generated citation-introducer line ("On ... wrote:", "Am ...
 * schrieb ...:", "<Name> schrieb am ... um ...:", ...) rather than genuine
 * new text. Shared between the plain-text citation scan below and
 * sanitize.ts's HTML quoted-region collapse (re #234).
 */
export function isAttributionLine(line: string): boolean {
  return LEAD_ATTRIBUTION_RE.test(line) || NAME_FIRST_SCHRIEB_RE.test(line);
}

/**
 * Matches the RFC 3676 signature delimiter "-- " and the common variant
 * "--", once the candidate text has been trimmed. Shared with sanitize.ts's
 * HTML-path signature-block detection (re #234).
 */
export const SIG_DELIM_RE = /^-- ?$/;

/**
 * Returns the index of the first signature-delimiter line (`^-- ?$`), or -1.
 * The signature runs from that line to the end of the message.
 */
function findSigDelimiter(lines: string[]): number {
  for (let i = 0; i < lines.length; i++) {
    if (SIG_DELIM_RE.test(lines[i]!)) return i;
  }
  return -1;
}

/**
 * Scans `lines` (the body before any signature) backwards to find the start
 * of a single contiguous trailing citation. A citation is:
 *
 *   - zero or one attribution line immediately before
 *   - one or more `>`-prefixed lines, with blank lines allowed within the run
 *
 * Returns the line index at which the citation starts, or -1 when the
 * trailing-most non-blank line is not `>`-prefixed (no trailing citation).
 */
function findTrailingCitation(lines: string[]): number {
  let end = lines.length - 1;

  // Skip trailing blank lines.
  while (end >= 0 && lines[end]!.trim() === '') end--;

  // The trailing-most non-blank line must be a quoted line to have a citation.
  if (end < 0 || !isQuotedLine(lines[end]!)) return -1;

  // Scan backwards to find the start of the contiguous quoted run, then
  // optionally a preceding attribution line.
  let citationStart = end;
  let i = end - 1;

  while (i >= 0) {
    const trimmed = lines[i]!.trim();

    if (trimmed === '') {
      // Blank line within the potential quoted run — keep scanning.
      i--;
      continue;
    }

    if (isQuotedLine(lines[i]!)) {
      citationStart = i;
      i--;
      continue;
    }

    if (isAttributionLine(trimmed)) {
      // Attribution line at the top of the run; include it and stop.
      citationStart = i;
      break;
    }

    // Non-blank, non-quoted, non-attribution: the citation starts just below.
    break;
  }

  return citationStart;
}

/**
 * Returns true when a line starts with one or more `>` characters, optionally
 * followed by a space. Covers `> body`, `>>nested`, and bare `>`.
 */
function isQuotedLine(line: string): boolean {
  return /^>+(\s|$)/.test(line.trim());
}
