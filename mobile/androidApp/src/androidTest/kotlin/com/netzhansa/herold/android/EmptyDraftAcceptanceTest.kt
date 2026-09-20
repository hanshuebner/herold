package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
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
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * A composer the reader never changed (issue #371, the 0.9.10
 * hand-back).
 *
 * A reply opens carrying the address it answers, a `Re:` subject and
 * the quoted original, so "there is something in the composer" is true
 * of it from the first frame. What decides whether closing it keeps a
 * draft is whether the reader changed anything since it opened, and
 * that decision has to hold on both ways out of the composer - the
 * close control and back.
 *
 * The class delivers the conversations it reads and asserts only on
 * those, and takes away any draft it leaves behind, so it runs in any
 * position of the suite and twice over (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class EmptyDraftAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    /** The conversations this run created, cleaned of drafts at the end. */
    private val touched = mutableListOf<Email>()

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl,
                    DevInstance.email,
                    DevInstance.password,
                    null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Leaves the instance's Drafts mailbox as this class found it. */
    @After
    fun draftsCleared() = runBlocking {
        val server = serverClient()
        val accountId = server.session().mailAccountId!!
        touched.forEach { parent ->
            val ids = serverDrafts(parent)
            if (ids.isNotEmpty()) server.emailDestroy(accountId, ids)
        }
        touched.clear()
    }

    /**
     * The maintainer's sequence: open a reply, cancel it, do it again.
     * Nothing was typed either time, so the conversation ends where it
     * started and the server holds no draft for it.
     */
    @Test
    fun t99_closingAnUntouchedReplyTwiceLeavesNoDraft() = runBlocking {
        val parent = deliverAndOpen("untouched reply")

        openReply()
        closeComposer()
        openReply()
        closeComposer()

        assertNoDraft(parent)
        compose.captureScreen("99-thread-after-two-cancelled-replies")
    }

    /**
     * The same reply, left by back rather than by the close control.
     * Back pops the composer without running the close path, so what
     * the composer does as it stops is what decides here.
     */
    @Test
    fun t100_backingOutOfAnUntouchedReplyLeavesNoDraft() = runBlocking {
        val parent = deliverAndOpen("untouched reply back")

        openReply()
        Gestures.pressBack(compose)
        awaitConversation()
        Thread.sleep(SAVE_MS)

        assertNoDraft(parent)
    }

    /**
     * A reply the reader did write in is still kept, and the
     * conversation it is rendered in still throws it away - by the
     * card's own Discard, which is what is left once the snackbar the
     * save offered has come down.
     */
    @Test
    fun t101_anEditedReplyIsStillKeptAndStillDiscards() = runBlocking {
        val parent = deliverAndOpen("edited reply")

        openReply()
        compose.typeInBody("Yes, that works for me.")
        compose.onNodeWithTag("compose-close").performClick()
        awaitConversation()

        compose.waitUntil(TIMEOUT_MS) { runBlocking { threadDrafts(parent).isNotEmpty() } }
        val draft = threadDrafts(parent).first()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText(UndoMessages.DRAFT_SAVED).fetchSemanticsNodes().isNotEmpty()
        }

        compose.onNodeWithTag("thread-draft-discard-${draft.id}", useUnmergedTree = true).performClick()
        compose.waitUntil(TIMEOUT_MS) { runBlocking { threadDrafts(parent).isEmpty() } }
        assertNoDraft(parent)
    }

    // ---- helpers -------------------------------------------------------

    /** Opens a reply on the conversation and waits for its quoted body. */
    private fun openReply() {
        compose.onNodeWithTag("thread-reply").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        // The quote reaches the editor a moment after the screen does;
        // closing before it is up is not the flow being checked.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("compose-editor-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Leaves the composer by its close control, typing nothing. */
    private fun closeComposer() {
        compose.onNodeWithTag("compose-close").performClick()
        awaitConversation()
        // Long enough for a save the close started to reach the server.
        Thread.sleep(SAVE_MS)
    }

    private fun awaitConversation() {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-reply").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** No draft row, no draft card, no draft on the server. */
    private suspend fun assertNoDraft(parent: Email) {
        app.container.session.value!!.syncEngine.syncAll()
        assertEquals(
            "the conversation holds a draft the reader never wrote",
            emptyList<String>(),
            threadDrafts(parent).map { it.id },
        )
        compose.waitForIdle()
        assertEquals(
            "the thread view renders a draft card",
            0,
            compose.onAllNodes(hasTestTagStartingWith("thread-draft-"), useUnmergedTree = true)
                .fetchSemanticsNodes().size,
        )
        assertEquals(
            "the server holds a draft in the conversation",
            emptyList<String>(),
            serverDrafts(parent),
        )
    }

    private suspend fun threadDrafts(parent: Email): List<Email> =
        app.container.store.threadEmailList(parent.accountId, parent.threadId)
            .filter { row -> row.keywords.any { it.equals(Keywords.DRAFT, ignoreCase = true) } }

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

    /** One independent client for the whole class. */
    private suspend fun serverClient(): JmapClient =
        held ?: DevInstance.serverClient().also { held = it }

    private var held: JmapClient? = null

    /** Delivers a message and opens its conversation; returns its last message. */
    private fun deliverAndOpen(tag: String): Email = runBlocking {
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
        val row = seeded ?: error("the seeded message \"$subject\" never reached the inbox")

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${row.threadId}"))
        compose.onNodeWithTag("thread-row-${row.threadId}").performClick()
        awaitConversation()
        val parent = app.container.store.threadEmails(row.accountId, row.threadId).first().last()
        touched += parent
        parent
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L

        /** Long enough for a save started by the close to have landed. */
        const val SAVE_MS = 4_000L
    }
}
