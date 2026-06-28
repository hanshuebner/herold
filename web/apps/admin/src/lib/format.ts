/**
 * Locale-aware formatting helpers for the admin SPA.
 *
 * All formatters resolve locale and time zone from the browser's configured
 * environment via the Intl APIs (no hardcoded locale string or time zone
 * identifier). Passing undefined as the locale argument tells Intl to use
 * the browser's default locale.
 */

/**
 * Format an ISO 8601 timestamp as a relative time string using the browser's
 * locale (e.g. "3 minutes ago", "in 2 hours", "yesterday").
 *
 * Returns an empty string for null/undefined input. Returns the raw value if
 * parsing fails.
 */
export function formatRelative(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;

  const diffMs = d.getTime() - Date.now();
  const absMs = Math.abs(diffMs);

  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });

  if (absMs < 60_000) {
    return rtf.format(Math.round(diffMs / 1000), 'second');
  }
  if (absMs < 3_600_000) {
    return rtf.format(Math.round(diffMs / 60_000), 'minute');
  }
  if (absMs < 86_400_000) {
    return rtf.format(Math.round(diffMs / 3_600_000), 'hour');
  }
  return rtf.format(Math.round(diffMs / 86_400_000), 'day');
}

/**
 * Format options for a date+time display with hour and minute precision
 * (no seconds). Suitable for event timestamps and log entries.
 */
export const DATE_TIME_SHORT: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
};

/**
 * Format options for a date+time display with second precision. Suitable for
 * queue items and other high-resolution timestamps.
 */
export const DATE_TIME_WITH_SECONDS: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
};

/**
 * Format options for a date-only display (no time). Suitable for creation
 * dates on list views.
 */
export const DATE_ONLY: Intl.DateTimeFormatOptions = {
  year: 'numeric',
  month: 'short',
  day: 'numeric',
};

/**
 * Format an ISO 8601 timestamp as an absolute date/time string using the
 * browser's locale and time zone.
 *
 * @param iso    ISO 8601 string, or null/undefined.
 * @param opts   Intl.DateTimeFormatOptions. Defaults to DATE_TIME_WITH_SECONDS.
 *
 * Returns an empty string for null/undefined input. Returns the raw value if
 * parsing fails.
 */
export function formatAbsolute(
  iso: string | null | undefined,
  opts: Intl.DateTimeFormatOptions = DATE_TIME_WITH_SECONDS,
): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, opts);
}

/**
 * Format an ISO 8601 timestamp as a date-only string using the browser's
 * locale and time zone (e.g. "Jun 28, 2026").
 *
 * Returns an empty string for null/undefined input. Returns the raw value if
 * parsing fails.
 */
export function formatDateOnly(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, DATE_ONLY);
}
