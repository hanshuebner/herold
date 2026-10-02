package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation.ActivityResult
import android.content.Intent
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.espresso.Espresso
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.Intents.intended
import androidx.test.espresso.intent.Intents.intending
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.espresso.intent.matcher.IntentMatchers.hasDataString
import androidx.test.espresso.intent.matcher.IntentMatchers.hasType
import androidx.test.espresso.intent.matcher.IntentMatchers.isInternal
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.media.AttachmentFiles
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.hamcrest.CoreMatchers.allOf
import org.hamcrest.CoreMatchers.not
import org.hamcrest.CoreMatchers.startsWith
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * An attached file that carries a Content-ID of its own (issue #503).
 *
 * Gmail stamps one on every attachment, so the shape reaching the pane
 * is `Content-Disposition: attachment` next to a `Content-ID` the body
 * never names. Such a part belongs in the Attachments section, where the
 * hand-off of issue #500 reaches it; a part is the body's own only when
 * its disposition says so, or when the HTML draws it as `cid:`.
 *
 * The checks seed their own mail and read only that, so the class needs
 * no particular suite position (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class AttachmentCidAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val authority get() = AttachmentFiles.authority(instrumentation.targetContext)

    @Before
    fun signedIn() {
        grantNotificationPermission()
        Intents.init()
        runBlocking {
            app.signInAsDevInstancePrincipal()
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @After
    fun releaseIntents() {
        Intents.release()
    }

    @Test
    fun t10_anAttachedFileCarryingAContentIdIsListedAndOpens() {
        intending(allOf(not(isInternal()), hasAction(Intent.ACTION_VIEW)))
            .respondWith(ActivityResult(Activity.RESULT_OK, null))

        val pdf = acceptancePdf()
        val subject = "cid attachment " + System.currentTimeMillis()
        DevInstance.deliverMailWithCidAttachment(subject, pdf, PDF_NAME, PDF_TYPE)
        val message = awaitInbox(subject)

        openThread(message)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-$PDF_NAME").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-messages")
            .performScrollToNode(hasTestTag("attachment-$PDF_NAME"))
        compose.captureScreen("503-attachments")

        // The listed row is the one the hand-off of issue #500 acts on.
        compose.onNodeWithTag("attachment-$PDF_NAME").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            Intents.getIntents().any { it.action == Intent.ACTION_VIEW }
        }
        intended(
            allOf(
                hasAction(Intent.ACTION_VIEW),
                hasType(PDF_TYPE),
                hasDataString(startsWith("content://$authority/")),
            ),
        )
        val handedOff = Intents.getIntents().last { it.action == Intent.ACTION_VIEW }
        assertTrue(
            "the read grant must travel with the intent",
            handedOff.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0,
        )
        val served = instrumentation.targetContext.contentResolver
            .openInputStream(handedOff.data!!)!!.use { it.readBytes() }
        assertArrayEquals("the viewer was handed different bytes", pdf, served)
    }

    /**
     * The other half of the rule: a part the HTML draws as `cid:` stays
     * out of the Attachments section however its disposition reads, so a
     * body's own image is not listed under it a second time.
     */
    @Test
    fun t20_aPartTheBodyDrawsIsNotListedAsAnAttachment() {
        val subject = "cid referenced " + System.currentTimeMillis()
        DevInstance.deliverMailWithInlineImage(subject)
        val message = awaitInbox(subject)

        openThread(message)
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitForIdle()
        assertTrue(
            "a part the body draws must not be listed as an attachment",
            compose.onAllNodesWithTag("attachment-dot.png").fetchSemanticsNodes().isEmpty(),
        )
    }

    // ---- helpers ---------------------------------------------------------

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLL_ATTEMPTS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.emailList().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the store")
    }

    private fun openThread(message: Email) {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${message.threadId}"))
        compose.onNodeWithTag("thread-row-${message.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val PDF_NAME = "invoice.pdf"
        const val PDF_TYPE = "application/pdf"
        const val TIMEOUT_MS = 60_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
