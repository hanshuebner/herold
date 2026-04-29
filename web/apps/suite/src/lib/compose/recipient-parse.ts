/**
 * Pure helpers for parsing recipient address strings into chips.
 *
 * Implements the parsing rules for REQ-MAIL-11a..d:
 *   - Recognized formats: bare `local@domain`, `Name <local@domain>`,
 *     `local@domain "Name"`.
 *   - Whitespace inside `<...>` or `"..."` groups is NOT a separator.
 *   - Commit triggers: comma, semicolon, Tab, Enter, or trailing space
 *     after a structurally-complete address token.
 *   - Paste parsing produces one chip per recognized fragment; an
 *     unrecognized tail is returned in `rest`.
 */

export interface Recipient {
  name?: string;
  email: string;
}

// Basic email address pattern: local@domain where domain has at least one dot.
// Deliberately liberal — RFC 5321 allows many characters; we validate shape
// rather than trying to be RFC-exhaustive.
const EMAIL_RE = /^[^\s@<>"()\[\],;]+@[^\s@<>"()\[\],;]+\.[^\s@<>"()\[\],;]+$/;

/** True when `email` looks like a plausible email address. */
function isEmail(email: string): boolean {
  return EMAIL_RE.test(email);
}

/**
 * True when the buffer has no unmatched open `<` or open `"` — i.e.
 * the user is not mid-way through a structured address form. Used to
 * decide whether a separator character should commit.
 */
export function isStructurallyComplete(buffer: string): boolean {
  let inAngle = false;
  let inQuote = false;
  for (let i = 0; i < buffer.length; i++) {
    const ch = buffer[i]!;
    if (inQuote) {
      if (ch === '\\') {
        i++; // skip escaped character
      } else if (ch === '"') {
        inQuote = false;
      }
    } else if (inAngle) {
      if (ch === '>') {
        inAngle = false;
      }
    } else {
      if (ch === '"') {
        inQuote = true;
      } else if (ch === '<') {
        inAngle = true;
      }
    }
  }
  return !inAngle && !inQuote;
}

/**
 * Try to parse a single address token from the start of the string.
 * Returns the recognized Recipient and the unparsed remainder, or
 * null when the token is not recognizable.
 *
 * Recognizes:
 *   - `local@domain`
 *   - `Name <local@domain>`  (display name before angle address)
 *   - `local@domain "Name"`  (bare address followed by quoted name)
 *   - `"Name" <local@domain>` (quoted name before angle address)
 */
function tryParseOne(s: string): { recipient: Recipient; rest: string } | null {
  const trimmed = s.trimStart();
  if (!trimmed) return null;

  // Pattern: `Name <email>` or `"Name" <email>` (angle-bracket form)
  // Note: no `s` flag — use [\s\S] for multiline matching to stay within ES2017.
  const angleMatch = trimmed.match(/^("(?:[^"\\]|\\.)*"|[^<,;"\n]+?)\s*<([^>]*?)>([\s\S]*)/);
  if (angleMatch) {
    const rawName = angleMatch[1]!.trim();
    const email = angleMatch[2]!.trim();
    const rest = angleMatch[3]!;
    if (isEmail(email)) {
      // Strip surrounding quotes if the name was quoted.
      const name = rawName.startsWith('"') && rawName.endsWith('"')
        ? rawName.slice(1, -1).replace(/\\(.)/g, '$1').trim()
        : rawName;
      return { recipient: { name: name || undefined, email }, rest };
    }
  }

  // Pattern: `email "Name"` (bare address then quoted display name)
  const addrThenName = trimmed.match(/^([^\s<>,;"]+)\s+"((?:[^"\\]|\\.)*?)"\s*([\s\S]*)/);
  if (addrThenName) {
    const email = addrThenName[1]!.trim();
    const name = addrThenName[2]!.replace(/\\(.)/g, '$1').trim();
    const rest = addrThenName[3]!;
    if (isEmail(email)) {
      return { recipient: { name: name || undefined, email }, rest };
    }
  }

  // Pattern: bare `email` — no surrounding names or brackets.
  // Token ends at the first separator, space, or end of string.
  const bareMatch = trimmed.match(/^([^\s<>,;\n"]+)([\s\S]*)/);
  if (bareMatch) {
    const email = bareMatch[1]!.trim();
    const rest = bareMatch[2]!;
    if (isEmail(email)) {
      return { recipient: { email }, rest };
    }
  }

  return null;
}

/**
 * Attempt to extract one or more recipients from `buffer` greedily,
 * advancing past separator characters between tokens. The unrecognized
 * tail (if any) is returned in `rest` — it stays in the input buffer
 * per REQ-MAIL-11d.
 *
 * Respects `<...>` and `"..."` grouping: a separator character inside
 * a group is part of the token, not a split point.
 */
export function tryCommit(buffer: string): { chips: Recipient[]; rest: string } {
  const chips: Recipient[] = [];
  let remaining = buffer;

  while (remaining.length > 0) {
    // Skip leading separators and whitespace.
    const trimmed = remaining.replace(/^[\s,;\n]+/, '');
    if (!trimmed) {
      remaining = '';
      break;
    }

    const result = tryParseOne(trimmed);
    if (!result) {
      remaining = trimmed;
      break;
    }

    chips.push(result.recipient);
    remaining = result.rest;

    // Consume trailing separator(s) between tokens.
    remaining = remaining.replace(/^[\s,;\n]+/, '');
  }

  return { chips, rest: remaining };
}

/**
 * Parse a multi-address paste value in one pass. Splits on commas,
 * semicolons, and newlines (per REQ-MAIL-11c) while respecting
 * structural grouping. Produces one chip per recognized address;
 * the unrecognized tail (if any) is returned in `rest`.
 */
export function parsePaste(text: string): { chips: Recipient[]; rest: string } {
  return tryCommit(text);
}

/**
 * Render a Recipient back to a display string suitable for the
 * compose.to / cc / bcc string field (for autosave and snapshot).
 */
export function recipientToString(r: Recipient): string {
  if (r.name) {
    return `${r.name} <${r.email}>`;
  }
  return r.email;
}
