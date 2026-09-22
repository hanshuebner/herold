package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.sync.SyncTypes
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * A conversation whose first message sits in Trash - never reached by any
 * inbox fill - still opens complete once its reply has synced (issue #461).
 *
 * The reply lands through an incremental sync that fetches only the
 * message it touched; the store then holds one of the conversation's two
 * messages. Opening the thread must complete it against the server rather
 * than take a non-empty cache as a finished one.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ThreadCompletionAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app
        get() = InstrumentationRegistry.getInstrumentation()
            .targetContext.applicationContext as HeroldApplication

    @Test
    fun t10_aThreadWithATrashOnlyMemberCompletesOnOpen() = runBlocking {
        grantNotificationPermission()

        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val mailboxes = client.mailboxGet(accountId).list
        val inboxId = mailboxes.first { it.role == MailboxRoles.INBOX }.id
        val trashId = mailboxes.first { it.role == MailboxRoles.TRASH }.id

        // An anchor in the Inbox, so the account's very first email fill
        // has something to query: an empty first fill leaves no state to
        // fold from, so the next sync repeats a full fill rather than
        // asking Email/changes - and a full fill completes every thread
        // it touches, papering over the incremental-fold gap this test
        // means to reproduce.
        val anchorSubject = "Anchor " + System.nanoTime()
        DevInstance.deliverMail(
            subject = anchorSubject,
            from = "Bob Example <bob@example.local>",
            body = "Unrelated mail, so the first fill has something to fold from.",
        )
        DevInstance.awaitFiled(anchorSubject)

        // The original message is delivered and moved to Trash before the
        // device's first sync, so that sync's fold treats its existence
        // as already-known baseline rather than as a change - the way a
        // conversation the reader trashed long ago never reaches an
        // inbox fill on a phone signing in for the first time.
        val subject = "Trashed original " + System.nanoTime()
        val parentMessageId = "trashed-parent-" + System.nanoTime() + "@acceptance.test"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "The first message of the conversation.",
            messageId = parentMessageId,
        )
        DevInstance.awaitFiled(subject)
        val originalId = client.emailGet(accountId, client.emailQueryInbox(accountId, inboxId, 20)).list
            .first { it.subject == subject }.id
        client.emailSet(
            accountId,
            mapOf(
                originalId to buildJsonObject {
                    put("mailboxIds/$inboxId", JsonPrimitive(null as String?))
                    put("mailboxIds/$trashId", JsonPrimitive(true))
                },
            ),
        )

        // A clean slate on the device, so its own first email sync is the
        // one that establishes the baseline above.
        app.container.signOut()
        val result = app.container.signInWithPassword(
            DevInstance.baseUrl,
            DevInstance.email,
            DevInstance.password,
            null,
        )
        assertTrue("sign-in failed: $result", result is SignInResult.Success)
        val session = app.container.session.value ?: error("sign-in left no session")
        session.syncEngine.syncAll()
        compose.awaitTag("inbox-list")
        compose.awaitTag("thread-row-${app.container.store.emailList().first { it.subject == anchorSubject }.threadId}")

        // The first fill folded from a real cursor and never touched the
        // trashed original's thread; a vacuous scenario - a second full
        // fill papering over the incremental path this test means to
        // exercise - would already show it here.
        assertTrue(
            "the first sync set no Email sync state; the next sync would repeat a full " +
                "fill instead of asking Email/changes, and the scenario would not reproduce",
            app.container.store.syncState(accountId, SyncTypes.EMAIL) != null,
        )
        assertTrue(
            "the first fill already reached the trashed original's thread; the scenario " +
                "did not set up a partial cache to complete",
            app.container.store.emailList().none { it.subject == subject },
        )

        val replySubject = "Re: $subject"
        DevInstance.deliverMail(
            subject = replySubject,
            from = "Filip Example <filip@example.local>",
            body = "The answer, which quotes and continues the trashed message.",
            headers = "In-Reply-To: <$parentMessageId>\r\nReferences: <$parentMessageId>\r\n",
        )
        DevInstance.awaitFiled(replySubject)
        // The incremental sync: only the reply is a change since the
        // cursor above, so only the reply is fetched (issue #461).
        session.syncEngine.syncAll()

        val reply = app.container.store.emailList().first { it.subject == replySubject }
        val threadId = reply.threadId
        assertEquals(
            "the store already held both messages before the thread was opened; " +
                "the scenario did not reproduce the partial cache",
            1,
            app.container.store.threadEmailList(reply.accountId, threadId).size,
        )

        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.store.threadEmailList(reply.accountId, threadId).size == 2 }
        }

        val messages = app.container.store.threadEmailList(reply.accountId, threadId)
        assertEquals(2, messages.size)
        messages.forEach { message ->
            compose.onNodeWithTag("message-${message.id}").assertExists()
        }
        compose.captureScreen("461-thread-completes-trashed-member")

        app.container.signOut()
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
