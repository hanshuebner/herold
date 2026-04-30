/**
 * Advanced-search panel state helpers.
 *
 * The panel lets users construct a structured query by filling in typed
 * fields. On submit the fields are serialised to the same `operator:value`
 * query string format that the GlobalBar free-text input uses, so the
 * existing URL routing and `parseQuery` path handle both entry points
 * without duplication.
 */

export interface AdvancedSearchFields {
  from: string;
  to: string;
  subject: string;
  body: string;
  /** ISO 8601 date string or empty. */
  after: string;
  /** ISO 8601 date string or empty. */
  before: string;
  /** Mailbox id from mail.mailboxes, or empty for "any mailbox". */
  mailboxId: string;
  hasAttachment: boolean;
}

export function emptyFields(): AdvancedSearchFields {
  return {
    from: '',
    to: '',
    subject: '',
    body: '',
    after: '',
    before: '',
    mailboxId: '',
    hasAttachment: false,
  };
}

/**
 * Serialise panel fields into an `operator:value ...` query string.
 * Only fields that have been filled in contribute tokens; the result
 * is a compact string suitable for `encodeURIComponent` in the router.
 */
export function fieldsToQuery(fields: AdvancedSearchFields, mailboxNameById: Map<string, string>): string {
  const parts: string[] = [];

  if (fields.from.trim()) parts.push(`from:${quoteIfNeeded(fields.from.trim())}`);
  if (fields.to.trim()) parts.push(`to:${quoteIfNeeded(fields.to.trim())}`);
  if (fields.subject.trim()) parts.push(`subject:${quoteIfNeeded(fields.subject.trim())}`);
  if (fields.body.trim()) parts.push(`body:${quoteIfNeeded(fields.body.trim())}`);
  if (fields.after.trim()) {
    // Convert YYYY-MM-DD from the date input to ISO 8601.
    parts.push(`after:${toISODate(fields.after.trim())}`);
  }
  if (fields.before.trim()) {
    parts.push(`before:${toISODate(fields.before.trim())}`);
  }
  if (fields.mailboxId.trim()) {
    const name = mailboxNameById.get(fields.mailboxId.trim());
    if (name) parts.push(`label:${quoteIfNeeded(name)}`);
  }
  if (fields.hasAttachment) parts.push('has:attachment');

  return parts.join(' ');
}

/**
 * Parse an `operator:value ...` query string back into panel fields.
 * Unknown operators and plain text tokens are silently ignored — the
 * caller should display the raw query string separately for those.
 *
 * `mailboxIdByName` is a case-insensitive lookup used to reverse
 * `label:<name>` back to a mailbox id for the dropdown.
 */
export function queryToFields(
  query: string,
  mailboxIdByName: Map<string, string>,
): AdvancedSearchFields {
  const fields = emptyFields();
  if (!query.trim()) return fields;

  // Match operator:"quoted value" as a single token first, then plain tokens.
  const re = /([a-z_]+):"[^"]*"|([a-z_]+):\S+|\S+/gi;
  const tokens = query.match(re) ?? [];

  for (const tok of tokens) {
    const m = tok.match(/^([a-z_]+):(.+)$/i);
    if (!m) continue;
    const op = m[1]!.toLowerCase();
    const val = unquote(m[2]!);

    switch (op) {
      case 'from':
        fields.from = val;
        break;
      case 'to':
        fields.to = val;
        break;
      case 'subject':
        fields.subject = val;
        break;
      case 'body':
        fields.body = val;
        break;
      case 'after':
        // Convert ISO timestamp back to YYYY-MM-DD for the date input.
        fields.after = toDateInputValue(val);
        break;
      case 'before':
        fields.before = toDateInputValue(val);
        break;
      case 'label': {
        const lower = val.toLowerCase();
        for (const [name, id] of mailboxIdByName.entries()) {
          if (name.toLowerCase() === lower) {
            fields.mailboxId = id;
            break;
          }
        }
        break;
      }
      case 'has':
        if (val === 'attachment') fields.hasAttachment = true;
        break;
    }
  }

  return fields;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function quoteIfNeeded(s: string): string {
  return s.includes(' ') ? `"${s}"` : s;
}

function unquote(s: string): string {
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) return s.slice(1, -1);
  return s;
}

/**
 * Normalise a date string to YYYY-MM-DD for use in an `<input type="date">`.
 * Accepts ISO 8601 full timestamps (keeps only the date part) or YYYY-MM-DD.
 */
function toDateInputValue(iso: string): string {
  if (!iso) return '';
  // If it contains a T it is a full ISO timestamp; strip to the date part.
  const tIdx = iso.indexOf('T');
  return tIdx > 0 ? iso.slice(0, tIdx) : iso;
}

/**
 * Convert a YYYY-MM-DD string (from `<input type="date">`) to an ISO 8601
 * date string. Pass-through if already longer (full timestamp).
 */
function toISODate(s: string): string {
  if (s.length === 10 && /^\d{4}-\d{2}-\d{2}$/.test(s)) {
    return s;
  }
  return s;
}
