/**
 * Heuristics for separating an email body's "fresh" content from the
 * quoted reply history. Used by the thread reader to hide the prior
 * message by default and reveal it on demand.
 */

/**
 * Splits a plain-text body into the "fresh" portion (all new content, always
 * shown) and the "quoted" portion (the prior message history, hidden behind
 * the "..." affordance by default). Detection rules, applied top-down to find
 * the first quoted-history boundary:
 *
 *   1. An "On <date>, <addr> wrote:" / "Am <date> schrieb <addr>:"
 *      attribution line, followed by lines beginning with `>`.
 *   2. A line that consists solely of `>` quote prefixes (any depth).
 *   3. The legacy `--` (sigdash) is *not* a quote boundary; it just
 *      separates a signature.
 *
 * Fresh content includes any text before the first quoted block AND any
 * non-quoted text found after or interleaved within the quoted section.
 * Only purely-quoted segments (attribution lines + `>`-prefixed lines) are
 * collapsed. A reply where all new text follows the quote is a common
 * composition pattern and is never trimmed.
 *
 * If no quoted block is found, `quoted` is empty and the original body is
 * returned in `fresh`.
 */
export function splitQuotedText(body: string): {
  fresh: string;
  quoted: string;
} {
  if (!body) return { fresh: '', quoted: '' };

  const lines = body.split(/\r?\n/);
  const boundary = findQuoteBoundary(lines);
  if (boundary < 0) return { fresh: body, quoted: '' };

  const freshLines = lines.slice(0, boundary);
  const quotedLines = lines.slice(boundary);

  // Drop trailing blank lines from `fresh` so we don't keep the blank line
  // separator the user typed before the attribution.
  while (freshLines.length > 0 && freshLines[freshLines.length - 1]!.trim() === '') {
    freshLines.pop();
  }

  // Separate any non-quoted content interleaved within or trailing the quoted
  // section. Such content is new text from the sender and must always be visible.
  const { actualQuotedLines, extraFreshLines } = extractNonQuotedContent(quotedLines);

  // Combine pre-quote fresh and any extracted non-quoted content.
  const freshParts: string[] = [];
  if (freshLines.length > 0) freshParts.push(freshLines.join('\n'));
  if (extraFreshLines.length > 0) freshParts.push(extraFreshLines.join('\n'));

  return {
    fresh: freshParts.join('\n'),
    quoted: actualQuotedLines.join('\n'),
  };
}

const ATTRIBUTION_RE =
  /^(On\b[\s\S]+\bwrote\s*:|Am\b[\s\S]+schrieb\b[\s\S]*:|El\b[\s\S]+escribi[oó]\s*:|Le\b[\s\S]+a\s+[ée]crit\s*:)\s*$/i;

function findQuoteBoundary(lines: string[]): number {
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    // Rule 1: attribution line followed by quoted block.
    if (ATTRIBUTION_RE.test(line.trim())) {
      const next = nextNonBlank(lines, i + 1);
      if (next !== -1 && lines[next]!.trim().startsWith('>')) {
        return i;
      }
      // Attribution-only with no quoted block beneath: still treat as
      // boundary so the "On ... wrote:" line collapses with the rest.
      if (next !== -1) return i;
    }
    // Rule 2: a quote-prefix line — one or more `>` characters at the
    // start, followed by whitespace or end of line. Covers `> body`,
    // `>>nested`, and `>` on its own.
    if (/^>+(\s|$)/.test(line.trim())) {
      return i;
    }
  }
  return -1;
}

/**
 * Within the lines that make up the quoted section (from the first quote
 * boundary to the end), separates the purely-quoted lines from any non-quoted
 * content (trailing new text or inline replies between quoted paragraphs).
 *
 * Blank lines that fall between quoted segments are kept with the quoted
 * section. Blank lines that separate a quoted segment from a non-quoted
 * segment are treated as separators and discarded (so the caller does not
 * render a leading blank before the fresh content).
 */
function extractNonQuotedContent(quotedLines: string[]): {
  actualQuotedLines: string[];
  extraFreshLines: string[];
} {
  const quotedOnly: string[] = [];
  const extraFresh: string[] = [];
  const pendingBlanks: string[] = [];

  for (const line of quotedLines) {
    const trimmed = line.trim();
    const isQuotedLine =
      /^>+(\s|$)/.test(trimmed) || ATTRIBUTION_RE.test(trimmed);

    if (isQuotedLine) {
      // Blank lines buffered since the last quoted line belong to the quoted
      // section (they separate quoted paragraphs, e.g. multi-block replies).
      quotedOnly.push(...pendingBlanks, line);
      pendingBlanks.length = 0;
    } else if (trimmed === '') {
      // Buffer — we don't yet know whether this blank separates two quoted
      // segments (goes to quoted) or precedes fresh content (discarded).
      pendingBlanks.push(line);
    } else {
      // Non-quoted, non-blank: new content from the sender.
      // Blank lines already in extraFresh are paragraph separators within the
      // fresh content and must be kept. Leading blanks (those before the first
      // fresh line) are discarded so we don't emit a spurious leading newline.
      if (extraFresh.length > 0) {
        extraFresh.push(...pendingBlanks);
      }
      extraFresh.push(line);
      pendingBlanks.length = 0;
    }
  }
  // Any remaining pending blanks are trailing whitespace — discard them.

  return { actualQuotedLines: quotedOnly, extraFreshLines: extraFresh };
}

function nextNonBlank(lines: string[], from: number): number {
  for (let i = from; i < lines.length; i++) {
    if (lines[i]!.trim() !== '') return i;
  }
  return -1;
}
