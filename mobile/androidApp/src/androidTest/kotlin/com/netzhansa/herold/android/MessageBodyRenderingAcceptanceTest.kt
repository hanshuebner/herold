package com.netzhansa.herold.android

import android.graphics.Rect
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
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
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * How a message body renders in the conversation view: a desktop-width
 * document fits the card (issue #430) and the quoted history folds
 * behind a control the reader opens with one tap (issue #432).
 *
 * The measurements come from the WebView's accessibility tree, which is
 * the only handle a test has on a body rendered with JavaScript off: a
 * text node's bounds are in screen coordinates, so a run of text wider
 * than the card is what "clipped at the screen edge" reads as here.
 *
 * The class leaves the app on the inbox and writes no account state, so
 * it runs in any position of the suite and twice over (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class MessageBodyRenderingAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    /**
     * A desktop-width document is laid out to the card's width: the
     * right-hand column of a 700px table ends inside the body surface,
     * and the body surface itself ends inside the screen, so nothing of
     * the conversation runs off sideways (issue #430).
     */
    @Test
    fun t10_aDesktopWidthBodyFitsTheCard() {
        val message = seedHtmlMessage("Wide", WIDE_BODY)
        openThread(message.threadId)
        awaitBody(message, WIDE_MARKER)

        val body = bodySurface()
        val rightColumn = textNode(RIGHT_EDGE_MARKER)
        Log.i(TAG, "wide body=$body right-column=$rightColumn")
        compose.captureScreen("m4-body-wide")

        assertTrue(
            "the body surface runs past the screen: $body in ${device.displayWidth}px",
            body.right <= device.displayWidth,
        )
        // The accessibility bounds stop at the window, so a run of text
        // that ends exactly on the body surface's edge is one the reader
        // cannot see the end of.
        assertTrue(
            "the wide table's right column is cut off at the body's edge: " +
                "$rightColumn in $body",
            rightColumn.right < body.right,
        )
        backToInbox()
    }

    /** A narrow message renders as it always did, inside the card. */
    @Test
    fun t20_aNarrowBodyIsUnchanged() {
        val message = seedHtmlMessage("Narrow", NARROW_BODY)
        openThread(message.threadId)
        awaitBody(message, NARROW_MARKER)

        val body = bodySurface()
        val text = textNode(NARROW_MARKER)
        Log.i(TAG, "narrow body=$body text=$text")
        compose.captureScreen("m4-body-narrow")

        assertTrue(
            "the narrow body's text ends outside the body surface: " +
                "$text in $body",
            text.right <= body.right,
        )
        backToInbox()
    }

    /**
     * What no reflow can narrow scrolls sideways inside the body
     * surface, and the conversation around it stays where it is
     * (issue #430).
     */
    @Test
    fun t25_whatCannotReflowScrollsInsideTheCard() {
        val message = seedHtmlMessage("Nowrap", UNSHRINKABLE_BODY)
        openThread(message.threadId)
        awaitBody(message, UNSHRINKABLE_MARKER)

        val body = bodySurface()
        compose.captureScreen("m4-body-nowrap")
        val cardBefore = compose.onNodeWithTag("message-recipients-${message.id}")
            .fetchSemanticsNode().boundsInRoot.left
        val endBefore = textNode(ROW_END_MARKER)
        Log.i(TAG, "nowrap body=$body end-of-row=$endBefore")
        assertTrue(
            "the end of the row was on screen without scrolling: $endBefore",
            endBefore.width() == 0,
        )

        var endAfter = endBefore
        var swipes = 0
        while (endAfter.width() == 0 && swipes < MAX_SWIPES) {
            device.swipe(
                body.right - SWIPE_INSET,
                body.centerY(),
                body.left + SWIPE_INSET,
                body.centerY(),
                SWIPE_STEPS,
            )
            device.waitForIdle()
            endAfter = textNode(ROW_END_MARKER)
            swipes++
        }
        Log.i(TAG, "nowrap after $swipes swipes end-of-row=$endAfter")
        compose.captureScreen("m4-body-nowrap-scrolled")

        assertTrue(
            "the body did not scroll sideways onto what the card cut off " +
                "in $swipes swipes: $endBefore then $endAfter",
            endAfter.width() >= LEGIBLE_MIN_PX && endAfter.right <= body.right,
        )
        assertTrue(
            "the conversation scrolled sideways with the body",
            compose.onNodeWithTag("message-recipients-${message.id}")
                .fetchSemanticsNode().boundsInRoot.left == cardBefore,
        )
        backToInbox()
    }

    /**
     * A reply carrying an attribution line and the full quoted original
     * shows only the fresh text, behind a control that opens the history
     * on one tap (issue #432).
     */
    @Test
    fun t30_quotedHistoryFoldsAndOpensOnATap() {
        val message = seedHtmlMessage("Quoted", QUOTED_BODY)
        openThread(message.threadId)
        awaitBody(message, FRESH_MARKER)

        compose.captureScreen("m4-body-quote-folded")
        assertTrue(
            "the quoted history rendered unfolded",
            !device.hasObject(By.textContains(QUOTED_MARKER)),
        )
        assertTrue(
            "the attribution line rendered outside the fold",
            !device.hasObject(By.textContains(ATTRIBUTION_MARKER)),
        )
        val chip = device.wait(Until.findObject(By.textContains(SHOW_LABEL)), TIMEOUT_MS)
            ?: error("the body showed no control to open the quoted history")

        chip.click()
        assertTrue(
            "the quoted history did not open on a tap",
            device.wait(Until.hasObject(By.textContains(QUOTED_MARKER)), TIMEOUT_MS),
        )
        assertTrue(
            "the attribution line stayed hidden after the fold opened",
            device.hasObject(By.textContains(ATTRIBUTION_MARKER)),
        )
        compose.captureScreen("m4-body-quote-expanded")
        backToInbox()
    }

    /**
     * A Thunderbird reply keeps the text its sender wrote on screen:
     * the client puts that text and the attribution line in one
     * `moz-cite-prefix` div, and only the citation and the quoted
     * message belong behind the chip (issue #432).
     */
    @Test
    fun t35_aThunderbirdReplyKeepsItsOwnTextOutsideTheFold() {
        val message = seedHtmlMessage("Thunderbird", THUNDERBIRD_BODY)
        openSeededThread(message)
        awaitBody(message, TB_FRESH_MARKER)

        compose.captureScreen("m4-body-thunderbird-folded")
        assertTrue(
            "the paragraph above the reply folded away with the quote",
            device.hasObject(By.textContains(TB_LEAD_MARKER)),
        )
        assertTrue(
            "the reply's own text folded away with the quote",
            device.hasObject(By.textContains(TB_FRESH_MARKER)),
        )
        assertTrue(
            "the quoted message rendered unfolded",
            !device.hasObject(By.textContains(TB_QUOTED_MARKER)),
        )
        assertTrue(
            "the attribution line rendered outside the fold",
            !device.hasObject(By.textContains(TB_ATTRIBUTION_MARKER)),
        )

        val chip = device.wait(Until.findObject(By.textContains(SHOW_LABEL)), TIMEOUT_MS)
            ?: error("the body showed no control to open the quoted history")
        chip.click()
        assertTrue(
            "the quoted message did not open on a tap",
            device.wait(Until.hasObject(By.textContains(TB_QUOTED_MARKER)), TIMEOUT_MS),
        )
        compose.captureScreen("m4-body-thunderbird-expanded")
        backToInbox()
    }

    /**
     * A message that carries only a `text/plain` part reads as text:
     * its lines each stand on their own (issue #458). The server names
     * that one part in both body lists (RFC 8621 4.1.4), and read as
     * HTML its newlines are whitespace, which runs the whole message
     * into one block.
     */
    @Test
    fun t40_aTextOnlyBodyKeepsItsLineBreaks() {
        val message = seedPlainMessage("Plain", PLAIN_BODY)
        openSeededThread(message)
        awaitBody(message, PLAIN_FIRST_MARKER)

        val stored = runBlocking { app.container.store.email(message.accountId, message.id) }
        val body = bodySurface()
        val lines = textLineCount(deviceScreen(), body)
        Log.i(TAG, "plain body=$body lines=$lines")
        compose.captureScreen("m4-body-plain-text")

        assertNull(
            "the text-only message was cached with an HTML body: ${stored?.bodyHtml}",
            stored?.bodyHtml,
        )
        assertTrue(
            "the message's last line never rendered",
            device.hasObject(By.textContains(PLAIN_LAST_MARKER)),
        )
        assertTrue(
            "the body drew $lines lines where the message has $PLAIN_LINES, so its " +
                "line breaks ran together",
            lines >= PLAIN_MIN_LINES,
        )
        backToInbox()
    }

    // ---- helpers ---------------------------------------------------------

    /** Delivers one text/plain message and waits for the store to hold it. */
    private fun seedPlainMessage(tag: String, body: String): Email {
        signInAndSync()
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            headers = "MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n",
            body = body,
        )
        return awaitInbox(subject)
    }

    /** Delivers one HTML message and waits for the store to hold it. */
    private fun seedHtmlMessage(tag: String, body: String): Email {
        signInAndSync()
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            headers = "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n",
            body = body,
        )
        return awaitInbox(subject)
    }

    /**
     * The body surface's place on the screen. The measurements come
     * from the accessibility node rather than from UiAutomator's
     * visible bounds, which are clipped to the window and so report a
     * run of text that leaves the screen as ending at its edge.
     */
    private fun bodySurface(): Rect =
        boundsOf("the conversation showed no message body") {
            it.className == "android.webkit.WebView"
        }

    /** Where a run of the body's own text was laid out. */
    private fun textNode(text: String): Rect =
        boundsOf("the message body showed no \"$text\"") {
            it.text?.contains(text) == true || it.contentDescription?.contains(text) == true
        }

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

    /** Waits for the body surface and for the WebView to have drawn it. */
    private fun awaitBody(message: Email, marker: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "the message body never rendered",
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

    /**
     * Opens the conversation a seeded message landed in, picked by its
     * own subject.
     *
     * A row's tag carries the thread's id alone, and the instance
     * seeds a second account whose threads are numbered from the same
     * start, so the inbox can hold two rows under one tag. The subject
     * the seed just wrote is what tells them apart.
     */
    private fun openSeededThread(message: Email) {
        backToInbox()
        val row = hasTestTag("thread-row-${message.threadId}") and
            hasText(message.subject.orEmpty(), substring = true)
        compose.onNodeWithTag("inbox-list").performScrollToNode(row)
        compose.onAllNodes(row)[0].performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-messages").fetchSemanticsNodes().isNotEmpty()
        }
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
        const val TAG = "HeroldBodyRendering"

        const val WIDE_MARKER = "A desktop-width newsletter."
        const val RIGHT_EDGE_MARKER = "Right column of the layout table"
        const val NARROW_MARKER = "A short note that always fitted."
        const val UNSHRINKABLE_MARKER = "A row that cannot be narrowed."
        const val NOWRAP_MARKER = "Unbreakable row:"
        const val ROW_END_MARKER = "the end of the row"
        const val FRESH_MARKER = "Fresh text of the reply."
        const val ATTRIBUTION_MARKER = "Alice Example wrote:"
        const val QUOTED_MARKER = "The original message being answered"
        const val SHOW_LABEL = "Show trimmed content"

        /** A mail authored for a desktop reading pane (issue #430). */
        val WIDE_BODY = "<html><body>" +
            "<div style=\"width:700px\">" +
            "<p>$WIDE_MARKER</p>" +
            "<table width=\"700\" style=\"width:700px\" cellpadding=\"8\">" +
            "<tr>" +
            "<td width=\"350\" style=\"width:350px\">Left column of the layout table, " +
            "as a desktop template writes it.</td>" +
            "<td width=\"350\" style=\"width:350px\">$RIGHT_EDGE_MARKER, which is the " +
            "part that runs off the screen.</td>" +
            "</tr></table></div></body></html>\r\n"

        /**
         * A document no reflow can narrow: the cell refuses to wrap, so
         * its width is its content's width whatever the card allows.
         */
        val UNSHRINKABLE_BODY = "<html><body>" +
            "<p>$UNSHRINKABLE_MARKER</p>" +
            "<table><tr style=\"white-space:nowrap\">" +
            "<td>$NOWRAP_MARKER</td>" +
            (1..40).joinToString("") { "<td>$it</td>" } +
            "<td>$ROW_END_MARKER</td>" +
            "</tr></table></body></html>\r\n"

        val NARROW_BODY = "<html><body><p>$NARROW_MARKER</p></body></html>\r\n"

        /** A top-posted reply, the shape every client writes (issue #432). */
        val QUOTED_BODY = "<html><body>" +
            "<p>$FRESH_MARKER</p>" +
            "<p>On Mon, 15 Sep 2026, $ATTRIBUTION_MARKER</p>" +
            "<blockquote><p>$QUOTED_MARKER, which the reader should not have " +
            "to scroll through.</p></blockquote>" +
            "</body></html>\r\n"

        const val TB_LEAD_MARKER = "A line above the reply."
        const val TB_FRESH_MARKER = "Das passt mir gut."
        const val TB_ATTRIBUTION_MARKER = "um 14:12 schrieb"
        const val TB_QUOTED_MARKER = "The original Thunderbird message"

        /**
         * A Thunderbird reply composed above the citation: the reply
         * text and the attribution line share the `moz-cite-prefix`
         * div, and the attribution is written over a text node, a
         * `mailto:` link and the colon after it (issue #432).
         */
        val THUNDERBIRD_BODY = "<html><body>" +
            "<p>$TB_LEAD_MARKER</p>" +
            "<div class=\"moz-cite-prefix\">Hallo Bob,<br><br>$TB_FRESH_MARKER<br><br>" +
            "Am 20.09.26 $TB_ATTRIBUTION_MARKER " +
            "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:bob@example.local\">" +
            "bob@example.local</a>:<br></div>" +
            "<blockquote type=\"cite\" cite=\"mid:abc@example.local\">" +
            "<p>$TB_QUOTED_MARKER, which the reader should not have to scroll " +
            "through.</p></blockquote>" +
            "</body></html>\r\n"

        const val PLAIN_FIRST_MARKER = "Hello Bob,"
        const val PLAIN_LAST_MARKER = "Anna Example"

        /** A plain-text mail written one short line at a time. */
        val PLAIN_BODY = (
            listOf(
                PLAIN_FIRST_MARKER,
                "",
                "Item one.",
                "Item two.",
                "Item three.",
                "Item four.",
                "Item five.",
                "Item six.",
                "Item seven.",
                "Item eight.",
                "",
                "Regards",
                PLAIN_LAST_MARKER,
            ).joinToString("\r\n")
            ) + "\r\n"

        /** How many lines of text [PLAIN_BODY] puts on screen. */
        const val PLAIN_LINES = 11

        /**
         * How many the check insists on: every line of the message but
         * the last one or two, which a shorter screen cuts off. The
         * same words rendered as HTML fill the card's width instead and
         * come to four lines or so.
         */
        const val PLAIN_MIN_LINES = 10

        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60
        /**
         * How far inside the body surface a swipe starts and ends. The
         * system's back gesture owns a strip along each edge, and a
         * swipe that begins there leaves the conversation instead of
         * scrolling the body.
         */
        const val SWIPE_INSET = 200
        const val SWIPE_STEPS = 20

        /** How many swipes the check gives the row before it gives up. */
        const val MAX_SWIPES = 8

        /**
         * How much of the row's last cell has to stand in the card for
         * it to count as reached; a body that only gives up its own
         * padding leaves a sliver of it against the edge.
         */
        const val LEGIBLE_MIN_PX = 100

    }
}
