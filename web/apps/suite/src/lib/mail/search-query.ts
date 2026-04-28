/**
 * The suite's search query language → JMAP `FilterCondition` / `FilterOperator`.
 *
 * Per docs/requirements/07-search.md REQ-SRC-40..45. v1 supports the
 * dominant operator set: from / to / cc / bcc / subject / body / has /
 * is / before / after / label / list. AND is implicit. Negation (`-`)
 * and OR are deferred.
 *
 * Bareword tokens map to `{ text: <value> }` for full-text search.
 * Multiple terms combine into a `{ operator: 'AND', conditions: [...] }`
 * tree.
 */

import type { Mailbox } from './types';

export type FilterCondition = Record<string, unknown>;
export interface FilterOperator {
  operator: 'AND' | 'OR' | 'NOT';
  conditions: (FilterCondition | FilterOperator)[];
}

export interface ParsedQuery {
  filter: FilterCondition | FilterOperator;
}

export interface ParseContext {
  /** Resolve `label:<name>` → mailbox id, if available. */
  mailboxes: Map<string, Mailbox>;
}

/**
 * UI-side projection of a single recognised query token. The search
 * route renders these as chips above the result list so the user can
 * see at a glance how the parser understood their input.
 */
export interface SearchChip {
  /** Original token string (e.g. "from:alice@x" or "weekly meeting"). */
  raw: string;
  /** Operator name when recognised, otherwise "text". */
  operator: string;
  /** Operator value (or text content). */
  value: string;
  /** Human-friendly label rendered inside the chip. */
  label: string;
}

/** Decode a query into UI chips without committing to a JMAP filter. */
export function decodeChips(input: string): SearchChip[] {
  const trimmed = input.trim();
  if (!trimmed) return [];
  const tokens = tokenize(trimmed);
  return tokens.map((tok) => decodeOne(tok));
}

function decodeOne(token: string): SearchChip {
  const m = token.match(/^([a-z_]+):(.+)$/i);
  if (!m) {
    const value = unquote(token);
    return { raw: token, operator: 'text', value, label: value };
  }
  const op = m[1]!.toLowerCase();
  const val = unquote(m[2]!);
  return { raw: token, operator: op, value: val, label: chipLabel(op, val) };
}

function chipLabel(op: string, val: string): string {
  switch (op) {
    case 'has':
      return val === 'attachment' ? 'has attachment' : `has:${val}`;
    case 'is':
      return `is ${val}`;
    case 'before':
      return `before ${val}`;
    case 'after':
      return `after ${val}`;
    case 'newer_than':
      return `newer than ${val}`;
    case 'older_than':
      return `older than ${val}`;
    case 'label':
      return `label ${val}`;
    case 'list':
      return `list ${val}`;
    case 'filename':
      return `filename ${val}`;
    default:
      return `${op}: ${val}`;
  }
}

/** Parse a suite search-query string into a JMAP filter shape. */
export function parseQuery(input: string, ctx: ParseContext): ParsedQuery {
  const trimmed = input.trim();
  if (!trimmed) return { filter: { text: '' } };
  const tokens = tokenize(trimmed);
  const conditions = tokens
    .map((tok) => parseToken(tok, ctx))
    .filter((c): c is FilterCondition => c !== null);
  if (conditions.length === 0) return { filter: { text: trimmed } };
  if (conditions.length === 1) return { filter: conditions[0]! };
  return { filter: { operator: 'AND', conditions } };
}

function tokenize(input: string): string[] {
  // Match "quoted phrase" or non-whitespace runs. Quoted phrases are
  // preserved with their quotes so the token parser can detect them.
  const re = /"[^"]*"|\S+/g;
  return input.match(re) ?? [];
}

function parseToken(token: string, ctx: ParseContext): FilterCondition | null {
  // operator:value pattern (operator name is letters / underscore).
  const m = token.match(/^([a-z_]+):(.+)$/i);
  if (m) {
    const name = m[1]!.toLowerCase();
    const value = unquote(m[2]!);
    return mapOperator(name, value, ctx);
  }
  return { text: unquote(token) };
}

function unquote(s: string): string {
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) return s.slice(1, -1);
  return s;
}

function mapOperator(
  name: string,
  value: string,
  ctx: ParseContext,
): FilterCondition | null {
  switch (name) {
    case 'from':
      return { from: value };
    case 'to':
      return { to: value };
    case 'cc':
      return { cc: value };
    case 'bcc':
      return { bcc: value };
    case 'subject':
      return { subject: value };
    case 'body':
      return { body: value };
    case 'header':
      return { header: [value] };
    case 'list':
      return { header: ['List-Id', value] };
    case 'label': {
      // Resolve label name → mailbox id.
      const lower = value.toLowerCase();
      for (const m of ctx.mailboxes.values()) {
        if (m.name.toLowerCase() === lower) return { inMailbox: m.id };
      }
      // Unknown label — match nothing rather than ignore: produce a filter
      // that no email satisfies. JMAP doesn't have a "false" sentinel, so
      // approximate with a clearly impossible inMailbox.
      return { inMailbox: '__unknown_label__' };
    }
    case 'has':
      if (value === 'attachment') return { hasAttachment: true };
      return null;
    case 'is':
      if (value === 'unread') return { notKeyword: '$seen' };
      if (value === 'read') return { hasKeyword: '$seen' };
      if (value === 'starred' || value === 'flagged') return { hasKeyword: '$flagged' };
      if (value === 'unstarred' || value === 'unflagged') return { notKeyword: '$flagged' };
      if (value === 'snoozed') return { hasKeyword: '$snoozed' };
      if (value === 'important') return { hasKeyword: '$important' };
      return null;
    case 'before':
      return { before: parseDate(value) };
    case 'after':
      return { after: parseDate(value) };
    case 'newer_than': {
      const ms = relativeDurationMs(value);
      if (ms === null) return null;
      return { after: new Date(Date.now() - ms).toISOString() };
    }
    case 'older_than': {
      const ms = relativeDurationMs(value);
      if (ms === null) return null;
      return { before: new Date(Date.now() - ms).toISOString() };
    }
    case 'size':
      // size:>1000000 etc. — basic exact match for now.
      const n = parseInt(value, 10);
      if (!Number.isFinite(n)) return null;
      return { minSize: n };
    case 'filename':
      return { attachmentName: value };
    default:
      // Unknown operator — fall through as text search.
      return { text: `${name}:${value}` };
  }
}

function parseDate(value: string): string {
  // Relative form: "1d" / "2w" / "3m" / "1y"
  const ms = relativeDurationMs(value);
  if (ms !== null) {
    return new Date(Date.now() - ms).toISOString();
  }
  // ISO YYYY-MM-DD or full datetime
  const d = new Date(value);
  if (!Number.isNaN(d.getTime())) return d.toISOString();
  return value;
}

function relativeDurationMs(value: string): number | null {
  const m = value.match(/^(\d+)([dwmy])$/);
  if (!m) return null;
  const n = parseInt(m[1]!, 10);
  const unit = m[2]!;
  const day = 86400000;
  switch (unit) {
    case 'd':
      return n * day;
    case 'w':
      return n * 7 * day;
    case 'm':
      return n * 30 * day;
    case 'y':
      return n * 365 * day;
  }
  return null;
}
