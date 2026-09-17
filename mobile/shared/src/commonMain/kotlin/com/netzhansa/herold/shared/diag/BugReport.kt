package com.netzhansa.herold.shared.diag

import kotlinx.datetime.Instant
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.add
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject

/** What the report is: a defect, or something the client should do. */
enum class BugKind(val wire: String) {
    BUG("bug"),
    FEATURE("feature"),
}

/** One outbox entry as the report describes it, with its subject dropped. */
data class OutboxLine(
    val kind: String,
    val state: String,
    val attempts: Int,
    val label: String,
    val lastError: String? = null,
)

/** What the queue looked like at the moment of the gesture. */
data class OutboxSummary(
    val queued: Int = 0,
    val sending: Int = 0,
    val failed: Int = 0,
    val entries: List<OutboxLine> = emptyList(),
) {
    val total: Int get() = queued + sending + failed
}

/** What the phone is and what the app running on it was built from. */
data class DeviceFacts(
    val appVersion: String,
    val appCommit: String,
    val androidVersion: String,
    val sdkInt: Int,
    val manufacturer: String,
    val model: String,
)

/** Where the reconciler stood. */
data class SyncFacts(
    val state: String,
    val lastError: String? = null,
    val offline: Boolean = false,
)

/** Which channel carries push and whether it is registered. */
data class PushFacts(
    val choice: String,
    val transport: String? = null,
    val distributor: String? = null,
    val registered: Boolean = false,
    val permissionGranted: Boolean = false,
)

/**
 * Everything the reporter captured at the moment of the gesture, before
 * the sheet opened (REQ-AND-SYS-51). None of it is user content: no
 * message bodies, no subjects, no credential.
 */
data class BugCapture(
    val route: String,
    val routeArguments: Map<String, String> = emptyMap(),
    val accountScope: String? = null,
    val threadId: String? = null,
    val principal: String? = null,
    val serverUrl: String? = null,
    val accountIds: List<String> = emptyList(),
    val device: DeviceFacts,
    val sync: SyncFacts,
    val outbox: OutboxSummary = OutboxSummary(),
    val push: PushFacts,
    val logs: List<LogLine> = emptyList(),
    /** PNG bytes, in capture order; the window as it was. */
    val screenshots: List<ByteArray> = emptyList(),
    /**
     * The session facts the maintainer may include to reproduce, held
     * apart from everything ticket-eligible. The bearer credential is
     * never among them: it does not leave Keystore-backed storage.
     */
    val sessionDetails: Map<String, String> = emptyMap(),
)

/**
 * What the maintainer ticked in the sheet, and the text they typed if
 * they typed any. Both texts are optional: a report is sent with one
 * tap and described on the desktop, where there is a keyboard
 * (`/bug-inbox`, issue #408).
 */
data class BugSubmission(
    val title: String = "",
    val note: String = "",
    val kind: BugKind = BugKind.BUG,
    val includeScreenshot: Boolean = true,
    val includeLogs: Boolean = true,
    val includeSessionDetails: Boolean = false,
) {
    /** True when the maintainer described the problem on the phone. */
    val descriptionEntered: Boolean get() = title.isNotBlank() || note.isNotBlank()
}

/** One part of the bundle as the mail carries it. */
data class BugBundleFile(val name: String, val type: String, val bytes: ByteArray) {
    override fun equals(other: Any?): Boolean =
        other is BugBundleFile && name == other.name && type == other.type && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = (name.hashCode() * 31 + type.hashCode()) * 31 + bytes.contentHashCode()
}

/** The mail a report becomes: its subject, its body, and its parts. */
data class BugBundle(
    val subject: String,
    val bodyText: String,
    val files: List<BugBundleFile>,
)

/**
 * Builds the drop `herold bug-fetch` expands (`internal/admin/cmd_bugfetch.go`):
 * `report.json` in the browser panel's public-meta shape, `report.md`,
 * `logs.txt`, `screenshot-N.png`, and `private.json` when the maintainer
 * asked for the session details. The mail carries them as separate parts
 * and its body is `report.md`, so a report reads as mail even before the
 * triage tooling touches it.
 */
object BugBundleWriter {
    /** What the reporter puts in front of the title. */
    const val SUBJECT_PREFIX = "herold bug:"

    /** The label the sent copy is filed under, which the fetch queries. */
    const val LABEL = "Bug reports"

    /** The bundle shape's version, as the drop's `protocol` field. */
    const val PROTOCOL = "herold-bug-mail/1"

    /** The app the drop says it came from. */
    const val APP_ID = "herold-android"
    const val APP_NAME = "herold Android"

    private val json = Json { prettyPrint = true }

    fun build(submission: BugSubmission, capture: BugCapture, createdAtMs: Long): BugBundle {
        val createdAt = Instant.fromEpochMilliseconds(createdAtMs).toString()
        val logs = if (submission.includeLogs) capture.logs else emptyList()
        val screenshots = if (submission.includeScreenshot) capture.screenshots else emptyList()
        val meta = reportJson(submission, capture, createdAt, logs, screenshots.size)
        val markdown = reportMarkdown(submission, capture, createdAt, logs)

        val files = mutableListOf(
            BugBundleFile("report.json", "application/json", (json.encodeToString(JsonObject.serializer(), meta) + "\n").encodeToByteArray()),
            BugBundleFile("report.md", "text/markdown", markdown.encodeToByteArray()),
            BugBundleFile("logs.txt", "text/plain", logsTxt(logs).encodeToByteArray()),
        )
        screenshots.forEachIndexed { index, bytes ->
            files += BugBundleFile("screenshot-${index + 1}.png", "image/png", bytes)
        }
        if (submission.includeSessionDetails && capture.sessionDetails.isNotEmpty()) {
            val private = buildJsonObject {
                put("note", "repro-only; never quoted into a ticket")
                capture.sessionDetails.forEach { (key, value) -> put(key, value) }
            }
            files += BugBundleFile(
                "private.json",
                "application/json",
                (json.encodeToString(JsonObject.serializer(), private) + "\n").encodeToByteArray(),
            )
        }
        return BugBundle(
            subject = subject(submission, capture, createdAt),
            bodyText = markdown,
            files = files,
        )
    }

    /**
     * The mail's subject, which is also how the fetch recognises a
     * report. A described report is named by its title; an undescribed
     * one by where it was raised and when, so a Sent folder of one-tap
     * reports still distinguishes them.
     */
    fun subject(submission: BugSubmission, capture: BugCapture, createdAt: String): String {
        val title = submission.title.trim()
        if (title.isNotEmpty()) return "$SUBJECT_PREFIX $title"
        return "$SUBJECT_PREFIX ${routeLabel(capture.route)} $createdAt"
    }

    /**
     * The route without its ids, for a subject line: `thread` rather
     * than `thread/{accountId}/{threadId}`.
     */
    fun routeLabel(route: String): String =
        route.trim().trimStart('/').substringBefore('/').ifBlank { "unknown" }

    /**
     * What the sketch reads: the title, then the note under it. Empty
     * when neither was typed, which is what `descriptionEntered` says
     * and what the desktop fills in.
     */
    fun sketch(submission: BugSubmission): String {
        val title = submission.title.trim()
        val note = submission.note.trim()
        return listOf(title, note).filter { it.isNotEmpty() }.joinToString("\n\n")
    }

    private fun reportJson(
        submission: BugSubmission,
        capture: BugCapture,
        createdAt: String,
        logs: List<LogLine>,
        screenshotCount: Int,
    ): JsonObject = buildJsonObject {
        put("protocol", PROTOCOL)
        put("createdAt", createdAt)
        put("kind", submission.kind.wire)
        // Whether the sketch is the maintainer's words or a blank the
        // desktop has to fill in (issue #408).
        put("descriptionEntered", submission.descriptionEntered)
        put("sketch", sketch(submission))
        putJsonObject("page") {
            put("url", routeUrl(capture))
            put("title", capture.route)
        }
        putJsonObject("app") {
            put("id", APP_ID)
            put("name", APP_NAME)
            put("version", versionLabel(capture.device))
        }
        putJsonObject("principal") {
            put("id", JsonPrimitive(capture.accountScope))
            put("label", capture.principal.orEmpty())
        }
        put("context", contextJson(capture))
        putJsonArray("logs") {
            logs.forEach { line ->
                addJsonObject {
                    put("ts", line.atMs)
                    put("level", line.level)
                    put("ctx", line.ctx)
                    put("msg", line.message)
                }
            }
        }
        put("screenshotCount", screenshotCount)
    }

    /** The version string a ticket's environment line carries. */
    fun versionLabel(device: DeviceFacts): String =
        device.appVersion + if (device.appCommit.isBlank()) "" else " (${device.appCommit})"

    /** The route as a URL, so the drop's `page.url` names a place. */
    fun routeUrl(capture: BugCapture): String {
        val query = capture.routeArguments.entries
            .filter { it.value.isNotBlank() }
            .joinToString("&") { "${it.key}=${it.value}" }
        return "herold://" + capture.route.trimStart('/') + if (query.isBlank()) "" else "?$query"
    }

    private fun contextJson(capture: BugCapture): JsonObject = buildJsonObject {
        put("route", capture.route)
        putJsonObject("routeArguments") {
            capture.routeArguments.forEach { (key, value) -> put(key, value) }
        }
        put("accountScope", JsonPrimitive(capture.accountScope))
        put("threadId", JsonPrimitive(capture.threadId))
        put("serverUrl", JsonPrimitive(capture.serverUrl))
        putJsonArray("accountIds") { capture.accountIds.forEach { add(it) } }
        putJsonObject("device") {
            put("appVersion", capture.device.appVersion)
            put("appCommit", capture.device.appCommit)
            put("androidVersion", capture.device.androidVersion)
            put("sdkInt", capture.device.sdkInt)
            put("manufacturer", capture.device.manufacturer)
            put("model", capture.device.model)
        }
        putJsonObject("sync") {
            put("state", capture.sync.state)
            put("lastError", JsonPrimitive(capture.sync.lastError))
            put("offline", capture.sync.offline)
        }
        putJsonObject("outbox") {
            put("total", capture.outbox.total)
            put("queued", capture.outbox.queued)
            put("sending", capture.outbox.sending)
            put("failed", capture.outbox.failed)
            putJsonArray("entries") {
                capture.outbox.entries.forEach { entry ->
                    addJsonObject {
                        put("kind", entry.kind)
                        put("state", entry.state)
                        put("attempts", entry.attempts)
                        put("label", Redaction.outboxLabel(entry.label))
                        put("lastError", JsonPrimitive(entry.lastError))
                    }
                }
            }
        }
        putJsonObject("push") {
            put("choice", capture.push.choice)
            put("transport", JsonPrimitive(capture.push.transport))
            put("distributor", JsonPrimitive(capture.push.distributor))
            put("registered", capture.push.registered)
            put("permissionGranted", capture.push.permissionGranted)
        }
    }

    /**
     * The mail's body, in the sections `herold bug-sink` renders a
     * browser drop into, so a phone report and a browser report read the
     * same way in a ticket.
     */
    fun reportMarkdown(
        submission: BugSubmission,
        capture: BugCapture,
        createdAt: String,
        logs: List<LogLine>,
    ): String = buildString {
        if (!submission.descriptionEntered) append(NO_DESCRIPTION).append("\n\n")
        val heading = submission.title.trim().ifBlank {
            routeLabel(capture.route) + " " + createdAt
        }
        append("# ").append(submission.kind.wire.replaceFirstChar { it.uppercase() })
        append(": ").append(heading).append("\n\n")

        if (submission.descriptionEntered) {
            append("## Description\n\n")
            append(sketch(submission)).append("\n\n")
        }

        append("## Page\n\n")
        append("- URL: ").append(routeUrl(capture)).append("\n")
        append("- Title: ").append(capture.route).append("\n\n")

        append("## App\n\n")
        append(APP_NAME).append(" ").append(versionLabel(capture.device)).append("\n")
        append("Android ").append(capture.device.androidVersion)
        append(" (API ").append(capture.device.sdkInt).append("), ")
        append(capture.device.manufacturer).append(" ").append(capture.device.model).append("\n\n")

        append("## Principal\n\n")
        append(capture.principal?.takeIf { it.isNotBlank() } ?: "(not reported)").append("\n\n")

        append("## State\n\n")
        append("- Captured: ").append(createdAt).append("\n")
        append("- Sync: ").append(capture.sync.state)
        capture.sync.lastError?.let { append(" (last error: ").append(it).append(")") }
        append("\n")
        append("- Offline: ").append(capture.sync.offline).append("\n")
        append("- Outbox: ").append(capture.outbox.total).append(" entries")
        append(" (").append(capture.outbox.queued).append(" queued, ")
        append(capture.outbox.sending).append(" sending, ")
        append(capture.outbox.failed).append(" failed)\n")
        capture.outbox.entries.forEach { entry ->
            append("  - ").append(entry.kind).append(" ").append(entry.state)
            append(", ").append(entry.attempts).append(" attempts: ")
            append(Redaction.outboxLabel(entry.label))
            entry.lastError?.let { append(" - ").append(it) }
            append("\n")
        }
        append("- Push: ").append(capture.push.choice)
        capture.push.transport?.let { append(" -> ").append(it) }
        append(", registered=").append(capture.push.registered)
        append(", notifications=").append(capture.push.permissionGranted).append("\n")
        capture.accountScope?.let { append("- Account scope: ").append(it).append("\n") }
        capture.threadId?.let { append("- Thread: ").append(it).append("\n") }
        append("\n")

        append("## Logs (tail)\n\n")
        val tail = logs.takeLast(MARKDOWN_LOG_TAIL)
        if (tail.isEmpty()) {
            append("(none)\n")
        } else {
            tail.forEach { append("- ").append(formatLine(it)).append("\n") }
        }
    }

    /** Every held line, one per line, as `logs.txt`. */
    fun logsTxt(logs: List<LogLine>): String =
        if (logs.isEmpty()) "" else logs.joinToString("\n") { formatLine(it) } + "\n"

    private fun formatLine(line: LogLine): String {
        val at = if (line.atMs > 0) Instant.fromEpochMilliseconds(line.atMs).toString() else ""
        val ctx = if (line.ctx.isBlank()) "" else "[${line.ctx}] "
        return (at + " " + line.level.uppercase() + " " + ctx + line.message).trim()
    }

    /**
     * What `report.md` opens with when the report was sent with one tap.
     * `/bug-inbox` reads it as the prompt to ask for the description on
     * the desktop (issue #408).
     */
    const val NO_DESCRIPTION = "No description entered on the phone."

    /** How many log lines the mail body repeats; `logs.txt` carries them all. */
    private const val MARKDOWN_LOG_TAIL = 50
}
