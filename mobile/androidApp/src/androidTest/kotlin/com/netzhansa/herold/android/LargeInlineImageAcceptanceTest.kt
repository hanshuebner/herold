package com.netzhansa.herold.android

import android.graphics.Bitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.ByteArrayOutputStream

/**
 * A picture-heavy newsletter (issue #420): a message whose inline part
 * is larger than Android's 2 MiB cursor window opens, reads, and leaves
 * the app running. On the reporting device the same shape closed the
 * app as the thread was tapped, and again on the restart that restored
 * the route.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class LargeInlineImageAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            app.signInAsDevInstancePrincipal()
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t92_aThreadWhoseInlinePartOutgrowsTheCursorWindowOpens() {
        val photo = noiseJpeg(1800, 1400)
        assertTrue(
            "the fixture must outgrow the 2 MiB cursor window, saw ${photo.size} bytes",
            photo.size > 2 * 1024 * 1024,
        )
        val subject = "oversized inline image " + System.currentTimeMillis()
        DevInstance.deliverMailWithImage(subject, photo, "newsletter.jpg", inline = true)
        val seeded = awaitInbox(subject)

        compose.scrollListToThread(seeded.threadId)
        compose.onNodeWithTag("thread-row-${seeded.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-messages").assertIsDisplayed()

        // The thread opens on its newest message, and drawing that body
        // is what resolves the `cid:` reference through the blob cache.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${seeded.id}").fetchSemanticsNodes().isNotEmpty()
        }
        Thread.sleep(SETTLE_MS)
        compose.captureScreen("92-oversized-inline-image")

        // Still the thread, in the same process: the part was read
        // without taking the app with it.
        compose.onNodeWithTag("thread-messages").assertIsDisplayed()

        // And it came through the cache, which is what the row layout
        // could not do: the part is bigger than a cursor window.
        val cached = runBlocking {
            val part = awaitInlinePart(seeded.accountId, seeded.id)
            app.container.store.cachedBlob(seeded.accountId, part)
        }
        assertTrue(
            "the oversized part must come back out of the cache, saw ${cached?.bytes?.size}",
            cached != null && cached.bytes.size == photo.size,
        )
    }

    /**
     * The blob id of the message's inline part. The list row carries no
     * parts; they arrive with the body the thread screen loads.
     */
    private suspend fun awaitInlinePart(accountId: String, emailId: String): String {
        repeat(POLLS) {
            app.container.store.email(accountId, emailId)?.attachments?.firstOrNull()
                ?.let { return it.blobId }
            Thread.sleep(POLL_MS)
        }
        error("the message never gained its inline part in the store")
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    /**
     * A JPEG of random pixels, which the encoder cannot shrink: the
     * fixture has to weigh more than a cursor window holds.
     */
    private fun noiseJpeg(width: Int, height: Int): ByteArray {
        val pixels = IntArray(width * height)
        var state = 0x5eed
        for (index in pixels.indices) {
            state = state * 1103515245 + 12345
            pixels[index] = 0xFF000000.toInt() or (state ushr 8 and 0xFFFFFF)
        }
        val bitmap = Bitmap.createBitmap(pixels, width, height, Bitmap.Config.ARGB_8888)
        val out = ByteArrayOutputStream()
        bitmap.compress(Bitmap.CompressFormat.JPEG, 100, out)
        bitmap.recycle()
        return out.toByteArray()
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val SETTLE_MS = 2_000L
        const val POLLS = 30
        const val POLL_MS = 500L
    }
}
