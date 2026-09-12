package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.mail.UnsubscribeClient
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * List-Unsubscribe end to end (issue #361, suite REQ-UNS-01..41). The
 * message is delivered over the dev instance's SMTP listener carrying the
 * headers a real list sender writes, and the one-click POST goes to an
 * HTTPS sink running in this process, which records the request the app
 * actually made.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class UnsubscribeAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    @Test
    fun t10_oneClickPostsTheRfc8058BodyWithNoCookieAndNoReferrer(): Unit = runBlocking {
        signInAndSync()
        UnsubscribeSink().use { sink ->
            val subject = "Newsletter ${System.currentTimeMillis()}"
            DevInstance.deliverMail(
                subject = subject,
                from = "Weekly List <list@vendor.example>",
                body = "This week's news.\r\n",
                headers = "List-Unsubscribe: <${sink.url}>\r\n" +
                    "List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n",
            )
            val message = awaitInbox(subject)
            assertNotNull("the header never reached the store", message.listUnsubscribe)

            openThread(message.threadId)
            compose.waitUntil(TIMEOUT_MS) {
                compose.onAllNodesWithTag("thread-unsubscribe").fetchSemanticsNodes().isNotEmpty()
            }
            compose.captureScreen("m3a-unsubscribe-button")
            compose.onNodeWithTag("thread-unsubscribe").performClick()

            val recorded = sink.awaitRequest(TIMEOUT_MS)
            assertNotNull("no request reached the unsubscribe endpoint", recorded)
            assertEquals("POST", recorded!!.method)
            assertEquals("/unsub", recorded.path)
            assertEquals(UnsubscribeClient.ONE_CLICK_BODY, recorded.body)
            assertEquals(
                "application/x-www-form-urlencoded",
                recorded.header("Content-Type")?.substringBefore(';')?.trim(),
            )
            assertNull("the POST carried a cookie", recorded.header("Cookie"))
            assertTrue(
                "the POST carried a referrer: ${recorded.header("Referer")}",
                recorded.header("Referer").isNullOrBlank(),
            )
            assertNull("the POST carried the bearer token", recorded.header("Authorization"))

            compose.waitUntil(TIMEOUT_MS) {
                compose.onAllNodesWithText("Unsubscribed from Weekly List").fetchSemanticsNodes().isNotEmpty()
            }
            compose.captureScreen("m3a-unsubscribe-done")
        }
    }

    @Test
    fun t20_aCleartextUrlIsRefusedRatherThanOpened(): Unit = runBlocking {
        signInAndSync()
        val subject = "Cleartext list ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Old List <old@vendor.example>",
            body = "Unsubscribe over plain http.\r\n",
            headers = "List-Unsubscribe: <http://vendor.example/unsub>\r\n",
        )
        val message = awaitInbox(subject)
        openThread(message.threadId)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-unsubscribe").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-unsubscribe").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText(
                "The sender's unsubscribe link is unencrypted; " +
                    "use the link in the message body if you trust it.",
            ).fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m3a-unsubscribe-cleartext")
    }

    @Test
    fun t30_aMailtoUrlOpensAPrefilledCompose(): Unit = runBlocking {
        signInAndSync()
        val subject = "Mailto list ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Mailto List <mailto-list@vendor.example>",
            body = "Unsubscribe by mail.\r\n",
            headers = "List-Unsubscribe: <mailto:leave@vendor.example?subject=unsubscribe%20me>\r\n",
        )
        val message = awaitInbox(subject)
        openThread(message.threadId)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-unsubscribe").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-unsubscribe").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("leave@vendor.example").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("m3a-unsubscribe-mailto")
        assertTrue(
            "the composer did not open on the unsubscribe address",
            compose.onAllNodesWithText("unsubscribe me").fetchSemanticsNodes().isNotEmpty(),
        )
    }

    // ---- helpers ---------------------------------------------------------

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
        if (app.container.session.value == null) {
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
        }
        app.container.session.value!!.syncEngine.syncAll()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    private fun openThread(threadId: String) {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
