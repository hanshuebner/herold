package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.sync.SyncScheduler
import com.netzhansa.herold.shared.sync.SyncStatus
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
 * The pull-to-refresh indicator stops, on every path a pull can take
 * (issue #450).
 *
 * The reported phone was left with an indicator that ran for good. The
 * indicator runs for as long as the pass the gesture asked for, so it
 * stops when that pass ends, when it fails, when a pass already under
 * way carries it, and - whatever the wire does - within its ceiling.
 *
 * Each method signs in through a relay in the test process, so the
 * check takes the wire away, gives it back and makes it go silent
 * under an established connection with the radios untouched
 * (issue #433). The class signs out at the end, so it runs in any
 * position of the suite (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InboxRefreshAcceptanceTest {

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
        val session = app.container.session.value ?: error("the sign-in left no session")
        session.syncEngine.syncAll()
        compose.awaitTag("inbox-list")
        val rows = seedPrimary()
        compose.awaitTag("thread-row-${rows.first().threadId}")
        Unit
    }

    @After
    fun signedOut() = runBlocking {
        reach.slowBy(0)
        reach.close()
        app.container.signOut()
        Unit
    }

    /** A pull that reaches the server stops when its pass ends. */
    @Test
    fun t130_aPullThatReachesTheServerStopsWithItsPass(): Unit = runBlocking {
        val scheduler = app.container.session.value!!.syncScheduler
        val before = scheduler.lastSuccessAtMs.value

        pull()
        compose.waitUntil(CEILING_MS) { indicatorSays(RUNNING) }
        compose.waitUntil(CEILING_MS) { indicatorSays(READY) }
        compose.waitUntil(CEILING_MS) { scheduler.lastSuccessAtMs.value != before }
    }

    /**
     * A pull with nothing on the other end stops too, and what the
     * pass met is on the status indicator rather than in a spinner.
     *
     * A pass that is refused on the wire ends in milliseconds, too
     * fast to read the running indicator off the tree, so what the
     * gesture asked for is read off the scheduler: a pass ran, and the
     * indicator is back to its resting state.
     */
    @Test
    fun t131_aPullWithTheWireDownStopsAndShowsTheOfflineState(): Unit = runBlocking {
        val scheduler = app.container.session.value!!.syncScheduler
        reach.takeAway()
        val before = scheduler.completedPasses.value

        pull()
        compose.waitUntil(CEILING_MS) { scheduler.completedPasses.value > before }
        compose.waitUntil(CEILING_MS + CEILING_SLACK_MS) { indicatorSays(READY) }

        compose.waitUntil(OFFLINE_MS) { app.container.offline.value }
        openDiagnostics()
        assertTrue(
            "the connection line does not read Offline",
            compose.onAllNodesWithText("Offline", substring = true).fetchSemanticsNodes().isNotEmpty(),
        )
        captureDeviceScreen("450-pull-offline")
        compose.onNodeWithTag("diagnostics-back").performClick()
        reach.giveBack()
    }

    /** A pull that lands on a pass already running stops with that work. */
    @Test
    fun t132_aPullDuringAPassStopsWhenThatWorkFinishes(): Unit = runBlocking {
        val session = app.container.session.value!!
        // A slow wire, so the pass the pull lands on is demonstrably
        // still running when the finger goes down.
        reach.slowBy(SLOW_CHUNK_MS)
        session.syncScheduler.requestSync()
        compose.waitUntil(CEILING_MS) { session.syncEngine.status.value is SyncStatus.Syncing }

        pull()
        compose.waitUntil(CEILING_MS) { indicatorSays(RUNNING) }
        reach.slowBy(0)
        compose.waitUntil(CEILING_MS + CEILING_SLACK_MS) { indicatorSays(READY) }
    }

    /**
     * The wire accepts and answers nothing, so the pass the pull asked
     * for has no end of its own. The indicator stops on its ceiling.
     */
    @Test
    fun t133_aPullOnASilentWireStopsOnItsCeiling(): Unit = runBlocking {
        reach.goSilent()

        val at = System.currentTimeMillis()
        pull()
        compose.waitUntil(CEILING_MS) { indicatorSays(RUNNING) }
        compose.waitUntil(CEILING_MS + CEILING_SLACK_MS) { indicatorSays(READY) }
        captureDeviceScreen("450-pull-ceiling")
        android.util.Log.i(
            "herold.acceptance",
            "the indicator ran for ${System.currentTimeMillis() - at} ms on a silent wire",
        )
    }

    // ---- helpers ---------------------------------------------------------

    /** The pull gesture, on the message list. */
    private fun pull() {
        compose.waitForIdle()
        Gestures.dragDown(compose.onNodeWithTag("inbox-list").fetchSemanticsNode().boundsInWindow)
    }

    /** What the pull indicator says, read off its node. */
    private fun indicatorSays(description: String): Boolean =
        compose.onAllNodes(hasTestTag("inbox-refresh-indicator")).fetchSemanticsNodes()
            .any { it.config.getOrNull(SemanticsProperties.ContentDescription)?.contains(description) == true }

    private fun openDiagnostics() {
        compose.waitUntil(CEILING_MS) {
            compose.onAllNodesWithTag("status-indicator").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("status-indicator").performClick()
        compose.awaitTag("diagnostics-screen")
    }

    /** Conversations in the leading lane, so there is a list to pull on. */
    private suspend fun seedPrimary() =
        SeededRows.accountId(app).let { accountId ->
            SeededRows.seed(
                app = app,
                accountId = accountId,
                mailboxId = SeededRows.mailboxId(app, accountId, MailboxRoles.INBOX),
                token = TOKEN,
                count = SEEDED,
                keywords = setOf(Keywords.SEEN),
            )
        }

    private companion object {
        /** How many conversations the check seeds. */
        const val SEEDED = 12

        /** What names the seeded set. */
        const val TOKEN = "Refresh450"

        /** What the pull indicator reads in each of its two states. */
        const val RUNNING = "Refreshing"
        const val READY = "Pull to refresh"

        /** The indicator's own ceiling, which every wait here allows. */
        const val CEILING_MS = SyncScheduler.FORCED_SYNC_CEILING_MS

        /** What a check allows on top of the ceiling, for the gesture and the frames. */
        const val CEILING_SLACK_MS = 6_000L

        /** The offline indication's grace period plus room for a pass. */
        const val OFFLINE_MS = 30_000L

        /** What each relayed chunk waits, to stretch a pass. */
        const val SLOW_CHUNK_MS = 250L
    }
}
