package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.currentTime
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The retry schedule and the loop that follows it (issue #436): a pass
 * that failed comes back within seconds, the wait stays bounded however
 * long the outage runs, a success puts it back to the floor, and a
 * forced sync cuts whatever wait is pending.
 *
 * The clock is the test dispatcher's, so the checks state the schedule
 * rather than waiting it out.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class SyncSchedulerTest {

    @Test
    fun theBackoffGrowsAndStaysBounded() {
        val schedule = RetrySchedule()
        val waits = (1..8).map { schedule.recordFailure() }
        assertEquals(listOf(5_000L, 10_000L, 20_000L, 40_000L, 60_000L, 60_000L, 60_000L, 60_000L), waits)
        assertEquals(8, schedule.failures)
        waits.forEach { assertTrue(it <= RetrySchedule.MAX_RETRY_MS, "the wait stays bounded, saw $it") }
    }

    @Test
    fun aSuccessPutsTheWaitBackToTheFloor() {
        val schedule = RetrySchedule()
        repeat(4) { schedule.recordFailure() }
        assertEquals(RetrySchedule.IDLE_MS, schedule.recordSuccess())
        assertEquals(0, schedule.failures)
        assertEquals(RetrySchedule.FIRST_RETRY_MS, schedule.recordFailure())
    }

    @Test
    fun aFailedPassIsRetriedOnTheSchedule() = runTest {
        val at = mutableListOf<Long>()
        val scheduler = SyncScheduler(
            pass = {
                at += currentTime
                SyncStatus.Failed("no wire")
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        advanceTimeBy(40_001)
        loop.cancelAndJoin()

        // Immediately, then 5 s, 10 s, 20 s, 40 s after the first.
        assertEquals(listOf(0L, 5_000L, 15_000L, 35_000L), at)
    }

    @Test
    fun aSuccessLeavesTheFloorAndNoLongerBacksOff() = runTest {
        val at = mutableListOf<Long>()
        var fail = true
        val scheduler = SyncScheduler(
            pass = {
                at += currentTime
                if (fail) SyncStatus.Failed("no wire") else SyncStatus.Idle
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        advanceTimeBy(5_001)
        fail = false
        advanceTimeBy(10_000)
        assertEquals(listOf(0L, 5_000L, 15_000L), at)
        assertEquals(15_000L, scheduler.lastSuccessAtMs.value)

        // The next wait is the idle floor, not a backoff step.
        advanceTimeBy(59_000)
        assertEquals(3, at.size)
        advanceTimeBy(1_100)
        assertEquals(4, at.size)
        loop.cancelAndJoin()
    }

    @Test
    fun aForcedSyncCutsThePendingBackoff() = runTest {
        val at = mutableListOf<Long>()
        val scheduler = SyncScheduler(
            pass = {
                at += currentTime
                SyncStatus.Failed("no wire")
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        // Four failures in: the loop is sitting on a 40 s wait.
        advanceTimeBy(35_001)
        assertEquals(listOf(0L, 5_000L, 15_000L, 35_000L), at)

        advanceTimeBy(1_000)
        scheduler.requestSync()
        runCurrent()
        assertEquals(36_001L, at.last())
        loop.cancelAndJoin()
    }

    @Test
    fun aForcedSyncDuringAPassIsNotLost() = runTest {
        val at = mutableListOf<Long>()
        val scheduler = SyncScheduler(
            pass = {
                at += currentTime
                SyncStatus.Idle
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        assertEquals(listOf(0L), at)

        // Asked for while the loop is waiting out the floor; the pass
        // runs at once rather than at the end of the minute.
        advanceTimeBy(1_000)
        scheduler.requestSync()
        runCurrent()
        assertEquals(listOf(0L, 1_000L), at)
        loop.cancelAndJoin()
    }

    /**
     * What the inbox's pull to refresh stands on (issue #444): the call
     * returns when the pass it asked for has finished, so the indicator
     * runs for as long as the sync does.
     */
    @Test
    fun aForcedSyncReturnsWhenItsPassHasFinished() = runTest {
        val gate = Channel<Unit>(Channel.UNLIMITED)
        var started = 0
        val scheduler = SyncScheduler(
            pass = {
                started++
                gate.receive()
                SyncStatus.Idle
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        // The loop's own first pass is under way; the refresh asks for
        // one of its own and waits for that one.
        assertEquals(1, started)

        var returned = false
        val refresh = launch {
            scheduler.syncNow()
            returned = true
        }
        runCurrent()

        gate.send(Unit)
        runCurrent()
        assertEquals(2, started)
        assertTrue(!returned, "the refresh ended on a pass it did not ask for")

        gate.send(Unit)
        runCurrent()
        assertTrue(returned, "the refresh did not end with its pass")

        refresh.join()
        loop.cancelAndJoin()
    }

    /** A refresh asked for between passes waits for the one it wakes. */
    @Test
    fun aForcedSyncBetweenPassesWaitsForTheOneItWakes() = runTest {
        val gate = Channel<Unit>(Channel.UNLIMITED)
        var started = 0
        val scheduler = SyncScheduler(
            pass = {
                started++
                if (started > 1) gate.receive()
                SyncStatus.Idle
            },
            now = { currentTime },
        )
        val loop = launch { scheduler.run() }
        runCurrent()
        assertEquals(1, started)

        var returned = false
        val refresh = launch {
            scheduler.syncNow()
            returned = true
        }
        runCurrent()
        assertEquals(2, started)
        assertTrue(!returned, "the refresh ended before its pass did")

        gate.send(Unit)
        runCurrent()
        assertTrue(returned, "the refresh did not end with its pass")

        refresh.join()
        loop.cancelAndJoin()
    }

    /** The scheduler is usable from a plain scope, as the shell uses it. */
    @Test
    fun theLoopEndsWithItsScope() = runTest {
        val scope = TestScope(testScheduler)
        val scheduler = SyncScheduler(pass = { SyncStatus.Idle }, now = { currentTime })
        val loop = scope.launch { scheduler.run() }
        runCurrent()
        loop.cancelAndJoin()
        assertTrue(loop.isCancelled)
    }
}
