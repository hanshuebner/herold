package com.netzhansa.herold.android

import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice

/**
 * Touch input driven through the input dispatcher, the path a finger's
 * events take.
 *
 * Compose's `performTouchInput { swipeRight() }` injects events straight
 * into the composition; a finger's events arrive through the dispatcher
 * with real timing and velocity. Issue #338 was reported from a device and
 * passed the semantic swipe, so the gesture checks drive the dispatcher.
 *
 * Each gesture is injected as a fixed number of pointer positions.
 * Injection waits for the app to finish handling every event, so a
 * gesture shaped by a wall-clock budget - what `adb shell input swipe`
 * does - spends its whole budget inside the first event on a loaded host
 * and delivers a bare down-up pair: the row under it reads that as a tap
 * and opens the conversation instead of archiving it (issue #379). A step
 * count leaves the path the pointer travels the same however slowly the
 * device runs; only the wall-clock time the gesture takes stretches.
 */
object Gestures {

    /** Swipes left-to-right across the middle of [bounds] in [steps] positions. */
    fun swipeAcross(bounds: Rect, steps: Int = SWIPE_STEPS) {
        val y = bounds.center.y.toInt()
        val from = (bounds.left + bounds.width * 0.1f).toInt()
        val to = (bounds.left + bounds.width * 0.95f).toInt()
        inject(from, y, to, y, steps)
    }

    /** The same swipe, aimed at the node carrying [tag]. */
    fun swipeAcrossNode(rule: ComposeTestRule, tag: String, steps: Int = SWIPE_STEPS) {
        rule.waitForIdle()
        swipeAcross(rule.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow, steps)
    }

    /**
     * The back a destination is popped on.
     *
     * The activity opts into `android:enableOnBackInvokedCallback`, so the
     * back key and the edge gesture arrive at the same
     * `OnBackInvokedCallback` and pop the same destination: a check whose
     * subject is what the popped-to screen holds is driven by either. The
     * key event carries no timing for a detector to read, where the edge
     * gesture is committed by the pointer's velocity across the threshold,
     * and a loaded host stretches a gesture past it (issue #393).
     */
    fun pressBack(rule: ComposeTestRule) {
        rule.waitForIdle()
        UiDevice.getInstance(InstrumentationRegistry.getInstrumentation()).pressBack()
    }

    /**
     * Drags the content of [bounds] upwards, the gesture that scrolls a
     * list down.
     *
     * A list keeps the place a finger put it and pins one nobody has
     * touched to its newest message (issue #439), so a check whose
     * subject is that place has to move the list the way a finger does:
     * a programmatic scroll is what the pin itself uses.
     */
    fun dragUp(bounds: Rect, steps: Int = SWIPE_STEPS) {
        val x = bounds.center.x.toInt()
        // The gesture stays in the upper part of the list: the search
        // field takes focus with the keyboard, which covers the lower
        // part of a list that reaches the bottom of the window, and a
        // drag that starts under the keyboard never reaches the list.
        val from = (bounds.top + bounds.height * 0.55f).toInt()
        val to = (bounds.top + bounds.height * 0.1f).toInt()
        inject(x, from, x, to, steps)
    }

    /**
     * Drags the content of [bounds] downwards from near its top, the
     * pull that asks the message list for a refresh (issue #444).
     */
    fun dragDown(bounds: Rect, steps: Int = SWIPE_STEPS) {
        val x = bounds.center.x.toInt()
        val from = (bounds.top + bounds.height * 0.15f).toInt()
        val to = (bounds.top + bounds.height * 0.7f).toInt()
        inject(x, from, x, to, steps)
    }

    /** Injects a down, [steps] - 1 moves along the line, and an up. */
    private fun inject(fromX: Int, fromY: Int, toX: Int, toY: Int, steps: Int) {
        val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())
        check(device.swipe(fromX, fromY, toX, toY, steps)) {
            "swipe ($fromX, $fromY) -> ($toX, $toY) was not injected"
        }
    }

    private const val SWIPE_STEPS = 40
}
