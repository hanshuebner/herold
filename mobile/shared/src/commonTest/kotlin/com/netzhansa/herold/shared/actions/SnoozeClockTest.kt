package com.netzhansa.herold.shared.actions

import kotlinx.datetime.Instant
import kotlinx.datetime.LocalDate
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

    @Test
    fun theCustomPickOpensOnTheNextFullLocalHour() {
        // 09:13 local -> 10:00 local.
        assertEquals(
            Instant.parse("2026-09-11T08:00:00Z"),
            SnoozeClock.nextFullHour(Instant.parse("2026-09-11T07:13:42Z"), berlin),
        )
        // 23:30 local -> midnight, which is the following calendar day.
        assertEquals(
            Instant.parse("2026-09-11T22:00:00Z"),
            SnoozeClock.nextFullHour(Instant.parse("2026-09-11T21:30:00Z"), berlin),
        )
    }

    @Test
    fun aPickedDateAndTimeIsReadInTheDevicesZone() {
        assertEquals(
            Instant.parse("2026-09-11T12:35:00Z"),
            SnoozeClock.customWakeTime(LocalDate(2026, 9, 11), hour = 14, minute = 35, zone = berlin),
        )
    }

    @Test
    fun theWakeTimeReadsRelativeToToday() {
        val now = Instant.parse("2026-09-11T07:00:00Z") // Friday 09:00 local
        assertEquals("16:00", SnoozeClock.describe(Instant.parse("2026-09-11T14:00:00Z"), now, berlin))
        assertEquals("08:00 tomorrow", SnoozeClock.describe(Instant.parse("2026-09-12T06:00:00Z"), now, berlin))
        assertEquals("Monday, 08:00", SnoozeClock.describe(Instant.parse("2026-09-14T06:00:00Z"), now, berlin))
        assertEquals("Oct 5, 08:00", SnoozeClock.describe(Instant.parse("2026-10-05T06:00:00Z"), now, berlin))
    }

    @Test
    fun anUnparseableWakeValueReadsAsNull() {
        assertEquals(null, SnoozeClock.parseWake("soon"))
        assertEquals(Instant.parse("2026-09-12T06:00:00Z"), SnoozeClock.parseWake("2026-09-12T06:00:00Z"))
    }
}
