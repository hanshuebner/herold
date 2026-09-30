package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation.ActivityResult
import android.content.Intent
import android.net.Uri
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTouchInput
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
import java.io.File

/**
 * Getting a received attachment out of the app (issue #500,
 * REQ-AND-SYS-31/36). A PDF is a part the reading pane cannot render
 * itself, so the two ways out - another app, and a file the user keeps -
 * are the whole of what the row offers.
 *
 * Each check seeds its own message and reads only that one, so the class
 * needs no particular suite position and leaves no account-wide state
 * behind (issue #414). The outgoing intents are stubbed, so the
 * assertions read what the app asked the system for without a viewer
 * taking the foreground.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class AttachmentOpenAcceptanceTest {

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
    fun t10_tappingAPdfRowHandsTheDownloadedFileToAViewerApp() {
        intending(allOf(not(isInternal()), hasAction(Intent.ACTION_VIEW)))
            .respondWith(ActivityResult(Activity.RESULT_OK, null))

        val pdf = pdfBytes()
        val message = seed(pdf)
        openAttachments(message)

        compose.onNodeWithTag("attachment-$PDF_NAME").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            Intents.getIntents().any { it.action == Intent.ACTION_VIEW }
        }
        compose.captureScreen("500-attachment-open-with")

        intended(
            allOf(
                hasAction(Intent.ACTION_VIEW),
                hasType(PDF_TYPE),
                hasDataString(startsWith("content://$authority/")),
            ),
        )

        // The URI the viewer was handed carries the bytes the sender
        // attached, which the bearer-authenticated blob download is the
        // only way to have obtained.
        val handedOff = Intents.getIntents().last { it.action == Intent.ACTION_VIEW }
        assertTrue(
            "the read grant must travel with the intent",
            handedOff.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0,
        )
        val served = instrumentation.targetContext.contentResolver
            .openInputStream(handedOff.data!!)!!.use { it.readBytes() }
        assertArrayEquals("the viewer was handed different bytes", pdf, served)
    }

    @Test
    fun t20_theSaveActionWritesTheFileToThePickedDocument() {
        val target = File(instrumentation.targetContext.cacheDir, "saved-$PDF_NAME").apply { delete() }
        intending(hasAction(Intent.ACTION_CREATE_DOCUMENT)).respondWith(
            ActivityResult(Activity.RESULT_OK, Intent().setData(Uri.fromFile(target))),
        )

        val pdf = pdfBytes()
        val message = seed(pdf)
        openAttachments(message)

        compose.onNodeWithTag("attachment-$PDF_NAME").performTouchInput { longClick() }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-save-$PDF_NAME").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("attachment-save-$PDF_NAME").performClick()

        compose.waitUntil(TIMEOUT_MS) { target.isFile && target.length() == pdf.size.toLong() }
        compose.captureScreen("500-attachment-saved")

        intended(allOf(hasAction(Intent.ACTION_CREATE_DOCUMENT), hasType(PDF_TYPE)))
        assertArrayEquals("the saved document differs from the attachment", pdf, target.readBytes())
    }

    // ---- helpers ---------------------------------------------------------

    /** A small but structurally real PDF, so a viewer app could open it. */
    private fun pdfBytes(): ByteArray {
        val objects = listOf(
            "1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
            "2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
            "3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] " +
                "/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n",
            "4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
            "5 0 obj\n<< /Length 62 >>\nstream\nBT /F1 18 Tf 20 100 Td " +
                "(herold acceptance) Tj ET\nendstream\nendobj\n",
        )
        val header = "%PDF-1.4\n"
        val offsets = mutableListOf<Int>()
        val body = buildString {
            append(header)
            objects.forEach {
                offsets += length
                append(it)
            }
        }
        val xrefAt = body.length
        val xref = buildString {
            append("xref\n0 ${objects.size + 1}\n0000000000 65535 f \n")
            offsets.forEach { append(it.toString().padStart(10, '0') + " 00000 n \n") }
            append("trailer\n<< /Size ${objects.size + 1} /Root 1 0 R >>\nstartxref\n$xrefAt\n%%EOF\n")
        }
        return (body + xref).encodeToByteArray()
    }

    private fun seed(pdf: ByteArray): Email {
        val subject = "pdf attachment " + System.currentTimeMillis()
        DevInstance.deliverMailWithAttachment(subject, pdf, PDF_NAME, PDF_TYPE)
        return awaitInbox(subject)
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

    /** Opens the seeded thread and waits for its attachment row. */
    private fun openAttachments(message: Email) {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            Espresso.pressBack()
            compose.waitForIdle()
        }
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${message.threadId}"))
        compose.onNodeWithTag("thread-row-${message.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-$PDF_NAME").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-messages")
            .performScrollToNode(hasTestTag("attachment-$PDF_NAME"))
    }

    private companion object {
        const val PDF_NAME = "report.pdf"
        const val PDF_TYPE = "application/pdf"
        const val TIMEOUT_MS = 60_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
    }
}
