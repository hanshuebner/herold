package com.netzhansa.herold.android

import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Where a message list stands after the reader has been into a
 * conversation and come back (issue #439).
 *
 * The reported flow: All Mail scrolled some way down, a message opened,
 * back - and the list at the top again. Each check notes the row
 * leading the list, scrolls well past the fold, notes the row leading
 * it there, opens a conversation, presses back, and reads the leading
 * row again.
 *
 * The lists are seeded straight into the local store ([SeededRows]),
 * which is what every screen renders from, so a list longer than the
 * screen does not cost forty SMTP deliveries.
 *
 * `t98` runs with the radios off, since the cached search is what puts
 * the seeded rows in the results list; it turns them back on itself.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ScrollPositionAcceptanceTest {

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

    @After
    fun radiosBack() {
        radios(up = true)
    }

    @Test
    fun t95_theInboxKeepsItsOffsetAcrossAThread(): Unit = runBlocking {
        val rows = seedInbox("Inbox439", category = null)
        compose.awaitTag("thread-row-${rows.first().threadId}")

        val left = compose.scrollPastTheFold("inbox-list", compose.leadingRow("inbox-list"))

        compose.openThreadBelowTheEdge("inbox-list")
        Gestures.pressBack(compose)
        compose.awaitTag("inbox-list")

        assertEquals(
            "the inbox came back at another row than the one it was left on",
            left,
            compose.leadingRow("inbox-list"),
        )
        compose.captureScreen("107-inbox-back-at-the-row-it-was-left-on")

        // Mail arriving under a reader who has placed the list keeps its
        // anchor: the row they are on stays where it is, rather than the
        // list jumping to the new message.
        seed("Inbox439fresh", MailboxRoles.INBOX, category = null, count = 1)
        compose.awaitTag("inbox-list")
        assertEquals(
            "mail arriving under the reader moved the list",
            left,
            compose.settledLeadingRow("inbox-list"),
        )
    }

    @Test
    fun t96_eachLaneKeepsItsOwnOffset(): Unit = runBlocking {
        // Two lanes: uncategorised mail, which the primary lane carries,
        // and a category the seeded mail states, which earns a tab of
        // its own (issue #404, #427).
        val primary = seedInbox("Lane439P", category = null)
        val promotions = seedInbox("Lane439C", category = LANE)
        compose.awaitTag("inbox-tab-$LANE")
        compose.onNodeWithTag("inbox-tab-primary").performClick()
        compose.awaitTag("thread-row-${primary.first().threadId}")

        val primaryRow = compose.scrollPastTheFold("inbox-list", compose.leadingRow("inbox-list"))

        compose.onNodeWithTag("inbox-tab-$LANE").performClick()
        compose.awaitTag("thread-row-${promotions.first().threadId}")
        val laneTop = compose.leadingRow("inbox-list")
        assertNotEquals("the lane opened on the other lane's row", primaryRow, laneTop)
        val laneRow = compose.scrollPastTheFold("inbox-list", laneTop, drags = 1)

        compose.openThreadBelowTheEdge("inbox-list")
        Gestures.pressBack(compose)
        compose.awaitTag("inbox-list")
        assertEquals(
            "the lane came back at another row than the one it was left on",
            laneRow,
            compose.leadingRow("inbox-list"),
        )

        // The other lane kept its own offset rather than taking this one.
        compose.onNodeWithTag("inbox-tab-primary").performClick()
        compose.awaitTag("inbox-list")
        assertEquals(
            "the primary lane did not come back at its own row",
            primaryRow,
            compose.leadingRow("inbox-list"),
        )
        compose.captureScreen("108-each-lane-at-its-own-row")
    }

    @Test
    fun t97_aMailboxDestinationKeepsItsOffset(): Unit = runBlocking {
        val archived = seedFolder("AllMail439", MailboxRoles.ARCHIVE)
        seedInbox("Inbox439b", category = null)

        // The inbox is left partway down, so the check below reads All
        // Mail's own offset rather than one carried over.
        val inboxRow = compose.scrollPastTheFold("inbox-list", compose.leadingRow("inbox-list"), drags = 1)

        openDestination(ALL_MAIL)
        compose.awaitTag("thread-row-${archived.first().threadId}")
        val archivedRow = compose.scrollPastTheFold("mailbox-list", compose.leadingRow("mailbox-list"))

        compose.openThreadBelowTheEdge("mailbox-list")
        Gestures.pressBack(compose)
        compose.awaitTag("mailbox-list")
        // The picture is taken before the assertion, so a run that fails
        // it leaves the list as the reader would have found it.
        compose.captureScreen("109-all-mail-back-from-a-conversation")
        assertEquals(
            "All Mail came back at another row than the one it was left on",
            archivedRow,
            compose.leadingRow("mailbox-list"),
        )

        // Back to the inbox and out to All Mail again: each destination
        // holds the offset it was left at.
        openDestination("drawer-inbox")
        compose.awaitTag("inbox-list")
        assertEquals(
            "the inbox took All Mail's offset instead of its own",
            inboxRow,
            compose.leadingRow("inbox-list"),
        )
        openDestination(ALL_MAIL)
        compose.awaitTag("mailbox-list")
        assertEquals(
            "All Mail lost its offset while the inbox was open",
            archivedRow,
            compose.leadingRow("mailbox-list"),
        )
    }

    @Test
    fun t98_searchResultsKeepTheirOffset(): Unit = runBlocking {
        val token = "Search439"
        val rows = seedInbox(token, category = null)

        // The seeded rows are the client's own, so the results that hold
        // them are the cached ones the field answers with when the
        // server cannot be reached (REQ-AND-SYNC-13).
        radios(up = false)
        awaitOffline(compose)

        compose.onNodeWithTag("inbox-search").performClick()
        compose.awaitTag("search-field")
        compose.onNodeWithTag("search-field").performTextInput(token)
        compose.onNodeWithTag("search-field").performImeAction()
        compose.awaitTag("search-row-${rows.first().threadId}")

        val left = compose.scrollPastTheFold(
            listTag = "search-results",
            top = compose.leadingRow("search-results", prefix = SEARCH_ROW),
            prefix = SEARCH_ROW,
        )

        compose.openThreadBelowTheEdge("search-results", prefix = SEARCH_ROW)
        Gestures.pressBack(compose)
        compose.awaitTag("search-results")
        assertEquals(
            "the results came back at another row than the one they were left on",
            left,
            compose.leadingRow("search-results", prefix = SEARCH_ROW),
        )
        compose.captureScreen("110-search-results-back-at-the-row-they-were-left-on")
    }

    // ---- helpers ---------------------------------------------------------

    private suspend fun seedInbox(token: String, category: String?): List<Email> =
        seed(token, MailboxRoles.INBOX, category)

    private suspend fun seedFolder(token: String, role: String): List<Email> = seed(token, role, null)

    private suspend fun seed(
        token: String,
        role: String,
        category: String?,
        count: Int = SEEDED,
    ): List<Email> {
        val accountId = SeededRows.accountId(app)
        return SeededRows.seed(
            app = app,
            accountId = accountId,
            mailboxId = SeededRows.mailboxId(app, accountId, role),
            token = token,
            count = count,
            keywords = category?.let { setOf(Keywords.SEEN, Keywords.categoryKeyword(it)) }
                ?: setOf(Keywords.SEEN),
        )
    }

    /** Opens the drawer and picks a destination, bringing its row into view first. */
    private fun openDestination(tag: String) {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.awaitTag("drawer-inbox")
        compose.onNodeWithTag("inbox-drawer").performScrollToNode(hasTestTag(tag))
        compose.onNodeWithTag(tag).performClick()
    }

    /** Turns the emulator's radios off and on. */
    private fun radios(up: Boolean) {
        val verb = if (up) "enable" else "disable"
        shellOut("svc wifi $verb")
        shellOut("svc data $verb")
    }

    private companion object {
        /** How many conversations each seeded list holds. */
        const val SEEDED = 40

        /** The category the lane check's own mail states. */
        const val LANE = "promotions"

        const val ALL_MAIL = "drawer-folder-${MailboxRoles.ARCHIVE}"
        const val SEARCH_ROW = "search-row-"
    }
}

/** How long a screen has to come up before a wait gives up. */
private const val TIMEOUT_MS = 30_000L

/** The test tag a conversation row in a message list carries. */
internal const val THREAD_ROW = "thread-row-"

internal fun ComposeTestRule.awaitTag(tag: String, timeoutMs: Long = TIMEOUT_MS) {
    waitUntil(timeoutMs) { onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty() }
}

/**
 * The test tags of the rows of the list tagged [listTag] that are
 * properly on screen, top first.
 *
 * A LazyColumn keeps rows in its semantics tree that the reader cannot
 * see: the ones it composed ahead of the viewport, whose visible bounds
 * come back clamped to a sliver at the list's edge, indistinguishable by
 * position from the row half-scrolled under the top. A row counts here
 * once at least half of it is on screen, which is the row the reader
 * would say the list stands on - and a restored offset puts the same
 * row in the same place, so the reading repeats.
 */
internal fun ComposeTestRule.rowsInView(listTag: String, prefix: String = THREAD_ROW): List<String> {
    waitForIdle()
    val matcher = SemanticsMatcher("test tag starts with $prefix") { node ->
        node.config.getOrNull(SemanticsProperties.TestTag)?.startsWith(prefix) == true
    }
    val nodes = onAllNodes(matcher).fetchSemanticsNodes().filter { it.boundsInWindow.width > 1f }
    if (nodes.isEmpty()) return emptyList()
    val whole = nodes.maxOf { it.boundsInWindow.height }
    return nodes
        .filter { it.boundsInWindow.height >= whole / 2f }
        .sortedBy { it.boundsInWindow.top }
        .mapNotNull { it.config.getOrNull(SemanticsProperties.TestTag) }
}

/** The tag of the row leading the list tagged [listTag]. */
internal fun ComposeTestRule.leadingRow(listTag: String, prefix: String = THREAD_ROW): String =
    rowsInView(listTag, prefix).firstOrNull() ?: error("no row of \"$listTag\" is on screen")

/**
 * Drags the list tagged [listTag] down past the fold and returns the row
 * leading it once it has come to rest.
 *
 * The drag is a finger's, because the list only keeps a place the reader
 * put there: a list nobody has touched is pinned to its newest message,
 * and that pin is itself a programmatic scroll.
 */
internal fun ComposeTestRule.scrollPastTheFold(
    listTag: String,
    top: String,
    drags: Int = 2,
    prefix: String = THREAD_ROW,
): String {
    repeat(drags) {
        Gestures.dragUp(onNodeWithTag(listTag).fetchSemanticsNode().boundsInWindow)
        waitForIdle()
    }
    val leading = settledLeadingRow(listTag, prefix)
    check(leading != top) { "the list \"$listTag\" did not move: $leading still leads it" }
    return leading
}

/** The leading row once a fling has run out. */
internal fun ComposeTestRule.settledLeadingRow(listTag: String, prefix: String = THREAD_ROW): String {
    var last = leadingRow(listTag, prefix)
    repeat(SETTLE_READS) {
        Thread.sleep(SETTLE_MS)
        val now = leadingRow(listTag, prefix)
        if (now == last) return now
        last = now
    }
    return last
}

/** How often, and how far apart, the leading row is read before it counts as still. */
private const val SETTLE_READS = 10
private const val SETTLE_MS = 400L

/** Opens the conversation the row tagged [rowTag] stands for. */
internal fun ComposeTestRule.openThread(rowTag: String) {
    onNodeWithTag(rowTag).performClick()
    awaitTag("thread-messages")
}

/**
 * Opens the conversation one row below the leading one, which is clear
 * of the app bar however far the leading row is scrolled under it.
 */
internal fun ComposeTestRule.openThreadBelowTheEdge(listTag: String, prefix: String = THREAD_ROW) {
    val rows = rowsInView(listTag, prefix)
    openThread(rows.getOrNull(1) ?: rows.first())
}

/** Waits until the shell says the phone has no connection. */
internal fun awaitOffline(rule: ComposeTestRule) {
    val app = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication
    rule.waitUntil(TIMEOUT_MS) { app.container.offline.value }
}
