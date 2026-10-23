/**
 * Date-column formatter shared by the message-list views (re #529).
 *
 * A message received on the browser's current local calendar day shows
 * its time of day; an older message from the current year shows
 * month + day; anything from an earlier year also shows the year.
 * `now` is a parameter so callers and tests can pin the reference point.
 */

export function formatRowDate(iso: string, tag: string, now: Date = new Date()): string {
  const d = new Date(iso);
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  if (sameDay) {
    return d.toLocaleTimeString(tag, { timeStyle: 'short' });
  }
  const opts: Intl.DateTimeFormatOptions =
    d.getFullYear() === now.getFullYear()
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
  return d.toLocaleDateString(tag, opts);
}
