package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.actions.UndoActions
import com.netzhansa.herold.shared.actions.UndoMessages
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Discarding a draft reply (issue #371). The conversation renders drafts
 * out of the local store, so a discard is done only when the row is gone
 * from the store, the card is gone from the thread and the server holds
 * no draft for the conversation - whatever order the save and the
 * discard resolved in.
 *
 * The class signs in through a [ServerReach] relay, so a check can take
 * the wire away between the close and the discard with the radios
 * untouched.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class DraftDiscardAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private lateinit var reach: ServerReach

    @Before
    fun signedIn() {
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
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun wireDown() {
        reach.close()
    }

    /** Reply, close, Discard at once, with the wire up throughout. */
    @Test
    fun t96_discardingAReplyAtOnceLeavesNoDraft() = runBlocking {
        val parent = deliverAndReply("discard at once")
        closeWithContent()
        awaitDiscardOffer()
        compose.onNodeWithText(UndoActions.DISCARD).performClick()

        awaitNoDraft(parent)
        app.container.session.value!!.syncEngine.syncAll()
        awaitNoDraft(parent)
        assertNoDraftCard()
        compose.captureScreen("96-thread-after-an-immediate-discard")
        assertNoServerDraft(parent)
    }

    /**
     * The fetch that was in flight when the discard happened. A sync
     * pass reads the conversation's messages and writes what it read
     * into the store; one that read the draft before the destroy lands
     * after it, and the row it writes must not bring the card back
     * (issue #371, the 0.9.5 hand-back).
     */
    @Test
    fun t97_aFetchInFlightDoesNotBringTheDiscardedDraftBack() = runBlocking {
        val parent = deliverAndReply("stale fetch")
        closeWithContent()
        awaitDiscardOffer()
        val draft = awaitThreadDraft(parent)
        // What an Email/get issued before the discard answers with.
        val inFlight = app.container.store.email(parent.accountId, draft.id)!!

        compose.onNodeWithText(UndoActions.DISCARD).performClick()
        awaitNoDraft(parent)

        app.container.store.upsertEmails(listOf(inFlight))
        assertEquals(
            "a fetch in flight put the discarded draft back",
            emptyList<String>(),
            threadDrafts(parent).map { it.id },
        )
        Thread.sleep(SETTLE_MS)
        assertNoDraftCard()
        assertNoServerDraft(parent)
    }

    /**
     * A discard issued for a draft the client has no server id for: the
     * save was queued with no wire, and the drain wrote it while the
     * offer stood. The discard has to take away the message the save
     * went on to create, not only the queue entry it knew about.
     */
    @Test
    fun t98_discardingADraftTheDrainHasJustWrittenTakesItAway() = runBlocking {
        val parent = deliverAndReply("queued draft")
        reach.takeAway()
        closeWithContent()
        awaitDiscardOffer()
        // The wire comes back and the queued save reaches the server
        // while the offer is still on screen.
        reach.giveBack()
        app.container.session.value!!.syncEngine.drainOutbox()
        val onServer = awaitServerDraft(parent)
        assertNotNull("the drain never wrote the queued draft", onServer)

        compose.onNodeWithText(UndoActions.DISCARD).performClick()
        awaitNoDraft(parent)
        app.container.session.value!!.syncEngine.syncAll()
        awaitNoDraft(parent)
        assertNoDraftCard()
        assertNoServerDraft(parent)
    }

    // ---- helpers -------------------------------------------------------

    /**
     * Writes a line into the open composer and closes it. The line goes
     * in the body: an edit of the subject makes the draft a conversation
     * of its own, and this class reads the conversation the reply
     * answers.
     */
    private fun closeWithContent() {
        compose.typeInBody("A line of the answer.")
        compose.onNodeWithTag("compose-close").performClick()
    }

    private fun awaitDiscardOffer() {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText(UndoMessages.DRAFT_SAVED).fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Waits until the store holds no draft row for the conversation. */
    private fun awaitNoDraft(parent: Email) {
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { threadDrafts(parent).isEmpty() }
        }
    }

    private fun assertNoDraftCard() {
        compose.waitForIdle()
        assertEquals(
            "the thread view still renders a draft card",
            0,
            compose.onAllNodes(hasTestTagStartingWith("thread-draft-"), useUnmergedTree = true)
                .fetchSemanticsNodes().size,
        )
    }

    private suspend fun threadDrafts(parent: Email): List<Email> =
        app.container.store.threadEmailList(parent.accountId, parent.threadId)
            .filter { row -> row.keywords.any { it.equals(Keywords.DRAFT, ignoreCase = true) } }

    /** The conversation's draft, once the store has it. */
    private fun awaitThreadDraft(parent: Email): Email {
        compose.waitUntil(TIMEOUT_MS) { runBlocking { threadDrafts(parent).isNotEmpty() } }
        return runBlocking { threadDrafts(parent).first() }
    }

    /** What the server holds in the conversation's Drafts mailbox. */
    private suspend fun serverDrafts(parent: Email): List<String> {
        val server = serverClient()
        val accountId = server.session().mailAccountId!!
        val drafts = server.mailboxGet(accountId).list.first { it.role == "drafts" }.id
        val ids = server.emailQuery(
            accountId,
            buildJsonObject { put("inMailbox", drafts) },
            50,
            collapseThreads = false,
        )
        return server.emailGet(accountId, ids).list
            .filter { it.threadId == parent.threadId }
            .map { it.id }
    }

    private suspend fun awaitServerDraft(parent: Email): String? {
        repeat(DELIVERY_POLLS) {
            serverDrafts(parent).firstOrNull()?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        return null
    }

    private suspend fun assertNoServerDraft(parent: Email) {
        assertEquals(
            "the server still holds a draft in the conversation",
            emptyList<String>(),
            serverDrafts(parent),
        )
    }

    /** One independent client for the whole class, off the relay. */
    private suspend fun serverClient(): JmapClient =
        held ?: DevInstance.serverClient().also { held = it }

    private var held: JmapClient? = null

    /**
     * Delivers a fresh message, opens its conversation and starts a
     * reply on it; returns the message the reply answers.
     */
    private fun deliverAndReply(tag: String): Email = runBlocking {
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject, body = "Parent body for $tag.")
        DevInstance.awaitFiled(subject)

        var seeded: Email? = null
        repeat(DELIVERY_POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            seeded = app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
            if (seeded != null) return@repeat
            Thread.sleep(DELIVERY_POLL_MS)
        }
        val parentRow = seeded ?: error("the seeded message \"$subject\" never reached the inbox")

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${parentRow.threadId}"))
        compose.onNodeWithTag("thread-row-${parentRow.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-reply").fetchSemanticsNodes().isNotEmpty()
        }
        val parent = app.container.store.threadEmails(parentRow.accountId, parentRow.threadId).first().last()
        compose.onNodeWithTag("thread-reply").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        parent
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L

        /** Long enough for a re-inserted row to reach the screen. */
        const val SETTLE_MS = 2_000L
    }
}
