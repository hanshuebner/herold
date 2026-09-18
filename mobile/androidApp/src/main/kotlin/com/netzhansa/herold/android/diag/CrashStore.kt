package com.netzhansa.herold.android.diag

import android.content.Context
import com.netzhansa.herold.shared.diag.CrashRecord
import com.netzhansa.herold.shared.diag.CrashRecords
import java.io.File

/**
 * Where the trace of a crash waits for the next bug report (issue #420).
 * The diagnostic ring dies with the process, so a report filed after a
 * crash says nothing about it unless the crash itself was written down:
 * the uncaught-exception handler puts the trace and the ring's tail here,
 * the next report carries them as `crash.txt`, and the record is dropped
 * as the report takes it.
 *
 * It also carries the marker the shell reads on the next start: after an
 * abnormal exit the shell opens the inbox rather than restoring the
 * screen the app died on, so a crash is not repeated unattended.
 */
class CrashStore(context: Context) {
    private val dir = File(context.applicationContext.filesDir, DIR)

    /** The most recent crash, or null when the app exited normally. */
    fun load(): CrashRecord? {
        val file = File(dir, RECORD)
        if (!file.isFile) return null
        val text = runCatching { file.readText() }.getOrNull() ?: run {
            clear()
            return null
        }
        return CrashRecords.decode(text) ?: run {
            clear()
            null
        }
    }

    /** True when a record is held, without parsing it. */
    fun isHeld(): Boolean = File(dir, RECORD).isFile

    /**
     * Writes [record] over whatever was held and raises the marker that
     * keeps the next start off the screen the app died on. Runs inside
     * the uncaught-exception handler, so it swallows its own failures:
     * a crash that cannot be written down is still a crash.
     */
    fun write(record: CrashRecord) {
        runCatching {
            dir.mkdirs()
            val staging = File(dir, "$RECORD.new")
            staging.writeText(CrashRecords.encode(record))
            staging.renameTo(File(dir, RECORD))
            File(dir, RESTORE_BLOCK).writeText(record.atMs.toString())
        }
    }

    /**
     * Whether this start follows a crash, taking the marker with it: the
     * shell asks once per process, so the screen is restored normally on
     * every later start even while the trace waits for a report.
     */
    fun consumeRestoreBlock(): Boolean {
        val marker = File(dir, RESTORE_BLOCK)
        if (!marker.isFile) return false
        runCatching { marker.delete() }
        return true
    }

    /** Drops the held record, once a report carries it. */
    fun clear() {
        runCatching {
            File(dir, RECORD).delete()
            File(dir, "$RECORD.new").delete()
        }
    }

    private companion object {
        const val DIR = "crash"
        const val RECORD = "crash.json"
        const val RESTORE_BLOCK = "restore-blocked"
    }
}
