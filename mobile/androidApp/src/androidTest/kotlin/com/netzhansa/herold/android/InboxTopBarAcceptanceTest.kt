package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsNode
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The inbox's chrome as issue #444 asks for it, with the Gmail app as
 * the reference.
 *
 * The top row carries the drawer button, the search field and the
 * avatar and nothing else; the open mailbox's name, the lanes and the
 * status dot stand in a row beneath it; reloading is the pull gesture
 * on the message list, which runs for as long as the sync pass it asks
 * for and leaves the list where it stands; reporting a problem is a
 * drawer entry and signing out sits under the avatar.
 *
 * The lane the tab check needs comes from mail seeded into the local
 * store carrying a category keyword (issue #404), so the class writes
 * no account-wide state and needs no cleanup of its own (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InboxTopBarAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            app.signInAsDevInstancePrincipal()
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.awaitTag("inbox-list")
    }

    /**
     * The three controls of the top row, left to right, and the row
     * below it carrying the mailbox's name, its lanes and the dot.
     */
    @Test
    fun t110_theTopRowHoldsTheDrawerTheFieldAndTheAvatar(): Unit = runBlocking {
        // Mail in the leading lane and mail in one of its own, so the
        // row under the top row carries a name, two tabs and the dot
        // over a list with conversations in it.
        val rows = seedPrimary()
        seedLane()
        compose.awaitTag("inbox-tabs")
        compose.awaitTag("thread-row-${rows.first().threadId}")

        val topBar = compose.onNodeWithTag("inbox-top-bar").fetchSemanticsNode()
        val bar = topBar.boundsInWindow
        val inTheBar = controlsOf(topBar)
            .sortedBy { it.boundsInWindow.left }
            .map { it.config.getOrNull(SemanticsProperties.TestTag) ?: UNTAGGED }
        assertEquals(
            "the top row does not hold the drawer, the search field and the avatar alone",
            listOf("inbox-drawer-open", "inbox-search", "inbox-avatar"),
            inTheBar,
        )

        assertEquals(
            "the reload button is still in the shell",
            0,
            compose.onAllNodesWithTag("inbox-refresh").fetchSemanticsNodes().size,
        )
        assertEquals(
            "the overflow menu is still in the shell",
            0,
            compose.onAllNodesWithTag("inbox-overflow").fetchSemanticsNodes().size,
        )

        // The mailbox name, the lanes and the status dot are a row of
        // their own, under the top row.
        val row = compose.onNodeWithTag("inbox-mailbox-row").fetchSemanticsNode().boundsInWindow
        assertTrue(
            "the mailbox row is not below the top row: ${row.top} against ${bar.bottom}",
            row.top >= bar.bottom,
        )
        listOf("inbox-title", "inbox-tabs", "status-indicator").forEach { tag ->
            val node = compose.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow
            assertTrue("$tag is not in the mailbox row", row.contains(node.center))
        }

        compose.captureScreen("111-inbox-top-row")
    }

    /**
     * The pull gesture runs a sync pass, says so for as long as the
     * pass takes, and leaves the list standing where it was.
     */
    @Test
    fun t111_thePullGestureRunsASyncAndTheIndicatorClearsWithIt(): Unit = runBlocking {
        val rows = seedPrimary()
        compose.awaitTag("thread-row-${rows.first().threadId}")
        val leading = compose.leadingRow("inbox-list")
        val scheduler = app.container.session.value!!.syncScheduler
        val before = scheduler.lastSuccessAtMs.value

        Gestures.dragDown(compose.onNodeWithTag("inbox-list").fetchSemanticsNode().boundsInWindow)

        compose.waitUntil(TIMEOUT_MS) { indicatorSays(RUNNING) }
        captureDeviceScreen("112-pull-to-refresh-running")

        compose.waitUntil(TIMEOUT_MS) { indicatorSays(READY) }
        // The pass the gesture asked for reached the server.
        compose.waitUntil(TIMEOUT_MS) { scheduler.lastSuccessAtMs.value != before }
        assertEquals(
            "the pull moved the list",
            leading,
            compose.settledLeadingRow("inbox-list"),
        )
    }

    /** Reporting a problem is where the drawer's pinned entries are. */
    @Test
    fun t112_reportingAProblemIsInTheDrawer() {
        compose.reportProblemFromTheDrawer()
        compose.awaitTag("bug-sheet")
        compose.onNodeWithTag("bug-cancel").performClick()
        compose.awaitTag("inbox-list")
    }

    /**
     * Signing out is under the avatar, and it signs out: the shell
     * comes back on the sign-in screen. The principal is signed in
     * again afterwards, so the next class finds the device as it was.
     */
    @Test
    fun t113_signingOutIsUnderTheAvatar(): Unit = runBlocking {
        compose.onNodeWithTag("inbox-avatar").performClick()
        compose.awaitTag("account-sign-out")
        compose.onNodeWithTag("account-sign-out").performClick()
        compose.awaitTag("signin-submit")

        app.signInAsDevInstancePrincipal()
        compose.awaitTag("inbox-list")
    }

    // ---- helpers ---------------------------------------------------------

    /**
     * The controls of the subtree [node] roots: every node in it that
     * takes a tap. Read off the tree rather than off the window's
     * geometry, so what the drawer holds behind the screen is no part
     * of the answer.
     */
    private fun controlsOf(node: SemanticsNode): List<SemanticsNode> {
        val here = if (node.config.contains(SemanticsActions.OnClick)) listOf(node) else emptyList()
        return here + node.children.flatMap { controlsOf(it) }
    }

    /** What the pull indicator says, read off its node. */
    private fun indicatorSays(description: String): Boolean =
        compose.onAllNodes(hasTestTag("inbox-refresh-indicator")).fetchSemanticsNodes()
            .any { it.config.getOrNull(SemanticsProperties.ContentDescription)?.contains(description) == true }

    /**
     * A handful of conversations carrying a category the account has
     * not pinned, which earns the inbox a lane of its own (issue #404).
     */
    private suspend fun seedLane() = seed("${TOKEN}lane", LANE)

    /** The same, uncategorised, so the rows stand in the leading lane. */
    private suspend fun seedPrimary() = seed(TOKEN, category = null)

    private suspend fun seed(token: String, category: String?) =
        SeededRows.accountId(app).let { accountId ->
            SeededRows.seed(
                app = app,
                accountId = accountId,
                mailboxId = SeededRows.mailboxId(app, accountId, MailboxRoles.INBOX),
                token = token,
                count = SEEDED,
                keywords = category
                    ?.let { setOf(Keywords.SEEN, Keywords.categoryKeyword(it)) }
                    ?: setOf(Keywords.SEEN),
            )
        }

    private companion object {
        /** How many conversations the seeded lane holds. */
        const val SEEDED = 12

        /** The category the seeded mail states. */
        const val LANE = "promotions"

        /** What names the seeded set. */
        const val TOKEN = "TopBar444"

        /** What the pull indicator reads in each of its two states. */
        const val RUNNING = "Refreshing"
        const val READY = "Pull to refresh"

        /** What a control in the top row that carries no test tag reads as. */
        const val UNTAGGED = "(untagged)"

        const val TIMEOUT_MS = 30_000L
    }
}
