package com.netzhansa.herold.android

import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.Rect
import android.os.SystemClock
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.ui.thread.MessageBodyWebView
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.ByteArrayOutputStream
import java.util.concurrent.CopyOnWriteArrayList

/**
 * Where the reading pane's body gets its images from while the message
 * is on screen (issue #440).
 *
 * The body surface's request interceptor lives as long as the WebView
 * does, while what it may serve changes under it: the reader asks for
 * the remote images, and the parts an inline `cid:` reference names
 * arrive after the body first rendered. Both cases are driven here on
 * the surface alone, with resolvers the check owns and no network, so
 * the assertions are about the surface and not about a server's image
 * proxy.
 *
 * What "the image rendered" reads as: the marker paragraph below the
 * image moves down the page by about the image's height. An image the
 * interceptor refused is an empty response, which lays out as nothing,
 * so the marker stays where it was.
 *
 * The class needs no account and no dev instance, and leaves nothing
 * behind, so it runs in any position of the suite and twice over
 * (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BodyImageSourceAcceptanceTest {

    @get:Rule
    val compose = createComposeRule()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    /** How tall the image is on screen at the document's scale of 1. */
    private val imageHeightPx: Int
        get() = (IMAGE_CSS_PX * instrumentation.targetContext.resources.displayMetrics.density).toInt()

    /**
     * Asking for the remote images loads them into the message being
     * read: the interceptor consults the resolver current at request
     * time, and the image takes its place in the document.
     */
    @Test
    fun t10_askingForRemoteImagesLoadsThemIntoTheOpenMessage() {
        val png = solidPng()
        val asked = CopyOnWriteArrayList<String>()
        val showing = mutableStateOf(false)
        val serve: (String) -> Pair<String, ByteArray>? = { url ->
            asked += url
            "image/png" to png
        }
        val refuse: (String) -> Pair<String, ByteArray>? = { _ -> null }

        compose.setContent {
            val show = showing.value
            val body = HtmlSanitizer.sanitize(
                REMOTE_BODY,
                loadRemoteImages = show,
                fitToWidthCssPx = CONTENT_CSS_PX,
            )
            MessageBodyWebView(
                html = HtmlSanitizer.document(body.html, darkTheme = false, contentWidthCssPx = CONTENT_CSS_PX),
                imageSources = show,
                resolveInlineImage = refuse,
                resolveRemoteImage = if (show) serve else refuse,
                onLink = {},
                modifier = Modifier.fillMaxSize().testTag(BODY_TAG),
            )
        }

        val before = awaitMarker()
        Log.i(TAG, "remote blocked: marker at $before, ${asked.size} resolver calls")
        compose.captureScreen("m4-body-images-blocked")
        assertTrue("the blocked body already fetched an image: $asked", asked.isEmpty())

        compose.runOnUiThread { showing.value = true }
        compose.waitForIdle()

        awaitRequest(asked)
        assertEquals(
            "the interceptor served the request from the resolver of the first composition",
            listOf(IMAGE_URL),
            asked.toList(),
        )

        val after = awaitMarkerBelow(before + imageHeightPx / 2)
        Log.i(TAG, "remote shown: marker at $after, image ${imageHeightPx}px tall")
        compose.captureScreen("m4-body-images-shown")
        assertTrue(
            "the image did not take its place in the document: marker at $before, then $after, " +
                "for an image ${imageHeightPx}px tall",
            after >= before + imageHeightPx / 2,
        )
    }

    /**
     * An inline part that reaches the store after the body first
     * rendered still shows: the document is loaded again once the
     * resolver can produce it, and the interceptor serves it
     * (issue #440).
     */
    @Test
    fun t20_anInlinePartThatArrivesLateStillShows() {
        val png = solidPng()
        val served = CopyOnWriteArrayList<String>()
        val missed = CopyOnWriteArrayList<String>()
        val arrived = mutableStateOf(false)
        val serve: (String) -> Pair<String, ByteArray>? = { cid ->
            served += cid
            "image/png" to png
        }
        val absent: (String) -> Pair<String, ByteArray>? = { cid ->
            missed += cid
            null
        }
        val html = HtmlSanitizer.document(
            HtmlSanitizer.sanitize(INLINE_BODY, fitToWidthCssPx = CONTENT_CSS_PX).html,
            darkTheme = false,
            contentWidthCssPx = CONTENT_CSS_PX,
        )

        compose.setContent {
            val here = arrived.value
            MessageBodyWebView(
                html = html,
                imageSources = here,
                resolveInlineImage = if (here) serve else absent,
                resolveRemoteImage = { _ -> null },
                onLink = {},
                modifier = Modifier.fillMaxSize().testTag(BODY_TAG),
            )
        }

        val before = awaitMarker()
        awaitRequest(missed)
        assertEquals("the body asked for another part", listOf(INLINE_CID), missed.toList())
        Log.i(TAG, "inline missing: marker at $before")

        compose.runOnUiThread { arrived.value = true }
        compose.waitForIdle()

        awaitRequest(served)
        assertEquals(
            "the body asked the resolver of the first composition, which has no part to give",
            listOf(INLINE_CID),
            served.toList(),
        )
        val after = awaitMarkerBelow(before + imageHeightPx / 2)
        Log.i(TAG, "inline arrived: marker at $after, image ${imageHeightPx}px tall")
        compose.captureScreen("m4-body-inline-late")
        assertTrue(
            "the late inline part never reached the body: marker at $before, then $after, " +
                "for an image ${imageHeightPx}px tall",
            after >= before + imageHeightPx / 2,
        )
    }

    // ---- helpers ---------------------------------------------------------

    /**
     * Waits for the body to render and answers where the marker sits.
     *
     * The waits below poll from the test thread rather than through the
     * Compose rule, since what they read - the accessibility tree and
     * what the WebView's own thread recorded - is not the composition.
     */
    private fun awaitMarker(): Int = awaitMarkerBelow(1)

    /** Waits until the marker stands at or below [top], and answers where. */
    private fun awaitMarkerBelow(top: Int): Int {
        val deadline = SystemClock.uptimeMillis() + TIMEOUT_MS
        var seen = markerTop()
        while (seen < top && SystemClock.uptimeMillis() < deadline) {
            SystemClock.sleep(POLL_MS)
            seen = markerTop()
        }
        return seen
    }

    /** Waits for the body to have asked its resolver for an image. */
    private fun awaitRequest(asked: List<String>) {
        val deadline = SystemClock.uptimeMillis() + TIMEOUT_MS
        while (asked.isEmpty() && SystemClock.uptimeMillis() < deadline) {
            SystemClock.sleep(POLL_MS)
        }
        assertTrue("the body never asked a resolver for its image", asked.isNotEmpty())
    }

    /**
     * The top edge of the marker paragraph on screen, or 0 while the
     * body has not drawn it. The bounds come from the accessibility
     * tree, which is the handle a test has on a document rendered with
     * JavaScript off.
     */
    private fun markerTop(): Int {
        val root = instrumentation.uiAutomation.rootInActiveWindow ?: return 0
        val node = firstNode(root) { it.text?.contains(MARKER) == true } ?: return 0
        val bounds = Rect().also { node.getBoundsInScreen(it) }
        return bounds.top
    }

    private fun firstNode(
        node: AccessibilityNodeInfo,
        match: (AccessibilityNodeInfo) -> Boolean,
    ): AccessibilityNodeInfo? {
        if (match(node)) return node
        for (i in 0 until node.childCount) {
            val child = node.getChild(i) ?: continue
            firstNode(child, match)?.let { return it }
        }
        return null
    }

    /** An image of a known size, which is all the document needs of it. */
    private fun solidPng(): ByteArray {
        val bitmap = Bitmap.createBitmap(IMAGE_CSS_PX, IMAGE_CSS_PX, Bitmap.Config.ARGB_8888)
        bitmap.eraseColor(Color.rgb(0x1f, 0x6f, 0xeb))
        return ByteArrayOutputStream().also { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()
    }

    private companion object {
        const val TAG = "HeroldBodyImages"
        const val BODY_TAG = "message-body-under-test"

        const val MARKER = "Below the image."
        const val IMAGE_URL = "https://images.example.invalid/logo.png"
        const val INLINE_CID = "logo@example.invalid"

        /** How wide the card gives the body, and how big the image is. */
        const val CONTENT_CSS_PX = 336
        const val IMAGE_CSS_PX = 200

        val REMOTE_BODY = "<html><body><img src=\"$IMAGE_URL\"><p>$MARKER</p></body></html>"
        val INLINE_BODY = "<html><body><img src=\"cid:$INLINE_CID\"><p>$MARKER</p></body></html>"

        const val TIMEOUT_MS = 15_000L
        const val POLL_MS = 100L
    }
}
