package com.netzhansa.herold.android

import android.app.Activity
import android.app.Instrumentation
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Shader
import android.net.Uri
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.espresso.intent.Intents
import androidx.test.espresso.intent.matcher.IntentMatchers.hasAction
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.android.media.ImageSize
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.ByteArrayOutputStream
import java.io.File

/**
 * Photos on a phone (issue #341): what a camera-sized image weighs when it
 * is attached, and what the reading pane does with one that arrives.
 *
 * The picker is stubbed the way the milestone 1c compose acceptance stubs
 * it, so the whole attach path runs without driving the SAF UI.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class AttachmentAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedIn() {
        grantNotificationPermission()
        Intents.init()
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

    @After
    fun releaseIntents() {
        Intents.release()
    }

    @Test
    fun t40_scalingBoundsAPhotoToTheChosenEdgeAndDecodesForTheScreen() {
        val photo = photoJpeg(4000, 3000)
        assertTrue("the fixture must be multi-megapixel, saw ${photo.size} bytes", photo.size > 1_000_000)
        assertEquals(4000 to 3000, ImageScaling.dimensions(photo))

        val large = ImageScaling.scale("camera.jpg", "image/jpeg", photo, ImageSize.LARGE)
        val (width, height) = ImageScaling.dimensions(large.bytes)!!
        assertEquals(2048, maxOf(width, height))
        assertTrue(
            "Large must weigh less than the original, saw ${large.bytes.size} of ${photo.size}",
            large.bytes.size < photo.size,
        )

        val small = ImageScaling.scale("camera.jpg", "image/jpeg", photo, ImageSize.SMALL)
        assertTrue("Small must weigh less than Large", small.bytes.size < large.bytes.size)
        assertEquals(
            "Original is sent as it was picked",
            photo.size,
            ImageScaling.scale("camera.jpg", "image/jpeg", photo, ImageSize.ORIGINAL).bytes.size,
        )

        // The reading pane's two decodes: a chip-sized thumbnail and the
        // bytes handed to the WebView, both bounded, neither the original.
        val thumbnail = ImageScaling.thumbnail(photo, 256)
        assertNotNull("an image attachment must produce a thumbnail", thumbnail)
        assertTrue("the thumbnail must be small, saw ${thumbnail!!.width}", thumbnail.width <= 512)
        val display = ImageScaling.forDisplay(photo, 1080)
        assertTrue("an inline image is decoded at display size", display.size < photo.size)
        assertEquals(1080, ImageScaling.dimensions(display)!!.first)
    }

    @Test
    fun t41_attachingAPhotoOffersTheSizeChoiceAndSendsTheScaledImage() = runBlocking {
        val parent = deliverAndReply("acceptance photo")
        val photo = photoJpeg(4000, 3000)

        stubPicker("camera.jpg", "image/jpeg", photo)
        compose.onNodeWithTag("compose-attach").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("image-size-dialog").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("image-size-dialog").assertIsDisplayed()
        compose.captureScreen("40-image-size-choice")

        // Large is the default and the remembered choice; taking the
        // dialog's confirm is what a user does.
        compose.onNodeWithTag("image-size-confirm").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-ready-camera.jpg").fetchSemanticsNodes().isNotEmpty()
        }

        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        val delivered = awaitDelivered(parent.subject)
        val attachment = delivered.attachments.firstOrNull { it.name == "camera.jpg" }
        assertNotNull("the photo must arrive with the reply", attachment)
        assertTrue(
            "the photo must be sent scaled, saw ${attachment!!.size} of ${photo.size} bytes",
            attachment.size < photo.size / 2,
        )
    }

    @Test
    fun t42_aReceivedPhotoRendersAsAThumbnailNotAFullResolutionImage() = runBlocking {
        val subject = "photo attachment " + System.currentTimeMillis()
        DevInstance.deliverMailWithImage(subject, photoJpeg(3000, 2000), "holiday.jpg", inline = false)
        val seeded = awaitInbox(subject)

        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${seeded.threadId}"))
        compose.onNodeWithTag("thread-row-${seeded.threadId}").performClick()
        // The chip merges its children's semantics, so the thumbnail is
        // only visible in the unmerged tree.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("attachment-thumbnail-holiday.jpg", useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("attachment-thumbnail-holiday.jpg", useUnmergedTree = true).assertIsDisplayed()
        compose.captureScreen("41-attachment-thumbnail")
    }

    // ---- helpers -------------------------------------------------------

    /**
     * A JPEG of [width] x [height] with a gradient in it, so it compresses
     * to the megabytes a camera photo weighs rather than to nothing.
     */
    private fun photoJpeg(width: Int, height: Int): ByteArray {
        val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        val canvas = Canvas(bitmap)
        val paint = Paint()
        paint.shader = LinearGradient(
            0f, 0f, width.toFloat(), height.toFloat(),
            intArrayOf(0xFF2244AA.toInt(), 0xFFEE7722.toInt(), 0xFF11CC66.toInt()),
            null,
            Shader.TileMode.CLAMP,
        )
        canvas.drawRect(0f, 0f, width.toFloat(), height.toFloat(), paint)
        // Detail the encoder cannot smooth away, so the file has real bulk.
        val speck = Paint()
        var seed = 1L
        repeat(width * height / 400) {
            seed = seed * 6364136223846793005L + 1442695040888963407L
            val x = ((seed ushr 16) % width).toInt().toFloat()
            val y = ((seed ushr 33) % height).toInt().toFloat()
            speck.color = (seed and 0xFFFFFF).toInt() or 0xFF000000.toInt()
            canvas.drawRect(x, y, x + 3f, y + 3f, speck)
        }
        val out = ByteArrayOutputStream()
        bitmap.compress(Bitmap.CompressFormat.JPEG, 92, out)
        bitmap.recycle()
        return out.toByteArray()
    }

    private fun stubPicker(name: String, type: String, bytes: ByteArray) {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val file = File(context.cacheDir, name).apply { writeBytes(bytes) }
        val result = Instrumentation.ActivityResult(
            Activity.RESULT_OK,
            Intent().setDataAndType(Uri.fromFile(file), type),
        )
        Intents.intending(hasAction(Intent.ACTION_OPEN_DOCUMENT)).respondWith(result)
    }

    /** Seeds a message, opens it and starts a reply on it. */
    private fun deliverAndReply(prefix: String): Email {
        val subject = "$prefix ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject = subject, body = "Please send me the photo.")
        val parent = awaitInbox(subject)
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${parent.threadId}"))
        compose.onNodeWithTag("thread-row-${parent.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-reply").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-reply").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
        return parent
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(30) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(500)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    /** The reply as the recipient's account received it. */
    private suspend fun awaitDelivered(parentSubject: String): Email {
        val expected = if (parentSubject.startsWith("Re: ")) parentSubject else "Re: $parentSubject"
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        repeat(DELIVERY_POLLS) {
            val ids = recipient.emailQueryInbox(
                accountId,
                recipient.mailboxGet(accountId).list.first { it.role == "inbox" }.id,
                20,
            )
            val found = recipient.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == expected }
            if (found != null) return found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no message with subject \"$expected\" reached ${DevInstance.recipientEmail}")
    }

    private companion object {
        const val TIMEOUT_MS = 60_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L
    }
}
