package com.netzhansa.herold.android

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Forwarding an HTML message (issue #431). The forward is addressed to
 * the signed-in principal, so the same run reads the copy that arrived
 * over JMAP and renders it in the reading pane - what the recipient got,
 * not what the composer claims to have sent.
 *
 * The seeded original is a formatted mail: a heading, a table, an inline
 * image, and the two things a forward must never carry on - a script and
 * a style block.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ForwardHtmlAcceptanceTest {

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
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t93_aForwardedHtmlMessageArrivesWithItsMarkup() = runBlocking {
        val subject = "html forward ${System.currentTimeMillis()}"
        val cid = "logo-" + System.nanoTime() + "@acceptance.test"
        DevInstance.deliverMailWithInlineImage(
            subject = subject,
            cid = cid,
            html = "<html><head><style>.brand { color: #b00020 }</style></head><body>" +
                "<h1 class=\"brand\">$HEADING</h1>" +
                "<table border=\"1\"><tr><td><strong>Build</strong></td><td>2026.9</td></tr></table>" +
                "<p><img src=\"cid:$cid\" alt=\"dot\"></p>" +
                "<script>window.evil = 1</script>" +
                "</body></html>",
        )
        DevInstance.awaitFiled(subject)
        val seeded = awaitInbox(subject)

        // Forward it to the principal itself, so the copy that arrives is
        // readable in this session.
        compose.scrollListToThread(seeded.threadId)
        compose.onNodeWithTag("thread-row-${seeded.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-forward").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-forward").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.email + ",")
        // The editor's document is up once it has published its body.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("compose-editor-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-body").performTouchInput { click(Offset(30f, 20f)) }
        compose.captureScreen("93-forward-quotes-the-html")

        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        // What arrived, read off the server rather than off the screen.
        val delivered = awaitForward("Fwd: $subject")
        val html = delivered.bodyHtml.orEmpty()
        assertTrue("the forward must carry the original's heading, saw $html", html.contains(HEADING))
        assertTrue("the forward must carry the original's markup, saw $html", html.contains("<strong>Build</strong>"))
        assertTrue("the forward must carry the original's table, saw $html", html.contains("<table"))
        assertTrue(
            "the forward must reference the inline part by cid, saw $html",
            html.contains("cid:$cid"),
        )
        assertTrue(
            "the inline part must arrive with the forward, saw ${delivered.attachments}",
            delivered.attachments.any { it.isInline && it.cid?.trim('<', '>') == cid },
        )
        assertTrue("no script leaves with a forward, saw $html", !html.contains("script", ignoreCase = true))
        assertTrue("no style block leaves with a forward, saw $html", !html.contains("<style", ignoreCase = true))
        assertTrue(
            "the text alternative still holds the plain-text rendering, saw ${delivered.bodyText}",
            delivered.bodyText.orEmpty().contains(HEADING),
        )

        // And the same copy renders in the reading pane, markup and all.
        val received = awaitStored(seeded.accountId, delivered.id)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        if (compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isEmpty()) {
            compose.scrollListToThread(received.threadId)
            compose.onNodeWithTag("thread-row-${received.threadId}").performClick()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${received.id}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-messages").performScrollToNode(hasTestTag("message-body-${received.id}"))
        Thread.sleep(SETTLE_MS)
        compose.captureScreen("93-forward-as-received")
    }

    /** The seeded original, once the client's sync has it. */
    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    /** The forwarded copy, as the receiving account holds it. */
    private suspend fun awaitForward(subject: String): Email {
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val inbox = client.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(POLLS) {
            val ids = client.emailQueryInbox(accountId, inbox, 20)
            client.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
                ?.let { return it }
            Thread.sleep(POLL_MS)
        }
        error("the forward \"$subject\" never reached ${DevInstance.email}")
    }

    /** The same message, once the client's store holds it. */
    private suspend fun awaitStored(accountId: String, id: String): Email {
        repeat(POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.email(accountId, id)?.let { return it }
            Thread.sleep(POLL_MS)
        }
        error("the forwarded copy never reached the store")
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val SETTLE_MS = 2_000L
        const val POLLS = 30
        const val POLL_MS = 1_000L

        /** A word that appears in the original's markup and nowhere else. */
        const val HEADING = "Release notes"
    }
}
