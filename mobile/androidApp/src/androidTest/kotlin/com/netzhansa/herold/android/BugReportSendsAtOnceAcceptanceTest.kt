package com.netzhansa.herold.android

import android.util.Log
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.sync.SyncStatus
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeNotNull
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * A bug report leaves on the tap (issue #438, REQ-AND-SYS-53). A report
 * is not correspondence: it is queued with no hold, the drain is asked
 * for at once, and the confirmation offers nothing to take back.
 *
 * The first check measures the stretch from the tap to the report being
 * listed by `GET /api/v1/bug-reports`. The second takes the wire away
 * with the radio up - the shape a phone in a lift meets - and expects
 * the report to sit in the outbox and to leave on the pass that follows
 * the connection returning.
 *
 * The class signs out at the end and leaves no account state behind, so
 * it runs in any position of the suite (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BugReportSendsAtOnceAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app
        get() = InstrumentationRegistry.getInstrumentation()
            .targetContext.applicationContext as HeroldApplication

    private val key: String
        get() = DevInstance.bugReportsKey
            ?: error("no heroldBugReportsKey; mint one with `herold api-key create --scope bug-reports`")

    private var reach: ServerReach? = null

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @After
    fun noSessionIsLeftBehind() {
        reach?.close()
        reach = null
        runBlocking { app.container.signOut() }
    }

    /**
     * Online: the upload starts on the tap, so the server has the
     * report seconds later, and the confirmation says so without
     * offering an Undo.
     */
    @Test
    fun t101_aReportTappedOnlineIsOnTheServerWithinSeconds() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        signedIn(DevInstance.baseUrl)
        val title = "report at once ${System.currentTimeMillis()}"
        raiseTheSheet()
        compose.onNodeWithTag("bug-title").performTextInput(title)

        val tapped = System.currentTimeMillis()
        compose.onNodeWithTag("bug-send").performClick()

        // The confirmation: the report is on its way, with nothing to
        // take back on it.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("on its way", substring = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the report's confirmation offers an Undo",
            compose.onAllNodesWithText("Undo", substring = true).fetchSemanticsNodes().isEmpty(),
        )

        // Measured before the screenshot, whose settle would otherwise
        // be counted as part of the upload.
        val elapsed = awaitReport(title) - tapped
        Log.i(TAG, "the report reached the server ${elapsed}ms after the tap")
        assertTrue(
            "the report took ${elapsed}ms to reach the server",
            elapsed <= ARRIVAL_BUDGET_MS,
        )
        compose.captureScreen("101-report-on-its-way")
    }

    /**
     * Offline: the report waits in the outbox, says so, and leaves on
     * the pass that follows the connection returning.
     */
    @Test
    fun t102_aReportRaisedOfflineLeavesWhenTheConnectionReturns() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        val wire = ServerReach(DevInstance.baseUrl).also { reach = it }
        signedIn(wire.baseUrl)
        val session = app.container.session.value ?: error("the sign-in left no session")

        // The wire goes while the radio stays up.
        wire.takeAway()
        val failed = session.syncEngine.syncAll()
        assertTrue("the pass must fail on the wire, saw $failed", failed is SyncStatus.Failed)
        compose.waitUntil(OFFLINE_TIMEOUT_MS) { app.container.offline.value }

        val title = "offline report ${System.currentTimeMillis()}"
        raiseTheSheet()
        compose.onNodeWithTag("bug-title").performTextInput(title)
        compose.onNodeWithTag("bug-send").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Offline", substring = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("102-report-waits-for-a-connection")
        val queued = app.container.outbox.list().single { it.kind == OutboxKind.BUG_REPORT }
        assertEquals(OutboxState.QUEUED, queued.state)
        assertEquals("being offline is not an attempt the report spends", 0, queued.attempts)

        // The connection is back: the pass that follows it drains the
        // queue, and the report is on the server.
        wire.giveBack()
        session.syncEngine.syncAll()
        awaitReport(title)
        assertTrue(
            "the posted report is still in the outbox",
            app.container.outbox.list().none { it.kind == OutboxKind.BUG_REPORT },
        )
        compose.captureScreen("103-report-left-after-the-connection-returned")
    }

    /** Signs in against [baseUrl] and waits for the message list. */
    private fun signedIn(baseUrl: String) = runBlocking {
        app.container.signOut()
        val result = app.container.signInWithPassword(
            baseUrl,
            DevInstance.email,
            DevInstance.password,
            null,
        )
        assertTrue("sign-in against $baseUrl failed: $result", result is SignInResult.Success)
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Opens the reporter from the message list, through the drawer. */
    private fun raiseTheSheet() {
        compose.reportProblemFromTheDrawer()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** When the report named [title] appeared in the server's listing. */
    private fun awaitReport(title: String): Long {
        repeat(ARRIVAL_POLLS) {
            if (BugReportsApi.list(DevInstance.baseUrl, key).any { it.title == title }) {
                return System.currentTimeMillis()
            }
            Thread.sleep(ARRIVAL_POLL_MS)
        }
        error("the report \"$title\" never reached the server's bug reports")
    }

    private companion object {
        const val TAG = "herold.acceptance.report"
        const val TIMEOUT_MS = 30_000L

        /** The offline indication's grace period plus room for the wait. */
        const val OFFLINE_TIMEOUT_MS = 30_000L

        /** What "on the tap" is worth in wall-clock time on the emulator. */
        const val ARRIVAL_BUDGET_MS = 4_000L

        const val ARRIVAL_POLLS = 60
        const val ARRIVAL_POLL_MS = 200L
    }
}
