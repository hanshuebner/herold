package com.netzhansa.herold.android

import android.os.ParcelFileDescriptor
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.platform.app.InstrumentationRegistry

/**
 * Touch input driven through the shell's `input` command, the same path an
 * `adb shell input swipe` takes.
 *
 * Compose's `performTouchInput { swipeRight() }` injects events straight
 * into the composition; a finger's events arrive through the input
 * dispatcher with real timing and velocity. Issue #338 was reported from a
 * device and passed the semantic swipe, so the gesture checks drive the
 * shell path.
 */
object Gestures {

    /** Swipes left-to-right across the middle of [bounds] in [durationMs]. */
    fun swipeAcross(bounds: Rect, durationMs: Int = 200) {
        val y = bounds.center.y.toInt()
        val from = (bounds.left + bounds.width * 0.1f).toInt()
        val to = (bounds.left + bounds.width * 0.95f).toInt()
        shell("input swipe $from $y $to $y $durationMs")
    }

    /** The same swipe, aimed at the node carrying [tag]. */
    fun swipeAcrossNode(rule: ComposeTestRule, tag: String, durationMs: Int = 200) {
        rule.waitForIdle()
        swipeAcross(rule.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow, durationMs)
    }

    /** Runs a shell command and waits for it to finish. */
    private fun shell(command: String) {
        val fd = InstrumentationRegistry.getInstrumentation().uiAutomation.executeShellCommand(command)
        ParcelFileDescriptor.AutoCloseInputStream(fd).use { stream ->
            while (stream.read() != -1) {
                // Draining the pipe is what makes the command run to completion.
            }
        }
    }
}
