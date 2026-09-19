package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation.ActivityResult
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.Intents.intended
import androidx.test.espresso.intent.Intents.intending
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.espresso.intent.matcher.IntentMatchers.hasData
import androidx.test.espresso.intent.matcher.IntentMatchers.isInternal
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.hamcrest.CoreMatchers.allOf
import org.hamcrest.CoreMatchers.not
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Links in a message body (issue #425, REQ-AND-SYS-04). A seeded message
 * carries a plain https link, an https link with `target="_blank"` and a
 * `mailto:` link; each tap has to leave the reading pane standing and hand
 * the URL out of the WebView.
 *
 * The outgoing intents are stubbed, so the assertions read what the app
 * asked the system for without a browser taking the foreground.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class MessageLinkAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    @Before
    fun stubOutgoingIntents() {
        Intents.init()
        intending(not(isInternal())).respondWith(ActivityResult(Activity.RESULT_OK, null))
    }

    @After
    fun releaseIntents() {
        Intents.release()
    }

    @Test
    fun t10_anHttpsLinkOpensOutsideTheMessageAndLeavesItDisplayed() {
        val message = seedLinkMessage()
        openThread(message.threadId)
        awaitBody(message)
        tapLink(PLAIN_LINK_TEXT)

        compose.captureScreen("m4-link-https")
        intended(allOf(hasAction(android.content.Intent.ACTION_VIEW), hasData(PLAIN_URL)))
        assertBodyStillShown(message)
    }

    @Test
    fun t20_aTargetBlankLinkTakesTheSamePath() {
        val message = seedLinkMessage()
        openThread(message.threadId)
        awaitBody(message)
        tapLink(BLANK_LINK_TEXT)

        compose.captureScreen("m4-link-target-blank")
        intended(allOf(hasAction(android.content.Intent.ACTION_VIEW), hasData(BLANK_URL)))
        assertBodyStillShown(message)
    }

    @Test
    fun t30_aMailtoLinkOpensTheComposerOnAddressAndSubject() {
        val message = seedLinkMessage()
        openThread(message.threadId)
        awaitBody(message)
        tapLink(MAILTO_LINK_TEXT)

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithText("sales@vendor.example").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the composer did not carry the link's subject",
            compose.onAllNodesWithText("Your offer").fetchSemanticsNodes().isNotEmpty(),
        )
        compose.captureScreen("m4-link-mailto")
    }

    // ---- helpers ---------------------------------------------------------

    /** Delivers the fixture message and waits for it to reach the store. */
    private fun seedLinkMessage(): Email {
        signInAndSync()
        val subject = "Links ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Vendor <vendor@example.local>",
            headers = "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n",
            body = "<html><body>" +
                "<p>$BODY_MARKER</p>" +
                "<p><a href=\"$PLAIN_URL\">$PLAIN_LINK_TEXT</a></p>" +
                "<p><a href=\"$BLANK_URL\" target=\"_blank\">$BLANK_LINK_TEXT</a></p>" +
                "<p><a href=\"mailto:sales@vendor.example?subject=Your%20offer\">$MAILTO_LINK_TEXT</a></p>" +
                "</body></html>\r\n",
        )
        return awaitInbox(subject)
    }

    /**
     * Taps a link by its text in the WebView's accessibility tree, which
     * is the only handle a test has on the body's own content.
     */
    private fun tapLink(label: String) {
        val link = device.wait(Until.findObject(By.text(label)), TIMEOUT_MS)
            ?: error("the message body showed no \"$label\" link")
        link.click()
        device.waitForIdle()
    }

    /**
     * Waits for the body surface and for the WebView to have put its
     * content in the accessibility tree, which is where the taps land.
     */
    private fun awaitBody(message: Email) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the message body never rendered",
            device.wait(Until.hasObject(By.textContains(BODY_MARKER)), TIMEOUT_MS),
        )
    }

    private fun assertBodyStillShown(message: Email) {
        compose.waitForIdle()
        assertTrue(
            "the reading pane left the thread",
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty(),
        )
        assertTrue(
            "the message body went blank after the tap",
            device.wait(Until.hasObject(By.textContains(BODY_MARKER)), TIMEOUT_MS),
        )
    }

    private fun signInAndSync() = runBlocking {
        grantNotificationPermission()
        app.signInAsDevInstancePrincipal()
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
        const val PLAIN_URL = "https://vendor.example/offer"
        const val BLANK_URL = "https://vendor.example/new-window"
        const val PLAIN_LINK_TEXT = "Open the offer"
        const val BLANK_LINK_TEXT = "Open in a new window"
        const val MAILTO_LINK_TEXT = "Write to sales"
        const val BODY_MARKER = "This message carries links."
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
