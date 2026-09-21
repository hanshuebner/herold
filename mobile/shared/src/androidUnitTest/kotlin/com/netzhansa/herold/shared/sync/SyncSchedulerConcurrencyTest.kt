package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.joinAll
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicLong
import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * What a forced sync is allowed to end on, under real threads
 * (issue #450).
 *
 * The rule is one line: `syncNow` returns when a pass that started
 * after the call has finished. A pass that was already running when
 * the call arrived is one the caller did not ask for, so ending on it
 * hands the pull-to-refresh gesture a result gathered before the user
 * made it.
 *
 * The scheduler holds that rule in one value, so a caller reads what
 * has been asked for and what has been done about it together. Reading
 * a pass counter and a "a pass is running" flag separately cannot hold
 * it: a call that lands between the two writes a finishing pass makes -
 * the flag cleared, the count not yet raised - reads "nothing running,
 * N done", asks for N + 1, and is answered at once by the pass that was
 * already on its way out.
 *
 * That window is a few instructions wide and has no suspension point in
 * it, so no single-threaded check can step into it. This one walks in
 * from the side: many more caller threads than the host has cores, so
 * the scheduler runs a pass every few microseconds and the OS preempts
 * callers part-way through reading the state often enough to land in
 * the window. Against a scheduler that reads the two separately it
 * takes a few thousand calls to catch it; against one value there is
 * nothing to catch.
 *
 * It runs on the host JVM rather than in `commonTest`, because the
 * point of it is threads and a memory model.
 */
class SyncSchedulerConcurrencyTest {

    @Test
    fun aForcedSyncOnlyEndsOnAPassThatStartedAfterIt() {
        val threads = Executors.newFixedThreadPool(CALLERS + 1)
        try {
            runBlocking {
                val dispatcher = threads.asCoroutineDispatcher()
                // Every pass counts itself in before it does anything, so
                // a caller can say whether one began after it asked.
                val started = AtomicLong(0)
                val scheduler = SyncScheduler(
                    pass = {
                        started.incrementAndGet()
                        SyncStatus.Idle
                    },
                    now = { 0L },
                )
                val loop = launch(dispatcher) { scheduler.run() }

                // How many calls came back on a pass that was already
                // running when they were made.
                val early = AtomicLong(0)
                val callers = (1..CALLERS).map {
                    launch(dispatcher) {
                        repeat(CALLS) {
                            val before = started.get()
                            scheduler.syncNow()
                            if (started.get() <= before) early.incrementAndGet()
                        }
                    }
                }
                callers.joinAll()
                loop.cancelAndJoin()

                assertEquals(
                    0L,
                    early.get(),
                    "${early.get()} of ${CALLERS * CALLS} forced syncs ended on a pass " +
                        "that started before they were made",
                )
            }
        } finally {
            threads.shutdownNow()
        }
    }

    private companion object {
        /**
         * More callers than any host has cores, so a caller is
         * regularly preempted part-way through reading the scheduler's
         * state - which is what puts a call inside the window.
         */
        const val CALLERS = 32

        /** Calls per caller. Enough to catch a window this narrow. */
        const val CALLS = 400
    }
}
