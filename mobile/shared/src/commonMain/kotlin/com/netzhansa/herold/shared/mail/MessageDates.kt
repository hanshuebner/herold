package com.netzhansa.herold.shared.mail

import kotlinx.datetime.DayOfWeek
import kotlinx.datetime.Instant
import kotlinx.datetime.Month
import kotlinx.datetime.TimeZone
import kotlinx.datetime.toLocalDateTime

/**
 * The two dates a message in the reading pane carries (issue #428): the
 * short one on the sender's line, and the full one the expanded
 * recipients block states.
 *
 * Both are pure given [TimeZone] and the reference instant, so the
 * boundaries - same day, same year, older - are unit-tested without a
 * clock.
 */
object MessageDates {

    /**
     * The date on the sender's line: the clock time for a message from
     * today, the day and month within the year, the year as well beyond
     * it.
     */
    fun short(atMs: Long, nowMs: Long, zone: TimeZone): String {
        if (atMs <= 0) return ""
        val at = Instant.fromEpochMilliseconds(atMs).toLocalDateTime(zone)
        val now = Instant.fromEpochMilliseconds(nowMs).toLocalDateTime(zone)
        return when {
            at.date == now.date -> clock(at.hour, at.minute)
            at.date.year == now.date.year -> "${at.date.dayOfMonth} ${abbreviate(at.date.month)}"
            else -> "${at.date.dayOfMonth} ${abbreviate(at.date.month)} ${at.date.year}"
        }
    }

    /** The full timestamp: weekday, date and clock time in the device's zone. */
    fun full(atMs: Long, zone: TimeZone): String {
        if (atMs <= 0) return ""
        val at = Instant.fromEpochMilliseconds(atMs).toLocalDateTime(zone)
        return "${abbreviate(at.date.dayOfWeek)}, ${at.date.dayOfMonth} ${abbreviate(at.date.month)} " +
            "${at.date.year} at ${clock(at.hour, at.minute)}"
    }

    private fun clock(hour: Int, minute: Int): String =
        hour.toString().padStart(2, '0') + ":" + minute.toString().padStart(2, '0')

    private fun abbreviate(month: Month): String = titleCase(month.name).take(3)

    private fun abbreviate(day: DayOfWeek): String = titleCase(day.name).take(3)

    private fun titleCase(name: String): String = name.take(1) + name.drop(1).lowercase()
}
