package com.netzhansa.herold.shared.diag

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** The bounded ring a report carries, and what it refuses to carry. */
class LogRingTest {

    @Test
    fun theRingKeepsTheMostRecentLines() {
        var clock = 0L
        val ring = LogRing(capacity = 3) { clock }
        repeat(5) { i ->
            clock = i.toLong()
            ring.info("test", "line $i")
        }
        assertEquals(3, ring.size())
        assertEquals(listOf("line 2", "line 3", "line 4"), ring.lines().map { it.message })
        assertEquals(listOf(2L, 3L, 4L), ring.lines().map { it.atMs })
    }

    @Test
    fun anEmptyRingHasNoLines() {
        assertTrue(LogRing(capacity = 8).lines().isEmpty())
    }

    @Test
    fun theLevelAndContextTravelWithTheLine() {
        val ring = LogRing(capacity = 4) { 7L }
        ring.warn("herold.outbox", "the drain gave up")
        val line = ring.lines().single()
        assertEquals(LogLevel.WARN, line.level)
        assertEquals("herold.outbox", line.ctx)
        assertEquals("the drain gave up", line.message)
    }

    @Test
    fun switchingTheRingOffForgetsWhatItHeld() {
        val ring = LogRing(capacity = 4)
        ring.info("test", "before")
        ring.enabled = false
        ring.info("test", "after")
        assertTrue(ring.lines().isEmpty())
        ring.enabled = true
        ring.info("test", "again")
        assertEquals(listOf("again"), ring.lines().map { it.message })
    }

    @Test
    fun clearEmptiesTheRing() {
        val ring = LogRing(capacity = 4)
        ring.info("test", "one")
        ring.clear()
        assertEquals(0, ring.size())
    }

    @Test
    fun aBearerTokenNeverReachesTheRing() {
        val ring = LogRing(capacity = 4)
        ring.info("herold.auth", "sent Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.secret.part")
        val held = ring.lines().single().message
        assertFalse(held.contains("eyJhbGciOiJIUzI1NiJ9"), "held: $held")
        assertTrue(held.contains(Redaction.PLACEHOLDER), "held: $held")
    }

    @Test
    fun aSubjectNeverReachesTheRing() {
        val ring = LogRing(capacity = 4)
        ring.info("herold.outbox", "outbox: \"Send: quarterly numbers\" waits for a connection")
        val held = ring.lines().single().message
        assertFalse(held.contains("quarterly numbers"), "held: $held")
    }

    @Test
    fun aKeyedSecretIsRedactedButItsNameIsKept() {
        assertEquals(
            "refresh_token=(redacted) rejected",
            Redaction.scrub("refresh_token=8f3c-abcdef rejected"),
        )
        assertEquals(
            "\"api_key\": (redacted)",
            Redaction.scrub("\"api_key\": \"sk-live-1234\""),
        )
    }

    @Test
    fun anOrdinaryLineIsLeftAlone() {
        val message = "sync: account a1 reconciled 12 messages in 340 ms"
        assertEquals(message, Redaction.scrub(message))
    }

    @Test
    fun anOutboxLabelKeepsItsKindAndLosesItsSubject() {
        assertEquals("Send: (redacted)", Redaction.outboxLabel("Send: quarterly numbers"))
        assertEquals(
            "Create the \"Bug reports\" label",
            Redaction.outboxLabel("Create the \"Bug reports\" label"),
        )
    }
}
