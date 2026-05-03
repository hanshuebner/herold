/**
 * Client-log viewer filter state encoder / decoder.
 *
 * Provides a typed representation of the URL query parameters for
 * GET /api/v1/admin/clientlog and helpers to build / parse them.
 * REQ-ADM-230.
 */

export interface ClientlogFilters {
  slice: 'auth' | 'public';
  app: '' | 'suite' | 'admin';
  kind: '' | 'error' | 'log' | 'vital';
  level: '' | 'trace' | 'debug' | 'info' | 'warn' | 'error';
  since: string;
  until: string;
  user: string;
  session_id: string;
  request_id: string;
  route: string;
  text: string;
}

export const DEFAULT_FILTERS: ClientlogFilters = {
  slice: 'auth',
  app: '',
  kind: '',
  level: '',
  since: '',
  until: '',
  user: '',
  session_id: '',
  request_id: '',
  route: '',
  text: '',
};

/**
 * Encode a filters object as URLSearchParams ready for appending to the
 * API path. Empty / default values are omitted to keep URLs clean.
 */
export function encodeFilters(
  filters: ClientlogFilters,
  cursor?: string,
  limit = 50,
): URLSearchParams {
  const p = new URLSearchParams();
  // slice is always sent (server defaults to 'auth' but we are explicit).
  p.set('slice', filters.slice);
  p.set('limit', String(limit));
  if (filters.app) p.set('app', filters.app);
  if (filters.kind) p.set('kind', filters.kind);
  if (filters.level) p.set('level', filters.level);
  if (filters.since.trim()) {
    const ts = datetimeLocalToRFC3339(filters.since.trim());
    if (ts) p.set('since', ts);
  }
  if (filters.until.trim()) {
    const ts = datetimeLocalToRFC3339(filters.until.trim());
    if (ts) p.set('until', ts);
  }
  if (filters.user.trim()) p.set('user', filters.user.trim());
  if (filters.session_id.trim()) p.set('session_id', filters.session_id.trim());
  if (filters.request_id.trim()) p.set('request_id', filters.request_id.trim());
  if (filters.route.trim()) p.set('route', filters.route.trim());
  if (filters.text.trim()) p.set('text', filters.text.trim());
  if (cursor) p.set('cursor', cursor);
  return p;
}

/**
 * Decode URL search params back into a ClientlogFilters object.
 * Unknown or invalid values are silently replaced with defaults.
 */
export function decodeFilters(params: URLSearchParams): ClientlogFilters {
  const out: ClientlogFilters = { ...DEFAULT_FILTERS };

  const slice = params.get('slice');
  if (slice === 'auth' || slice === 'public') out.slice = slice;

  const app = params.get('app');
  if (app === 'suite' || app === 'admin') out.app = app;

  const kind = params.get('kind');
  if (kind === 'error' || kind === 'log' || kind === 'vital') out.kind = kind;

  const level = params.get('level');
  if (
    level === 'trace' ||
    level === 'debug' ||
    level === 'info' ||
    level === 'warn' ||
    level === 'error'
  ) {
    out.level = level;
  }

  out.since = params.get('since') ?? '';
  out.until = params.get('until') ?? '';
  out.user = params.get('user') ?? '';
  out.session_id = params.get('session_id') ?? '';
  out.request_id = params.get('request_id') ?? '';
  out.route = params.get('route') ?? '';
  out.text = params.get('text') ?? '';

  return out;
}

/**
 * Convert a datetime-local input value ("YYYY-MM-DDTHH:MM") to an RFC 3339
 * string. The input is treated as UTC. Returns null if the input does not
 * parse cleanly.
 *
 * The datetime-local pattern is strictly "YYYY-MM-DDTHH:MM" (19 chars before
 * optional seconds). We reject strings that are too short or lack the 'T'
 * date/time separator so we do not accidentally parse partial values.
 */
function datetimeLocalToRFC3339(raw: string): string | null {
  // Accept already-fully-specified ISO strings.
  if (raw.includes('Z') || raw.includes('+')) {
    const d = new Date(raw);
    return isNaN(d.getTime()) ? null : d.toISOString().replace('.000Z', 'Z');
  }
  // Require at least "YYYY-MM-DDTHH:MM" (16 chars) with a 'T' separator.
  if (raw.length < 16 || !raw.includes('T')) return null;
  // Append seconds and Z to force UTC interpretation.
  const normalised = raw + ':00Z';
  const d = new Date(normalised);
  if (isNaN(d.getTime())) return null;
  return d.toISOString().replace('.000Z', 'Z');
}
