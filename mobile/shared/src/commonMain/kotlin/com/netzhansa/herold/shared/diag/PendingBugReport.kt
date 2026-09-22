package com.netzhansa.herold.shared.diag

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * A report the maintainer is still adding screens to (REQ-AND-SYS-53,
 * issue #424): what has been captured, what has been typed, and when it
 * was started. It is held while the maintainer walks to the next screen
 * and is written to app storage, so a process death between two
 * captures does not throw the first one away.
 */
data class PendingBugReport(
    val startedAtMs: Long,
    val submission: BugSubmission,
    val capture: BugCapture,
) {
    /** How many screens the report carries, which is what the marker shows. */
    val captureCount: Int get() = capture.shots.size

    /** True once the report is older than a day and is dropped unsent. */
    fun isExpired(nowMs: Long): Boolean = nowMs - startedAtMs >= PendingBugReports.TTL_MS
}

/**
 * The on-disk form of a pending report. The log ring is not part of it:
 * the ring never reaches disk (REQ-AND-SYS-52) and the bundle takes it
 * at send time anyway. Neither are the session details, which are read
 * again when the report is sent, so no grant id sits in a file waiting
 * for a report that is never finished.
 */
@Serializable
data class PendingBugReportRecord(
    val version: Int = PendingBugReports.VERSION,
    val startedAtMs: Long = 0,
    val title: String = "",
    val note: String = "",
    val kind: String = BugKind.BUG.wire,
    val includeScreenshot: Boolean = true,
    val includeLogs: Boolean = true,
    val includeSessionDetails: Boolean = false,
    val shots: List<PendingShotRecord> = emptyList(),
    val facts: PendingFactsRecord = PendingFactsRecord(),
)

/** One capture, with its picture named by the file that holds it. */
@Serializable
data class PendingShotRecord(
    val route: String = BugCapture.UNKNOWN_ROUTE,
    val routeArguments: Map<String, String> = emptyMap(),
    val threadId: String? = null,
    val capturedAtMs: Long = 0,
    val file: String? = null,
)

/** The state around the first capture, as the report will report it. */
@Serializable
data class PendingFactsRecord(
    val accountScope: String? = null,
    val principal: String? = null,
    val serverUrl: String? = null,
    val accountIds: List<String> = emptyList(),
    val appVersion: String = "",
    val appCommit: String = "",
    val androidVersion: String = "",
    val sdkInt: Int = 0,
    val manufacturer: String = "",
    val model: String = "",
    val syncState: String = "",
    val syncLastError: String? = null,
    val offline: Boolean = false,
    val outbox: List<PendingOutboxRecord> = emptyList(),
    val outboxQueued: Int = 0,
    val outboxSending: Int = 0,
    val outboxFailed: Int = 0,
    val outboxDeferred: Int = 0,
    val pushChoice: String = "",
    val pushTransport: String? = null,
    val pushDistributor: String? = null,
    val pushRegistered: Boolean = false,
    val notificationsGranted: Boolean = false,
)

/** One queued mutation as the report describes it, with its subject dropped. */
@Serializable
data class PendingOutboxRecord(
    val kind: String = "",
    val state: String = "",
    val attempts: Int = 0,
    val label: String = "",
    val lastError: String? = null,
)

/**
 * Turns a pending report into the record app storage keeps, and back.
 * The pictures travel as files beside the record, named by
 * [screenshotFile], so re-reading a report after a process death is a
 * read of the manifest plus one read per capture.
 */
object PendingBugReports {
    /** The record's shape, so an older file is dropped rather than misread. */
    const val VERSION = 1

    /** How long an unfinished report is kept before it is dropped. */
    const val TTL_MS = 24L * 60 * 60 * 1000

    private val json = Json { ignoreUnknownKeys = true }

    /** The file that holds the picture of capture [index] (1-based). */
    fun screenshotFile(index: Int): String = "capture-$index.png"

    fun encode(report: PendingBugReport): String =
        json.encodeToString(PendingBugReportRecord.serializer(), record(report))

    /**
     * The record for [report]. A capture with no picture is kept: the
     * screen it names is still part of the report.
     */
    fun record(report: PendingBugReport): PendingBugReportRecord {
        val capture = report.capture
        val submission = report.submission
        return PendingBugReportRecord(
            startedAtMs = report.startedAtMs,
            title = submission.title,
            note = submission.note,
            kind = submission.kind.wire,
            includeScreenshot = submission.includeScreenshot,
            includeLogs = submission.includeLogs,
            includeSessionDetails = submission.includeSessionDetails,
            shots = capture.shots.mapIndexed { at, shot ->
                PendingShotRecord(
                    route = shot.route,
                    routeArguments = shot.routeArguments,
                    threadId = shot.threadId,
                    capturedAtMs = shot.capturedAtMs,
                    file = shot.screenshot?.let { screenshotFile(at + 1) },
                )
            },
            facts = PendingFactsRecord(
                accountScope = capture.accountScope,
                principal = capture.principal,
                serverUrl = capture.serverUrl,
                accountIds = capture.accountIds,
                appVersion = capture.device.appVersion,
                appCommit = capture.device.appCommit,
                androidVersion = capture.device.androidVersion,
                sdkInt = capture.device.sdkInt,
                manufacturer = capture.device.manufacturer,
                model = capture.device.model,
                syncState = capture.sync.state,
                syncLastError = capture.sync.lastError,
                offline = capture.sync.offline,
                outbox = capture.outbox.entries.map {
                    PendingOutboxRecord(it.kind, it.state, it.attempts, it.label, it.lastError)
                },
                outboxQueued = capture.outbox.queued,
                outboxSending = capture.outbox.sending,
                outboxFailed = capture.outbox.failed,
                outboxDeferred = capture.outbox.deferred,
                pushChoice = capture.push.choice,
                pushTransport = capture.push.transport,
                pushDistributor = capture.push.distributor,
                pushRegistered = capture.push.registered,
                notificationsGranted = capture.push.permissionGranted,
            ),
        )
    }

    /**
     * The report [text] describes, with [picture] answering for each
     * capture's file. A record written by another shape, or one that
     * cannot be parsed at all, reads as no pending report: an
     * unreadable file is dropped rather than failing the next capture.
     */
    fun decode(text: String, picture: (String) -> ByteArray?): PendingBugReport? {
        val record = runCatching {
            json.decodeFromString(PendingBugReportRecord.serializer(), text)
        }.getOrNull() ?: return null
        if (record.version != VERSION || record.shots.isEmpty()) return null
        return report(record, picture)
    }

    /** The in-memory report a [record] and its pictures make. */
    fun report(record: PendingBugReportRecord, picture: (String) -> ByteArray?): PendingBugReport {
        val facts = record.facts
        val capture = BugCapture(
            shots = record.shots.map { shot ->
                BugShot(
                    route = shot.route,
                    routeArguments = shot.routeArguments,
                    threadId = shot.threadId,
                    capturedAtMs = shot.capturedAtMs,
                    screenshot = shot.file?.let(picture),
                )
            },
            accountScope = facts.accountScope,
            principal = facts.principal,
            serverUrl = facts.serverUrl,
            accountIds = facts.accountIds,
            device = DeviceFacts(
                appVersion = facts.appVersion,
                appCommit = facts.appCommit,
                androidVersion = facts.androidVersion,
                sdkInt = facts.sdkInt,
                manufacturer = facts.manufacturer,
                model = facts.model,
            ),
            sync = SyncFacts(
                state = facts.syncState,
                lastError = facts.syncLastError,
                offline = facts.offline,
            ),
            outbox = OutboxSummary(
                queued = facts.outboxQueued,
                sending = facts.outboxSending,
                failed = facts.outboxFailed,
                deferred = facts.outboxDeferred,
                entries = facts.outbox.map {
                    OutboxLine(it.kind, it.state, it.attempts, it.label, it.lastError)
                },
            ),
            push = PushFacts(
                choice = facts.pushChoice,
                transport = facts.pushTransport,
                distributor = facts.pushDistributor,
                registered = facts.pushRegistered,
                permissionGranted = facts.notificationsGranted,
            ),
        )
        return PendingBugReport(
            startedAtMs = record.startedAtMs,
            submission = BugSubmission(
                title = record.title,
                note = record.note,
                kind = BugKind.entries.firstOrNull { it.wire == record.kind } ?: BugKind.BUG,
                includeScreenshot = record.includeScreenshot,
                includeLogs = record.includeLogs,
                includeSessionDetails = record.includeSessionDetails,
            ),
            capture = capture,
        )
    }
}
