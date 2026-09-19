package com.netzhansa.herold.android

import android.util.Log
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.sync.RetrySchedule
import com.netzhansa.herold.shared.sync.SyncStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.util.concurrent.atomic.AtomicLong

/**
 * How far behind the server the app is allowed to get (issue #436).
 *
 * The reported phone went 14 minutes without a single reconciliation
 * pass after one sync failed. These checks state the other side of
 * that: a change on the server reaches the list in seconds while the
 * shell holds the foreground, a pass that failed comes back on a
 * bounded retry rather than waiting for something else to happen, and
 * the diagnostics screen says when the last pass reached the server.
 *
 * The waits go through the Compose rule rather than a bare sleep: the
 * rule owns the frame clock, so a check that only sleeps freezes the
 * composition the foreground sync lives in and measures the harness
 * instead of the app.
 *
 * The class signs out at the end and writes no account state beyond the
 * mail it delivers itself, so it runs in any position of the suite
 * (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class SyncFreshnessAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app
        get() = InstrumentationRegistry.getInstrumentation()
            .targetContext.applicationContext as HeroldApplication

    private lateinit var reach: ServerReach

    private val watcher = CoroutineScope(Dispatchers.IO)

    @Before
    fun signedInThroughTheRelay() {
        grantNotificationPermission()
        reach = ServerReach(DevInstance.baseUrl)
        runBlocking {
            app.container.signOut()
            val result = app.container.signInWithPassword(
                reach.baseUrl,
                DevInstance.email,
                DevInstance.password,
                null,
            )
            assertTrue("sign-in through the relay failed: $result", result is SignInResult.Success)
        }
        // The mail shell has to be on screen: the sync loop and the
        // event stream live in its composition.
        compose.waitUntil(SHELL_TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-title").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun wireDown() {
        reach.close()
    }

    /** A change on the server reaches the store while the app is watched. */
    @Test
    fun t90_aServerSideChangeReachesTheListInSeconds() {
        runBlocking { app.container.session.value?.syncEngine?.syncAll() }
        val took = deliverAndWait("freshness probe", FRESHNESS_BUDGET_MS)
        Log.i(TAG, "with the app in the foreground the message took $took")
        assertTrue(
            "a delivered message has to reach the store within " +
                "${FRESHNESS_BUDGET_MS / 1000} s, took $took",
            took != null,
        )
    }

    /**
     * A pass that failed is retried on the bounded backoff, with nothing
     * else prompting it: the wire stays down for the whole check, so no
     * reconnected event stream can be what brings the passes back.
     */
    @Test
    fun t91_aFailedPassComesBackOnABoundedRetry() {
        val session = app.container.session.value ?: error("the sign-in left no session")
        runBlocking { session.syncEngine.syncAll() }

        reach.takeAway()
        val failed = runBlocking { session.syncEngine.syncAll() }
        assertTrue("the pass must fail on the wire, saw $failed", failed is SyncStatus.Failed)

        // Long enough for the first two retry steps of the schedule.
        val deadline = System.currentTimeMillis() + RETRY_WATCH_MS
        runCatching {
            compose.waitUntil(RETRY_WATCH_MS) {
                System.currentTimeMillis() > deadline
            }
        }
        val retries = DiagLog.ring.lines().filter { it.message.startsWith("pass ") }
        retries.forEach { Log.i(TAG, "${it.atMs} ${it.message}") }
        assertTrue(
            "the loop has to retry a failed pass, saw ${retries.size} retry lines",
            retries.size >= 2,
        )
        val waits = retries.mapNotNull { line ->
            Regex("next in (\\d+) s").find(line.message)?.groupValues?.get(1)?.toLongOrNull()
        }
        assertTrue("every retry line states its wait, saw $waits", waits.size == retries.size)
        assertTrue(
            "the wait stays within the bound of ${RetrySchedule.MAX_RETRY_MS / 1000} s, saw $waits",
            waits.all { it <= RetrySchedule.MAX_RETRY_MS / 1000 },
        )

        // With the wire back the loop catches up on its own, and the
        // diagnostics screen says when it last did.
        reach.giveBack()
        val took = deliverAndWait("recovery probe", RECOVERY_BUDGET_MS)
        Log.i(TAG, "after the wire came back the message took $took")
        assertTrue(
            "the store has to catch up within ${RECOVERY_BUDGET_MS / 1000} s, took $took",
            took != null,
        )

        openDiagnostics()
        compose.onNodeWithTag("diagnostics-last-sync").assertExists()
        compose.captureScreen("41-sync-last-successful")
        compose.onNodeWithTag("diagnostics-back").performClick()
    }

    /**
     * Coming back to the foreground reconciles at once. The stream is
     * dropped while the shell is away, so the change delivered in that
     * window is only in the store afterwards because the return ran a
     * pass.
     */
    @Test
    fun t92_comingBackToTheForegroundReconciles() {
        runBlocking { app.container.session.value?.syncEngine?.syncAll() }
        val subject = "foreground probe " + System.nanoTime()

        shellOut("input keyevent KEYCODE_SLEEP")
        // The pass that was in flight when the screen went off gets to
        // finish, so the delivery lands with nothing running.
        Thread.sleep(SETTLE_MS)
        runBlocking {
            DevInstance.deliverMail(subject = subject, body = "Does the phone notice?")
            DevInstance.awaitFiled(subject)
        }
        val away = runBlocking { app.container.store.emailList() }.none { it.subject == subject }
        assertTrue("the store must not hold the message before the shell comes back", away)

        shellOut("input keyevent KEYCODE_WAKEUP")
        shellOut("wm dismiss-keyguard")
        val back = System.currentTimeMillis()
        val seenAt = watchFor(subject, FOREGROUND_BUDGET_MS)
        val took = seenAt?.let { it - back }
        Log.i(TAG, "coming back to the foreground took ${took ?: "longer than the budget"} ms")
        assertTrue(
            "returning to the foreground has to reconcile within " +
                "${FOREGROUND_BUDGET_MS / 1000} s, took $took",
            took != null,
        )
    }

    /** Delivers one message and reports how long the store took to hold it. */
    private fun deliverAndWait(label: String, budgetMs: Long): Long? {
        val subject = "$label " + System.nanoTime()
        runBlocking {
            DevInstance.deliverMail(subject = subject, body = "Does the phone notice?")
            DevInstance.awaitFiled(subject)
        }
        val filedAt = System.currentTimeMillis()
        return watchFor(subject, budgetMs)?.minus(filedAt)
    }

    /** When the store first held [subject], or null within [budgetMs]. */
    private fun watchFor(subject: String, budgetMs: Long): Long? {
        val seenAt = AtomicLong(0)
        val poller = watcher.launch {
            while (seenAt.get() == 0L) {
                if (app.container.store.emailList().any { it.subject == subject }) {
                    seenAt.set(System.currentTimeMillis())
                } else {
                    delay(POLL_MS)
                }
            }
        }
        runCatching { compose.waitUntil(budgetMs) { seenAt.get() != 0L } }
        poller.cancel()
        return seenAt.get().takeIf { it != 0L }
    }

    private fun openDiagnostics() {
        compose.waitUntil(SHELL_TIMEOUT_MS) {
            compose.onAllNodesWithTag("status-indicator").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("status-indicator").performClick()
        compose.waitUntil(SHELL_TIMEOUT_MS) {
            compose.onAllNodesWithTag("diagnostics-screen").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val SHELL_TIMEOUT_MS = 30_000L

        /** What the shell is allowed to lag the server by while watched. */
        const val FRESHNESS_BUDGET_MS = 30_000L

        /** Long enough for two steps of the retry schedule. */
        const val RETRY_WATCH_MS = 20_000L

        /** The catch-up after an outage: the bound plus room for a pass. */
        const val RECOVERY_BUDGET_MS = 90_000L

        /** What coming back to the foreground is allowed to take. */
        const val FOREGROUND_BUDGET_MS = 30_000L

        const val SETTLE_MS = 10_000L
        const val POLL_MS = 500L
        const val TAG = "herold.sync.acceptance"
    }
}
