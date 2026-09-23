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
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The inbox against the Junk rule (issue #467), driven against an
 * ephemeral herold: a second JMAP session files one message to Junk and
 * another to Trash while both keep their Inbox membership, which is what
 * a classifier verdict and the reported Trash pair look like on the wire.
 * The junked conversation leaves the inbox lane and is listed under Spam;
 * the trashed one stays in the inbox, since Trash never coexists with
 * another mailbox (issue #460) and the pair is the store's defect rather
 * than something the list hides.
 *
 *   am instrument ... -e class com.netzhansa.herold.android.JunkExclusionAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 */
@RunWith(AndroidJUnit4::class)
class JunkExclusionAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun t60_aJunkedConversationLeavesTheInboxAndATrashedOneStaysListed(): Unit = runBlocking {
        app.signInAsDevInstancePrincipal()
        syncNow()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }

        val stamp = System.currentTimeMillis()
        val junked = deliverAndAwait("Junked $stamp")
        val trashed = deliverAndAwait("Trashed $stamp")
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(junked.threadId) }
        compose.waitUntil(TIMEOUT_MS) { compose.listHoldsThread(trashed.threadId) }
        compose.captureScreen("m4-467-both-listed")

        // The filing happens on another session of the same account, the
        // way a classifier verdict or another client's move reaches this
        // one: an Email/set patch that adds a membership and leaves the
        // Inbox membership standing.
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val inboxId = mailboxId(client, accountId, MailboxRoles.INBOX)
        val junkId = mailboxId(client, accountId, MailboxRoles.JUNK)
        val trashId = mailboxId(client, accountId, MailboxRoles.TRASH)
        addMembership(client, accountId, junked.id, junkId)
        addMembership(client, accountId, trashed.id, trashId)
        assertServerHolds(client, accountId, junked.id, setOf(inboxId, junkId))
        assertServerHolds(client, accountId, trashed.id, setOf(inboxId, trashId))

        // REQ-CAT-independent: whatever lane the conversation sat in, the
        // junked one is out of the inbox once the change is reconciled.
        compose.waitUntil(TIMEOUT_MS) {
            syncNow()
            compose.listLacksThread(junked.threadId)
        }
        assertTrue(
            "the conversation in Inbox and Trash must stay listed",
            compose.listHoldsThread(trashed.threadId),
        )
        compose.captureScreen("m4-467-junked-gone-trashed-listed")

        // The Spam destination lists what is filed there, so the rule
        // hides the message from the inbox rather than from the client.
        openDestination("drawer-folder-${MailboxRoles.JUNK}")
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("mailbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(FILL_TIMEOUT_MS) {
            compose.onAllNodesWithText(junked.subject, substring = true).fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m4-467-junk-destination")
    }

    // ---- helpers ---------------------------------------------------------

    private fun syncNow(): Boolean {
        runBlocking { app.container.session.value!!.syncEngine.syncAll() }
        compose.waitForIdle()
        return true
    }

    /** Delivers one message and returns it once it is in the local inbox. */
    private fun deliverAndAwait(subject: String): Email {
        DevInstance.deliverMail(subject = subject, body = "Filed by another session.")
        return runBlocking {
            DevInstance.awaitFiled(subject)
            repeat(DELIVERY_POLLS) {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                    ?.let { return@runBlocking it }
                Thread.sleep(POLL_MS)
            }
            error("the seeded message \"$subject\" never reached the inbox")
        }
    }

    private suspend fun mailboxId(client: JmapClient, accountId: String, role: String): String =
        client.mailboxGet(accountId).list.first { it.role == role }.id

    /** Adds one membership and leaves every other membership standing. */
    private suspend fun addMembership(
        client: JmapClient,
        accountId: String,
        emailId: String,
        mailboxId: String,
    ) {
        val outcome = client.emailSet(
            accountId,
            mapOf(emailId to buildJsonObject { put("mailboxIds/$mailboxId", true) }),
        )
        check(outcome.notUpdated.isEmpty()) { "the membership add was refused: ${outcome.notUpdated}" }
    }

    /** The state the rule is about, read back from the server's own view. */
    private suspend fun assertServerHolds(
        client: JmapClient,
        accountId: String,
        emailId: String,
        mailboxIds: Set<String>,
    ) {
        val held = DevInstance.serverEmail(client, accountId, emailId)!!.mailboxIds
        assertTrue("the server holds $held for $emailId, expected $mailboxIds", held.containsAll(mailboxIds))
    }

    private fun openDestination(tag: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer").performScrollToNode(hasTestTag(tag))
        compose.onNodeWithTag(tag).performClick()
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val FILL_TIMEOUT_MS = 60_000L
        const val DELIVERY_POLLS = 40
        const val POLL_MS = 500L
    }
}
