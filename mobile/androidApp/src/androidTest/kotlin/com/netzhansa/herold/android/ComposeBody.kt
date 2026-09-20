package com.netzhansa.herold.android

import android.os.SystemClock
import android.view.KeyEvent
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.click
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.test.platform.app.InstrumentationRegistry

/**
 * Typing into the composer's body, which is a WebView holding a
 * contenteditable rather than a Compose text field (issue #344).
 *
 * The tap lands in the editor's first line - the empty paragraph above
 * the quoted original - so the text goes where a reply is written. A key
 * event reaches the document only once the editable holds the caret,
 * which the tap grants asynchronously; keys sent before that are dropped
 * and the text loses its leading characters. The page reports its focus
 * and the editor is made to echo a keystroke, so the text goes in over a
 * path that has been seen to carry one.
 */
fun ComposeTestRule.typeInBody(text: String, timeoutMs: Long = BODY_TIMEOUT_MS) {
    // The editor's document loads asynchronously and publishes its
    // body's length once it is up.
    waitUntil(timeoutMs) { editorChars() >= 0 }
    val before = editorChars()
    onNodeWithTag("compose-body").performTouchInput { click(Offset(30f, 20f)) }
    waitUntil(timeoutMs) {
        onAllNodesWithTag("compose-body-focused").fetchSemanticsNodes().isNotEmpty()
    }
    awaitTypingLands(before, timeoutMs)
    InstrumentationRegistry.getInstrumentation().sendStringSync(text)
    waitUntil(timeoutMs) { editorChars() >= before + text.length }
}

/**
 * Types a probe character until the editor echoes it, then takes it
 * back, leaving the body as it was over an input path that has been
 * seen to carry a keystroke. The editable's focus flaps once as the
 * keyboard comes up, so the page's report of holding the caret does not
 * yet mean a key arrives; what the document echoes does (issue #344).
 */
private fun ComposeTestRule.awaitTypingLands(before: Int, timeoutMs: Long) {
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    val deadline = SystemClock.uptimeMillis() + timeoutMs
    while (editorChars() <= before) {
        if (SystemClock.uptimeMillis() > deadline) error("the editor took no keystroke")
        instrumentation.sendStringSync(BODY_PROBE)
        runCatching { waitUntil(BODY_ECHO_MS) { editorChars() > before } }
    }
    var chars = editorChars()
    while (chars > before) {
        if (SystemClock.uptimeMillis() > deadline) error("the probe character stayed in the editor")
        instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_DEL)
        runCatching { waitUntil(BODY_ECHO_MS) { editorChars() < chars } }
        chars = editorChars()
    }
}

/** How many characters the editor's body holds, or -1 before it loads. */
fun ComposeTestRule.editorChars(): Int =
    onAllNodes(hasTestTagStartingWith("compose-editor-"), useUnmergedTree = true)
        .fetchSemanticsNodes()
        .firstNotNullOfOrNull {
            it.config.getOrNull(SemanticsProperties.TestTag)
                ?.removePrefix("compose-editor-")?.toIntOrNull()
        } ?: -1

/** The character sent to see whether a keystroke reaches the document. */
private const val BODY_PROBE = "x"

/** How long one probe character is given to come back. */
private const val BODY_ECHO_MS = 2_000L

private const val BODY_TIMEOUT_MS = 30_000L
