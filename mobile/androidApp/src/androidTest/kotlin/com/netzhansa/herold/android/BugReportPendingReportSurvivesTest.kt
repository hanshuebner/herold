package com.netzhansa.herold.android

import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.diag.PendingReportStore
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The other half of `BugReportMultiCaptureAcceptanceTest`: the report
 * that class left open has to still be there (issue #424).
 *
 * Run it in its own instrumentation invocation, after that class and
 * after the app process has been killed:
 *
 *   adb shell am instrument -w -e class \
 *     com.netzhansa.herold.android.BugReportMultiCaptureAcceptanceTest ...
 *   adb shell am kill com.netzhansa.herold.android.debug
 *   adb shell am instrument -w -e class \
 *     com.netzhansa.herold.android.BugReportPendingReportSurvivesTest ...
 *
 * The second invocation is a cold process: nothing of the first one is
 * in memory, so what the marker shows comes from app storage alone.
 */
@RunWith(AndroidJUnit4::class)
class BugReportPendingReportSurvivesTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val store get() = PendingReportStore(
        InstrumentationRegistry.getInstrumentation().targetContext,
    )

    @Test
    fun anOpenReportIsStillThereAfterTheProcessWasKilled() {
        val held = store.load()
            ?: error(
                "no open report in app storage; run BugReportMultiCaptureAcceptanceTest first, " +
                    "then kill the app process",
            )
        assertTrue("the open report lost its captures", held.captureCount >= 1)
        assertTrue("the open report lost its picture", held.capture.screenshots.isNotEmpty())
        assertTrue(
            "the open report lost the screen it was taken on: ${held.capture.route}",
            held.capture.route.isNotBlank(),
        )

        // And the shell says so: the marker is on screen in a process
        // that never saw the capture being taken.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-pending-chip").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("106-the-open-report-survived-the-process")

        // Leave the device as it was found: the report is discarded
        // through the sheet, which is the way out the maintainer has.
        compose.onNodeWithTag("bug-pending-chip").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("bug-cancel").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-pending-chip").fetchSemanticsNodes().isEmpty() ||
                compose.onAllNodesWithTag("bug-confirm-discard").fetchSemanticsNodes().isNotEmpty()
        }
        if (compose.onAllNodesWithTag("bug-confirm-discard").fetchSemanticsNodes().isNotEmpty()) {
            compose.onNodeWithTag("bug-confirm-discard").performClick()
        }
        compose.waitUntil(TIMEOUT_MS) { store.load() == null }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
    }
}
