package com.netzhansa.herold.shared.diag

import kotlinx.datetime.Instant
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * What the app was doing when it died (REQ-AND-SYS-52, issue #420): the
 * uncaught exception's trace, the screen it happened on, and the tail of
 * the diagnostic ring that led up to it.
 *
 * The ring itself still never outlives the process; this is the one
 * exception, written by the uncaught-exception handler so the next bug
 * report carries the crash. It is dropped as soon as a report takes it,
 * and it is redacted the way the ring is - the lines it holds came
 * through [LogRing].
 */
data class CrashRecord(
    val atMs: Long,
    val threadName: String,
    val exception: String,
    val message: String,
    val stack: String,
    val appVersion: String = "",
    val appCommit: String = "",
    /** The route the shell was on, when the shell had named one. */
    val route: String? = null,
    val logs: List<LogLine> = emptyList(),
) {
    /** The exception's first line, as a ticket's title would carry it. */
    val headline: String get() = if (message.isBlank()) exception else "$exception: $message"
}

/** The on-disk form of a crash record. */
@Serializable
data class CrashRecordFile(
    val version: Int = CrashRecords.VERSION,
    val atMs: Long = 0,
    val threadName: String = "",
    val exception: String = "",
    val message: String = "",
    val stack: String = "",
    val appVersion: String = "",
    val appCommit: String = "",
    val route: String? = null,
    val logs: List<CrashLogLine> = emptyList(),
)

/** One held log line, as the crash file keeps it. */
@Serializable
data class CrashLogLine(
    val atMs: Long = 0,
    val level: String = LogLevel.INFO,
    val ctx: String = "",
    val message: String = "",
)

/**
 * Reads and writes the crash record app storage keeps between the death
 * of one process and the next report, and renders the `crash.txt` a
 * bundle carries.
 */
object CrashRecords {
    /** The file's shape, so an older record is dropped rather than misread. */
    const val VERSION = 1

    /** The drop name the trace travels under. */
    const val FILE = "crash.txt"

    /** How many ring lines the record keeps around the crash. */
    const val LOG_TAIL = 200

    /** How many stack frames `report.md` repeats; `crash.txt` carries all. */
    const val MARKDOWN_STACK_LINES = 24

    private val json = Json { ignoreUnknownKeys = true }

    fun encode(record: CrashRecord): String =
        json.encodeToString(CrashRecordFile.serializer(), file(record))

    fun file(record: CrashRecord): CrashRecordFile = CrashRecordFile(
        atMs = record.atMs,
        threadName = record.threadName,
        exception = record.exception,
        message = record.message,
        stack = record.stack,
        appVersion = record.appVersion,
        appCommit = record.appCommit,
        route = record.route,
        logs = record.logs.takeLast(LOG_TAIL).map {
            CrashLogLine(it.atMs, it.level, it.ctx, it.message)
        },
    )

    /**
     * The record [text] describes, or null when it is of another shape
     * or cannot be parsed at all: an unreadable file is dropped rather
     * than failing the report that reads it.
     */
    fun decode(text: String): CrashRecord? {
        val held = runCatching { json.decodeFromString(CrashRecordFile.serializer(), text) }
            .getOrNull() ?: return null
        if (held.version != VERSION || held.stack.isBlank()) return null
        return CrashRecord(
            atMs = held.atMs,
            threadName = held.threadName,
            exception = held.exception,
            message = held.message,
            stack = held.stack,
            appVersion = held.appVersion,
            appCommit = held.appCommit,
            route = held.route,
            logs = held.logs.map { LogLine(it.atMs, it.level, it.ctx, it.message) },
        )
    }

    /** `crash.txt`: the header, the trace, then the ring that led to it. */
    fun text(record: CrashRecord): String = buildString {
        append("herold Android ").append(record.appVersion)
        if (record.appCommit.isNotBlank()) append(" (").append(record.appCommit).append(")")
        append("\n")
        append("crashed at ").append(timestamp(record.atMs))
        append(" on thread ").append(record.threadName.ifBlank { "unknown" }).append("\n")
        record.route?.takeIf { it.isNotBlank() }?.let { append("route: ").append(it).append("\n") }
        append("\n")
        append(record.stack.trimEnd()).append("\n")
        if (record.logs.isNotEmpty()) {
            append("\n--- diagnostic log before the crash ---\n")
            record.logs.forEach { append(line(it)).append("\n") }
        }
    }

    /** The `## Crash` section `report.md` carries when a record is attached. */
    fun markdown(record: CrashRecord): String = buildString {
        append("## Crash\n\n")
        append("The previous run ended in an uncaught exception; the trace is `")
        append(FILE).append("`.\n\n")
        append("- When: ").append(timestamp(record.atMs)).append("\n")
        append("- Thread: ").append(record.threadName.ifBlank { "unknown" }).append("\n")
        record.route?.takeIf { it.isNotBlank() }?.let { append("- Route: ").append(it).append("\n") }
        append("- Build: ").append(record.appVersion)
        if (record.appCommit.isNotBlank()) append(" (").append(record.appCommit).append(")")
        append("\n\n")
        append("```\n")
        append(record.stack.trimEnd().lines().take(MARKDOWN_STACK_LINES).joinToString("\n"))
        append("\n```\n")
    }

    internal fun line(held: LogLine): String {
        val at = if (held.atMs > 0) timestamp(held.atMs) else ""
        val ctx = if (held.ctx.isBlank()) "" else "[${held.ctx}] "
        return (at + " " + held.level.uppercase() + " " + ctx + held.message).trim()
    }

    private fun timestamp(atMs: Long): String =
        if (atMs > 0) Instant.fromEpochMilliseconds(atMs).toString() else "(unknown)"
}
