package com.netzhansa.herold.shared.diag

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The report that is still open (issue #424): what app storage keeps of
 * it, what comes back after the process was killed, and what it drops.
 */
class PendingBugReportTest {

    private val firstShot = BugShot(
        route = "thread/{accountId}/{threadId}",
        routeArguments = mapOf("accountId" to "acct-1", "threadId" to "T42"),
        threadId = "T42",
        capturedAtMs = 1_700_000_000_500,
        screenshot = byteArrayOf(0x89.toByte(), 'P'.code.toByte()),
    )

    private val secondShot = BugShot(
        route = "inbox",
        capturedAtMs = 1_700_000_060_000,
        screenshot = byteArrayOf(0x89.toByte(), 'N'.code.toByte()),
    )

    private val capture = BugCapture(
        shots = listOf(firstShot, secondShot),
        accountScope = "acct-1",
        principal = "alice@example.local",
        serverUrl = "http://10.0.2.2:8080",
        accountIds = listOf("acct-1"),
        device = DeviceFacts("1.4.2", "abc1234", "16", 36, "Google", "sdk_gphone64_arm64"),
        sync = SyncFacts(state = "failed", lastError = "the server could not be reached", offline = true),
        outbox = OutboxSummary(
            queued = 1,
            entries = listOf(OutboxLine("SEND", "QUEUED", 0, "Send: quarterly numbers")),
        ),
        push = PushFacts(choice = "AUTOMATIC", transport = "fcm", registered = true),
        logs = listOf(LogLine(1_700_000_000_000, LogLevel.INFO, "herold.shell", "shell state=mail")),
        sessionDetails = mapOf("grantId" to "grant-9"),
    )

    private val report = PendingBugReport(
        startedAtMs = 1_700_000_000_000,
        submission = BugSubmission(title = "blank after sync", note = "twice in a row"),
        capture = capture,
    )

    /** The pictures as the store would hold them, keyed by their file. */
    private fun pictures(): Map<String, ByteArray> =
        capture.shots.mapIndexedNotNull { at, shot ->
            shot.screenshot?.let { PendingBugReports.screenshotFile(at + 1) to it }
        }.toMap()

    private fun roundTrip(): PendingBugReport {
        val text = PendingBugReports.encode(report)
        val files = pictures()
        return PendingBugReports.decode(text) { files[it] }
            ?: error("the held report did not read back")
    }

    @Test
    fun theCapturesComeBackInOrderWithTheirScreensAndPictures() {
        val back = roundTrip()
        assertEquals(2, back.captureCount)
        assertEquals(listOf("thread/{accountId}/{threadId}", "inbox"), back.capture.shots.map { it.route })
        assertEquals(mapOf("accountId" to "acct-1", "threadId" to "T42"), back.capture.shots[0].routeArguments)
        assertEquals("T42", back.capture.shots[0].threadId)
        assertEquals(1_700_000_060_000, back.capture.shots[1].capturedAtMs)
        assertEquals(2, back.capture.screenshots.size)
        assertTrue(back.capture.shots[0].screenshot!!.contentEquals(firstShot.screenshot!!))
        // The report's own route is still the screen it was raised on.
        assertEquals("thread/{accountId}/{threadId}", back.capture.route)
    }

    @Test
    fun theTextAndTheChecklistComeBackAsTheyWereTyped() {
        val back = roundTrip()
        assertEquals("blank after sync", back.submission.title)
        assertEquals("twice in a row", back.submission.note)
        assertTrue(back.submission.descriptionEntered)
        assertEquals(BugKind.BUG, back.submission.kind)
        assertTrue(back.submission.includeScreenshot)
        assertFalse(back.submission.includeSessionDetails)
    }

    @Test
    fun theStateAroundTheFirstCaptureComesBackWithIt() {
        val facts = roundTrip().capture
        assertEquals("acct-1", facts.accountScope)
        assertEquals("alice@example.local", facts.principal)
        assertEquals("1.4.2", facts.device.appVersion)
        assertEquals(36, facts.device.sdkInt)
        assertEquals("failed", facts.sync.state)
        assertTrue(facts.sync.offline)
        assertEquals(1, facts.outbox.total)
        assertEquals("Send: quarterly numbers", facts.outbox.entries.single().label)
        assertEquals("fcm", facts.push.transport)
    }

    /**
     * The ring never reaches disk (REQ-AND-SYS-52) and the session ids
     * are read again at send time, so neither is in the file a killed
     * process leaves behind.
     */
    @Test
    fun neitherTheLogRingNorTheSessionDetailsAreWrittenDown() {
        val text = PendingBugReports.encode(report)
        assertFalse(text.contains("shell state=mail"), text)
        assertFalse(text.contains("grant-9"), text)
        val back = roundTrip()
        assertTrue(back.capture.logs.isEmpty())
        assertTrue(back.capture.sessionDetails.isEmpty())
    }

    @Test
    fun aReportOlderThanADayIsExpired() {
        assertFalse(report.isExpired(report.startedAtMs + PendingBugReports.TTL_MS - 1))
        assertTrue(report.isExpired(report.startedAtMs + PendingBugReports.TTL_MS))
    }

    @Test
    fun anUnreadableOrEmptyRecordIsNoReportAtAll() {
        assertNull(PendingBugReports.decode("{not json", { null }))
        assertNull(PendingBugReports.decode("""{"version":1,"startedAtMs":1,"shots":[]}""") { null })
        assertNull(
            PendingBugReports.decode(
                """{"version":99,"startedAtMs":1,"shots":[{"route":"inbox"}]}""",
            ) { null },
        )
    }

    /** A capture whose picture file is gone is still the screen it names. */
    @Test
    fun aMissingPictureLeavesTheCapture() {
        val back = PendingBugReports.decode(PendingBugReports.encode(report)) { null }
            ?: error("the held report did not read back")
        assertEquals(2, back.captureCount)
        assertTrue(back.capture.screenshots.isEmpty())
        assertEquals("inbox", back.capture.shots[1].route)
    }
}
