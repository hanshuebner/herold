package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.withTimeoutOrNull

/**
 * How long the client waits before the next reconciliation pass
 * (REQ-AND-SYNC-12, issue #436).
 *
 * A pass that failed is retried after [firstRetryMs], doubling up to
 * [maxRetryMs] while failures continue, so a server that is down for an
 * hour is asked once a minute rather than once a second. A pass that
 * succeeded resets the wait to [idleMs], which is the floor under the
 * event stream: the stream carries a change in under a second, and the
 * floor is what covers a stream that is up but says nothing.
 */
class RetrySchedule(
    private val firstRetryMs: Long = FIRST_RETRY_MS,
    private val maxRetryMs: Long = MAX_RETRY_MS,
    private val idleMs: Long = IDLE_MS,
) {
    private var consecutiveFailures = 0

    /** How many passes in a row have failed. */
    val failures: Int get() = consecutiveFailures

    /** The wait after a pass that reached the server. */
    fun recordSuccess(): Long {
        consecutiveFailures = 0
        return idleMs
    }

    /** The wait after a pass that failed, bounded by [maxRetryMs]. */
    fun recordFailure(): Long {
        consecutiveFailures++
        var wait = firstRetryMs
        repeat(consecutiveFailures - 1) {
            if (wait < maxRetryMs) wait *= 2
        }
        return if (wait > maxRetryMs) maxRetryMs else wait
    }

    companion object {
        /** A transient failure is retried soon enough to go unnoticed. */
        const val FIRST_RETRY_MS = 5_000L

        /**
         * The longest the client waits between attempts. A minute keeps
         * a foregrounded app at most a minute behind the server through
         * an outage of any length, at one request a minute.
         */
        const val MAX_RETRY_MS = 60_000L

        /** The floor under the event stream while nothing has failed. */
        const val IDLE_MS = 60_000L
    }
}

/**
 * Runs reconciliation passes for as long as the shell holds the
 * foreground: one pass, then a wait the [schedule] decides, then the
 * next (issue #436). [requestSync] cuts any wait short, which is what a
 * pull to refresh, a push and coming back to the foreground do.
 *
 * It takes the pass as a function rather than a [SyncEngine] so the
 * schedule runs in `commonTest` on a virtual clock with no network.
 */
class SyncScheduler(
    private val pass: suspend () -> SyncStatus,
    private val schedule: RetrySchedule = RetrySchedule(),
    private val now: () -> Long = { 0L },
    /** Where the loop's decisions go; the app's diagnostic ring. */
    private val log: (String) -> Unit = {},
) {
    private val wake = Channel<Unit>(Channel.CONFLATED)

    private val _lastSuccessAtMs = MutableStateFlow<Long?>(null)

    /**
     * When the last pass that reached the server finished. The
     * diagnostics screen shows it, so a lag is read off the screen
     * rather than guessed at (REQ-AND-SYS-54).
     */
    val lastSuccessAtMs: StateFlow<Long?> = _lastSuccessAtMs.asStateFlow()

    /** Runs the next pass now, whatever the wait stands at. */
    fun requestSync() {
        wake.trySend(Unit)
    }

    /**
     * Passes and waits until the caller's scope is cancelled. The first
     * pass runs immediately, so starting the loop is itself the forced
     * sync a return to the foreground asks for.
     */
    suspend fun run() {
        while (true) {
            val status = try {
                pass()
            } catch (c: CancellationException) {
                throw c
            } catch (t: Throwable) {
                SyncStatus.Failed(t.message ?: "sync failed")
            }
            val wait = if (status is SyncStatus.Failed) {
                val next = schedule.recordFailure()
                log("pass ${schedule.failures} failed (${status.message}); next in ${next / 1000} s")
                next
            } else {
                _lastSuccessAtMs.value = now()
                schedule.recordSuccess()
            }
            withTimeoutOrNull(wait) { wake.receive() }
        }
    }
}
