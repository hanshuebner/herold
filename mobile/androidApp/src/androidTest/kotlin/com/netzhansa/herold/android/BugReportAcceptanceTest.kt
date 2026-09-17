package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.outbox.OutboxKind
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeNotNull
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * The in-app bug reporter (issues #407, #417). A report raised from the
 * app reaches the server's bug-reports queue - `POST /api/v1/bug-reports`
 * on the account's bearer token - carrying the bundle `herold bug-fetch`
 * expands: `report.json` naming the route and the build, and the window
 * as a PNG. Nothing of it is filed in the user's mailboxes.
 *
 * The checks read the result back the way the maintainer's Mac does,
 * with a bug-reports-scoped key (`heroldBugReportsKey`). The shake check
 * drives the gesture: the emulator's console is told to shake the device
 * and the sheet is expected to open on its own.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BugReportAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

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
     * One tap from a conversation's overflow, with nothing typed. The
     * capture still reaches the server, and the report says that the
     * description is still owed.
     */
    @Test
    fun t96_aReportSentWithOneTapArrivesWithTheCaptureAndNoDescription() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        val known = reportIds()
        openAThread()
        raiseTheSheet()
        compose.captureScreen("96-bug-report-sheet-one-tap")
        compose.onNodeWithTag("bug-send").performClick()

        // The queue names the report before it leaves.
        val queued = awaitQueuedReport()
        assertTrue(
            "the outbox row reads \"${queued.label}\"",
            queued.label.startsWith("Bug report: thread "),
        )

        val arrived = awaitReport(known) { it.route.startsWith("thread/") }
        assertFalse("an untyped report must say the description is still owed", arrived.descriptionEntered)
        assertEquals(DevInstance.email, arrived.email)
        assertTrue("the report carries no screenshot", arrived.screenshotCount >= 1)

        val drop = BugReportsApi.drop(DevInstance.baseUrl, key, arrived.id)
        val names = drop.keys.sorted()
        assertTrue("the drop carries no report.json, saw $names", drop.containsKey("report.json"))
        assertTrue("the drop carries no PNG, saw $names", names.any { it.endsWith(".png") })
        val png = drop.entries.first { it.key.endsWith(".png") }.value
        assertTrue("the screenshot is not a PNG", png.size > 4 && png[1] == 'P'.code.toByte())

        val meta = JSONObject(drop["report.json"]!!.decodeToString())
        assertFalse(meta.getBoolean("descriptionEntered"))
        assertEquals("", meta.optString("sketch"))
        val route = meta.getJSONObject("context").getString("route")
        assertTrue("report.json names no thread route, saw $route", route.startsWith("thread/"))
        val version = meta.getJSONObject("app").getString("version")
        assertTrue("report.json names no version, saw \"$version\"", version.isNotBlank())
        assertTrue("report.json carries no title", meta.getString("title").startsWith("thread "))
        compose.captureScreen("97-bug-report-sent")
    }

    /** A described report carries the words the maintainer typed. */
    @Test
    fun t97_aDescribedReportCarriesItsTitleAndNote() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        val title = "described report ${System.currentTimeMillis()}"
        val note = "It went blank right after the sync finished."
        val known = reportIds()
        openAThread()
        raiseTheSheet()
        compose.onNodeWithTag("bug-title").performTextInput(title)
        compose.onNodeWithTag("bug-note").performTextInput(note)
        compose.captureScreen("98-bug-report-sheet-described")
        compose.onNodeWithTag("bug-send").performClick()

        val arrived = awaitReport(known) { it.title == title }
        assertTrue("a typed report must say so", arrived.descriptionEntered)

        val meta = BugReportsApi.reportJson(DevInstance.baseUrl, key, arrived.id)
        assertTrue(meta.getBoolean("descriptionEntered"))
        assertEquals(title, meta.getString("title"))
        assertEquals("$title\n\n$note", meta.getString("sketch"))
    }

    /**
     * The gesture. The emulator console is driven over the host's
     * loopback, which the device reaches at 10.0.2.2, so the shake is
     * injected from inside the run rather than by hand.
     */
    @Test
    fun t98_aShakeOpensTheSheet() {
        val console = EmulatorConsole.fromArguments()
        assumeNotNull("no emulator console argument; skipping the shake", console)
        console!!.use { it.shake() }
        compose.waitUntil(SHAKE_TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("99-shake-opened-the-sheet")
        compose.onNodeWithTag("bug-cancel").performClick()
    }

    /**
     * From the inbox overflow, and then the other end of it: the report
     * is on the server and the account's mail is untouched - no "Bug
     * reports" label, and nothing addressed to the user themselves.
     */
    @Test
    fun t99_theReportGoesToTheServerAndNotToTheMailbox() = runBlocking {
        assumeNotNull("no bug-reports key; skipping", DevInstance.bugReportsKey)
        val known = reportIds()
        compose.onNodeWithTag("inbox-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("menu-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("menu-report-problem").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("bug-send").performClick()
        val arrived = awaitReport(known) { it.route.startsWith("inbox") }
        assertTrue("the report is not named after the inbox", arrived.title.startsWith("inbox "))

        // The other end: the client created no label for it and filed no
        // copy of it (issue #417 replaced the mail transport).
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val mailboxes = client.mailboxGet(accountId).list
        assertTrue(
            "the client created a \"Bug reports\" label: ${mailboxes.map { it.name }}",
            mailboxes.none { it.name.equals("Bug reports", ignoreCase = true) },
        )
        val inbox = mailboxes.first { it.role == "inbox" }.id
        val ids = client.emailQueryInbox(accountId, inbox, 20)
        val subjects = client.emailGet(accountId, ids).list.map { it.subject.orEmpty() }
        assertTrue(
            "a report was mailed to the account: $subjects",
            subjects.none { it.startsWith("herold bug:") },
        )
        compose.captureScreen("100-report-raised-from-the-inbox")
    }

    /** Opens the reporter from the conversation's overflow. */
    private fun raiseTheSheet() {
        compose.onNodeWithTag("thread-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("thread-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("thread-report-problem").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        // The capture happened before the sheet: its thumbnail is the
        // conversation the report is about.
        assertTrue(
            "the sheet shows no screenshot thumbnail",
            compose.onAllNodesWithTag("bug-thumbnail").fetchSemanticsNodes().isNotEmpty(),
        )
    }

    /** Seeds a conversation and opens it, so the report has a thread route. */
    private fun openAThread() {
        val subject = "bug report seed ${System.currentTimeMillis()}"
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

    /** The report entry the tap queued, while it waits out its undo window. */
    private suspend fun awaitQueuedReport(): com.netzhansa.herold.shared.outbox.OutboxEntry {
        repeat(QUEUE_POLLS) {
            app.container.outbox.list().firstOrNull { it.kind == OutboxKind.BUG_REPORT }
                ?.let { return it }
            Thread.sleep(QUEUE_POLL_MS)
        }
        error("no bug report was queued")
    }

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
        const val SHAKE_TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 40
        const val DELIVERY_POLL_MS = 1_000L
        const val QUEUE_POLLS = 20
        const val QUEUE_POLL_MS = 250L
    }
}
