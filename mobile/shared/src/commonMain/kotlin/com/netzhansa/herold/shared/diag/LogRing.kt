package com.netzhansa.herold.shared.diag

/**
 * One line of the diagnostic ring: when it was written, how loud it was,
 * which subsystem wrote it, and what it said.
 */
data class LogLine(
    val atMs: Long,
    val level: String,
    val ctx: String,
    val message: String,
)

/** The three levels the ring records. */
object LogLevel {
    const val INFO = "info"
    const val WARN = "warn"
    const val ERROR = "error"
}

/**
 * A bounded in-memory record of what the app's own loggers said, so a bug
 * report carries the run-up to the problem (REQ-AND-SYS-52). It holds the
 * most recent [capacity] lines and nothing else: it is never written to
 * disk, it is dropped when the process ends, and the settings toggle can
 * switch it off.
 *
 * Every line passes through [Redaction], so a credential or a subject
 * that reached a log call does not reach the ring.
 */
class LogRing(
    private val capacity: Int = DEFAULT_CAPACITY,
    private val now: () -> Long = { 0L },
) {
    private val lock = DiagLock()
    private val slots = arrayOfNulls<LogLine>(capacity)

    /** Where the next line goes; also the count of lines ever recorded. */
    private var written = 0L

    /**
     * Whether new lines are kept. Switching it off clears what is held,
     * so turning the diagnostic log off actually forgets it.
     */
    var enabled: Boolean = true
        set(value) {
            field = value
            if (!value) clear()
        }

    /** Records one line, dropping the oldest when the ring is full. */
    fun record(level: String, ctx: String, message: String) {
        if (!enabled || capacity <= 0) return
        val line = LogLine(now(), level, ctx, Redaction.scrub(message))
        lock.withLock {
            slots[(written % capacity).toInt()] = line
            written++
        }
    }

    fun info(ctx: String, message: String) = record(LogLevel.INFO, ctx, message)

    fun warn(ctx: String, message: String) = record(LogLevel.WARN, ctx, message)

    fun error(ctx: String, message: String) = record(LogLevel.ERROR, ctx, message)

    /** The lines held, oldest first. */
    fun lines(): List<LogLine> = lock.withLock {
        if (written == 0L) return@withLock emptyList()
        val held = minOf(written, capacity.toLong()).toInt()
        val first = written - held
        (0 until held).mapNotNull { slots[((first + it) % capacity).toInt()] }
    }

    /** How many lines are held. */
    fun size(): Int = lock.withLock { minOf(written, capacity.toLong()).toInt() }

    fun clear() = lock.withLock {
        slots.fill(null)
        written = 0
    }

    companion object {
        /**
         * How many lines a report carries. A few hundred covers the
         * minutes before the gesture without turning the mail into a
         * log dump.
         */
        const val DEFAULT_CAPACITY = 400
    }
}

/**
 * What must not leave the device in a diagnostic line: the bearer
 * credential in any of the shapes a log call can put it in, and the
 * subject of a message, which is user content rather than diagnosis.
 */
object Redaction {
    /** What a removed value is replaced by. */
    const val PLACEHOLDER = "(redacted)"

    private val bearer = Regex("""(?i)\bbearer\s+\S+""")

    private val keyedSecret = Regex(
        """(?i)\b(authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|api[_-]?key|""" +
            """token|password|passphrase|secret|code_verifier)("?\s*[:=]\s*)"?[^\s",;}]+"?""",
    )

    /** The outbox labels that carry a subject after their prefix. */
    private val labelledSubject = Regex("""\b(Send|Draft|Reply):[^"\n]*""")

    /** [text] with every credential and every subject taken out of it. */
    fun scrub(text: String): String {
        var out = bearer.replace(text, "Bearer $PLACEHOLDER")
        out = keyedSecret.replace(out) { match -> match.groupValues[1] + match.groupValues[2] + PLACEHOLDER }
        out = labelledSubject.replace(out) { match -> match.groupValues[1] + ": " + PLACEHOLDER }
        return out
    }

    /**
     * An outbox entry's label with its subject taken out, for the outbox
     * summary a report carries.
     */
    fun outboxLabel(label: String): String = scrub(label)
}
