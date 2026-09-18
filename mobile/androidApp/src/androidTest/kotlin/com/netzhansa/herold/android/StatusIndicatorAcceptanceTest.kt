package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.sync.SyncStatus
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.File

/**
 * The status indicator holds its slot (REQ-AND-SYNC-30, issue #421), in
 * two phases the harness runs around a connectivity toggle:
 *
 *   am instrument ... -e class StatusIndicatorAcceptanceTest#t70_layoutHoldsOnlineAndWhileSyncing
 *   adb shell svc data disable && adb shell svc wifi disable
 *   am instrument ... -e class StatusIndicatorAcceptanceTest#t71_layoutHoldsOffline
 *
 * Phase one measures the list and a named row while idle and again
 * while a sync runs, and reads the diagnostics screen the indicator
 * opens. Phase two measures the same things with the radios off. The
 * measurements cross the phases in a file, because each phase is its
 * own process.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class StatusIndicatorAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    /** Where phase one leaves what phase two compares against. */
    private val ledger: File
        get() = File(
            InstrumentationRegistry.getInstrumentation().targetContext.filesDir,
            "status-indicator-layout.txt",
        )

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun t70_layoutHoldsOnlineAndWhileSyncing() = runBlocking {
        if (app.container.session.value == null) {
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        // The indicator is there whatever the state is; that is the
        // point of it.
        compose.onNodeWithTag("status-indicator").assertIsDisplayed()

        val rowTag = firstRowTag()
        val idle = measure(rowTag)
        compose.captureScreen("70-inbox-status-online")

        // A sync the user asked for: the dot turns and the list stays
        // exactly where it was.
        compose.onNodeWithTag("inbox-refresh").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            app.container.session.value?.syncEngine?.status?.value is SyncStatus.Syncing
        }
        assertTrue(
            "the in-flight dot is drawn while the sync runs",
            compose.onAllNodesWithTag("status-dot-busy", useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty(),
        )
        val syncing = measure(rowTag)
        // No waitForIdle here: the in-flight dot animates, and an
        // animation in flight never reaches idle.
        captureDeviceScreen("71-inbox-status-syncing")
        assertEquals("the list does not move when a sync starts", idle, syncing)

        ledger.writeText("$rowTag\n${idle.listTop}\n${idle.rowTop}\n${idle.rowLeft}\n")

        // The diagnostics screen the indicator opens, with the ring on
        // it (REQ-AND-SYS-54).
        compose.waitUntil(TIMEOUT_MS) {
            app.container.session.value?.syncEngine?.status?.value !is SyncStatus.Syncing
        }
        compose.onNodeWithTag("status-indicator").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("diagnostics-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("diagnostics-sync").assertIsDisplayed()
        compose.onNodeWithTag("diagnostics-outbox").assertIsDisplayed()
        compose.onNodeWithTag("diagnostics-push").assertIsDisplayed()
        assertTrue(
            "the ring the shell has been writing is on the screen",
            compose.onAllNodesWithTag("diagnostics-log-line", useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("73-diagnostics")
        compose.onNodeWithTag("diagnostics-back").performClick()
        Unit
    }

    @Test
    fun t71_layoutHoldsOffline() {
        val recorded = ledger.readLines().takeIf { it.size >= 4 }
            ?: error("phase one must run online first")
        val rowTag = recorded[0]

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        // The offline indication waits out the grace period before it
        // says anything (REQ-AND-SYNC-30).
        compose.waitUntil(OFFLINE_TIMEOUT_MS) {
            compose.onAllNodesWithTag("status-dot-offline", useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag(rowTag, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
        }

        val offline = measure(rowTag)
        assertEquals("the list holds its top with no connection", recorded[1].toFloat(), offline.listTop, 0f)
        assertEquals("the row holds its position with no connection", recorded[2].toFloat(), offline.rowTop, 0f)
        assertEquals("the row holds its left edge with no connection", recorded[3].toFloat(), offline.rowLeft, 0f)
        compose.captureScreen("72-inbox-status-offline")

        // The way to the detail is the same tap offline as online.
        compose.onNodeWithTag("status-indicator").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("diagnostics-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("diagnostics-connectivity").assertIsDisplayed()
        compose.captureScreen("74-diagnostics-offline")
        compose.onNodeWithTag("diagnostics-back").performClick()
    }

    /** Where the list and one known row sit, in root coordinates. */
    private data class Layout(val listTop: Float, val rowTop: Float, val rowLeft: Float)

    private fun measure(rowTag: String): Layout {
        val list = compose.onNodeWithTag("inbox-list").fetchSemanticsNode().boundsInRoot
        val row = compose.onNodeWithTag(rowTag, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        return Layout(listTop = list.top, rowTop = row.top, rowLeft = row.left)
    }

    /** The tag of the row the phases measure. */
    private fun firstRowTag(): String {
        val node = compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
            .fetchSemanticsNodes()
            .minByOrNull { it.boundsInRoot.top }
            ?: error("the inbox listed no rows")
        return node.config.getOrNull(SemanticsProperties.TestTag) ?: error("the row carries no tag")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L

        /** The offline indication's grace period plus room for the wait. */
        const val OFFLINE_TIMEOUT_MS = 30_000L
    }
}
