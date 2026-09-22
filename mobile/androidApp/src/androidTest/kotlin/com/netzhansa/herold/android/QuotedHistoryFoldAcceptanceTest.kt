package com.netzhansa.herold.android

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
import com.netzhansa.herold.shared.mail.QuotedFoldShapes
import com.netzhansa.herold.shared.mail.QuotedFoldShapes.Fixture
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The quoted-history fold, read off the screen (issue #456).
 *
 * Seven shapes, each delivered four times - plain, with an unrelated
 * paragraph ahead of it, with one after it, and with one at either end -
 * are opened in the reader, and what the reader can see is measured in
 * the WebView's accessibility tree: text the fold hides is not in it,
 * text outside the fold is, and a tap on the chip brings the hidden text
 * into it. A check that reads a string the sanitiser returned cannot
 * fail the way the reported defects did, which is why this one reads the
 * rendering instead.
 *
 * The bodies are [QuotedFoldShapes], the same table the host-JVM check
 * measures and byte for byte the Suite's own end-to-end corpus
 * (`web/apps/suite/tests/e2e-live/quoted-history-fold.spec.ts`).
 *
 * The position permutation is the part that earns its keep: of the four
 * rewrites this logic has had, the one that hid a sender's reply on a
 * released build passes every shape in its plain form and fails three of
 * them the moment a paragraph stands above the quote.
 *
 * One method per shape, so a regression in one shape does not hide the
 * next, and each delivers only its own four messages. The class writes
 * no account state and ends on the inbox, so it runs in any position of
 * the suite and twice over (issue #414).
 *
 *     adb shell am instrument -w -r \
 *       -e class com.netzhansa.herold.android.QuotedHistoryFoldAcceptanceTest \
 *       -e heroldBaseUrl http://10.0.2.2:<backend-port> \
 *       -e heroldSmtpAddr 10.0.2.2:<smtp-port> \
 *       com.netzhansa.herold.android.test/androidx.test.runner.AndroidJUnitRunner
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class QuotedHistoryFoldAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    @Test
    fun t40_aThunderbirdReplyKeepsTheSendersOwnText() = checkShape(0)

    @Test
    fun t41_anUnrecognisedAttributionLeavesTheReplyVisible() = checkShape(1)

    @Test
    fun t42_aQuotedMessagesOwnReplyStaysBehindTheFold() = checkShape(2)

    @Test
    fun t43_aQuotedTopPostIsNotLiftedOut() = checkShape(3)

    @Test
    fun t44_aReplyUnderTheQuoteKeepsEverythingVisible() = checkShape(4)

    @Test
    fun t45_anAttributionShapedLineOfTheSendersStaysVisible() = checkShape(5)

    @Test
    fun t46_aParagraphBetweenTheCitationAndItsQuoteStaysFolded() = checkShape(6)

    /** One shape, in all four positions. */
    private fun checkShape(index: Int) {
        val shape = QuotedFoldShapes.shapes[index]
        signInAndSync()
        val delivered = QuotedFoldShapes.Wrap.entries.map { wrap ->
            val fixture = QuotedFoldShapes.fixture(shape, wrap)
            fixture to deliver(fixture)
        }
        delivered.forEach { (fixture, subject) ->
            val message = awaitInbox(subject)
            openSeededThread(message)
            assertFoldPlacement(fixture, message)
        }
        backToInbox()
    }

    /**
     * What the reader sees of one fixture: the fold is there when the
     * body owes one, the sender's own text is on screen, the quoted
     * message is not, and the tap brings it back.
     */
    private fun assertFoldPlacement(fixture: Fixture, message: Email) {
        val anchor = fixture.markers.first { !it.foldsAway }.text
        awaitBody(fixture, message, anchor)

        val folded = renderedText()
        Log.i(TAG, "${fixture.id} folds=${fixture.folds} rendered=$folded")
        val chip = device.hasObject(By.textContains(SHOW_LABEL))
        assertTrue(
            "${fixture.id}: the body ${if (fixture.folds) "showed no" else "showed a"} " +
                "control to open the quoted history",
            chip == fixture.folds,
        )

        fixture.markers.forEach { marker ->
            val hidden = fixture.folds && marker.foldsAway
            if (hidden) {
                assertTrue(
                    "${fixture.id}: the quoted \"${marker.text}\" rendered outside the " +
                        "fold, on screen: $folded",
                    !folded.contains(marker.text),
                )
            } else {
                assertTrue(
                    "${fixture.id}: \"${marker.text}\" is not on screen: $folded",
                    folded.contains(marker.text),
                )
            }
        }

        if (fixture.wrap == QuotedFoldShapes.Wrap.PLAIN) {
            compose.captureScreen("m4-fold-${fixture.id}")
        }
        if (!fixture.folds) return

        val hiddenMarkers = fixture.markers.filter { it.foldsAway }
        device.wait(Until.findObject(By.textContains(SHOW_LABEL)), TIMEOUT_MS)?.click()
        assertTrue(
            "${fixture.id}: the quoted history did not open on a tap",
            device.wait(Until.hasObject(By.textContains(hiddenMarkers.first().text)), TIMEOUT_MS),
        )
        val opened = renderedText()
        hiddenMarkers.forEach { marker ->
            assertTrue(
                "${fixture.id}: \"${marker.text}\" stayed hidden after the fold opened, " +
                    "on screen: $opened",
                opened.contains(marker.text),
            )
        }
        if (fixture.wrap == QuotedFoldShapes.Wrap.PLAIN) {
            compose.captureScreen("m4-fold-${fixture.id}-expanded")
        }
    }

    // ---- the device ------------------------------------------------------

    /**
     * Every run of text the body surface renders.
     *
     * The accessibility tree is what a test can read of a WebView whose
     * document runs with JavaScript off. A node that scrolled out of
     * view is still in it, with bounds outside the window, while text
     * inside a closed fold is not rendered and so has no node at all -
     * which is the distinction the checks above rest on.
     */
    private fun renderedText(): String {
        val root = instrumentation.uiAutomation.rootInActiveWindow
            ?: error("the conversation showed no window")
        val body = firstNode(root) { it.className == "android.webkit.WebView" }
            ?: error("the conversation showed no message body")
        val text = StringBuilder()
        collectText(body, text)
        return text.toString()
    }

    private fun collectText(node: AccessibilityNodeInfo, into: StringBuilder) {
        node.text?.let { into.append(it).append(" | ") }
        node.contentDescription?.let { into.append(it).append(" | ") }
        for (i in 0 until node.childCount) {
            node.getChild(i)?.let { collectText(it, into) }
        }
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

    /**
     * Waits for the body surface and for the WebView to have drawn it.
     *
     * The text waited on is the sender's own, so a rule that folds it
     * away fails here rather than further down, and the message names
     * the fixture: the position a shape fails in is the finding, and a
     * shape usually fails in one position and not the others.
     */
    private fun awaitBody(fixture: Fixture, message: Email, marker: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("message-body-${message.id}").fetchSemanticsNodes().isNotEmpty()
        }
        assertTrue(
            "${fixture.id}: the body never rendered the sender's \"$marker\"",
            device.wait(Until.hasObject(By.textContains(marker)), TIMEOUT_MS),
        )
    }

    // ---- the mail --------------------------------------------------------

    /**
     * Delivers one fixture's body as the message's only part.
     *
     * The subject carries a token minted once per run, and a message is
     * looked up by the whole subject: two fixtures of one shape differ
     * only in where their unrelated paragraphs stand, so a lookup that
     * matched loosely could answer one fixture's question with another
     * fixture's message and pass while measuring nothing.
     */
    private fun deliver(fixture: Fixture): String {
        val subject = "Fold corpus $RUN_TOKEN ${fixture.id} (re #456)"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            headers = "MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n",
            body = fixture.html,
        )
        return subject
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

    /** Opens the conversation a seeded message landed in, by its subject. */
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

    private fun backToInbox() {
        while (compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isEmpty()) {
            androidx.test.espresso.Espresso.pressBack()
            compose.waitForIdle()
        }
    }

    private companion object {
        const val TAG = "HeroldQuotedFold"
        const val SHOW_LABEL = "Show trimmed content"
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60

        /** Minted once per run, so no earlier run's mail answers a lookup. */
        val RUN_TOKEN: String = System.currentTimeMillis().toString(36)
    }
}
