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
 *               empty when no qualifying citation is found.
 *   tail      — always-visible content that follows the citation (typically
 *               a "-- " signature block); empty when there is no trailing
 *               signature or when nothing is collapsed.
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
 *    region: an optional attribution line ("On … wrote:" / "Am … schrieb:")
 *    immediately preceding consecutive `>`-prefixed lines (any nesting
 *    depth), with blank lines allowed within the run.
 * 2. Collapse that citation ONLY IF everything that follows it to the end of
 *    the message is empty/whitespace or a signature block. A signature block
 *    begins at the first line matching `^-- ?$` and runs to the end.
 * 3. If the trailing quoted region is followed by any real (non-blank,
 *    non-signature) content, collapse NOTHING — the whole body is returned
 *    as `head` with `collapsed = ""`. This is the case when a quote appears
 *    at the top and new text follows: nothing is collapsed, everything is
 *    visible in order.
 * 4. Order is never changed: rendering head + collapsed + tail preserves the
 *    original source sequence. The signature is always in `tail` (shown),
 *    never moved into `head`.
 */
export function splitQuotedText(body: string): BodySplit {
  if (!body) return { head: '', collapsed: '', tail: '' };

  const lines = body.split(/\r?\n/);

  // ── Step 1: locate trailing signature ──────────────────────────────────
  const sigStart = findSigDelimiter(lines);
  const bodyLines = sigStart >= 0 ? lines.slice(0, sigStart) : lines;
  const tailLines = sigStart >= 0 ? lines.slice(sigStart) : [];

  // ── Step 2: find the trailing citation within the body ─────────────────
  const citationStart = findTrailingCitation(bodyLines);

  if (citationStart < 0) {
    // No qualifying citation: render the full body verbatim, in order.
    return { head: body, collapsed: '', tail: '' };
  }

  // ── Step 3: build the three parts, preserving source order ─────────────
  const headLines = bodyLines.slice(0, citationStart);
  // Strip trailing blank separator between head and citation.
  while (headLines.length > 0 && headLines[headLines.length - 1]!.trim() === '') {
    headLines.pop();
  }

  const collapsedLines = bodyLines.slice(citationStart);
  // Strip trailing blank lines from the citation.
  while (
    collapsedLines.length > 0 &&
    collapsedLines[collapsedLines.length - 1]!.trim() === ''
  ) {
    collapsedLines.pop();
  }

  return {
    head: headLines.join('\n'),
    collapsed: collapsedLines.join('\n'),
    tail: tailLines.join('\n'),
  };
}

// ── Private helpers ───────────────────────────────────────────────────────────

const ATTRIBUTION_RE =
  /^(On\b[\s\S]+\bwrote\s*:|Am\b[\s\S]+schrieb\b[\s\S]*:|El\b[\s\S]+escribi[oó]\s*:|Le\b[\s\S]+a\s+[ée]crit\s*:)\s*$/i;

/** Matches the RFC 3676 signature delimiter "-- " and the common variant "--". */
const SIG_DELIM_RE = /^-- ?$/;

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

    if (ATTRIBUTION_RE.test(trimmed)) {
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
