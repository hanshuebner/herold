package com.netzhansa.herold.shared.mail

import kotlinx.datetime.Instant
import kotlinx.datetime.TimeZone
import kotlin.test.Test
import kotlin.test.assertEquals

/** The reading pane's two date renderings (issue #428). */
class MessageDatesTest {

    private val utc = TimeZone.UTC

    private fun at(iso: String): Long = Instant.parse(iso).toEpochMilliseconds()

    @Test
    fun aMessageFromTodayShowsItsClockTime() {
        assertEquals(
            "09:05",
            MessageDates.short(at("2026-09-19T09:05:00Z"), at("2026-09-19T18:00:00Z"), utc),
        )
    }

    @Test
    fun anEarlierDayThisYearShowsTheDayAndMonth() {
        assertEquals(
            "3 Feb",
            MessageDates.short(at("2026-02-03T09:05:00Z"), at("2026-09-19T18:00:00Z"), utc),
        )
    }

    @Test
    fun anOlderMessageCarriesItsYear() {
        assertEquals(
            "3 Feb 2024",
            MessageDates.short(at("2024-02-03T09:05:00Z"), at("2026-09-19T18:00:00Z"), utc),
        )
    }

    @Test
    fun theExpandedTimestampNamesTheWeekdayDateAndTime() {
        assertEquals(
            "Sat, 19 Sep 2026 at 09:05",
            MessageDates.full(at("2026-09-19T09:05:00Z"), utc),
        )
    }

    @Test
    fun aMessageWithNoDateRendersNothing() {
        assertEquals("", MessageDates.short(0, at("2026-09-19T18:00:00Z"), utc))
        assertEquals("", MessageDates.full(0, utc))
    }
}
