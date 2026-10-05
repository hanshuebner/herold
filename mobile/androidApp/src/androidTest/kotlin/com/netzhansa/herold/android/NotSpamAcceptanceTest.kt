package com.netzhansa.herold.android

import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleFields
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeNotNull
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Correcting a wrong spam verdict from the phone (issue #506): a
 * conversation the filing put in Junk offers "Not spam" in the
 * conversation overflow and in the Junk list's row menu, the correction
 * moves the message back to the inbox through the outbox, the
 * REQ-FILT-70 ham record reaches the server's audit log, and the
 * never-spam rule the sheet offers is written for the sender's domain.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class NotSpamAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    /**
     * The whole correction, as a reader makes it: open the Junk-filed
     * conversation, choose "Not spam", opt the sender's domain out of
     * spam filing for good, and land back on a list with the message in
     * the inbox, the server agreeing, and the feedback recorded.
     */
    @Test
    fun t10_aJunkFiledConversationIsCorrectedFromTheConversationOverflow() {
        val seeded = seedJunkMessage()

        openJunkThread(seeded.threadId)

        compose.openThreadOverflow()
        compose.onNodeWithTag("thread-not-spam").assertExists()
        compose.captureScreen("506-not-spam-action")
        compose.onNodeWithTag("thread-not-spam").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("not-spam-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("not-spam-scope-domain").performClick()
        compose.captureScreen("506-not-spam-sheet")
        compose.onNodeWithTag("not-spam-confirm").performClick()

        // The local store is what the screens render from, so the move
        // shows there before the drain has run (REQ-AND-SYNC-20).
        compose.waitUntil(TIMEOUT_MS) {
            val local = runBlocking { app.container.store.email(seeded.accountId, seeded.id) }
            local != null && local.mailboxIds.contains(inboxId) && !local.keywords.contains(Keywords.JUNK)
        }

        // The server holds the same move once the outbox has drained.
        val client = runBlocking { DevInstance.serverClient() }
        val serverAccount = runBlocking { client.session().mailAccountId!! }
        awaitServerInbox(seeded, client, serverAccount)

        // The ham record the correction posted (REQ-FILT-70). The audit
        // log is admin-scoped, so the check needs the harness's key.
        val adminUrl = DevInstance.adminUrl
        val adminKey = DevInstance.adminKey
        assumeNotNull(adminUrl, adminKey)
        val feedback = awaitFeedback(adminUrl!!, adminKey!!, seeded.id)
        assertEquals("ham", feedback.metadata["kind"])
        assertEquals("ham", feedback.metadata["corrected_verdict"])

        // The never-spam rule the sheet offered, for the sender's domain.
        val domain = seeded.fromEmail.substringAfterLast('@').lowercase()
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.store.managedRuleList() }.any { rule ->
                rule.actions.any { it.kind == RuleActions.NEVER_SPAM } &&
                    rule.conditions.any { it.field == RuleFields.FROM_DOMAIN && it.value == domain }
            }
        }

        backToInbox()
    }

    /** A conversation outside Junk offers the reverse correction instead. */
    @Test
    fun t20_aConversationOutsideJunkOffersReportSpam() {
        signInAndSync()
        val subject = "Report spam ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "A message the reader files as spam themselves.",
        )
        val message = awaitStore(subject)

        backToInbox()
        compose.scrollListToThread(message.threadId)
        compose.onNodeWithTag("thread-row-${message.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }

        compose.openThreadOverflow()
        compose.onNodeWithTag("thread-report-spam").assertExists()
        compose.onNodeWithTag("thread-report-phishing").assertExists()
        compose.onNodeWithTag("thread-report-spam").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            val local = runBlocking { app.container.store.email(message.accountId, message.id) }
            local != null && local.keywords.contains(Keywords.JUNK) && !local.mailboxIds.contains(inboxId)
        }

        val adminUrl = DevInstance.adminUrl
        val adminKey = DevInstance.adminKey
        assumeNotNull(adminUrl, adminKey)
        assertEquals("spam", awaitFeedback(adminUrl!!, adminKey!!, message.id).metadata["kind"])

        backToInbox()
    }

    // ---- helpers ---------------------------------------------------------

    /** The account's inbox as the local store holds it. */
    private val inboxId: String
        get() = runBlocking { app.container.store.mailboxList() }
            .first { it.role == MailboxRoles.INBOX }.id

    /**
     * Delivers a message and files it in Junk from a second client, so
     * the conversation under test is in Junk whatever the instance's
     * classifier makes of a seeded body.
     */
    private fun seedJunkMessage(): Email {
        signInAndSync()
        val subject = "Meter reading ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Utility Billing <no-reply@utility.example>",
            body = "Thank you for submitting your meter reading.",
        )
        val message = awaitStore(subject)
        runBlocking {
            val client = DevInstance.serverClient()
            val accountId = client.session().mailAccountId!!
            DevInstance.fileInJunk(client, accountId, message.id)
        }
        // The fold brings the filing into the local store, which is
        // where the screen reads the Junk membership from.
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                val local = app.container.store.email(message.accountId, message.id)
                local != null && local.mailboxIds.none { it == inboxId }
            }
        }
        return runBlocking { app.container.store.email(message.accountId, message.id)!! }
    }

    /** Walks the drawer to Spam and opens the conversation listed there. */
    private fun openJunkThread(threadId: String) {
        backToInbox()
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-folder-junk").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-folder-junk").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("mailbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.listHoldsThread(threadId, listTag = "mailbox-list")
        }
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
        app.signInAsDevInstancePrincipal()
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun awaitStore(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    /** Waits until the server's own copy of the message is in the inbox. */
    private fun awaitServerInbox(
        message: Email,
        client: com.netzhansa.herold.shared.jmap.JmapClient,
        accountId: String,
    ) = runBlocking {
        val inbox = client.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            val held = DevInstance.serverEmail(client, accountId, message.id)
            if (held != null && held.mailboxIds.contains(inbox)) return@runBlocking
            Thread.sleep(POLL_MS)
        }
        error("the server never moved ${message.id} back to the inbox")
    }

    /** Waits for the message's `mail.spam.feedback` record to be written. */
    private fun awaitFeedback(adminUrl: String, adminKey: String, emailId: String): AuditApi.Entry {
        repeat(POLL_ATTEMPTS) {
            val held = AuditApi.list(adminUrl, adminKey, "mail.spam.feedback")
                .firstOrNull { it.metadata["email_id"] == emailId }
            if (held != null) return held
            Thread.sleep(POLL_MS)
        }
        error("the server never recorded spam feedback for $emailId")
    }

    /**
     * Leaves whatever screen is up for the inbox. Back pops the thread
     * view; the list screen keeps the destination it was left on, so
     * the Junk mailbox is left through the drawer, which is also how a
     * reader leaves it.
     */
    private fun backToInbox() {
        var guard = 0
        while (!compose.onTheInbox()) {
            assertTrue("the app never came back to the list", guard++ < BACK_PRESSES)
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
        if (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()) return
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-inbox").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-inbox").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val POLL_ATTEMPTS = 30
        const val POLL_MS = 1_000L
        const val BACK_PRESSES = 8
    }
}
