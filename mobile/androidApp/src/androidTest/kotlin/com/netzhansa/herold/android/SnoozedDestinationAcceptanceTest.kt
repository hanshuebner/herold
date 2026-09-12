package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.actions.SnoozeClock
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.datetime.Clock
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import kotlin.time.Duration.Companion.seconds

/**
 * The Snoozed destination (issue #353, suite REQ-SNZ-10/11/14): a snoozed
 * conversation leaves the inbox, is listed under Snoozed with the time it
 * comes back, and returns to the inbox when the server's snooze worker
 * releases it.
 *
 * Driven against an ephemeral herold whose snooze worker sweeps every five
 * seconds (`scripts/dev-instance.sh`), so the wake happens inside the run:
 *
 *   am instrument ... -e class com.netzhansa.herold.android.SnoozedDestinationAcceptanceTest \
 *     -e heroldBaseUrl ... -e heroldSmtpAddr ...
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class SnoozedDestinationAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun t50_snoozingFromTheListTakesTheThreadToTheSnoozedDestination() = runBlocking {
        signInAndSync()
        val target = deliverAndAwait("List snooze")

        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${target.threadId}"))
        compose.onNodeWithTag("thread-menu-${target.threadId}").performClick()
        compose.onNodeWithTag("action-snooze-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("snooze-TOMORROW_MORNING").performClick()

        // REQ-SNZ-10: off the inbox at once, not after the next sync.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isEmpty()
        }
        // And still off it once the server's view has been reconciled.
        syncNow()
        compose.waitForIdle()
        assertTrue(
            "the reconciled inbox must not list the snoozed conversation",
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isEmpty(),
        )

        openSnoozedDestination()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-wake-${target.threadId}", useUnmergedTree = true).assertIsDisplayed()
        compose.captureScreen("44-snoozed-destination")

        // The row opens the conversation, where the wake time is edited or
        // cancelled.
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("45-snoozed-thread-opened")
    }

    @Test
    fun t51_snoozingFromTheThreadViewAndWakingPutsTheThreadBackInTheInbox() = runBlocking {
        signInAndSync()
        val target = deliverAndAwait("Thread snooze")

        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${target.threadId}"))
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-snooze").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("snooze-LATER_TODAY").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isEmpty()
        }

        openSnoozedDestination()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }

        // The wake: the wake time is brought forward to a few seconds out,
        // which the instance's five-second sweep releases (REQ-SNZ-11).
        val server = DevInstance.serverClient()
        val accountId = server.session().mailAccountId!!
        app.container.session.value!!.client.emailSet(
            accountId,
            mapOf(
                target.id to buildJsonObject {
                    put("snoozedUntil", SnoozeClock.wireValue(Clock.System.now().plus(5.seconds)))
                },
            ),
        )
        awaitServerAwake(server, accountId, target.id)

        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.onNodeWithTag("drawer-inbox").performClick()
        compose.waitUntil(WAKE_TIMEOUT_MS) {
            syncNow()
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        compose.captureScreen("46-woken-back-into-the-inbox")
    }

    // ---- helpers -------------------------------------------------------

    private fun signInAndSync() = runBlocking {
        if (app.container.session.value == null) {
            val result = app.container.signIn(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
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
            body = "Snoozed, listed under Snoozed, and back again.",
        )
        return runBlocking {
            repeat(40) {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                    ?.let { return@runBlocking it }
                Thread.sleep(500)
            }
            error("the seeded message \"$subject\" never reached the inbox")
        }
    }

    private fun openSnoozedDestination() {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-snoozed").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-snoozed").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snoozed-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** Waits until the snooze worker has released the message. */
    private suspend fun awaitServerAwake(client: JmapClient, accountId: String, id: String) {
        val deadline = System.currentTimeMillis() + WAKE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (DevInstance.serverEmail(client, accountId, id)?.snoozedUntil == null) return
            Thread.sleep(POLL_MS)
        }
        error("the snooze worker never released $id")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val WAKE_TIMEOUT_MS = 60_000L
        const val POLL_MS = 1_000L
    }
}
