package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation
import android.content.Intent
import android.net.Uri
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.lifecycle.Lifecycle
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.sync.toStoreRow
import androidx.compose.ui.test.click
import androidx.compose.ui.test.doubleClick
import com.netzhansa.herold.shared.auth.SignInResult
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
import java.io.File

/**
 * The milestone 1c compose acceptance (issue #329), driven against an
 * ephemeral herold from `scripts/dev-instance.sh`.
 *
 * What the message actually became is read back from the *recipient's*
 * account over JMAP, so threading headers, body alternatives and
 * attachment parts are asserted as they were delivered, not as the screen
 * claims to have sent them.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ComposeAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        Intents.init()
        runBlocking {
            app.container.signOut()
            val result = app.container.signInWithPassword(DevInstance.baseUrl, DevInstance.email, DevInstance.password, null)
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun releaseIntents() {
        Intents.release()
    }

    @Test
    fun t20_aReplyWithAnAttachmentArrivesThreadedAndIntact() = runBlocking {
        val parent = deliverAndReply("acceptance reply")

        // The reply is addressed to the sender and carries Re: plus the
        // parent's Message-ID, all derived by the shared core.
        compose.onNodeWithTag("compose-to-chip-${parent.fromEmail}").assertIsDisplayed()

        typeInBody("Here is the file you asked for.")
        attachFile("acceptance.txt", "text/plain", "attachment payload\n".toByteArray())
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-ready-acceptance.txt").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("20-reply-with-attachment")

        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        val delivered = awaitDelivered(parent.subject)
        assertEquals("Re: ${parent.subject}", delivered.subject)
        assertTrue(
            "the reply must carry the parent's Message-ID in In-Reply-To, saw ${delivered.inReplyTo}",
            delivered.inReplyTo.contains(parent.messageId.first()),
        )
        assertTrue(
            "the reply must reference the parent, saw ${delivered.references}",
            delivered.references.contains(parent.messageId.first()),
        )
        val attachment = delivered.attachments.firstOrNull { it.name == "acceptance.txt" }
        assertNotNull("the attachment must arrive with the message", attachment)
        assertEquals(19L, attachment!!.size)
        assertTrue("the body must carry what was typed", delivered.bodyText.orEmpty().contains("asked for"))
    }

    @Test
    fun t21_aBoldWordAndAnInlineImageSurviveTheSend() = runBlocking {
        val parent = deliverAndReply("acceptance rich")

        // Type the word, select it by double-tapping it, then apply bold -
        // the order a user works in.
        typeInBody("Bolded")
        compose.onNodeWithTag("compose-body").performTouchInput { doubleClick(Offset(40f, 20f)) }
        compose.waitForIdle()
        compose.onNodeWithTag("compose-bold").performClick()
        compose.waitForIdle()
        // Collapse the selection past the word so the image does not replace it.
        compose.onNodeWithTag("compose-body").performTouchInput { click(Offset(300f, 20f)) }
        compose.waitForIdle()
        stubPicker("dot.png", "image/png", PNG_BYTES)
        compose.onNodeWithTag("compose-insert-image").performClick()
        // The stubbed picker answers at once; the image is uploaded and
        // placed at the cursor, and the toolbar's tag carries the count.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-toolbar-1").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("21-rich-body")

        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        val delivered = awaitDelivered(parent.subject)
        val html = delivered.bodyHtml.orEmpty()
        assertTrue("the HTML alternative must carry the bold run, saw $html", html.contains("<b>Bolded</b>"))
        val inline = delivered.attachments.firstOrNull { it.isInline }
        assertNotNull("the inline image must arrive as an inline part", inline)
        assertTrue(
            "the body must reference the inline image by cid, saw $html",
            html.contains("cid:" + inline!!.cid?.trim('<', '>').orEmpty()),
        )
        assertTrue(
            "an inline image is not an attachment (suite G8)",
            delivered.attachments.none { !it.isInline },
        )
    }

    @Test
    fun t22_theFromPickerListsIdentitiesFromBothAccounts() = runBlocking {
        val accounts = app.container.store.accountList()
        assertTrue("the seeded principal must hold two accounts, saw $accounts", accounts.size >= 2)
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-from").performClick()
        val identities = app.container.store.identities().first()
        val perAccount = identities.groupBy { it.accountId }
        assertTrue("identities must span both accounts, saw $perAccount", perAccount.size >= 2)
        identities.forEach { identity ->
            compose.onNodeWithTag("from-option-${identity.accountId}-${identity.id}", useUnmergedTree = true)
                .assertExists()
        }
        compose.captureScreen("22-from-picker-two-accounts")
    }

    @Test
    fun t23_backgroundingTheAppSavesTheDraft() = runBlocking {
        val subject = "draft on background ${System.currentTimeMillis()}"
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput(subject)
        typeInBody("Unsent thoughts.")
        compose.captureScreen("23-draft-before-backgrounding")

        // Backgrounding is what triggers the save (suite REQ-DFT-01/02).
        compose.activityRule.scenario.moveToState(Lifecycle.State.CREATED)

        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val draftsId = server.mailboxGet(accountId).list.first { it.role == "drafts" }.id
        var saved: com.netzhansa.herold.shared.domain.Email? = null
        repeat(DELIVERY_POLLS) {
            val ids = server.emailQuery(
                accountId,
                buildJsonObject { put("inMailbox", draftsId) },
                20,
                collapseThreads = false,
            )
            saved = server.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
            if (saved != null) return@repeat
            Thread.sleep(DELIVERY_POLL_MS)
        }
        val draft = saved ?: error("no draft with subject \"$subject\" reached the Drafts mailbox")
        assertTrue("a draft carries \$draft", draft.keywords.any { it.equals("\$draft", ignoreCase = true) })
        assertEquals(listOf(DevInstance.recipientEmail), draft.toAddresses.map { it.email })
        assertTrue("the draft carries the body", draft.bodyText.orEmpty().contains("Unsent thoughts"))
    }

    // ---- helpers -------------------------------------------------------

    /**
     * Delivers a fresh message over the instance's SMTP listener, opens
     * its thread and starts a reply. Seeding its own parent keeps the run
     * independent of what earlier runs left in the mailbox, and the parent
     * returned is the message the reply bar acts on - the thread's newest.
     */
    private fun deliverAndReply(tag: String): com.netzhansa.herold.shared.domain.Email = runBlocking {
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject, body = "Parent body for $tag.")

        var seeded: com.netzhansa.herold.shared.domain.Email? = null
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
        // The reply bar answers the thread's newest message.
        val parent = app.container.store.threadEmails(parentRow.accountId, parentRow.threadId).first().last()
        compose.onNodeWithTag("thread-reply").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        parent
    }

    /**
     * Types into the body editor, which is a WebView holding a
     * contenteditable. The tap lands in its first line - the empty
     * paragraph above the quoted original - so the text goes where a
     * reply is written.
     */
    private fun typeInBody(text: String) {
        // The editor's document loads asynchronously and publishes its
        // body's length once it is up.
        compose.waitUntil(TIMEOUT_MS) { editorChars() >= 0 }
        val before = editorChars()
        compose.onNodeWithTag("compose-body").performTouchInput { click(Offset(30f, 20f)) }
        compose.waitForIdle()
        InstrumentationRegistry.getInstrumentation().sendStringSync(text)
        compose.waitUntil(TIMEOUT_MS) { editorChars() >= before + text.length }
    }

    /** How many characters the editor's body holds, or -1 before it loads. */
    private fun editorChars(): Int =
        compose.onAllNodes(hasTestTagStartingWith("compose-editor-"), useUnmergedTree = true)
            .fetchSemanticsNodes()
            .firstNotNullOfOrNull {
                it.config.getOrNull(SemanticsProperties.TestTag)
                    ?.removePrefix("compose-editor-")?.toIntOrNull()
            } ?: -1

    /**
     * Stubs the system file picker with a file of our own, so the
     * attachment path runs end to end without driving the SAF UI.
     */
    private fun stubPicker(name: String, type: String, bytes: ByteArray) {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val file = File(context.cacheDir, name).apply { writeBytes(bytes) }
        val result = Instrumentation.ActivityResult(
            Activity.RESULT_OK,
            Intent().setDataAndType(Uri.fromFile(file), type),
        )
        Intents.intending(hasAction(Intent.ACTION_OPEN_DOCUMENT)).respondWith(result)
    }

    private fun attachFile(name: String, type: String, bytes: ByteArray) {
        stubPicker(name, type, bytes)
        compose.onNodeWithTag("compose-attach").performClick()
    }

    /** The message as the recipient's account received it. */
    private suspend fun awaitDelivered(parentSubject: String): com.netzhansa.herold.shared.domain.Email {
        val expected = if (parentSubject.startsWith("Re: ")) parentSubject else "Re: $parentSubject"
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        repeat(DELIVERY_POLLS) {
            val ids = recipient.emailQueryInbox(
                accountId,
                recipient.mailboxGet(accountId).list.first { it.role == "inbox" }.id,
                20,
            )
            val found = recipient.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == expected }
            if (found != null) return found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no message with subject \"$expected\" reached ${DevInstance.recipientEmail}")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L

        /** A 64x64 solid PNG, big enough to be visible in a screenshot. */
        val PNG_BYTES: ByteArray = android.util.Base64.decode(
            "iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAeklEQVR4nO3PUQkAIBTAwBfHTCY2" +
                "liH8OITBAtxm7fN1wwUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSg" +
                "BQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWPHYBWQmhLScUAZAAAAAA" +
                "SUVORK5CYII=",
            android.util.Base64.DEFAULT,
        )
    }
}
