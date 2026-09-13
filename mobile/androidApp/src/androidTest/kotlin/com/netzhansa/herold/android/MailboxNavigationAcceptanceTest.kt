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
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.domain.RuleActions
import com.netzhansa.herold.shared.domain.RuleFields
import com.netzhansa.herold.shared.domain.RuleOps
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.junit.Assert.assertFalse
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The drawer's folders and label tree (issue #374, suite REQ-UI-13b/d,
 * REQ-LBL-20), driven against an ephemeral herold.
 *
 * Both destinations are seeded on the server before the phone signs in, so
 * the initial fill - which covers the inbox - has not brought either
 * message into the local store. Opening the destination is what has to
 * fetch it.
 */
@RunWith(AndroidJUnit4::class)
class MailboxNavigationAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    @Test
    fun t40_aFolderAndALabelOpenFromTheDrawerAndListTheirThreads(): Unit = runBlocking {
        val stamp = System.currentTimeMillis()
        val labelName = "Drawer$stamp"
        val sender = "drawer-$stamp@vendor.example"
        val labelled = "Labelled $stamp"
        val archived = "Archived $stamp"

        // The seeding runs while the phone holds no session, so the
        // shell's own reconciliation cannot put the seeded mail in the
        // store behind the check below.
        app.container.signOut()
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!

        // A rule that labels matching mail and keeps it out of the inbox;
        // applying it is also what creates the label on the server.
        val created = client.managedRuleSet(
            accountId,
            create = mapOf("drawer" to rule("Drawer $stamp", sender, labelName)),
        )
        check(created.errors.isEmpty()) { "the seed rule was refused: ${created.errors}" }

        DevInstance.deliverMail(subject = labelled, from = "Drawer <$sender>", body = "Filed by the rule.")
        DevInstance.deliverMail(subject = archived, body = "Moved to the archive.")

        // The archived conversation is put where a second client would put
        // it, so the phone has never held it in its inbox.
        val archiveId = mailboxId(client, accountId, MailboxRoles.ARCHIVE)
        val inboxId = mailboxId(client, accountId, MailboxRoles.INBOX)
        val emailId = awaitServerInboxEmail(client, accountId, archived)
        val moved = client.emailSet(
            accountId,
            mapOf(
                emailId to buildJsonObject {
                    put("mailboxIds/$inboxId", JsonNull)
                    put("mailboxIds/$archiveId", true)
                },
            ),
        )
        check(moved.notUpdated.isEmpty()) { "the archive move was refused: ${moved.notUpdated}" }
        // `Email/query` answers from the server's index, which trails a
        // write; the phone signs in once the move is in what it answers.
        awaitServerQuery(client, accountId, archiveId, emailId)
        awaitServerLabel(client, accountId, labelName)

        // The first fill covers the inbox, so neither seeded message is in
        // the store when the shell comes up.
        signIn()
        val held = app.container.store.emailList().map { it.subject }
        assertFalse("the inbox fill brought the archived message in: $held", held.contains(archived))
        assertFalse("the inbox fill brought the labelled message in: $held", held.contains(labelled))

        openDrawer()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-folder-${MailboxRoles.ARCHIVE}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m3-drawer-folders-and-labels")
        openDestination("drawer-label-$labelName")
        awaitRow(labelled)
        compose.captureScreen("m3-label-thread-list")

        openDestination("drawer-folder-${MailboxRoles.ARCHIVE}")
        awaitRow(archived)
    }

    // ---- helpers ---------------------------------------------------------

    /** Waits for the destination's list to carry the conversation. */
    private fun awaitRow(subject: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("mailbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        // The fill behind an opened destination is a round trip for the
        // mailbox's newest messages and their threads, which is what this
        // waits on rather than on a repaint.
        compose.waitUntil(FILL_TIMEOUT_MS) {
            compose.onAllNodesWithText(subject, substring = true).fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * Opens the drawer and picks a destination. The sheet scrolls, and a
     * row the sheet has scrolled past is not where a tap would land, so
     * the row is brought into view first.
     */
    private fun openDestination(tag: String) {
        openDrawer()
        compose.onNodeWithTag("inbox-drawer").performScrollToNode(hasTestTag(tag))
        compose.onNodeWithTag(tag).performClick()
    }

    private fun openDrawer() {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-inbox").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun signIn() = runBlocking {
        grantNotificationPermission()
        // A fresh grant either way: the shell's own restore may have put a
        // session back while the seeding ran.
        val result = app.container.signInWithPassword(
            DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
        )
        org.junit.Assert.assertTrue("sign-in failed: $result", result is SignInResult.Success)
        val status = app.container.session.value!!.syncEngine.syncAll()
        check(status !is com.netzhansa.herold.shared.sync.SyncStatus.Failed) { "the first sync failed: $status" }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private suspend fun mailboxId(client: JmapClient, accountId: String, role: String): String =
        client.mailboxGet(accountId).list.first { it.role == role }.id

    /** The wire rule the seed installs: label matching mail, skip the inbox. */
    private fun rule(name: String, sender: String, label: String): JsonObject = buildJsonObject {
        put("name", name)
        put("enabled", true)
        put("order", 0)
        putJsonArray("conditions") {
            addJsonObject {
                put("field", RuleFields.FROM)
                put("op", RuleOps.CONTAINS)
                put("value", sender)
            }
        }
        putJsonArray("actions") {
            addJsonObject {
                put("kind", RuleActions.APPLY_LABEL)
                putJsonObject("params") { put("label", label) }
            }
            addJsonObject {
                put("kind", RuleActions.SKIP_INBOX)
                putJsonObject("params") { }
            }
        }
    }

    /** Waits for a mailbox's query to carry the message. */
    private suspend fun awaitServerQuery(
        client: JmapClient,
        accountId: String,
        mailboxId: String,
        emailId: String,
    ) {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (client.emailQueryInbox(accountId, mailboxId, QUERY_LIMIT).contains(emailId)) return
            Thread.sleep(POLL_MS)
        }
        error("the server's query for mailbox $mailboxId never carried $emailId")
    }

    /** Waits for the label the rule creates on first delivery. */
    private suspend fun awaitServerLabel(client: JmapClient, accountId: String, name: String) {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (client.mailboxGet(accountId).list.any { it.name == name }) return
            Thread.sleep(POLL_MS)
        }
        error("the label \"$name\" never appeared on the server")
    }

    private suspend fun awaitServerInboxEmail(
        client: JmapClient,
        accountId: String,
        subject: String,
    ): String {
        val inbox = mailboxId(client, accountId, MailboxRoles.INBOX)
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val ids = client.emailQueryInbox(accountId, inbox, QUERY_LIMIT)
            val found = client.emailGet(accountId, ids).list.firstOrNull { it.subject == subject }
            if (found != null) return found.id
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the server's inbox")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val FILL_TIMEOUT_MS = 60_000L
        const val POLL_MS = 500L
        const val QUERY_LIMIT = 30
    }
}
