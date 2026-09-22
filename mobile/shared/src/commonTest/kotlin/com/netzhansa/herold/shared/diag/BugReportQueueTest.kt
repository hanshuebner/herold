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
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
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

    private class Harness(var now: Long = 1_000_000, maxDeferredAttempts: Int = 24) {
        val store = FakeLocalStore()
        val spool = InMemoryBlobSpool()
        val outbox = Outbox(store) { now }
        val api = FakeJmapApi()
        val reports = FakeBugReportApi()
        val logs = mutableListOf<String>()
        val sender = BugReportSender(outbox, spool)
        val drainer = OutboxDrainer(
            api = api,
            store = store,
            outbox = outbox,
            spool = spool,
            bugReports = reports,
            log = { logs += it },
            now = { now },
            maxDeferredAttempts = maxDeferredAttempts,
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
    ): ComposeResult = h.sender.queue(
        BugBundleWriter.build(submission, capture, createdAtMs = 1_700_000_002_000),
        accountId = "acct-a",
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

    /**
     * A report leaves on the tap (issue #438): it is written due, and a
     * drain run at the very instant of the tap - the clock has not
     * moved - posts it.
     */
    @Test
    fun aQueuedReportCarriesNoHoldAndTheDrainTakesItAtOnce() = runTest {
        val h = Harness()
        val queued = queue(h)

        assertTrue(queued is ComposeResult.Queued, "the report was not queued: $queued")
        assertEquals(0, queued.heldUntilMs, "the report was queued behind a hold")
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.QUEUED, entry.state)
        assertEquals(0, entry.nextAttemptAt, "the drain is held off until ${entry.nextAttemptAt}")

        val outcome = h.drainer.drain()
        assertEquals(1, outcome.submitted)
        assertEquals(1, h.reports.posts.size)
    }

    /**
     * The loss this ticket is about (issue #420): reports carrying a
     * crash trace were refused by a server that did not yet know the
     * `crash.txt` part, and never went out again once it did. A refusal
     * the client cannot act on is the server being behind, so the report
     * waits for it and leaves by itself when the server catches up, with
     * no hand on the phone.
     */
    @Test
    fun aReportAnOlderServerCannotTakeLeavesWhenTheServerCatchesUp() = runTest {
        val h = Harness()
        h.reports.failure = JmapException(
            "the bug report was refused: unexpected part \"crash.txt\"",
            status = 400,
        )
        queue(h)

        val refused = h.drainer.drain()
        assertEquals(0, refused.submitted)
        assertEquals(1, refused.deferred, "the refused report was not held for a later server")
        val waiting = h.outbox.list().single()
        assertEquals(OutboxState.DEFERRED, waiting.state)
        assertTrue(waiting.isPending, "a report waiting for the server counts as gone")
        assertTrue(waiting.nextAttemptAt > h.now, "the next offer is not scheduled")
        assertTrue(
            waiting.lastError!!.contains("crash.txt"),
            "the server's reason is lost: ${waiting.lastError}",
        )
        // Nothing keeps a background job coming back for an entry whose
        // next offer is hours away.
        assertTrue(!refused.hasMore, "a deferred report keeps the drain job alive")

        // The server is upgraded and takes the part.
        h.reports.failure = null
        h.now += 30 * 60_000
        val taken = h.drainer.drain()

        assertEquals(1, taken.submitted)
        assertEquals(
            listOf("report.json", "report.md", "logs.txt", "screenshot-1.png"),
            h.reports.posts.single().map { it.name },
        )
        assertTrue(h.outbox.list().isEmpty(), "the delivered report is still queued")
    }

    /**
     * The waiting is bounded: a report no server will ever take stops
     * being offered, and the reader is told rather than the entry going
     * quiet. It is kept, because a queued report is the evidence for its
     * own defect.
     */
    @Test
    fun aReportNoServerTakesIsGivenUpOnInTheOpen() = runTest {
        val h = Harness(maxDeferredAttempts = 3)
        h.reports.failure = JmapException(
            "the bug report was refused: unexpected part \"crash.txt\"",
            status = 400,
        )
        queue(h)
        val announced = mutableListOf<String>()
        val watching = backgroundScope.launch { h.drainer.failures.collect { announced += it.message } }
        runCurrent()

        repeat(2) {
            h.drainer.drain()
            assertEquals(OutboxState.DEFERRED, h.outbox.list().single().state)
            h.now += 24 * 60 * 60_000
        }
        val last = h.drainer.drain()
        runCurrent()
        watching.cancel()

        assertEquals(1, last.rejected)
        val entry = h.outbox.list().single()
        assertEquals(OutboxState.FAILED, entry.state, "the entry the drain gave up on is not listed")
        assertEquals(3, entry.attempts)
        assertTrue(
            entry.lastError!!.contains("unsent after 3 attempts"),
            "the give-up does not say what became of the report: ${entry.lastError}",
        )
        assertTrue(
            announced.any { it.contains("unsent after 3 attempts") },
            "nobody was told the report was given up on: $announced",
        )
        assertEquals(0, h.reports.posts.size)
    }

    /**
     * A report the user retries by hand goes out at once rather than
     * waiting out the schedule: the maintainer knowing the server is
     * ready is better information than the backoff.
     */
    @Test
    fun aWaitingReportGoesOutAtOnceOnARetry() = runTest {
        val h = Harness()
        h.reports.failure = JmapException("the bug report was refused: unknown part", status = 400)
        queue(h)
        h.drainer.drain()
        assertEquals(OutboxState.DEFERRED, h.outbox.list().single().state)

        h.reports.failure = null
        h.outbox.retryAll()
        val outcome = h.drainer.drain()

        assertEquals(1, outcome.submitted)
        assertEquals(1, h.reports.posts.size)
        assertTrue(h.outbox.list().isEmpty(), "the retried report is still queued")
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
