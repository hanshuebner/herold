package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation
import android.content.Intent
import android.net.Uri
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.Keywords
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.domain.Identity
import com.netzhansa.herold.shared.outbox.OutboxEntry
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.coroutines.runBlocking
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
 * The milestone 2a acceptance (issue #351): the app is usable with no
 * connectivity, what the user did is durable, and it reaches the server
 * when a connection comes back.
 *
 * The harness runs the phases around the emulator's radios and a kill of
 * the app process, because that is what the bullets are about:
 *
 *   am instrument ... -e class OutboxAcceptanceTest#t60_warmOnline
 *   adb shell svc data disable && adb shell svc wifi disable
 *   am instrument ... -e class OutboxAcceptanceTest#t61_queueThreeThingsOffline
 *   adb shell am kill com.netzhansa.herold.android
 *   am instrument ... -e class OutboxAcceptanceTest#t62_theQueueSurvivesProcessDeath
 *   adb shell svc data enable && adb shell svc wifi enable
 *   am instrument ... -e class OutboxAcceptanceTest#t63_theQueueDrainsAndTheServerHoldsIt
 *   am instrument ... -e class OutboxAcceptanceTest#t64_aRefusedSendKeepsItsError
 *
 * What the server ended up with is read back through an independent JMAP
 * client, so the assertions are about herold's state and not about what
 * the screen claims.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class OutboxAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun ready() {
        grantNotificationPermission()
        Intents.init()
    }

    @After
    fun releaseIntents() {
        Intents.release()
    }

    /**
     * Phase one, online: sign in, seed the two conversations the offline
     * phase acts on, and cache them so they render with the radios off.
     */
    @Test
    fun t60_warmOnline() = runBlocking {
        if (app.container.session.value == null) {
            val result = app.container.signIn(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        val stamp = System.currentTimeMillis()
        DevInstance.deliverMail("outbox archive $stamp", body = "To be archived offline.")
        DevInstance.deliverMail("outbox star $stamp", body = "To be starred offline.")

        val seeded = awaitInInbox(listOf("outbox archive $stamp", "outbox star $stamp"))
        assertEquals(2, seeded.size)
        markerFile().writeText("$stamp")

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("60-online-before-going-offline")
    }

    /**
     * Radios off: archive one conversation, star another, and send a
     * message with an attachment. All three land in the list and in the
     * outbox at once (REQ-AND-SYNC-20/21).
     */
    @Test
    fun t61_queueThreeThingsOffline() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        val stamp = markerFile().readText().trim()
        val toArchive = inboxEmail("outbox archive $stamp")
        val toStar = inboxEmail("outbox star $stamp")

        compose.onNodeWithTag("inbox-list")
            .performScrollToNode(hasTestTag("thread-row-${toArchive.threadId}"))
        compose.onNodeWithTag("thread-menu-${toArchive.threadId}").performClick()
        compose.onNodeWithTag("action-archive-${toArchive.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { !storedEmail(toArchive).mailboxIds.contains(inboxId(toArchive)) }
        }

        compose.onNodeWithTag("inbox-list")
            .performScrollToNode(hasTestTag("thread-row-${toStar.threadId}"))
        compose.onNodeWithTag("thread-star-${toStar.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) { runBlocking { storedEmail(toStar).isFlagged } }

        compose.captureScreen("61-offline-list-reflects-all-three")

        // A send with an attachment: the picked file is copied into app
        // storage, so the queued entry still has it when the drain runs.
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput("outbox send $stamp")
        attachFile("offline.txt", "text/plain", ATTACHMENT_BYTES)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-pending-offline.txt").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        compose.waitUntil(TIMEOUT_MS) { runBlocking { app.container.outbox.list().size >= 3 } }
        val queued = app.container.outbox.list()
        assertEquals("three things are waiting", 3, queued.size)
        assertTrue(
            "the send is one of them, saw ${queued.map { it.kind to it.label }}",
            queued.any { it.kind == OutboxKind.SEND },
        )
        assertTrue("nothing has left yet", queued.all { it.state != OutboxState.FAILED })

        compose.onNodeWithTag("connectivity-chip").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("outbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("62-offline-outbox-three-queued")
        compose.onNodeWithTag("outbox-back").performClick()
    }

    /**
     * Still offline, after `am kill`: the queue is in the database, so a
     * fresh process finds it exactly as it was (REQ-AND-SYNC-22).
     */
    @Test
    fun t62_theQueueSurvivesProcessDeath() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        val queued = app.container.outbox.list()
        assertEquals("the queue survived the process, saw $queued", 3, queued.size)

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-drawer-open").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-outbox").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-outbox").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("outbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("63-queue-after-process-death")
        compose.onNodeWithTag("outbox-back").performClick()
    }

    /**
     * Radios back on: the queue drains in order with no user action, and
     * herold holds the archive, the star and the sent message with its
     * attachment (REQ-AND-SYNC-22/23).
     */
    @Test
    fun t63_theQueueDrainsAndTheServerHoldsIt() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        val stamp = markerFile().readText().trim()

        compose.waitUntil(DRAIN_TIMEOUT_MS) { runBlocking { app.container.outbox.list().isEmpty() } }
        compose.captureScreen("64-outbox-drained")

        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val archiveId = client.mailboxGet(accountId).list.first { it.role == MailboxRoles.ARCHIVE }.id

        val archived = awaitServerEmail(stamp, "outbox archive $stamp")
        assertTrue(
            "the archive must be on the server, saw ${archived.mailboxIds}",
            archived.mailboxIds.contains(archiveId),
        )
        val starred = awaitServerEmail(stamp, "outbox star $stamp")
        assertTrue(
            "the star must be on the server, saw ${starred.keywords}",
            starred.keywords.any { it.equals(Keywords.FLAGGED, ignoreCase = true) },
        )

        val delivered = awaitDelivered("outbox send $stamp")
        val attachment = delivered.attachments.firstOrNull { it.name == "offline.txt" }
        assertNotNull("the message queued offline arrives with its attachment", attachment)
        assertEquals(ATTACHMENT_BYTES.size.toLong(), attachment!!.size)
    }

    /**
     * A send herold refuses outright: the entry stays listed with the
     * server's reason and a retry re-submits it (REQ-AND-SYNC-23/25).
     * The seeded `alice@foreign.example` identity has no submission
     * configuration, so `EmailSubmission/set` answers `forbiddenFrom`
     * (see #336).
     */
    @Test
    fun t64_aRefusedSendKeepsItsError() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        app.container.session.value!!.syncEngine.syncAll()
        val foreign = awaitIdentity(FOREIGN_IDENTITY)

        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-from").performClick()
        compose.onNodeWithTag("from-option-${foreign.accountId}-${foreign.id}", useUnmergedTree = true)
            .performClick()
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput("refused send ${System.currentTimeMillis()}")
        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        val failed = awaitEntry { it.state == OutboxState.FAILED }
        assertTrue("the entry keeps the server's reason, saw ${failed.lastError}", failed.permanent)
        assertNotNull("a refusal must say why", failed.lastError)

        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.onNodeWithTag("drawer-outbox").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("outbox-entry-${failed.id}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("65-refused-send-with-its-error")

        // The retry re-submits the same entry: the server refuses it
        // again, and the attempt count is what shows it went back out.
        compose.onNodeWithTag("outbox-retry-${failed.id}").performClick()
        val retried = awaitEntry {
            it.id == failed.id && it.state == OutboxState.FAILED && it.attempts > failed.attempts
        }
        assertTrue("the retry submitted again", retried.attempts > failed.attempts)
        compose.captureScreen("66-refused-send-after-retry")
    }

    // ---- helpers ------------------------------------------------------

    private suspend fun awaitInInbox(subjects: List<String>): List<Email> {
        repeat(DELIVERY_POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            val rows = app.container.store.inboxEmails().first().filter { it.subject in subjects }
            if (rows.size == subjects.size) return rows
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("the seeded messages $subjects never reached the inbox")
    }

    private suspend fun inboxEmail(subject: String): Email =
        app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
            ?: error("\"$subject\" is not in the local inbox")

    private suspend fun storedEmail(email: Email): Email =
        app.container.store.email(email.accountId, email.id)!!

    private suspend fun inboxId(email: Email): String =
        app.container.store.mailboxList()
            .first { it.accountId == email.accountId && it.role == MailboxRoles.INBOX }.id

    /** The sender's own copy of a message, read back from the server. */
    private suspend fun awaitServerEmail(stamp: String, subject: String): Email {
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        repeat(DELIVERY_POLLS) {
            val ids = client.emailQuery(
                accountId,
                buildJsonObject { put("text", stamp) },
                50,
                collapseThreads = false,
            )
            val found = client.emailGet(accountId, ids).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
            if (found != null) return found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("the server never showed \"$subject\"")
    }

    /** The message as the recipient's account received it. */
    private suspend fun awaitDelivered(subject: String): Email {
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        repeat(DELIVERY_POLLS) {
            val ids = recipient.emailQueryInbox(
                accountId,
                recipient.mailboxGet(accountId).list.first { it.role == MailboxRoles.INBOX }.id,
                20,
            )
            val found = recipient.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
            if (found != null) return found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no message with subject \"$subject\" reached ${DevInstance.recipientEmail}")
    }

    /** The identity the account advertises under [address]. */
    private suspend fun awaitIdentity(address: String): Identity {
        repeat(DELIVERY_POLLS) {
            val found = app.container.store.identities().first()
                .firstOrNull { it.email.equals(address, ignoreCase = true) }
            if (found != null) return found
            app.container.session.value!!.syncEngine.syncAll()
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("the dev instance must seed the $address identity (HEROLD_DEV_EXTERNAL_SUBMISSION=1)")
    }

    /** The first outbox entry matching [predicate], within the drain's time. */
    private suspend fun awaitEntry(predicate: (OutboxEntry) -> Boolean): OutboxEntry {
        repeat(DRAIN_POLLS) {
            app.container.outbox.list().firstOrNull(predicate)?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no outbox entry matched, saw ${app.container.outbox.list()}")
    }

    private fun attachFile(name: String, type: String, bytes: ByteArray) {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val file = File(context.cacheDir, name).apply { writeBytes(bytes) }
        Intents.intending(hasAction(Intent.ACTION_OPEN_DOCUMENT)).respondWith(
            Instrumentation.ActivityResult(
                Activity.RESULT_OK,
                Intent().setDataAndType(Uri.fromFile(file), type),
            ),
        )
        compose.onNodeWithTag("compose-attach").performClick()
    }

    /** The stamp of the run, so the phases agree on which mail they mean. */
    private fun markerFile(): File =
        File(
            InstrumentationRegistry.getInstrumentation().targetContext.filesDir,
            "outbox-acceptance-stamp",
        )

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DRAIN_TIMEOUT_MS = 120_000L
        const val DELIVERY_POLLS = 30
        const val DRAIN_POLLS = 60
        const val DELIVERY_POLL_MS = 1_000L

        /** The identity herold refuses a submission from (dev-instance seed). */
        const val FOREIGN_IDENTITY = "alice@foreign.example"

        val ATTACHMENT_BYTES: ByteArray = "queued while offline\n".toByteArray()
    }
}
