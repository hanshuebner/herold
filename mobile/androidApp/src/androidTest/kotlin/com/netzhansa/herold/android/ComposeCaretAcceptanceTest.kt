package com.netzhansa.herold.android

import android.os.SystemClock
import android.util.Log
import android.view.KeyEvent
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The composer follows the caret (issue #453).
 *
 * The body is a WebView, so Compose's own bring-into-view never sees the
 * caret: a check that the line being typed is on screen has to read the
 * caret out of the page and compare it against the viewport the keyboard
 * leaves. Both directions matter - typing past the fold, and putting the
 * caret back into a paragraph that has scrolled off the top.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class ComposeCaretAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    @Before
    fun signedInWithTheComposerOpen() {
        grantNotificationPermission()
        runBlocking {
            app.container.signOut()
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl,
                DevInstance.email,
                DevInstance.password,
                null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-compose").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test
    fun t140_typingPastTheFoldKeepsTheCaretInView() {
        compose.typeInBody("Line 1")
        repeat(LINES) { line -> typeAnotherLine("Line ${line + 2}") }
        compose.captureScreen("140-caret-past-the-fold")

        val viewport = visibleBounds()
        val caret = compose.activity.bodyWebView().caretProbe()
        Log.i(TAG, "typed past the fold: $caret, viewport=$viewport, ime=${imeInset()}")
        assertTrue(
            "the caret's line must sit in the visible area, saw $caret against $viewport",
            caret.bottom <= viewport.second + SLACK_PX && caret.top >= viewport.first - SLACK_PX,
        )
    }

    @Test
    fun t141_movingTheCaretIntoAnEarlierParagraphBringsItIntoView() {
        compose.typeInBody("Line 1")
        repeat(LINES) { line -> typeAnotherLine("Line ${line + 2}") }

        // Put the composer where a reader who went looking at the end of
        // it leaves it, so the message's opening paragraphs are above the
        // visible area.
        compose.onNode(hasTestTagStartingWith("compose-editor-"), useUnmergedTree = true)
            .performScrollTo()
        compose.waitForIdle()

        // Walk the caret back up into the first paragraph, which is a
        // caret move with no edit behind it.
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        repeat(LINES) { instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_DPAD_UP) }
        compose.waitForIdle()
        SystemClock.sleep(SETTLE_MS)
        compose.captureScreen("141-caret-back-up")

        val viewport = visibleBounds()
        val caret = compose.activity.bodyWebView().caretProbe()
        Log.i(TAG, "caret walked back up: $caret, viewport=$viewport, ime=${imeInset()}")
        assertTrue(
            "the paragraph the caret moved into must be in the visible area, saw $caret against $viewport",
            caret.bottom <= viewport.second + SLACK_PX && caret.top >= viewport.first - SLACK_PX,
        )
    }

    /** Opens a new line and types into it, waiting for the editor to echo it. */
    private fun typeAnotherLine(text: String) {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val before = compose.editorChars()
        instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_ENTER)
        instrumentation.sendStringSync(text)
        compose.waitUntil(TIMEOUT_MS) { compose.editorChars() >= before + text.length }
    }

    /** The top and bottom of the composer's scrolling viewport, in window pixels. */
    private fun visibleBounds(): Pair<Float, Float> {
        compose.waitForIdle()
        val bounds = compose.onNodeWithTag("compose-screen").fetchSemanticsNode().boundsInWindow
        return bounds.top to bounds.bottom
    }

    /** How much of the window the keyboard takes, in pixels. */
    private fun imeInset(): Int {
        var inset = 0
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            val insets = ViewCompat.getRootWindowInsets(compose.activity.window.decorView)
            inset = insets?.getInsets(WindowInsetsCompat.Type.ime())?.bottom ?: 0
        }
        return inset
    }

    private companion object {
        /** Enough lines to run the document well past any viewport. */
        const val LINES = 40

        /** A line's worth of tolerance, for a rounded pixel mapping. */
        const val SLACK_PX = 8f

        const val SETTLE_MS = 1_000L
        const val TIMEOUT_MS = 30_000L
        const val TAG = "HeroldCaret"
    }
}
