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
import com.netzhansa.herold.shared.diag.BugBundleWriter
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.JmapClient
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
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
     * From a conversation's overflow: the sheet carries the screen it
     * was raised on, and the sent report is at the account's own
     * address with the bundle on it.
     */
    @Test
    fun t96_aReportFromAThreadArrivesAsMailWithTheBundle() = runBlocking {
        val title = "bug report ${System.currentTimeMillis()}"
        openAThread()

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
        compose.onAllNodesWithTag("bug-thumbnail").fetchSemanticsNodes().let {
            assertTrue("the sheet shows no screenshot thumbnail", it.isNotEmpty())
        }
        compose.onNodeWithTag("bug-title").performTextInput(title)
        compose.captureScreen("96-bug-report-sheet")
        compose.onNodeWithTag("bug-send").performClick()

        val subject = BugBundleWriter.subject(title)
        val arrived = awaitReport(subject)
        val names = arrived.attachments.map { it.name }
        assertTrue("the report carries no report.json, saw $names", names.contains("report.json"))
        assertTrue("the report carries no PNG, saw $names", names.any { it.endsWith(".png") })

        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val part = arrived.attachments.first { it.name == "report.json" }
        val bytes = client.downloadBlob(accountId, part.blobId, part.type, part.name).bytes
        val meta = json.parseToJsonElement(bytes.decodeToString()).jsonObject
        val route = meta["context"]!!.jsonObject["route"]!!.jsonPrimitive.content
        assertTrue("report.json names no thread route, saw $route", route.startsWith("thread/"))
        val version = meta["app"]!!.jsonObject["version"]!!.jsonPrimitive.content
        assertTrue("report.json names no version, saw \"$version\"", version.isNotBlank())
        assertNotNull(meta["sketch"]?.jsonPrimitive?.content)

        // And the copy is filed under the label the triage fetch reads,
        // left unread so the fetch picks it up.
        val filed = filedUnderLabel(client, accountId, subject)
        assertNotNull("the sent copy is not under \"${BugBundleWriter.LABEL}\"", filed)
        assertTrue(
            "the filed report is read; the fetch reads unread mail only",
            !filed!!.keywords.contains("\$seen"),
        )
        compose.captureScreen("97-bug-report-sent")
    }

    /**
     * The gesture. The emulator console is driven over the host's
     * loopback, which the device reaches at 10.0.2.2, so the shake is
     * injected from inside the run rather than by hand.
     */
    @Test
    fun t97_aShakeOpensTheSheet() {
        val console = EmulatorConsole.fromArguments()
        assumeNotNull("no emulator console argument; skipping the shake", console)
        console!!.use { it.shake() }
        compose.waitUntil(SHAKE_TIMEOUT_MS) {
            compose.onAllNodesWithTag("bug-sheet").fetchSemanticsNodes().isNotEmpty()
        }
        compose.captureScreen("98-shake-opened-the-sheet")
        compose.onNodeWithTag("bug-cancel").performClick()
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
    private suspend fun awaitReport(subject: String): Email {
        val client = DevInstance.serverClient()
        val accountId = client.session().mailAccountId!!
        val inbox = client.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(DELIVERY_POLLS) {
            val ids = client.emailQueryInbox(accountId, inbox, 20)
            client.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
                ?.let { return it }
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no bug report with subject \"$subject\" reached ${DevInstance.email}")
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
        const val DELIVERY_POLLS = 40
        const val DELIVERY_POLL_MS = 1_000L
    }
}
