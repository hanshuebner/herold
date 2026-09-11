package com.netzhansa.herold.shared.actions

import kotlinx.datetime.Instant
import kotlinx.datetime.TimeZone
import kotlin.test.Test
import kotlin.test.assertEquals

/** The suite's presets (REQ-SNZ-01..04), evaluated in the device's zone. */
class SnoozeClockTest {
    private val berlin = TimeZone.of("Europe/Berlin")

    @Test
    fun laterTodayIsFourPmBeforeTwoPmAndTomorrowMorningAfterwards() {
        // 2026-09-11 is a Friday. 09:00 local -> 16:00 the same day.
        val morning = Instant.parse("2026-09-11T07:00:00Z")
        assertEquals(
            Instant.parse("2026-09-11T14:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.LATER_TODAY, morning, berlin),
        )

        val afternoon = Instant.parse("2026-09-11T13:00:00Z") // 15:00 local
        assertEquals(
            Instant.parse("2026-09-12T06:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.LATER_TODAY, afternoon, berlin),
        )
    }

    @Test
    fun tomorrowMorningIsEightLocal() {
        assertEquals(
            Instant.parse("2026-09-12T06:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.TOMORROW_MORNING, Instant.parse("2026-09-11T20:00:00Z"), berlin),
        )
    }

    @Test
    fun thisWeekendIsTheNextSaturdayAndNeverToday() {
        // Friday -> tomorrow.
        assertEquals(
            Instant.parse("2026-09-12T06:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.THIS_WEEKEND, Instant.parse("2026-09-11T07:00:00Z"), berlin),
        )
        // Saturday -> the following Saturday, not the current instant.
        assertEquals(
            Instant.parse("2026-09-19T06:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.THIS_WEEKEND, Instant.parse("2026-09-12T07:00:00Z"), berlin),
        )
    }

    @Test
    fun nextWeekIsTheComingMondayAtEight() {
        assertEquals(
            Instant.parse("2026-09-14T06:00:00Z"),
            SnoozeClock.wakeTime(SnoozePreset.NEXT_WEEK, Instant.parse("2026-09-11T07:00:00Z"), berlin),
        )
    }
}
