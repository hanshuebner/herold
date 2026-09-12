package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailboxRoles
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Undo after Send (issue #354): the message waits out its window on the
 * phone, the list offers to take it back, and taking it hands the
 * compose back with everything in it.
 *
 * Both checks run online against the dev instance, because what they are
 * about is whether the server ever heard about the message.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class UndoSendAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signIn(
                    DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t70_undoWithinTheWindowKeepsTheMessageOnThePhone() = runBlocking {
        chooseWindow(seconds = 30)
        val subject = "undo send ${System.currentTimeMillis()}"
        write(subject)
        compose.onNodeWithTag("compose-send").performClick()

        // The list raises the offer for as long as the window lasts.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Sending").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("70-sending-with-undo")
        compose.onNodeWithText("Undo").performClick()

        // The compose comes back as it was, and nothing is queued.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText(subject).assertIsDisplayed()
        compose.captureScreen("71-undo-reopened-the-composer")
        assertTrue(
            "the queued send is gone, saw ${app.container.outbox.list()}",
            app.container.outbox.list().none { it.isPending },
        )

        // And the server never heard of it.
        Thread.sleep(SETTLE_MS)
        assertNull("an undone send must not reach the recipient", delivered(subject))
    }

    @Test
    fun t71_withoutAnUndoTheMessageGoesWhenTheWindowIsUp() = runBlocking {
        chooseWindow(seconds = 5)
        val subject = "held send ${System.currentTimeMillis()}"
        write(subject)
        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("Sending").fetchSemanticsNodes().isNotEmpty()
        }

        val arrived = awaitDelivered(subject)
        assertEquals("the held message is the one that arrived", subject, arrived.subject)
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.outbox.list().none { it.isPending } }
        }
        compose.captureScreen("72-held-send-delivered")
    }

    /** Picks the undo window on the settings screen, as a user would. */
    private fun chooseWindow(seconds: Int) {
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-settings").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-settings").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("settings-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("undo-send-$seconds").performClick()
        compose.captureScreen("73-undo-send-setting")
        compose.onNodeWithTag("settings-back").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun write(subject: String) {
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput(subject)
    }

    private suspend fun delivered(subject: String): Email? {
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        val ids = recipient.emailQueryInbox(
            accountId,
            recipient.mailboxGet(accountId).list.first { it.role == MailboxRoles.INBOX }.id,
            20,
        )
        return recipient.emailGet(accountId, ids, withBody = true).list
            .map { it.toStoreRow(accountId) }
            .firstOrNull { it.subject == subject }
    }

    private suspend fun awaitDelivered(subject: String): Email {
        repeat(DELIVERY_POLLS) {
            delivered(subject)?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no message with subject \"$subject\" reached ${DevInstance.recipientEmail}")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val SETTLE_MS = 10_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L
    }
}
