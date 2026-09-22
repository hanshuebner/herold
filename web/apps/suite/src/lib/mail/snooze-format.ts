/**
 * Shared "wake time relative to now" formatter for the snooze feature
 * (issue #274 and follow-ups). The SnoozePicker's quick-pick preview,
 * the snooze confirmation toast, and each row's wake time in the
 * Snoozed virtual folder (re #471) all render through this one
 * function so the three surfaces stay in agreement instead of
 * maintaining three near-identical date-math implementations.
 */

import { localeTag, t } from '../i18n/i18n.svelte';

/**
 * Formats `target` the way the rest of the snooze UI does:
 *   - today: just the time ("3:00 PM")
 *   - tomorrow: time + localized "tomorrow" ("3:00 PM tomorrow")
 *   - within the next week: weekday + time ("Monday, 3:00 PM")
 *   - further out: month/day + time ("May 12, 3:00 PM")
 *
 * `now` is a parameter (defaulting to the current time) so callers and
 * tests can pin the reference point.
 */
export function formatWakeTime(target: Date, now: Date = new Date()): string {
  const tag = localeTag();
  const time = target.toLocaleTimeString(tag, {
    hour: 'numeric',
    minute: '2-digit',
  });
  const dayDiff = Math.round(
    (new Date(target.getFullYear(), target.getMonth(), target.getDate()).getTime() -
      new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime()) /
      86400000,
  );
  if (dayDiff === 0) return time;
  if (dayDiff === 1) return `${time} ${t('mail.snooze.tomorrow')}`;
  if (dayDiff > 0 && dayDiff < 7) {
    return `${target.toLocaleDateString(tag, { weekday: 'long' })}, ${time}`;
  }
  return `${target.toLocaleDateString(tag, {
    month: 'short',
    day: 'numeric',
  })}, ${time}`;
}
