/**
 * Heuristics for separating an email body's "fresh" content from the
 * quoted reply history. Used by the thread reader to hide the prior
 * message by default and reveal it on demand.
 */

/**
 * Splits a plain-text body into the leading "fresh" portion and the
 * trailing quoted-history portion, if any. Detection rules, applied
 * top-down to find the first quoted-history boundary:
 *
 *   1. An "On <date>, <addr> wrote:" / "Am <date> schrieb <addr>:"
 *      attribution line, followed by lines beginning with `>`.
 *   2. A line that consists solely of `>` quote prefixes (any depth).
 *   3. The legacy `--` (sigdash) is *not* a quote boundary; it just
 *      separates a signature.
 *
 * If no boundary is found, `quoted` is empty and the original body is
 * returned in `fresh`. The split preserves the line break that ended
 * `fresh` so concatenating fresh + "\n" + quoted reconstructs the input.
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
  // Drop a single trailing blank line from `fresh` so we don't keep the
  // blank line separator the user typed before the attribution.
  while (
    freshLines.length > 0 &&
    freshLines[freshLines.length - 1]!.trim() === ''
  ) {
    freshLines.pop();
  }
  return {
    fresh: freshLines.join('\n'),
    quoted: quotedLines.join('\n'),
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

function nextNonBlank(lines: string[], from: number): number {
  for (let i = from; i < lines.length; i++) {
    if (lines[i]!.trim() !== '') return i;
  }
  return -1;
}
