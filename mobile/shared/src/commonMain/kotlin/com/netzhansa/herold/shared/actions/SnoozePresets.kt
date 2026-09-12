package com.netzhansa.herold.shared.actions

import kotlinx.datetime.DateTimeUnit
import kotlinx.datetime.DayOfWeek
import kotlinx.datetime.Instant
import kotlinx.datetime.LocalDate
import kotlinx.datetime.LocalDateTime
import kotlinx.datetime.LocalTime
import kotlinx.datetime.Month
import kotlinx.datetime.TimeZone
import kotlinx.datetime.isoDayNumber
import kotlinx.datetime.plus
import kotlinx.datetime.toInstant
import kotlinx.datetime.toLocalDateTime

/** The suite's snooze presets (docs/design/web/requirements/06-snooze.md REQ-SNZ-01..04). */
enum class SnoozePreset {
    LATER_TODAY,
    TOMORROW_MORNING,
    THIS_WEEKEND,
    NEXT_WEEK,
}

/**
 * Wake times for the snooze presets, computed in the device's time zone.
 * Pure given [now] and [zone], so the boundary cases (before/after 14:00, a
 * Saturday's "this weekend") are unit-tested without a clock.
 */
object SnoozeClock {
    private val MORNING = LocalTime(8, 0)
    private val AFTERNOON = LocalTime(16, 0)
    private const val AFTERNOON_CUTOFF_HOUR = 14

    fun wakeTime(preset: SnoozePreset, now: Instant, zone: TimeZone): Instant {
        val local = now.toLocalDateTime(zone)
        return when (preset) {
            SnoozePreset.LATER_TODAY ->
                if (local.hour < AFTERNOON_CUTOFF_HOUR) {
                    LocalDateTime(local.date, AFTERNOON).toInstant(zone)
                } else {
                    LocalDateTime(local.date.plus(1, DateTimeUnit.DAY), MORNING).toInstant(zone)
                }

            SnoozePreset.TOMORROW_MORNING ->
                LocalDateTime(local.date.plus(1, DateTimeUnit.DAY), MORNING).toInstant(zone)

            SnoozePreset.THIS_WEEKEND -> {
                val daysToSaturday = ((DayOfWeek.SATURDAY.isoDayNumber - local.date.dayOfWeek.isoDayNumber) + 7) % 7
                val days = if (daysToSaturday == 0) 7 else daysToSaturday
                LocalDateTime(local.date.plus(days, DateTimeUnit.DAY), MORNING).toInstant(zone)
            }

            SnoozePreset.NEXT_WEEK -> {
                val daysToMonday = ((DayOfWeek.MONDAY.isoDayNumber - local.date.dayOfWeek.isoDayNumber) + 7) % 7
                val days = if (daysToMonday == 0) 7 else daysToMonday
                LocalDateTime(local.date.plus(days, DateTimeUnit.DAY), MORNING).toInstant(zone)
            }
        }
    }

    /**
     * What a custom pick opens on (REQ-SNZ-05): the next full hour in the
     * device's zone, so the common "later today" edit is one dial turn away.
     */
    fun nextFullHour(now: Instant, zone: TimeZone): Instant {
        val local = now.toLocalDateTime(zone)
        return LocalDateTime(local.date, LocalTime(local.hour, 0)).toInstant(zone)
            .plus(1, DateTimeUnit.HOUR)
    }

    /** The instant a picked local date and clock time name in [zone] (REQ-SNZ-05). */
    fun customWakeTime(date: LocalDate, hour: Int, minute: Int, zone: TimeZone): Instant =
        LocalDateTime(date, LocalTime(hour, minute)).toInstant(zone)

    /** The `snoozedUntil` value `Email/set` takes: a UTC date-time string. */
    fun wireValue(instant: Instant): String = instant.toString()

    /** The wire value back, or null when the server sent something unparseable. */
    fun parseWake(value: String): Instant? = try {
        Instant.parse(value)
    } catch (e: IllegalArgumentException) {
        null
    }

    /**
     * A wake time as the snoozed indicator states it, following the suite's
     * relative phrasing (`formatSnoozeTarget`): the clock time alone today,
     * "tomorrow" the next day, the weekday within the week, and the date
     * beyond it.
     */
    fun describe(wakeAt: Instant, now: Instant, zone: TimeZone): String {
        val target = wakeAt.toLocalDateTime(zone)
        val time = target.hour.toString().padStart(2, '0') + ":" + target.minute.toString().padStart(2, '0')
        return when (target.date.toEpochDays() - now.toLocalDateTime(zone).date.toEpochDays()) {
            0 -> time
            1 -> "$time tomorrow"
            in 2..6 -> "${weekdayName(target.date.dayOfWeek)}, $time"
            else -> "${monthAbbreviation(target.date.month)} ${target.date.dayOfMonth}, $time"
        }
    }

    private fun weekdayName(day: DayOfWeek): String = titleCase(day.name)

    private fun monthAbbreviation(month: Month): String = titleCase(month.name).take(3)

    private fun titleCase(name: String): String =
        name.take(1) + name.drop(1).lowercase()
}
