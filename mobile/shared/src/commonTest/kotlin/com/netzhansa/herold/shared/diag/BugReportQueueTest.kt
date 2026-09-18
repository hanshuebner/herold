package com.netzhansa.herold.shared.diag

import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.fake.FakeJmapApi
import com.netzhansa.herold.shared.fake.FakeLocalStore
import com.netzhansa.herold.shared.jmap.BugReportApi
import com.netzhansa.herold.shared.jmap.BugReportPart
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.outbox.InMemoryBlobSpool
import com.netzhansa.herold.shared.outbox.Outbox
import com.netzhansa.herold.shared.outbox.OutboxDrainer
import com.netzhansa.herold.shared.outbox.OutboxKind
import com.netzhansa.herold.shared.outbox.OutboxState
import kotlinx.coroutines.test.runTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

/** The bug-reports endpoint as the drain sees it. */
private class FakeBugReportApi : BugReportApi {
    val posts = mutableListOf<List<BugReportPart>>()
    var failure: Throwable? = null
    var id = "20260917T101010Z-0f1e2d3c"

    override suspend fun postBugReport(parts: List<BugReportPart>): String {
        failure?.let { throw it }
        posts += parts
        return id
    }
}

/** What the transport raises when the phone is on no network at all. */
private class UnreachableHost : Exception("Unable to resolve host \"mail.example.local\"")

/**
 * A report from the sheet to the server (REQ-AND-SYS-53, issue #417):
 * the bundle is spooled into one outbox entry, the drain posts it to
 * `/api/v1/bug-reports`, and nothing about it reaches a mailbox.
 */
class BugReportQueueTest {

    private class Harness(var now: Long = 1_000_000) {
        val store = FakeLocalStore()
        val spool = InMemoryBlobSpool()
        val outbox = Outbox(store) { now }
        val api = FakeJmapApi()
        val reports = FakeBugReportApi()
        val logs = mutableListOf<String>()
        val sender = BugReportSender(outbox, spool) { now }
        val drainer = OutboxDrainer(
            api = api,
            store = store,
            outbox = outbox,
            spool = spool,
            bugReports = reports,
            log = { logs += it },
            now = { now },
        )
    }

    private val capture = BugCapture(
        shots = listOf(
            BugShot(
                route = "thread/{accountId}/{threadId}",
                routeArguments = mapOf("accountId" to "acct-a", "threadId" to "T42"),
                threadId = "T42",
                capturedAtMs = 1_700_000_001_000,
                screenshot = byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47),
            ),
        ),
        accountScope = "acct-a",
        device = DeviceFacts("1.4.2", "abc1234", "16", 36, "Google", "sdk_gphone64_arm64"),
        sync = SyncFacts(state = "idle"),
        push = PushFacts(choice = "AUTOMATIC"),
    )

    private suspend fun queue(
        h: Harness,
        submission: BugSubmission = BugSubmission(title = "the thread view is blank"),
        holdMs: Long = 0,
    ): ComposeResult = h.sender.queue(
        BugBundleWriter.build(submission, capture, createdAtMs = 1_700_000_002_000),
        accountId = "acct-a",
        holdMs = holdMs,
    )

    @Test
    fun aQueuedReportIsOneEntryNamedAfterItsTitle() = runTest {
        val h = Harness()
        val queued = queue(h)

        assertTrue(queued is ComposeResult.Queued, "the report was not queued: $queued")
        val entry = h.outbox.list().single()
        assertEquals(OutboxKind.BUG_REPORT, entry.kind)
        assertEquals("Bug report: the thread view is blank", entry.label)
        assertEquals("acct-a", entry.accountId)
    }

    @Test
    fun theDrainPostsEveryFileOfTheBundleAndKeepsNothingBehind() = runTest {
        val h = Harness()
        queue(h)
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.submitted)
        val posted = h.reports.posts.single()
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png"),
            posted.map { it.name },
        )
        assertEquals("image/png", posted.first { it.name == "screenshot-1.png" }.type)
        assertTrue(
            posted.first { it.name == "report.json" }.bytes.decodeToString()
                .contains("\"title\": \"the thread view is blank\""),
            "report.json does not carry the title",
        )
        assertTrue(h.outbox.list().isEmpty(), "the drained entry is still queued")
        // Nothing of the report is written as mail.
        assertTrue(h.api.emailSetCalls.isEmpty())
        assertTrue(h.api.mailboxSetCalls.isEmpty())
    }

    @Test
    fun aReportInsideItsUndoWindowIsNotPostedYet() = runTest {
        val h = Harness()
        val queued = queue(h, holdMs = 5_000) as ComposeResult.Queued
        h.drainer.drain()

        assertTrue(h.reports.posts.isEmpty(), "the report left before its undo window")
        // Undo inside the window drops the entry outright.
        assertEquals(queued.entryId, h.outbox.cancelIfQueued(queued.entryId)?.id)
        h.now += 10_000
        h.drainer.drain()
        assertTrue(h.reports.posts.isEmpty())
    }

    @Test
    fun theWindowOverTheReportGoesOut() = runTest {
        val h = Harness()
        queue(h, holdMs = 5_000)
        h.now += 6_000
        h.drainer.drain()

        assertEquals(1, h.reports.posts.size)
    }

    @Test
    fun aRefusedReportIsLeftFailedWithTheServersReason() = runTest {
        val h = Harness()
        h.reports.failure = JmapException(
            "the bug report was refused: payload_too_large: part \"screenshot-1.png\" is too big",
            status = 413,
        )
        queue(h)
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.rejected)
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state)
        assertTrue(entry.permanent)
        assertTrue(
            entry.lastError!!.contains("payload_too_large"),
            "the reason is lost: ${entry.lastError}",
        )
    }

    @Test
    fun withNoConnectionTheReportWaitsExactlyAsItWas() = runTest {
        val h = Harness()
        h.reports.failure = UnreachableHost()
        queue(h)
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.offline)
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.QUEUED, entry.state)
        assertEquals(0, entry.attempts, "being offline is not an attempt the report spends")
        assertNull(entry.lastError)

        // It leaves on the next drain, once there is a connection.
        h.reports.failure = null
        h.drainer.drain()
        assertEquals(1, h.reports.posts.size)
        assertTrue(h.outbox.list().isEmpty())
    }

    @Test
    fun aDrainedReportTakesItsSpooledFilesWithIt() = runTest {
        val h = Harness()
        queue(h)
        val handles = h.outbox.list().single().let { entry ->
            com.netzhansa.herold.shared.outbox.outboxJson
                .decodeFromString<com.netzhansa.herold.shared.outbox.BugReportPayload>(entry.payload)
                .parts.map { it.spool }
        }
        assertTrue(handles.isNotEmpty())
        h.drainer.drain()

        handles.forEach { handle ->
            if (h.spool.read(handle) != null) fail("the spooled $handle outlived the report")
        }
    }
}
