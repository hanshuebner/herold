package com.netzhansa.herold.shared.jmap

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The event stream's read timeout has to outlive the ping interval it
 * negotiates with the server, with margin (issue #493): an idle but healthy
 * stream can go a full ping period without a frame, so a timeout of one
 * ping period fires on a ping that is merely running late, not lost.
 */
class EventSourceReadTimeoutTest {

    @Test
    fun theTimeoutIsTwicePingPlusMargin() {
        assertEquals(75_000L, eventStreamReadTimeoutMillis(pingSeconds = 30))
        assertEquals(39_000L, eventStreamReadTimeoutMillis(pingSeconds = 12))
        assertEquals(17_000L, eventStreamReadTimeoutMillis(pingSeconds = 1))
    }

    @Test
    fun aCustomMarginIsHonoured() {
        assertEquals(20_000L, eventStreamReadTimeoutMillis(pingSeconds = 5, marginSeconds = 10))
    }

    /**
     * The bug the ticket reports (herold Android 0.10.2, issue #492's log):
     * OkHttp's 10 s default read timeout is shorter than the server's
     * negotiated 30 s ping, so a healthy idle stream reconnects on every
     * cycle. Any ping interval the client is expected to support must
     * derive a timeout comfortably past OkHttp's default.
     */
    @Test
    fun theDefaultPingIntervalClearsOkHttpsTenSecondDefault() {
        val okHttpDefaultReadTimeoutMillis = 10_000L
        assertTrue(
            eventStreamReadTimeoutMillis(pingSeconds = 30) > okHttpDefaultReadTimeoutMillis,
            "the derived timeout must exceed OkHttp's own default read timeout",
        )
    }
}
