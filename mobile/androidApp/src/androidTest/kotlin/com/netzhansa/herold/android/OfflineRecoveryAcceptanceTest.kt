package com.netzhansa.herold.android

import android.util.Log
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.diag.DiagLog
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.sync.SyncStatus
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

/**
 * Coming back from an outage the app showed as offline
 * (REQ-AND-SYNC-30, issue #433).
 *
 * The reported phone met a name that would not resolve while its radio
 * stayed up, and then stayed "Offline" across a sync that completed.
 * The check reproduces that shape in one process: the app signs in
 * through a relay in the test process, the relay is taken away so the
 * requests fail on the wire with the platform still reporting a
 * network, and then it is given back and the app is left to meet the
 * server again on a pass the shell calls off half way through - the
 * kind of pass that left the indicator stuck.
 *
 * The class signs out at the end and writes no account state, so it
 * runs in any position of the suite (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class OfflineRecoveryAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app
        get() = InstrumentationRegistry.getInstrumentation()
            .targetContext.applicationContext as HeroldApplication

    private lateinit var reach: ServerReach

    @Before
    fun signedInThroughTheRelay() = runBlocking {
        grantNotificationPermission()
        reach = ServerReach(DevInstance.baseUrl)
        app.container.signOut()
        val result = app.container.signInWithPassword(
            reach.baseUrl,
            DevInstance.email,
            DevInstance.password,
            null,
        )
        assertTrue("sign-in through the relay failed: $result", result is SignInResult.Success)
        Unit
    }

    @After
    fun wireDown() {
        reach.close()
    }

    @Test
    fun t40_aPassThatCannotGetThroughTurnsTheIndicatorAndOneThatDoesClearsIt() = runBlocking {
        val session = app.container.session.value ?: error("the sign-in left no session")
        session.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) { !app.container.offline.value }
        openDiagnostics()
        compose.onNodeWithTag("diagnostics-connectivity").assertIsDisplayed()
        assertTrue(
            "the screen says what the app is connected to",
            compose.onAllNodesWithText("Offline", substring = true).fetchSemanticsNodes().isEmpty(),
        )
        compose.captureScreen("38-recovery-connected")
        compose.onNodeWithTag("diagnostics-back").performClick()

        // The wire goes, the radio stays: what the report's device met.
        reach.takeAway()
        val failed = session.syncEngine.syncAll()
        assertTrue("the pass must fail on the wire, saw $failed", failed is SyncStatus.Failed)
        compose.waitUntil(OFFLINE_TIMEOUT_MS) { app.container.offline.value }
        openDiagnostics()
        assertTrue(
            "the connection line reads Offline",
            compose.onAllNodesWithText("Offline", substring = true).fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("39-recovery-offline")
        compose.onNodeWithTag("diagnostics-back").performClick()

        // The wire is back. The pass that meets the server is called off
        // half way through, the way the foreground loop calls one off
        // when the shell leaves the screen - the indicator follows the
        // traffic, not the end of the pass.
        reach.giveBack()
        val pass = launch { runCatching { session.syncEngine.syncAll() } }
        delay(CALLED_OFF_AFTER_MS)
        pass.cancel()
        pass.join()

        // The called-off pass is what has to clear it: a later pass
        // running to its end would clear it anyway, which is what hid
        // the bug, so the window is short.
        compose.waitUntil(RECOVERY_MS) { !app.container.offline.value }
        openDiagnostics()
        assertTrue(
            "the connection line reads connected again",
            compose.onAllNodesWithText("Offline", substring = true).fetchSemanticsNodes().isEmpty(),
        )
        compose.captureScreen("40-recovery-connected-again")

        // The two halves of the indication, as the ring recorded them.
        DiagLog.ring.lines()
            .filter { it.message.startsWith("network ") }
            .forEach { Log.i(RING_TAG, "${it.atMs} ${it.message}") }
        Unit
    }

    private fun openDiagnostics() {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("status-indicator").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("status-indicator").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("diagnostics-screen").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L

        /** The offline indication's grace period plus room for the wait. */
        const val OFFLINE_TIMEOUT_MS = 30_000L

        /** Long enough for the descriptor request to be answered. */
        const val CALLED_OFF_AFTER_MS = 400L

        /**
         * What the called-off pass itself gets to clear the indication,
         * short enough that the next pass cannot do it instead.
         */
        const val RECOVERY_MS = 2_000L

        const val RING_TAG = "herold.acceptance.ring"
    }
}
