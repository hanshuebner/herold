package com.netzhansa.herold.android

import android.util.Log
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.sync.SyncStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.util.concurrent.atomic.AtomicLong

/**
 * Observation run for issue #436: what the app does after one failed
 * sync, with the shell in the foreground the whole time.
 *
 * The waits go through the Compose rule rather than a bare delay: the
 * rule owns the frame clock, so a test that only sleeps freezes the
 * composition the foreground sync lives in and measures the harness
 * instead of the app.
 */
@RunWith(AndroidJUnit4::class)
class SyncRetryReproTest {

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
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        // The mail shell has to be on screen: the event stream lives in
        // its composition.
        compose.waitUntil(SHELL_TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-title").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun wireDown() {
        reach.close()
    }

    @Test
    fun t98_observeAChangeWithNothingBroken() {
        runBlocking { app.container.session.value?.syncEngine?.syncAll() }
        val healthy = deliverAndWait("healthy probe", WATCH_MS_HEALTHY)
        Log.i(TAG, "with the wire up the message took $healthy")
        dumpRing()
    }

    @Test
    fun t99_observeWhatFollowsAFailedSync() {
        val session = app.container.session.value ?: error("no session")
        runBlocking { session.syncEngine.syncAll() }

        // One pass that fails on the wire, the wire given straight back,
        // and a change on the server right after it.
        reach.takeAway()
        val failed = runBlocking { session.syncEngine.syncAll() }
        Log.i(TAG, "forced pass says $failed")
        assertTrue("the pass must fail on the wire, saw $failed", failed is SyncStatus.Failed)
        reach.giveBack()
        Log.i(TAG, "wire back")

        val afterFailure = deliverAndWait("after-failure probe", WATCH_MS_AFTER)
        Log.i(TAG, "after one failed pass the message took $afterFailure")
        dumpRing()
    }

    @Test
    fun t96_observeAChangeWhileTheScreenIsOff() {
        runBlocking { app.container.session.value?.syncEngine?.syncAll() }
        val subject = "screen-off probe " + System.nanoTime()

        shellOut("input keyevent KEYCODE_SLEEP")
        // The pass that was in flight when the screen went off gets to
        // finish, so the delivery lands with nothing running.
        Thread.sleep(SETTLE_MS)
        Log.i(TAG, "the screen is off and the app is quiet")
        runBlocking {
            DevInstance.deliverMail(subject = subject, body = "Does the phone notice?")
            DevInstance.awaitFiled(subject)
        }
        val filedAt = System.currentTimeMillis()
        Thread.sleep(SCREEN_OFF_MS)
        val heldWhileOff = runBlocking { app.container.store.emailList() }.any { it.subject == subject }
        Log.i(
            TAG,
            "after ${SCREEN_OFF_MS / 1000} s with the screen off the store " +
                if (heldWhileOff) "holds the message" else "does not hold the message",
        )

        shellOut("input keyevent KEYCODE_WAKEUP")
        shellOut("wm dismiss-keyguard")
        val seenAt = AtomicLong(0)
        val poller = watcher.launch {
            while (seenAt.get() == 0L) {
                if (app.container.store.emailList().any { it.subject == subject }) {
                    seenAt.set(System.currentTimeMillis())
                } else {
                    delay(500)
                }
            }
        }
        runCatching { compose.waitUntil(WATCH_MS_AFTER) { seenAt.get() != 0L } }
        poller.cancel()
        val back = System.currentTimeMillis()
        Log.i(
            TAG,
            "after the screen came back the message took " +
                if (seenAt.get() == 0L) "longer than the budget" else "${back - filedAt} ms from filing",
        )
        dumpRing()
    }

    @Test
    fun t97_observeAStreamThatWentSilent() {
        runBlocking { app.container.session.value?.syncEngine?.syncAll() }
        Log.i(TAG, "before the outage the message took ${deliverAndWait("silent-stream warmup", WATCH_MS_HEALTHY)}")

        // The wire the stream is holding goes silent without being
        // closed: the phone's network went out from under it.
        reach.swallow()
        Log.i(TAG, "the open connections are now silent")

        val afterSilence = deliverAndWait("silent-stream probe", WATCH_MS_AFTER)
        Log.i(TAG, "with the stream silent the message took $afterSilence")
        dumpRing()
    }

    /** Delivers one message and reports how long the store took to hold it. */
    private fun deliverAndWait(label: String, budgetMs: Long): String {
        val subject = "$label " + System.nanoTime()
        runBlocking {
            DevInstance.deliverMail(subject = subject, body = "Does the phone notice?")
            DevInstance.awaitFiled(subject)
        }
        val filedAt = System.currentTimeMillis()
        val seenAt = AtomicLong(0)
        val poller = watcher.launch {
            while (seenAt.get() == 0L) {
                if (app.container.store.emailList().any { it.subject == subject }) {
                    seenAt.set(System.currentTimeMillis())
                } else {
                    delay(500)
                }
            }
        }
        runCatching { compose.waitUntil(budgetMs) { seenAt.get() != 0L } }
        poller.cancel()
        val at = seenAt.get()
        return if (at == 0L) "longer than ${budgetMs / 1000} s" else "${at - filedAt} ms"
    }

    private fun dumpRing() {
        DiagLog.ring.lines().forEach { Log.i(RING_TAG, "${it.atMs} ${it.ctx} ${it.message}") }
    }

    private companion object {
        const val SHELL_TIMEOUT_MS = 30_000L
        const val WATCH_MS_HEALTHY = 45_000L
        const val SCREEN_OFF_MS = 60_000L
        const val SETTLE_MS = 10_000L
        const val WATCH_MS_AFTER = 120_000L
        const val TAG = "herold.repro436"
        const val RING_TAG = "herold.repro436.ring"
    }
}
