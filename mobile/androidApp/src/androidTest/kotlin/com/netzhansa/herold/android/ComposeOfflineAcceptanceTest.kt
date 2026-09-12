package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.runBlocking
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * What a send does with no connectivity: it goes into the durable
 * outbox, the compose closes, and the message leaves when there is a
 * connection (REQ-AND-SYNC-21).
 *
 * The harness runs it with the emulator's radios off, after
 * `SearchAcceptanceTest#t30...` has signed the app in.
 */
@RunWith(AndroidJUnit4::class)
class ComposeOfflineAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun notificationsAllowed() {
        grantNotificationPermission()
    }

    @Test
    fun t33_sendingWithoutAConnectionQueuesTheMessage() = runBlocking {
        // A cold start restores the stored token asynchronously; the check
        // needs the session the previous online phase left behind, not the
        // instant after launch.
        compose.waitUntil(TIMEOUT_MS) { app.container.session.value != null }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        val before = app.container.outbox.list().size
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput("offline send")
        compose.onNodeWithTag("compose-send").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { app.container.outbox.list().size > before }
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("connectivity-chip").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("connectivity-chip").assertIsDisplayed()
        compose.captureScreen("33-offline-send-queued")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
