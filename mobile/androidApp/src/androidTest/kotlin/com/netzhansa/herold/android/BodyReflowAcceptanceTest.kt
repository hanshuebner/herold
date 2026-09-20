package com.netzhansa.herold.android

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Rect
import android.util.Base64
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.ByteArrayOutputStream

/**
 * A `multipart/alternative` written for a desktop reading pane, read on
 * a phone (issue #430).
 *
 * The fixtures have the shape the message the maintainer handed back
 * has: a `text/plain` alternative carrying the whole message, and an
 * HTML branch inside `multipart/related` whose layout is a centred
 * fixed-width table inside a padded div, with a nested table and inline
 * images. One of them reflows to the card; the other declares content
 * that cannot narrow, and is read as its text alternative with the
 * sender's own layout one tap away.
 *
 * The measurements come from the accessibility tree, which is the only
 * handle a test has on a body rendered with JavaScript off.
 *
 * The class leaves the app on the inbox and writes no account state, so
 * it runs in any position of the suite and twice over (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BodyReflowAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    /**
     * The desktop newsletter reflows: its right-hand column ends inside
     * the body surface, the body surface ends inside the screen, and a
     * sideways swipe over the conversation moves nothing.
     */
    @Test
    fun t10_aFixedWidthDesktopLayoutFitsTheCard() {
        val message = seedAlternative("Newsletter", desktopHtml(), PLAIN_TEXT)
        openThread(message.threadId)
        awaitBody(message, HTML_MARKER)

        val body = bodySurface()
        val right = textNode(RIGHT_COLUMN_MARKER)
        Log.i(TAG, "newsletter body=$body right-column=$right screen=${device.displayWidth}")
        compose.captureScreen("m4-reflow-newsletter")

        assertTrue(
            "the body surface runs past the screen: $body in ${device.displayWidth}px",
            body.right <= device.displayWidth,
        )
        // The accessibility bounds stop at the window, so a run of text
        // that ends on the body's edge is one whose end is not on screen.
        assertTrue(
            "the 575px layout's right column is cut off at the body's edge: $right in $body",
            right.right < body.right,
        )

        val cardBefore = cardLeft(message)
        device.swipe(
            device.displayWidth - SWIPE_INSET,
            body.centerY(),
            SWIPE_INSET,
            body.centerY(),
            SWIPE_STEPS,
        )
        device.waitForIdle()
        assertTrue(
            "the conversation scrolled sideways",
            cardLeft(message) == cardBefore,
        )
        val rightAfter = textNode(RIGHT_COLUMN_MARKER)
        assertTrue(
            "the body scrolled sideways although it had reflowed: $right then $rightAfter",
            rightAfter == right,
        )
        backToInbox()
    }

    /**
     * An HTML branch that cannot narrow is read as the message's text
     * alternative, and the sender's layout is one tap away.
     */
    @Test
    fun t20_whatCannotReflowIsReadAsItsTextAlternative() {
        val message = seedAlternative("Unshrinkable", unshrinkableHtml(), PLAIN_TEXT)
        openThread(message.threadId)
        awaitBody(message, PLAIN_MARKER)

        compose.captureScreen("m4-reflow-text-alternative")
        assertTrue(
            "the HTML branch rendered although it cannot fit the card",
            !device.hasObject(By.textContains(NOWRAP_MARKER)),
        )
        val text = textNode(PLAIN_MARKER)
        val body = bodySurface()
        Log.i(TAG, "text alternative body=$body text=$text")
        assertTrue("the text alternative runs past the body surface: $text in $body", text.right <= body.right)

        compose.onNodeWithTag("show-html-${message.id}").performClick()
        val cameBack = awaitText(NOWRAP_MARKER)
        compose.captureScreen("m4-reflow-original-html")
        Log.i(TAG, "after the tap body=${bodySurface()} marker=$cameBack")
        assertTrue("the sender's own layout did not come back on a tap", cameBack != null)
        assertTrue(
            "the text alternative was still on screen with the original",
            findText(PLAIN_MARKER) == null,
        )
        compose.onNodeWithTag("show-text-${message.id}").assertExists()
        backToInbox()
    }

    // ---- fixtures --------------------------------------------------------

    /**
     * The shape of the handed-back message: a `text/plain` alternative
     * and an HTML branch inside `multipart/related` with four inline
     * images.
     */
    private fun seedAlternative(tag: String, html: String, text: String): Email {
        signInAndSync()
        val subject = "$tag ${System.currentTimeMillis()}"
        val alternative = "herold-alt-" + System.nanoTime()
        val related = "herold-rel-" + System.nanoTime()
        val body = buildString {
            append("--$alternative\r\n")
            append("Content-Type: text/plain; charset=utf-8\r\n\r\n")
            append(text)
            append("\r\n--$alternative\r\n")
            append("Content-Type: multipart/related; boundary=\"$related\"\r\n\r\n")
            append("--$related\r\n")
            append("Content-Type: text/html; charset=utf-8\r\n\r\n")
            append(html)
            (1..INLINE_IMAGES).forEach { index ->
                append("\r\n--$related\r\n")
                append("Content-Type: image/png\r\n")
                append("Content-Transfer-Encoding: base64\r\n")
                append("Content-ID: <$cid$index>\r\n")
                append("Content-Disposition: inline; filename=banner$index.png\r\n\r\n")
                append(bannerPng(index))
            }
            append("\r\n--$related--\r\n")
            append("\r\n--$alternative--\r\n")
        }
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            headers = "MIME-Version: 1.0\r\n" +
                "Content-Type: multipart/alternative; boundary=\"$alternative\"\r\n",
            body = body,
        )
        return awaitInbox(subject)
    }

    /**
     * A centred table declared at 575px inside a div with 20px padding,
     * with a nested 555px table and desktop-sized inline images: the
     * layout of the message in issue #430.
     */
    private fun desktopHtml(): String = "<html><body>" +
        "<div style=\"padding:20px;background:#eeeeee\">" +
        "<table width=\"575\" align=\"center\" style=\"width:575px\" cellpadding=\"0\" cellspacing=\"0\">" +
        "<tr><td><img src=\"cid:${cid}1\" width=\"555\" height=\"40\" alt=\"banner\"></td></tr>" +
        "<tr><td><p>$HTML_MARKER</p></td></tr>" +
        "<tr><td>" +
        "<table width=\"555\" style=\"width:555px\" cellpadding=\"8\"><tr>" +
        "<td width=\"277\" style=\"width:277px\">Left column of the newsletter, as the " +
        "template writes it for a desktop reading pane." +
        "<img src=\"cid:${cid}2\" width=\"261\" height=\"40\" alt=\"left\"></td>" +
        "<td width=\"278\" style=\"width:278px\">$RIGHT_COLUMN_MARKER, which is the part " +
        "that ran off the screen." +
        "<img src=\"cid:${cid}3\" width=\"262\" height=\"40\" alt=\"right\"></td>" +
        "</tr></table></td></tr>" +
        "<tr><td><img src=\"cid:${cid}4\" width=\"555\" height=\"40\" alt=\"footer\">" +
        "<p>https://newsletter.example.com/a/very/long/tracking/url/that/has/no/spaces/in/it/at/all</p>" +
        "</td></tr>" +
        "</table></div></body></html>"

    /** The same layout, around a row that states outright that it does not wrap. */
    private fun unshrinkableHtml(): String = "<html><body>" +
        "<div style=\"padding:20px\">" +
        "<table width=\"575\" align=\"center\" style=\"width:575px\">" +
        "<tr><td><p>$NOWRAP_MARKER</p>" +
        "<table><tr style=\"white-space:nowrap\">" +
        (1..40).joinToString("") { "<td>column $it</td>" } +
        "</tr></table>" +
        "</td></tr></table></div></body></html>"

    /** A PNG as wide as the template's images, so the layout has real width to lose. */
    private fun bannerPng(index: Int): String {
        val bitmap = Bitmap.createBitmap(BANNER_WIDTH_PX, BANNER_HEIGHT_PX, Bitmap.Config.ARGB_8888)
        Canvas(bitmap).drawColor(BANNER_COLOURS[index % BANNER_COLOURS.size])
        val bytes = ByteArrayOutputStream().also { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        return Base64.encodeToString(bytes.toByteArray(), Base64.DEFAULT)
    }

    private val cid = "banner-" + System.nanoTime() + "@acceptance.test-"

    // ---- helpers ---------------------------------------------------------

    private fun bodySurface(): Rect =
        boundsOf("the conversation showed no message body") {
            it.className == "android.webkit.WebView"
        }

    private fun textNode(text: String): Rect =
        boundsOf("the message body showed no \"$text\"") {
            it.text?.contains(text) == true || it.contentDescription?.contains(text) == true
        }

    /**
     * Where a run of text is, or null when the body does not hold it.
     * The reading pane's own accessibility tree is read afresh each
     * time: UiAutomator's own text search does not see a document the
     * WebView has reloaded in place.
     */
    private fun findText(text: String): Rect? {
        val root = instrumentation.uiAutomation.rootInActiveWindow ?: return null
        val node = firstNode(root) {
            it.text?.contains(text) == true || it.contentDescription?.contains(text) == true
        } ?: return null
        return Rect().also { node.getBoundsInScreen(it) }
    }

    /** Waits for a run of text to reach the body, and reports where it is. */
    private fun awaitText(text: String): Rect? {
        repeat(POLL_ATTEMPTS) {
            findText(text)?.let { return it }
            compose.waitForIdle()
            Thread.sleep(POLL_MS)
        }
        return null
    }

    private fun cardLeft(message: Email): Float =
        compose.onNodeWithTag("message-recipients-${message.id}").fetchSemanticsNode().boundsInRoot.left

    private fun boundsOf(missing: String, match: (AccessibilityNodeInfo) -> Boolean): Rect {
        val root = instrumentation.uiAutomation.rootInActiveWindow ?: error(missing)
        val node = firstNode(root, match) ?: error(missing)
        return Rect().also { node.getBoundsInScreen(it) }
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

    private fun awaitBody(message: Email, marker: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the message body never rendered \"$marker\"",
            device.wait(Until.hasObject(By.textContains(marker)), TIMEOUT_MS),
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
        backToInbox()
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-$threadId"))
        compose.onNodeWithTag("thread-row-$threadId").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun backToInbox() {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
    }

    private companion object {
        const val TAG = "HeroldBodyReflow"

        const val HTML_MARKER = "The newsletter as the sender laid it out."
        const val RIGHT_COLUMN_MARKER = "Right column of the newsletter"
        const val NOWRAP_MARKER = "A layout that cannot be narrowed."
        const val PLAIN_MARKER = "The same newsletter as plain text."

        val PLAIN_TEXT = "$PLAIN_MARKER\r\n\r\n" +
            "Everything the sender wrote, in lines a phone can read without\r\n" +
            "being told how wide the screen has to be.\r\n"

        const val INLINE_IMAGES = 4
        const val BANNER_WIDTH_PX = 555
        const val BANNER_HEIGHT_PX = 40
        val BANNER_COLOURS = intArrayOf(Color.rgb(0x2f, 0x6f, 0xed), Color.rgb(0xed, 0x8b, 0x2f))

        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60

        /**
         * How far inside the screen a swipe starts and ends; the
         * system's back gesture owns a strip along each edge.
         */
        const val SWIPE_INSET = 200
        const val SWIPE_STEPS = 20
    }
}
