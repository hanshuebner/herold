package com.netzhansa.herold.android

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeRight
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The offline acceptance bullet of issue #327, in two phases the harness
 * runs around an emulator connectivity toggle:
 *
 *   am instrument ... -e class OfflineAcceptanceTest#t1_warmTheCacheWhileOnline
 *   adb shell svc data disable && adb shell svc wifi disable
 *   am instrument ... -e class OfflineAcceptanceTest#t2_readOfflineAndQueueAnArchive
 *
 * Phase two asserts what the client promises with no connectivity: a
 * synced thread still opens from the local store (REQ-AND-SYNC-03), and
 * an archive applies at once and waits in the durable outbox for a
 * connection (REQ-AND-SYNC-20/22).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class OfflineAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun notificationsAllowed() {
        // The app asks for it contextually after the first sync; granted up
        // front the dialog never covers the screen these checks read.
        grantNotificationPermission()
    }

    @Test
    fun t1_warmTheCacheWhileOnline() = runBlocking {
        if (app.container.session.value == null) {
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        // The phase seeds the message it caches, so phase two reads a
        // thread this run put there rather than whatever was left behind.
        val subject = "offline read ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject, body = "Body to read with the radios off.")
        var seeded: Email? = null
        repeat(30) {
            app.container.session.value!!.syncEngine.syncAll()
            seeded = app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
            if (seeded != null) return@repeat
            Thread.sleep(500)
        }
        val target = seeded ?: error("the seeded message \"$subject\" never reached the inbox")

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(hasTestTagStartingWith("thread-row-"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${target.threadId}"))
        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(isRenderedMessageBody(), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-back").performClick()

        assertNotNull(
            "the opened message must be cached to read offline",
            app.container.store.email(target.accountId, target.id)?.let { it.bodyHtml ?: it.bodyText },
        )
    }

    @Test
    fun t2_readOfflineAndQueueAnArchive() = runBlocking {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("09-offline-inbox-from-local-store")

        val target = cachedMessage() ?: error("phase one must run online first")

        compose.onNodeWithTag("thread-row-${target.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodes(isRenderedMessageBody(), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("10-offline-thread-read")
        compose.onNodeWithTag("thread-back").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-row-${target.threadId}").fetchSemanticsNodes().isNotEmpty()
        }
        val inboxId = app.container.store.mailboxList()
            .first { it.accountId == target.accountId && it.role == "inbox" }.id
        compose.onNodeWithTag("thread-swipe-${target.threadId}").performTouchInput { swipeRight() }

        // The archive applies to the store at once and waits in the
        // outbox; the chip is what says so (REQ-AND-SYNC-20/30).
        compose.waitUntil(TIMEOUT_MS) {
            runBlocking { !app.container.store.email(target.accountId, target.id)!!.mailboxIds.contains(inboxId) }
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("connectivity-chip").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("connectivity-chip").assertIsDisplayed()
        compose.captureScreen("11-offline-archive-queued")

        val queued = app.container.outbox.list()
        assertTrue("the archive is durable, saw $queued", queued.isNotEmpty())
    }

    /** The newest inbox message whose body the store already holds. */
    private suspend fun cachedMessage(): Email? =
        app.container.store.inboxEmails().first()
            .filter { it.bodyHtml != null || it.bodyText != null }
            .maxByOrNull { it.receivedAt }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
