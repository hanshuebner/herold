package com.netzhansa.herold.shared.actions

import kotlinx.datetime.DateTimeUnit
import kotlinx.datetime.DayOfWeek
import kotlinx.datetime.Instant
import kotlinx.datetime.LocalDateTime
import kotlinx.datetime.LocalTime
import kotlinx.datetime.TimeZone
import kotlinx.datetime.isoDayNumber
import kotlinx.datetime.plus
import kotlinx.datetime.toInstant
import kotlinx.datetime.toLocalDateTime

/** The suite's snooze presets (docs/design/web/requirements/06-snooze.md REQ-SNZ-01..05). */
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

    /** The `snoozedUntil` value `Email/set` takes: a UTC date-time string. */
    fun wireValue(instant: Instant): String = instant.toString()
}
