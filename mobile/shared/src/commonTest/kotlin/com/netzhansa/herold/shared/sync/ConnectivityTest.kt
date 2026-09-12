package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals

/** The chip's hysteresis (REQ-AND-SYNC-30). */
class ConnectivityTest {

    @Test
    fun aShortDropNeverReachesTheUi() = runTest {
        val online = MutableStateFlow(true)
        val seen = mutableListOf<Boolean>()
        val collector = launch { online.offlineIndication(graceMs = 4_000).toList(seen) }

        advanceTimeBy(100)
        online.value = false
        advanceTimeBy(1_000)
        online.value = true
        advanceTimeBy(10_000)

        assertEquals(listOf(false), seen, "a one-second drop does not flash the chip")
        collector.cancel()
    }

    @Test
    fun aDropThatLastsIsShownAndClearsOnReconnect() = runTest {
        val online = MutableStateFlow(true)
        val seen = mutableListOf<Boolean>()
        val collector = launch { online.offlineIndication(graceMs = 4_000).toList(seen) }

        advanceTimeBy(100)
        online.value = false
        advanceTimeBy(5_000)
        online.value = true
        advanceTimeBy(1_000)

        assertEquals(listOf(false, true, false), seen)
        collector.cancel()
    }
}
