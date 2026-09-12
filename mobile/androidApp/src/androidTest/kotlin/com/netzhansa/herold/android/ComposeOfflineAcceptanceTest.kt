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
 * What a send does with no connectivity in milestone 1c: it fails where
 * the user can see it and leaves the compose open. Queueing it is
 * milestone 2's durable outbox (REQ-AND-SYNC-21), so this check pins the
 * current contract rather than a silent drop.
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
    fun t33_sendingWithoutAConnectionSaysSoAndKeepsTheCompose() = runBlocking {
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
        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-subject").performTextInput("offline send")
        compose.onNodeWithTag("compose-send").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-snackbar").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithText("No connection - the message was not sent").assertIsDisplayed()
        compose.onNodeWithTag("compose-screen").assertIsDisplayed()
        compose.captureScreen("33-offline-send-refused")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
