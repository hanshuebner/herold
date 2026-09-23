package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.actions.SnoozeWakeMessages
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.datetime.Clock
import kotlinx.datetime.TimeZone
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import kotlin.time.Duration.Companion.seconds

/**
 * Why a conversation is back (issue #470, server issue #469, suite
 * REQ-SNZ-11): a reminder set a few seconds out falls due, the server's
 * snooze worker releases the message and stamps `snoozeWokeAt` /
 * `snoozeWokeFor` on it, and the client says so on the conversation's
 * inbox row and in the conversation itself until it is read.
 *
 * Driven against an ephemeral herold whose snooze worker sweeps every five
 * seconds (`scripts/dev-instance.sh`), so the wake happens inside the run:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.SnoozeWakeAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 */
@RunWith(AndroidJUnit4::class)
class SnoozeWakeAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val zone = TimeZone.currentSystemDefault()

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun aWokenConversationSaysWhyItIsBackUntilItIsRead() = runBlocking<Unit> {
        signInAndSync()
        val target = deliverAndAwait("Wake marker")

        // The reminder: a few seconds out, which the instance's five-second
        // sweep releases inside the run.
        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        val dueAt = Clock.System.now().plus(5.seconds)
        app.container.session.value!!.client.emailSet(
            accountId,
            mapOf(target.id to buildJsonObject { put("snoozedUntil", SnoozeClock.wireValue(dueAt)) }),
        )
        val wokeFor = awaitServerWake(server, accountId, target.id)

        // The row states the reminder that brought the conversation back.
        val marker = "thread-woke-${target.threadId}"
        compose.waitUntil(WAKE_TIMEOUT_MS) {
            syncNow()
            compose.listHoldsThread(target.threadId) &&
                compose.onAllNodesWithTag(marker, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
        }
        compose.scrollListToThread(target.threadId)
        compose.onNodeWithTag(marker, useUnmergedTree = true).assertIsDisplayed()
        compose.captureScreen("47-woken-row-marker")

        // And so does the conversation, in full.
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-woke").assertIsDisplayed()
        compose.onNodeWithTag("thread-woke-for", useUnmergedTree = true).assertTextContains(
            SnoozeWakeMessages.banner(SnoozeWakeMessages.label(wokeFor, Clock.System.now(), zone)),
            substring = true,
        )
        compose.captureScreen("48-woken-conversation-banner")

        // Opening the conversation reads it, which clears the marker on the
        // server; the fold carries that clearing back and the indication
        // ends, on the row and in the conversation.
        awaitServerMarkerCleared(server, accountId, target.id)
        compose.onNodeWithTag("thread-back").performClick()
        compose.waitUntil(WAKE_TIMEOUT_MS) {
            syncNow()
            compose.listHoldsThread(target.threadId) &&
                compose.onAllNodesWithTag(marker, useUnmergedTree = true).fetchSemanticsNodes().isEmpty()
        }
        assertNull(
            "the store must hold no wake marker once the server has cleared it",
            app.container.store.email(target.accountId, target.id)!!.snoozeWokeFor,
        )
        compose.captureScreen("49-woken-marker-cleared")

        compose.scrollListToThread(target.threadId)
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "a conversation that has been read must not say it came back from a reminder",
            compose.onAllNodesWithTag("thread-woke").fetchSemanticsNodes().isEmpty(),
        )
    }

    // ---- helpers -------------------------------------------------------

    private fun signInAndSync() = runBlocking {
        app.signInAsDevInstancePrincipal()
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun syncNow(): Boolean {
        runBlocking { app.container.session.value!!.syncEngine.syncAll() }
        compose.waitForIdle()
        return true
    }

    /** Delivers one message and returns it once it is in the local inbox. */
    private fun deliverAndAwait(label: String): Email {
        val subject = DevInstance.deliverMail(
            subject = "$label ${System.currentTimeMillis()}",
            body = "Snoozed for a few seconds, then back with a reason.",
        )
        return runBlocking {
            repeat(40) {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                    ?.let { return@runBlocking it }
                Thread.sleep(POLL_MS)
            }
            error("the seeded message \"$subject\" never reached the inbox")
        }
    }

    /**
     * Waits until the snooze worker has released the message, and answers
     * with the deadline the server recorded the wake for.
     */
    private suspend fun awaitServerWake(client: JmapClient, accountId: String, id: String): String {
        val deadline = System.currentTimeMillis() + WAKE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val email = DevInstance.serverEmail(client, accountId, id)
            if (email?.snoozedUntil == null) {
                email?.snoozeWokeFor?.let { wokeFor ->
                    check(SnoozeClock.parseWake(wokeFor) != null) { "unparseable snoozeWokeFor: $wokeFor" }
                    return wokeFor
                }
            }
            Thread.sleep(POLL_MS)
        }
        error("the snooze worker never released $id with a wake marker")
    }

    /** Waits until the read has taken the marker off the message. */
    private suspend fun awaitServerMarkerCleared(client: JmapClient, accountId: String, id: String) {
        val deadline = System.currentTimeMillis() + WAKE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (DevInstance.serverEmail(client, accountId, id)?.snoozeWokeFor == null) return
            Thread.sleep(POLL_MS)
        }
        error("reading $id never cleared its wake marker on the server")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val WAKE_TIMEOUT_MS = 60_000L
        const val POLL_MS = 500L
    }
}
