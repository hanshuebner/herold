/**
 * relativeTimeAgo -- formats a Date as a locale-aware relative-time
 * annotation, e.g. "(17 hours ago)" / "(vor 17 Stunden)".
 *
 * Bucket selection mirrors Gmail's coarse granularity:
 *   < 60 s       -> "just now" / "gerade eben"  (no number, from i18n table)
 *   < 3600 s     -> minutes
 *   < 86400 s    -> hours
 *   < 7 days     -> days
 *   < 35 days    -> weeks
 *   otherwise    -> months
 *
 * The signed direction is always "past" -- we use the absolute delta so
 * that a date that appears to be in the future (clock skew) degrades to
 * a small "N minutes ago" rather than a confusing negative label.
 *
 * The locale string is consumed from `localeTag()` so the bucket
 * strings come from the browser's Intl implementation, not a hand-rolled
 * plural table.
 */

import { localeTag, t } from '../i18n/i18n.svelte';

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const WEEK = 7 * DAY;
const MONTH_APPROX = 30 * DAY;

export function relativeTimeAgo(date: Date, now: Date = new Date()): string {
  const deltaMs = now.getTime() - date.getTime();
  // Use absolute value so clock-skew / future dates degrade gracefully.
  const deltaSec = Math.abs(deltaMs) / 1000;

  if (deltaSec < MINUTE) {
    return t('time.justNow');
  }

  const rtf = new Intl.RelativeTimeFormat(localeTag(), { numeric: 'always' });

  if (deltaSec < HOUR) {
    const minutes = Math.floor(deltaSec / MINUTE);
    return rtf.format(-minutes, 'minute');
  }

  if (deltaSec < DAY) {
    const hours = Math.floor(deltaSec / HOUR);
    return rtf.format(-hours, 'hour');
  }

  if (deltaSec < WEEK) {
    const days = Math.floor(deltaSec / DAY);
    return rtf.format(-days, 'day');
  }

  if (deltaSec < 5 * WEEK) {
    const weeks = Math.floor(deltaSec / WEEK);
    return rtf.format(-weeks, 'week');
  }

  const months = Math.max(1, Math.floor(deltaSec / MONTH_APPROX));
  return rtf.format(-months, 'month');
}
