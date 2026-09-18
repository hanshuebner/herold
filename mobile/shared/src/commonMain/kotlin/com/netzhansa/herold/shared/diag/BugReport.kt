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
 * One screen a report was taken on (REQ-AND-SYS-53): the window as a
 * PNG and where the user stood when they asked. A report carries one
 * per capture, in the order they were taken, so a problem that needs
 * more than one picture is one report (issue #424).
 */
data class BugShot(
    val route: String,
    val routeArguments: Map<String, String> = emptyMap(),
    val threadId: String? = null,
    val capturedAtMs: Long = 0,
    /** PNG bytes, the window as it was; absent when the capture failed. */
    val screenshot: ByteArray? = null,
) {
    override fun equals(other: Any?): Boolean =
        other is BugShot &&
            route == other.route &&
            routeArguments == other.routeArguments &&
            threadId == other.threadId &&
            capturedAtMs == other.capturedAtMs &&
            screenshotEquals(other.screenshot)

    private fun screenshotEquals(other: ByteArray?): Boolean = when {
        screenshot == null -> other == null
        other == null -> false
        else -> screenshot.contentEquals(other)
    }

    override fun hashCode(): Int {
        var result = route.hashCode()
        result = 31 * result + routeArguments.hashCode()
        result = 31 * result + (threadId?.hashCode() ?: 0)
        result = 31 * result + capturedAtMs.hashCode()
        result = 31 * result + (screenshot?.contentHashCode() ?: 0)
        return result
    }
}

/**
 * Everything the reporter captured at the moment of the gesture, before
 * the sheet opened (REQ-AND-SYS-51). None of it is user content: no
 * message bodies, no subjects, no credential.
 *
 * The screens are in [shots]; the report's own route is the first
 * capture's, which is where the maintainer started complaining. The
 * facts around them - the build, the reconciler, the queue and push -
 * are the first capture's too, while the log ring is taken at send
 * time, so it covers the whole stretch between the captures.
 */
data class BugCapture(
    val shots: List<BugShot> = emptyList(),
    val accountScope: String? = null,
    val principal: String? = null,
    val serverUrl: String? = null,
    val accountIds: List<String> = emptyList(),
    val device: DeviceFacts,
    val sync: SyncFacts,
    val outbox: OutboxSummary = OutboxSummary(),
    val push: PushFacts,
    val logs: List<LogLine> = emptyList(),
    /**
     * The session facts the maintainer may include to reproduce, held
     * apart from everything ticket-eligible. The bearer credential is
     * never among them: it does not leave Keystore-backed storage.
     */
    val sessionDetails: Map<String, String> = emptyMap(),
) {
    /** Where the report was raised: the first capture's route. */
    val route: String get() = shots.firstOrNull()?.route ?: UNKNOWN_ROUTE

    /** The first capture's route arguments. */
    val routeArguments: Map<String, String> get() = shots.firstOrNull()?.routeArguments.orEmpty()

    /** The conversation the report was raised on, when it was raised on one. */
    val threadId: String? get() = shots.firstOrNull()?.threadId

    /** The pictures, in capture order, skipping a capture that has none. */
    val screenshots: List<ByteArray> get() = shots.mapNotNull { it.screenshot }

    /** The same report with one more screen on it. */
    fun withShot(shot: BugShot): BugCapture = copy(shots = shots + shot)

    /** The same report with the capture at [index] taken off the strip. */
    fun withoutShot(index: Int): BugCapture =
        copy(shots = shots.filterIndexed { at, _ -> at != index })

    companion object {
        /** What a report says when the shell could not name the screen. */
        const val UNKNOWN_ROUTE = "unknown"
    }
}

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

/** One file of the bundle, as the request carries it. */
data class BugBundleFile(val name: String, val type: String, val bytes: ByteArray) {
    override fun equals(other: Any?): Boolean =
        other is BugBundleFile && name == other.name && type == other.type && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = (name.hashCode() * 31 + type.hashCode()) * 31 + bytes.contentHashCode()
}

/** The drop a report becomes: what it is called, and its files. */
data class BugBundle(
    val title: String,
    val files: List<BugBundleFile>,
) {
    /** One file's bytes, by its drop name. */
    fun bytes(name: String): ByteArray? = files.firstOrNull { it.name == name }?.bytes

    /** `report.md`, which is what a ticket is written from. */
    val markdown: String get() = bytes("report.md")?.decodeToString().orEmpty()
}

/**
 * Builds the drop `herold bug-fetch` expands (`internal/admin/cmd_bugfetch.go`):
 * `report.json` in the browser panel's public-meta shape, `report.md`,
 * `logs.txt`, `screenshot-N.png`, and `private.json` when the maintainer
 * asked for the session details. They travel as the parts of one
 * `POST /api/v1/bug-reports` (issue #416), each under its drop name, and
 * the server writes the drop directory under those names.
 */
object BugBundleWriter {
    /** The bundle shape's version, as the drop's `protocol` field. */
    const val PROTOCOL = "herold-bug-mail/1"

    /** The app the drop says it came from. */
    const val APP_ID = "herold-android"
    const val APP_NAME = "herold Android"

    private val json = Json { prettyPrint = true }

    /**
     * The drop for [submission] and [capture]. [crash] is the trace the
     * uncaught-exception handler kept from a previous run, when there is
     * one: it travels as `crash.txt` and as a section of `report.md`, so
     * a report filed after a crash carries the stack without a cable
     * (issue #420).
     */
    fun build(
        submission: BugSubmission,
        capture: BugCapture,
        createdAtMs: Long,
        crash: CrashRecord? = null,
    ): BugBundle {
        val createdAt = Instant.fromEpochMilliseconds(createdAtMs).toString()
        val logs = if (submission.includeLogs) capture.logs else emptyList()
        // A picture is named by the capture it belongs to, so
        // `screenshot-2.png` is the screen `captures[1]` describes even
        // when an earlier capture took none.
        val pictures = if (submission.includeScreenshot) {
            capture.shots.mapIndexedNotNull { at, shot -> shot.screenshot?.let { (at + 1) to it } }
        } else {
            emptyList()
        }
        val meta = reportJson(submission, capture, createdAt, logs, pictures.size, crash)
        val markdown = reportMarkdown(submission, capture, createdAt, logs, crash)

        val files = mutableListOf(
            BugBundleFile("report.json", "application/json", (json.encodeToString(JsonObject.serializer(), meta) + "\n").encodeToByteArray()),
            BugBundleFile("report.md", "text/markdown", markdown.encodeToByteArray()),
            BugBundleFile("logs.txt", "text/plain", logsTxt(logs).encodeToByteArray()),
        )
        pictures.forEach { (index, bytes) ->
            files += BugBundleFile("screenshot-$index.png", "image/png", bytes)
        }
        crash?.let {
            files += BugBundleFile(CrashRecords.FILE, "text/plain", CrashRecords.text(it).encodeToByteArray())
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
        return BugBundle(title = title(submission, capture, createdAt), files = files)
    }

    /**
     * What the report is called, in `report.json` and on the outbox row.
     * A described report is named by its title; an undescribed one by
     * where it was raised and when, so a queue of one-tap reports still
     * distinguishes them.
     */
    fun title(submission: BugSubmission, capture: BugCapture, createdAt: String): String {
        val title = submission.title.trim()
        if (title.isNotEmpty()) return title
        return "${routeLabel(capture.route)} $createdAt"
    }

    /**
     * The route without its ids, for a title: `thread` rather than
     * `thread/{accountId}/{threadId}`.
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
        crash: CrashRecord?,
    ): JsonObject = buildJsonObject {
        put("protocol", PROTOCOL)
        put("createdAt", createdAt)
        put("kind", submission.kind.wire)
        // What the report is called, which is the row /bug-inbox and
        // `herold bug-fetch` list it by.
        put("title", title(submission, capture, createdAt))
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
        // Every screen the report was taken on, in capture order. The
        // report's own route stays the first one's, which is where the
        // maintainer started (issue #424).
        putJsonArray("captures") {
            capture.shots.forEachIndexed { at, shot ->
                addJsonObject {
                    put("index", at + 1)
                    put("route", shot.route)
                    putJsonObject("routeArguments") {
                        shot.routeArguments.forEach { (key, value) -> put(key, value) }
                    }
                    put("threadId", JsonPrimitive(shot.threadId))
                    put("capturedAt", Instant.fromEpochMilliseconds(shot.capturedAtMs).toString())
                }
            }
        }
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
        // The crash the previous run ended in, when the report carries
        // one; the trace itself is `crash.txt` (issue #420).
        crash?.let { record ->
            putJsonObject("crash") {
                put("at", Instant.fromEpochMilliseconds(record.atMs).toString())
                put("thread", record.threadName)
                put("exception", record.exception)
                put("message", record.message)
                put("route", JsonPrimitive(record.route))
                put("file", CrashRecords.FILE)
            }
        }
    }

    /** The version string a ticket's environment line carries. */
    fun versionLabel(device: DeviceFacts): String =
        device.appVersion + if (device.appCommit.isBlank()) "" else " (${device.appCommit})"

    /** The route as a URL, so the drop's `page.url` names a place. */
    fun routeUrl(capture: BugCapture): String = routeUrl(capture.route, capture.routeArguments)

    /** The same, for one capture of a report that carries several. */
    fun routeUrl(shot: BugShot): String = routeUrl(shot.route, shot.routeArguments)

    private fun routeUrl(route: String, arguments: Map<String, String>): String {
        val query = arguments.entries
            .filter { it.value.isNotBlank() }
            .joinToString("&") { "${it.key}=${it.value}" }
        return "herold://" + route.trimStart('/') + if (query.isBlank()) "" else "?$query"
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
     * `report.md`, in the sections `herold bug-sink` renders a browser
     * drop into, so a phone report and a browser report read the same
     * way in a ticket.
     */
    fun reportMarkdown(
        submission: BugSubmission,
        capture: BugCapture,
        createdAt: String,
        logs: List<LogLine>,
        crash: CrashRecord? = null,
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

        if (capture.shots.isNotEmpty()) {
            append("## Captures\n\n")
            capture.shots.forEachIndexed { at, shot ->
                val index = at + 1
                append(index).append(". ").append(routeUrl(shot))
                append(" - ").append(Instant.fromEpochMilliseconds(shot.capturedAtMs).toString())
                if (submission.includeScreenshot && shot.screenshot != null) {
                    append(" (screenshot-").append(index).append(".png)")
                }
                append("\n")
            }
            append("\n")
        }

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

        crash?.let { append(CrashRecords.markdown(it)).append("\n") }

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
