package com.netzhansa.herold.android

import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipe
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import com.netzhansa.herold.android.diag.PendingReportStore
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeNotNull
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * A report that carries more than one screen (issue #424). The
 * maintainer captures a conversation, walks to the inbox, taps the
 * marker to put that screen on the same report and sends once; the
 * server's drop carries both pictures and says which screen each was
 * taken on. The shake and the menu entry ask first, since those may
 * well mean a new problem.
 *
 * The last check leaves a report open on purpose:
 * `BugReportPendingReportSurvivesTest` runs after it, in its own
 * instrumentation invocation, and a fresh process is what a pending
 * report has to survive.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BugReportMultiCaptureAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val store get() = PendingReportStore(
        InstrumentationRegistry.getInstrumentation().targetContext,
    )

    private val key: String get() = DevInstance.bugReportsKey
        ?: error("no heroldBugReportsKey; mint one with `herold api-key create --scope bug-reports`")

    @Before
    fun signedIn() {
        grantNotificationPermission()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl, DevInstance.email, DevInstance.password, null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-list").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /**
     * Two screens, one report. The marker's tap captures the screen the
     * maintainer walked to and puts it on the open report, and the drop
     * the server hands back carries both pictures and both captures
     * with their routes.
     */
    @Test
    fun t01_twoCapturesOnTwoRoutesArriveAsOneReport() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        store.clear()
        val known = reportIds()
        val title = "two captures ${System.currentTimeMillis()}"

        openAThread()
        raiseTheSheetFromTheThread()
        compose.onNodeWithTag("bug-add-capture").performClick()

        // The report is open: the marker says so from whatever screen
        // the maintainer walks to.
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-pending-chip").fetchSemanticsNodes().isNotEmpty()
        }
        UiDevice.getInstance(InstrumentationRegistry.getInstrumentation()).pressBack()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-overflow").fetchSemanticsNodes().isNotEmpty()
        }

        // The marker is the way to add the screen you walked to: one
        // tap captures it and puts it on the open report.
        compose.onNodeWithTag("bug-pending-chip").performClick()

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-thumbnail-2").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("103-capture-strip-with-two-thumbnails")
        compose.onNodeWithTag("bug-title").performTextInput(title)
        compose.onNodeWithTag("bug-send").performClick()

        val arrived = awaitReport(known) { it.title == title }
        assertEquals("the report carries one picture per capture", 2, arrived.screenshotCount)

        val drop = BugReportsApi.drop(DevInstance.baseUrl, key, arrived.id)
        val names = drop.keys.sorted()
        assertTrue("the drop carries no screenshot-1.png, saw $names", drop.containsKey("screenshot-1.png"))
        assertTrue("the drop carries no screenshot-2.png, saw $names", drop.containsKey("screenshot-2.png"))
        assertTrue("screenshot-2.png is not a PNG", drop["screenshot-2.png"]!!.let { it.size > 4 && it[1] == 'P'.code.toByte() })

        val meta = JSONObject(drop["report.json"]!!.decodeToString())
        val captures = meta.getJSONArray("captures")
        assertEquals("report.json names two captures", 2, captures.length())
        val first = captures.getJSONObject(0)
        val second = captures.getJSONObject(1)
        assertEquals(1, first.getInt("index"))
        assertTrue(
            "the first capture is not the conversation: ${first.getString("route")}",
            first.getString("route").startsWith("thread/"),
        )
        assertNotNull("the first capture names no thread", first.optString("threadId"))
        assertEquals(2, second.getInt("index"))
        assertEquals("inbox", second.getString("route"))
        // The report itself is still named and routed by where it started.
        assertTrue(
            "the report's route moved off the first capture: ${meta.getJSONObject("context").getString("route")}",
            meta.getJSONObject("context").getString("route").startsWith("thread/"),
        )
        // And the sketch reads the captures out with their routes.
        val markdown = drop["report.md"]!!.decodeToString()
        assertTrue("report.md lists no captures:\n$markdown", markdown.contains("## Captures"))
        assertTrue("report.md misses the inbox capture:\n$markdown", markdown.contains("herold://inbox"))
        compose.captureScreen("104-two-capture-report-sent")
    }

    /**
     * The other entry points ask. A shake or "Report a problem" may
     * well mean a new problem, so with a report open they offer both
     * ways and the maintainer picks.
     */
    @Test
    fun t02_theMenuEntryAsksBeforeJoiningTheOpenReport() {
        store.clear()
        openAThread()
        raiseTheSheetFromTheThread()
        compose.onNodeWithTag("bug-add-capture").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-pending-chip").fetchSemanticsNodes().isNotEmpty()
        }
        UiDevice.getInstance(InstrumentationRegistry.getInstrumentation()).pressBack()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("inbox-overflow").fetchSemanticsNodes().isNotEmpty()
        }

        raiseTheReporterFromTheInbox()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-prompt").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("102-add-to-the-open-report-or-start-a-new-one")
        compose.onNodeWithTag("bug-prompt-add").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-thumbnail-2").fetchSemanticsNodes().isNotEmpty()
        }

        // And out again: a report of several captures is not dropped by
        // one tap.
        compose.onNodeWithTag("bug-cancel").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-confirm-discard").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("bug-confirm-discard").performClick()
        compose.waitUntil(TIMEOUT_MS) { store.load() == null }
    }

    /**
     * What the next process has to find: a report with a capture on it,
     * written to app storage the moment the maintainer said "Add
     * another capture".
     */
    @Test
    fun t03_addingAnotherCaptureLeavesTheReportOnDisk() {
        store.clear()
        openAThread()
        raiseTheSheetFromTheThread()
        compose.onNodeWithTag("bug-add-capture").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-pending-chip").fetchSemanticsNodes().isNotEmpty()
        }
        val held = awaitHeldReport()
        assertEquals("the open report holds one capture", 1, held.captureCount)
        assertTrue(
            "the held capture is not the conversation: ${held.capture.route}",
            held.capture.route.startsWith("thread/"),
        )
        assertTrue("the held capture has no picture", held.capture.screenshots.isNotEmpty())
        compose.captureScreen("105-the-open-report-marker")

        // The marker is dragged off whatever it covers, and a drag is
        // not a tap: nothing is captured by moving it.
        val before = compose.onNodeWithTag("bug-pending-chip").fetchSemanticsNode().boundsInRoot
        compose.onNodeWithTag("bug-pending-chip").performTouchInput {
            swipe(start = center, end = center + Offset(220f, -700f), durationMillis = 400)
        }
        compose.waitForIdle()
        val after = compose.onNodeWithTag("bug-pending-chip").fetchSemanticsNode().boundsInRoot
        assertTrue(
            "the marker did not move: $before -> $after",
            after.top < before.top - MOVED_BY_PX && after.left > before.left + MOVED_BY_PX,
        )
        assertTrue(
            "dragging the marker opened the sheet",
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isEmpty(),
        )
        assertEquals("dragging the marker took a capture", 1, awaitHeldReport().captureCount)
        compose.captureScreen("107-the-marker-dragged-clear")
    }

    /** The report as app storage holds it, once the write has landed. */
    private fun awaitHeldReport(): com.netzhansa.herold.shared.diag.PendingBugReport {
        repeat(QUEUE_POLLS) {
            store.load()?.let { return it }
            Thread.sleep(QUEUE_POLL_MS)
        }
        error("no open report reached app storage")
    }

    /** Opens the reporter from the conversation's overflow. */
    private fun raiseTheSheetFromTheThread() {
        compose.onNodeWithTag("thread-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-report-problem").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-thumbnail-1").fetchSemanticsNodes().isNotEmpty()
        }
    }

    /** And from the inbox's, which is the other entry point. */
    private fun raiseTheReporterFromTheInbox() {
        compose.onNodeWithTag("inbox-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("menu-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("menu-report-problem").performClick()
    }

    /** Seeds a conversation and opens it, so the report has a thread route. */
    private fun openAThread() {
        val subject = "multi capture seed ${System.currentTimeMillis()}"
        DevInstance.deliverMail(subject = subject, body = "Something looked wrong here.")
        runBlocking { DevInstance.awaitFiled(subject) }
        val seeded = awaitInbox(subject)
        compose.onNodeWithTag("inbox-list").performScrollToNode(hasTestTag("thread-row-${seeded.threadId}"))
        compose.onNodeWithTag("thread-row-${seeded.threadId}").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-overflow").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun awaitInbox(subject: String): Email = runBlocking {
        repeat(DELIVERY_POLLS) {
            app.container.session.value!!.syncEngine.syncAll()
            app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
                ?.let { return@runBlocking it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the inbox")
    }

    /** The reports the server already holds, so a new one is recognisable. */
    private fun reportIds(): Set<String> =
        BugReportsApi.list(DevInstance.baseUrl, key).map { it.id }.toSet()

    /** The report as it reached the server, once the drain posted it. */
    private fun awaitReport(
        known: Set<String>,
        matches: (BugReportsApi.Report) -> Boolean,
    ): BugReportsApi.Report {
        repeat(DELIVERY_POLLS) {
            BugReportsApi.list(DevInstance.baseUrl, key)
                .firstOrNull { it.id !in known && matches(it) }
                ?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no bug report reached the server's queue")
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 40
        const val DELIVERY_POLL_MS = 1_000L
        const val QUEUE_POLLS = 20
        const val QUEUE_POLL_MS = 250L

        /** How far the marker has to travel for a drag to have happened. */
        const val MOVED_BY_PX = 100f
    }
}
