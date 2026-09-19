package com.netzhansa.herold.android

import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The conversation screen's arrangement (issue #428): the short app bar,
 * the subject as a heading in the scrolling content, a message card with
 * its own recipients line and actions, and the reply pills below the
 * conversation.
 *
 * The class leaves the app on the inbox and writes no account state, so
 * it runs in any position of the suite and twice over (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ThreadLayoutAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    /**
     * The bar carries the conversation's actions; the subject is a
     * heading in the content, where it scrolls away with the messages.
     */
    @Test
    fun t10_theSubjectIsAHeadingInTheContentAndNotInTheAppBar() {
        val message = seedMessage("Layout")
        openThread(message.threadId)

        compose.onNode(
            hasTestTag("thread-title") and hasAnyAncestor(hasTestTag("thread-app-bar")),
        ).assertDoesNotExist()
        compose.onNode(
            hasTestTag("thread-title") and hasAnyAncestor(hasTestTag("thread-messages")),
        ).assertExists()

        // Back, archive, delete, mark-unread and the overflow, plus the
        // star beside the subject and the pills at the bottom.
        listOf("thread-back", "thread-archive", "thread-delete", "thread-unread", "thread-overflow")
            .forEach { compose.onNodeWithTag(it).assertExists() }
        compose.onNodeWithTag("thread-star").assertExists()
        compose.onNodeWithTag("thread-reply-bar").assertExists()
        compose.onNodeWithTag("thread-reply").assertExists()
        compose.onNodeWithTag("thread-reply-all").assertExists()
        compose.onNodeWithTag("thread-forward").assertExists()

        // Share and snooze left the bar for the conversation overflow.
        compose.onAllNodesWithTag("thread-share").assertCountEquals(0)
        compose.openThreadOverflow()
        compose.onNodeWithTag("thread-share").assertExists()
        compose.onNodeWithTag("thread-snooze").assertExists()
        compose.captureScreen("m4-thread-conversation-overflow")
        dismissMenu()

        compose.onNodeWithTag("message-avatar-${message.id}", useUnmergedTree = true).assertExists()
        compose.captureScreen("m4-thread-layout")
        backToInbox()
    }

    /** The recipients line opens onto the addresses and the full timestamp. */
    @Test
    fun t20_theRecipientsLineExpandsToTheAddressesAndTheTimestamp() {
        val message = seedMessage("Recipients")
        openThread(message.threadId)

        compose.onAllNodesWithTag("message-details-${message.id}", useUnmergedTree = true)
            .assertCountEquals(0)
        compose.onNodeWithTag("message-recipients-${message.id}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-details-${message.id}", useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("message-timestamp-${message.id}", useUnmergedTree = true).assertExists()
        assertTrue(
            "the expanded block did not name the recipient's address",
            compose.onAllNodesWithText(DevInstance.email, substring = true, useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("m4-thread-recipients-expanded")

        compose.onNodeWithTag("message-recipients-${message.id}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-details-${message.id}", useUnmergedTree = true)
                .fetchSemanticsNodes().isEmpty()
        }
        backToInbox()
    }

    /** The message's own overflow carries the answers and marking unread from here. */
    @Test
    fun t30_theMessageOverflowOffersReplyAndMarkUnreadFromHere() {
        val message = seedMessage("Message actions")
        openThread(message.threadId)

        compose.onNodeWithTag("message-reply-${message.id}").assertExists()
        compose.openMessageOverflow(message.id)
        compose.onNodeWithTag("message-menu-reply-${message.id}").assertExists()
        compose.onNodeWithTag("message-menu-reply-all-${message.id}").assertExists()
        compose.onNodeWithTag("message-menu-forward-${message.id}").assertExists()
        compose.onNodeWithTag("message-menu-star-${message.id}").assertExists()
        compose.onNodeWithTag("message-menu-unread-${message.id}").assertExists()
        compose.onNodeWithTag("thread-block").assertExists()
        compose.onNodeWithTag("thread-create-filter").assertExists()
        compose.onNodeWithTag("thread-why").assertExists()
        compose.captureScreen("m4-thread-message-overflow")

        // Marking unread from here leaves the conversation and puts the
        // row back as unread.
        compose.onNodeWithTag("message-menu-unread-${message.id}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.store.email(message.accountId, message.id) }?.isUnread == true
        }
        backToInbox()
    }

    /** The pills below the conversation answer its newest message. */
    @Test
    fun t40_theBottomPillsAnswerTheNewestMessage() {
        val parent = seedThreadOfTwo()
        val thread = runBlocking { app.container.store.threadEmailList(parent.accountId, parent.threadId) }
        assertEquals("the reply did not thread onto its parent", 2, thread.size)
        val newest = thread.last()
        assertTrue(
            "the newest message must come from the replier",
            newest.fromEmail == REPLIER_ADDRESS,
        )

        openThread(parent.threadId)
        compose.onNodeWithTag("thread-reply").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-to-chip-${newest.fromEmail}").assertExists()
        compose.captureScreen("m4-thread-reply-to-newest")

        compose.onNodeWithTag("compose-close").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        backToInbox()
    }

    // ---- helpers ---------------------------------------------------------

    /** Delivers one message and waits for the store to hold it. */
    private fun seedMessage(tag: String): Email {
        signInAndSync()
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "A message for the $tag check.",
        )
        return awaitInbox(subject)
    }

    /**
     * Delivers a message and an answer to it, so the conversation has an
     * older and a newer sender and the pills can be shown to act on the
     * newer one.
     */
    private fun seedThreadOfTwo(): Email {
        signInAndSync()
        val subject = "Newest ${System.currentTimeMillis()}"
        val parentId = "layout-parent-" + System.nanoTime() + "@acceptance.test"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "The first message of the conversation.",
            messageId = parentId,
        )
        val parent = awaitInbox(subject)
        DevInstance.deliverMail(
            subject = "Re: $subject",
            from = "Filip Example <$REPLIER_ADDRESS>",
            body = "The answer, which is the message a reply answers.",
            headers = "In-Reply-To: <$parentId>\r\nReferences: <$parentId>\r\n",
        )
        awaitThreadSize(parent, 2)
        return parent
    }

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
        app.signInAsDevInstancePrincipal()
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    private fun awaitThreadSize(parent: Email, size: Int) = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            if (app.container.store.threadEmailList(parent.accountId, parent.threadId).size >= size) {
                return@runBlocking
            }
            Thread.sleep(POLL_MS)
        }
        error("the conversation never grew to $size messages")
    }

    private fun openThread(threadId: String) {
        backToInbox()
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Closes an open menu without leaving the conversation. */
    private fun dismissMenu() {
        androidx.test.espresso.Espresso.pressBack()
        compose.waitForIdle()
    }

    private fun backToInbox() {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
    }

    private companion object {
        const val REPLIER_ADDRESS = "filip@example.local"
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
