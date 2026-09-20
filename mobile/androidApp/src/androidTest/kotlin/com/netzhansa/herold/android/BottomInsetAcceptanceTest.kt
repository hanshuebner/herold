package com.netzhansa.herold.android

import android.util.Log
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.hasTestTag
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * Nothing the user taps sits in the area the system keeps along the
 * bottom edge (issue #428). The shell draws edge to edge, so a row
 * pinned to the bottom of the window lands on the navigation bar - or,
 * on a gesture-navigated device, on the swipe-up handle - unless it
 * pads itself out of the inset the platform reports.
 *
 * The class reads the inset from the window it is running in, so the
 * same checks state the requirement under either navigation mode; run
 * it once with `cmd overlay enable com.android.internal.systemui.
 * navbar.gestural` and once with `...navbar.threebutton`.
 *
 * It leaves the app on the inbox and writes no account state, so it
 * runs in any position of the suite and twice over (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BottomInsetAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val app get() = instrumentation.targetContext.applicationContext as HeroldApplication

    /** The reply pills stand above the handle, and the bar behind them reaches the edge. */
    @Test
    fun t10_theReplyPillsStandAboveTheSystemsBottomArea() {
        val message = seedMessage("Bottom inset")
        openThread(message.threadId)

        report("thread")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-thread")
        assertAboveSystemArea("thread-reply")
        assertAboveSystemArea("thread-reply-all")
        assertAboveSystemArea("thread-forward")
        // The bar's own background carries on to the bottom of the
        // screen, so the handle sits on the bar's colour rather than on
        // the conversation scrolling underneath it.
        assertReachesTheBottomEdge("thread-reply-surface")
        // And it takes no more room than the device asks for: on a
        // three-button device the pills sit just above the bar, with no
        // empty band between them.
        assertNoDeadBand("thread-reply")

        backToInbox()
    }

    /** The compose button and the drawer's pinned entries clear it too. */
    @Test
    fun t20_theInboxComposeButtonAndTheDrawerEntriesStandAboveIt() {
        signInAndSync()

        report("inbox")
        assertAboveSystemArea("inbox-compose")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-inbox")

        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        report("drawer")
        assertAboveSystemArea("drawer-report-problem")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-drawer")
        // Picking the inbox closes the sheet and leaves the shell where
        // it was; a back press here would leave the app.
        compose.onNodeWithTag("drawer-inbox").performClick()
        compose.waitForIdle()
        backToInbox()
    }

    /** The diagnostics screen and the outbox end above it as well. */
    @Test
    fun t30_theDiagnosticsAndOutboxScreensEndAboveIt() {
        signInAndSync()

        compose.onNodeWithTag("status-indicator").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("diagnostics-screen").fetchSemanticsNodes().isNotEmpty()
        }
        report("diagnostics")
        // Nothing is pinned here: the facts and the log scroll, so the
        // screen takes the room up to the navigation bar and stops
        // there, with no band of empty screen above it.
        assertEndsAtTheNavigationBar("diagnostics-screen")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-diagnostics")
        compose.onNodeWithTag("diagnostics-back").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }

        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-outbox").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("drawer-outbox").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("outbox-empty").fetchSemanticsNodes().isNotEmpty() ||
                compose.onAllNodesWithTag("outbox-list").fetchSemanticsNodes().isNotEmpty()
        }
        report("outbox")
        assertEndsAtTheNavigationBar("outbox-screen")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-outbox")
        backToInbox()
    }

    /** The bug reporter's action row is the last pinned row in the shell. */
    @Test
    fun t40_theBugReportSheetActionRowStandsAboveIt() {
        signInAndSync()

        compose.reportProblemFromTheDrawer()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }

        report("bug sheet")
        assertAboveSystemArea("bug-cancel")
        assertAboveSystemArea("bug-send")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-bug-sheet")

        compose.onNodeWithTag("bug-cancel").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isEmpty()
        }
        backToInbox()
    }

    /** The snooze sheet, whose last row is a plain button under the presets. */
    @Test
    fun t50_theSnoozeSheetsLastRowStandsAboveIt() {
        val message = seedMessage("Snooze sheet")
        openThread(message.threadId)

        compose.openThreadOverflow()
        compose.onNodeWithTag("thread-snooze").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        report("snooze sheet")
        compose.captureScreen("m4-bottom-inset-${navigationMode()}-snooze-sheet")
        assertAboveSystemArea("snooze-custom")
        assertNodeAboveSystemArea(compose.onNodeWithText("Cancel"), "the snooze sheet's Cancel")

        compose.onNodeWithText("Cancel").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("snooze-sheet").fetchSemanticsNodes().isEmpty()
        }
        backToInbox()
    }

    // ---- the measurements ------------------------------------------------

    /**
     * The height, in pixels, of the area the system holds along the
     * bottom edge of this window: the navigation bar, and the strip a
     * gesture-navigated device reserves for the swipe that leaves the
     * app. This is the same report the UI pads itself out of.
     */
    private fun systemBottomPx(): Int = compose.runOnUiThread {
        val decor = compose.activity.window.decorView
        val insets = ViewCompat.getRootWindowInsets(decor) ?: error("the window reported no insets")
        insets.getInsets(
            WindowInsetsCompat.Type.navigationBars() or WindowInsetsCompat.Type.mandatorySystemGestures(),
        ).bottom
    }

    private fun navigationBarPx(): Int = compose.runOnUiThread {
        val decor = compose.activity.window.decorView
        val insets = ViewCompat.getRootWindowInsets(decor) ?: error("the window reported no insets")
        insets.getInsets(WindowInsetsCompat.Type.navigationBars()).bottom
    }

    private fun windowHeightPx(): Int = compose.runOnUiThread { compose.activity.window.decorView.height }

    private fun density(): Float = compose.activity.resources.displayMetrics.density

    /**
     * How the device is navigated, from the setting the platform keeps:
     * 2 is the gesture handle, 0 the three buttons. The overlay listing
     * shows both overlays enabled after a switch, so the setting is the
     * only unambiguous answer.
     */
    private fun navigationMode(): String =
        when (shellOut("settings get secure navigation_mode").trim()) {
            "2" -> "gesture"
            "1" -> "twobutton"
            else -> "threebutton"
        }

    /** Writes what this window reports, so a failure reads without a second run. */
    private fun report(screen: String) {
        compose.waitForIdle()
        val content = compose.activity.findViewById<android.view.View>(android.R.id.content)
        val at = IntArray(2).also { content.getLocationInWindow(it) }
        Log.i(
            TAG,
            "$screen: window ${windowHeightPx()} px, navigation bar ${navigationBarPx()} px, " +
                "system bottom ${systemBottomPx()} px, ${navigationMode()} navigation, density ${density()}, " +
                "content view at ${at[0]},${at[1]} sized ${content.width}x${content.height}",
        )
    }

    /** Fails when [tag] reaches into the system's bottom area. */
    private fun assertAboveSystemArea(tag: String) =
        assertNodeAboveSystemArea(compose.onNodeWithTag(tag), tag)

    /** Fails when [node] reaches into the system's bottom area. */
    private fun assertNodeAboveSystemArea(node: SemanticsNodeInteraction, tag: String) {
        compose.waitForIdle()
        val bottom = node.fetchSemanticsNode().boundsInWindow.bottom
        val ceiling = windowHeightPx() - systemBottomPx()
        Log.i(TAG, "\"$tag\" ends at ${bottom.toInt()} px; the system's area starts at $ceiling px")
        assertTrue(
            "\"$tag\" ends at ${bottom.toInt()} px, inside the area the system holds from " +
                "$ceiling px down (window ${windowHeightPx()} px, navigation bar ${navigationBarPx()} px, " +
                "system bottom ${systemBottomPx()} px, ${navigationMode()} navigation)",
            bottom <= ceiling + SLACK_PX,
        )
    }

    /** Fails when the background behind [tag] stops short of the screen's edge. */
    private fun assertReachesTheBottomEdge(tag: String) {
        compose.waitForIdle()
        val bottom = compose.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow.bottom
        Log.i(TAG, "\"$tag\" background ends at ${bottom.toInt()} px of ${windowHeightPx()} px")
        assertTrue(
            "\"$tag\" ends at ${bottom.toInt()} px, short of the window's ${windowHeightPx()} px, " +
                "so the system's area shows what scrolls behind it",
            bottom >= windowHeightPx() - SLACK_PX,
        )
    }

    /**
     * Fails when a scrolling screen stops short of the navigation bar,
     * which is the band of unused screen a hand-placed padding leaves
     * behind, or runs past it into the bar itself.
     */
    private fun assertEndsAtTheNavigationBar(tag: String) {
        compose.waitForIdle()
        val bottom = compose.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow.bottom
        val bar = windowHeightPx() - navigationBarPx()
        Log.i(TAG, "\"$tag\" ends at ${bottom.toInt()} px; the navigation bar starts at $bar px")
        assertTrue(
            "\"$tag\" ends at ${bottom.toInt()} px, not at the navigation bar's $bar px " +
                "(window ${windowHeightPx()} px, navigation bar ${navigationBarPx()} px, " +
                "${navigationMode()} navigation)",
            bottom >= bar - SLACK_PX && bottom <= bar + SLACK_PX,
        )
    }

    /** Fails when [tag] leaves more empty room above the system's area than a row's padding. */
    private fun assertNoDeadBand(tag: String) {
        compose.waitForIdle()
        val bottom = compose.onNodeWithTag(tag).fetchSemanticsNode().boundsInWindow.bottom
        val ceiling = windowHeightPx() - systemBottomPx()
        val gapDp = (ceiling - bottom) / density()
        assertTrue(
            "\"$tag\" leaves ${gapDp.toInt()} dp of empty room above the system's area, " +
                "more than a row's own padding: the inset is being paid twice",
            gapDp <= MAX_GAP_DP,
        )
    }

    // ---- helpers ---------------------------------------------------------

    private fun seedMessage(tag: String): Email {
        signInAndSync()
        val subject = "$tag ${System.currentTimeMillis()}"
        DevInstance.deliverMail(
            subject = subject,
            from = "Bob Example <bob@example.local>",
            body = "A message for the $tag check.",
        )
        return awaitInbox(subject)
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
        const val TAG = "BottomInset"
        const val TIMEOUT_MS = 30_000L
        const val POLL_MS = 500L
        const val POLL_ATTEMPTS = 60

        /** A pixel of rounding between a layout bound and an inset is not a bug. */
        const val SLACK_PX = 1f

        /** A pinned row's own padding, the most it may leave above the system's area. */
        const val MAX_GAP_DP = 16f
    }
}
