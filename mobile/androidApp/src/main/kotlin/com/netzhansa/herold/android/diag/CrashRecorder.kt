package com.netzhansa.herold.android.diag

import com.netzhansa.herold.shared.diag.CrashRecord
import java.io.PrintWriter
import java.io.StringWriter
import kotlin.system.exitProcess

/**
 * The app's uncaught-exception handler (REQ-AND-SYS-52). It writes the
 * trace and the tail of the diagnostic ring to app storage before the
 * process goes, so the next bug report carries the crash and the
 * maintainer needs no cable to read it (issue #420). The platform's own
 * handler runs after it, so the crash is still reported to the system
 * and the process still dies.
 */
object CrashRecorder {

    /** The screen the shell last drew, so a crash names where it happened. */
    @Volatile
    var route: String? = null

    /**
     * Installs the handler. [version] and [commit] identify the build the
     * trace came from, since a report may be filed from a later one.
     */
    fun install(store: CrashStore, version: String, commit: String) {
        val platform = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, error ->
            runCatching { store.write(record(thread, error, version, commit)) }
            if (platform != null) {
                platform.uncaughtException(thread, error)
            } else {
                exitProcess(EXIT_CODE)
            }
        }
    }

    /** The record a crash becomes, including the causes below it. */
    fun record(thread: Thread, error: Throwable, version: String, commit: String): CrashRecord {
        val writer = StringWriter()
        error.printStackTrace(PrintWriter(writer))
        return CrashRecord(
            atMs = System.currentTimeMillis(),
            threadName = thread.name.orEmpty(),
            exception = error::class.qualifiedName ?: error::class.simpleName.orEmpty(),
            message = error.message.orEmpty(),
            stack = writer.toString(),
            appVersion = version,
            appCommit = commit,
            route = route,
            logs = DiagLog.ring.lines(),
        )
    }

    private const val EXIT_CODE = 2
}
