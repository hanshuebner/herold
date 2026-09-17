package com.netzhansa.herold.android

import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.diag.BugBundleWriter
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
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
 * The in-app bug reporter (issue #407). A report raised from a
 * conversation's overflow reaches the maintainer's own mailbox under
 * the "Bug reports" label, carrying the bundle `herold bug-fetch`
 * expands - `report.json` naming the route and the build, and the
 * window as a PNG.
 *
 * The second check drives the gesture: the emulator's console is told
 * to shake the device and the sheet is expected to open on its own.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class BugReportAcceptanceTest {

    @get:Rule
    val compose = createAndroidComposeRule<MainActivity>()

    private val app get() = InstrumentationRegistry.getInstrumentation()
        .targetContext.applicationContext as HeroldApplication

    private val json = Json { ignoreUnknownKeys = true }

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
     * report still reaches the account's own address with the capture
     * on it, and says that the description is still owed.
     */
    @Test
    fun t96_aReportSentWithOneTapArrivesWithTheCaptureAndNoDescription() = runBlocking {
        openAThread()
        raiseTheSheet()
        compose.captureScreen("96-bug-report-sheet-one-tap")
        compose.onNodeWithTag("bug-send").performClick()

        // Nothing was typed, so the subject names where and when.
        val arrived = awaitReport { it.startsWith(BugBundleWriter.SUBJECT_PREFIX + " thread ") }
        val names = arrived.attachments.map { it.name }
        assertTrue("the report carries no report.json, saw $names", names.contains("report.json"))
        assertTrue("the report carries no PNG, saw $names", names.any { it.endsWith(".png") })

        val meta = reportJsonOf(arrived)
        assertEquals(
            "an untyped report must say the description is still owed",
            false,
            meta["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean(),
        )
        assertEquals("", meta["sketch"]?.jsonPrimitive?.content)
        val route = meta["context"]!!.jsonObject["route"]!!.jsonPrimitive.content
        assertTrue("report.json names no thread route, saw $route", route.startsWith("thread/"))
        val version = meta["app"]!!.jsonObject["version"]!!.jsonPrimitive.content
        assertTrue("report.json names no version, saw \"$version\"", version.isNotBlank())

        // And the copy is filed under the label the triage fetch reads,
        // left unread so the fetch picks it up.
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val filed = filedUnderLabel(client, accountId, arrived.subject)
        assertNotNull("the sent copy is not under \"${BugBundleWriter.LABEL}\"", filed)
        assertTrue(
            "the filed report is read; the fetch reads unread mail only",
            !filed!!.keywords.contains("\$seen"),
        )
        compose.captureScreen("97-bug-report-sent")
    }

    /** A described report carries the words the maintainer typed. */
    @Test
    fun t97_aDescribedReportCarriesItsTitleAndNote() = runBlocking {
        val title = "described report ${System.currentTimeMillis()}"
        val note = "It went blank right after the sync finished."
        openAThread()
        raiseTheSheet()
        compose.onNodeWithTag("bug-title").performTextInput(title)
        compose.onNodeWithTag("bug-note").performTextInput(note)
        compose.captureScreen("98-bug-report-sheet-described")
        compose.onNodeWithTag("bug-send").performClick()

        val arrived = awaitReport { it == BugBundleWriter.SUBJECT_PREFIX + " " + title }
        val meta = reportJsonOf(arrived)
        assertEquals(
            "a typed report must say so",
            true,
            meta["descriptionEntered"]?.jsonPrimitive?.content?.toBoolean(),
        )
        assertEquals("$title\n\n$note", meta["sketch"]?.jsonPrimitive?.content)
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
     * From the inbox overflow, and then the other end of it: the label
     * the client created carries the report, which is what
     * `herold bug-fetch` reads.
     */
    @Test
    fun t99_theReportIsReadableUnderTheLabelOnThePhone() = runBlocking {
        compose.onNodeWithTag("inbox-overflow").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("menu-report-problem").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("menu-report-problem").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("bug-send").performClick()
        val arrived = awaitReport { it.startsWith(BugBundleWriter.SUBJECT_PREFIX + " inbox ") }

        // The client's own view of the other end: the label it created,
        // carrying the report it filed there.
        compose.waitUntil(LABEL_TIMEOUT_MS) {
            runBlocking { app.container.session.value!!.syncEngine.syncAll() }
            compose.onAllNodesWithTag("drawer-label-${BugBundleWriter.LABEL}")
                .fetchSemanticsNodes().isNotEmpty() ||
                runBlocking {
                    app.container.store.mailboxList()
                        .any { it.name == BugBundleWriter.LABEL }
                }
        }
        compose.onNodeWithTag("inbox-drawer-open").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("drawer-label-${BugBundleWriter.LABEL}")
                .fetchSemanticsNodes().isNotEmpty()
        }
        compose.onNodeWithTag("inbox-drawer").performScrollToNode(
            hasTestTag("drawer-label-${BugBundleWriter.LABEL}"),
        )
        compose.captureScreen("100-bug-reports-label-in-the-drawer")
        compose.onNodeWithTag("drawer-label-${BugBundleWriter.LABEL}").performClick()
        compose.waitUntil(LABEL_TIMEOUT_MS) {
            compose.onAllNodesWithText(arrived.subject).fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("101-the-report-under-the-label")
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

    /** `report.json` as the arrived mail carries it. */
    private suspend fun reportJsonOf(mail: Email): JsonObject {
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val part = mail.attachments.first { it.name == "report.json" }
        val bytes = client.downloadBlob(accountId, part.blobId, part.type, part.name).bytes
        return json.parseToJsonElement(bytes.decodeToString()).jsonObject
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

    /** The report as it arrived in the account's own inbox. */
    private suspend fun awaitReport(matches: (String) -> Boolean): Email {
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val inbox = client.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(DELIVERY_POLLS) {
            val ids = client.emailQueryInbox(accountId, inbox, 20)
            client.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { matches(it.subject) }
                ?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no bug report reached ${DevInstance.email}")
    }

    /** The sent copy under the "Bug reports" label, once the drain filed it. */
    private suspend fun filedUnderLabel(
        client: JmapClient,
        accountId: String,
        subject: String,
    ): Email? {
        repeat(DELIVERY_POLLS) {
            val label = client.mailboxGet(accountId).list
                .firstOrNull { it.name.equals(BugBundleWriter.LABEL, ignoreCase = true) }
            if (label != null) {
                val ids = client.emailQueryInbox(accountId, label.id, 20)
                client.emailGet(accountId, ids).list
                    .map { it.toStoreRow(accountId) }
                    .firstOrNull { it.subject == subject }
                    ?.let { return it }
            }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        return null
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val SHAKE_TIMEOUT_MS = 30_000L
        const val LABEL_TIMEOUT_MS = 60_000L
        const val DELIVERY_POLLS = 40
        const val DELIVERY_POLL_MS = 1_000L
    }
}
