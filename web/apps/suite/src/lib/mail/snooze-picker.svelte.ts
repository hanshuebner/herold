/**
 * Snooze-target picker singleton — opened from list / thread keybinds
 * or per-message Snooze button. The active emailId lives here so a
 * single overlay component handles every entry point.
 */

class SnoozePicker {
  isOpen = $state(false);
  emailId = $state<string | null>(null);

  open(emailId: string): void {
    this.emailId = emailId;
    this.isOpen = true;
  }

  close(): void {
    this.isOpen = false;
    this.emailId = null;
  }
}

export const snoozePicker = new SnoozePicker();

/**
 * Compute a list of standard snooze quick-options relative to a
 * reference time. Public so the picker UI and tests can share the
 * exact same calculation.
 */
export interface SnoozeOption {
  label: string;
  /** ISO 8601 timestamp the picker will hand to snoozeEmail. */
  at: Date;
}

export function snoozeQuickOptions(now: Date = new Date()): SnoozeOption[] {
  const out: SnoozeOption[] = [];

  // Later today: now + 3 hours, only if before 18:00 local.
  const laterToday = new Date(now);
  laterToday.setHours(now.getHours() + 3, 0, 0, 0);
  if (laterToday.getDate() === now.getDate() && laterToday.getHours() <= 21) {
    out.push({ label: 'Later today', at: laterToday });
  }

  // Tomorrow morning: 8:00 next day.
  const tomorrowAm = new Date(now);
  tomorrowAm.setDate(now.getDate() + 1);
  tomorrowAm.setHours(8, 0, 0, 0);
  out.push({ label: 'Tomorrow morning', at: tomorrowAm });

  // This weekend: next Saturday 9:00. Saturday is JS day 6.
  const weekend = new Date(now);
  const daysToSat = (6 - now.getDay() + 7) % 7 || 7;
  weekend.setDate(now.getDate() + daysToSat);
  weekend.setHours(9, 0, 0, 0);
  // Only show if weekend is at least one day away — otherwise the
  // tomorrow-morning option is the better choice.
  if (daysToSat >= 2) {
    out.push({ label: 'This weekend', at: weekend });
  }

  // Next week: next Monday 8:00.
  const nextMon = new Date(now);
  const daysToMon = (1 - now.getDay() + 7) % 7 || 7;
  nextMon.setDate(now.getDate() + daysToMon);
  nextMon.setHours(8, 0, 0, 0);
  out.push({ label: 'Next week', at: nextMon });

  return out;
}

export const _internals_forTest = { snoozeQuickOptions };
