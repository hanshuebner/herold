package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeRight
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The offline acceptance bullet of issue #327. The harness signs the app in
 * and syncs it while online, then turns the emulator's radios off and runs
 * this class: a synced thread still opens from the local store
 * (REQ-AND-SYNC-03), and an archive attempt reports no connectivity and
 * leaves the row where it was - milestone 1a queues nothing.
 */
@RunWith(AndroidJUnit4::class)
class OfflineAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Test
    fun aSyncedThreadOpensOfflineAndAnArchiveAttemptFailsVisibly() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        val rows = compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
        assertTrue(
            "the local store must already hold synced mail before the radios go off",
            rows.fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("09-offline-inbox-from-local-store")

        val before = app.container.store.inboxEmails().first()
        val target = before.maxByOrNull { it.receivedAt } ?: error("no synced message")

        // Reading works with no connectivity: the body came from the cache.
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("message-body-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("10-offline-thread-read")
        compose.onNodeWithTag("thread-back").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-swipe-${target.threadId}").performTouchInput { swipeRight() }

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-snackbar").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-snackbar").assertIsDisplayed()
        compose.captureScreen("11-offline-archive-refused")

        // The row is still there and the store still has it in the inbox.
        compose.onNodeWithTag("thread-row-${target.threadId}").assertIsDisplayed()
        val after = app.container.store.email(target.accountId, target.id)!!
        assertTrue(
            "a failed action must leave the local membership untouched",
            after.mailboxIds == target.mailboxIds,
        )
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
