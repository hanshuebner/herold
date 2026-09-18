package com.netzhansa.herold.android.diag

import android.content.Context
import com.netzhansa.herold.shared.diag.PendingBugReport
import com.netzhansa.herold.shared.diag.PendingBugReports
import java.io.File

/**
 * Where a report waits while the maintainer walks to the next screen
 * (REQ-AND-SYS-53, issue #424). It lives in the app's private storage,
 * so the captures taken before the app was killed are still there when
 * it comes back, and it is dropped after a day or when the account
 * signs out.
 *
 * What it holds is what the sheet showed: the pictures, the screens
 * they were taken on and the text typed so far. The diagnostic ring is
 * not among them - it never reaches disk (REQ-AND-SYS-52) - and neither
 * are the session details, which are read again when the report is
 * sent.
 */
class PendingReportStore(
    context: Context,
    private val now: () -> Long = { System.currentTimeMillis() },
) {
    private val dir = File(context.applicationContext.filesDir, DIR)

    /**
     * The open report, or null when there is none, when the file cannot
     * be read, or when it has been waiting longer than a day. A report
     * that is too old is deleted as it is read, so the next capture
     * starts clean.
     */
    fun load(): PendingBugReport? {
        val manifest = File(dir, MANIFEST)
        if (!manifest.isFile) return null
        val text = runCatching { manifest.readText() }.getOrNull() ?: run {
            clear()
            return null
        }
        val report = PendingBugReports.decode(text) { name ->
            runCatching { File(dir, name).readBytes() }.getOrNull()
        }
        if (report == null || report.isExpired(now())) {
            clear()
            return null
        }
        return report
    }

    /** Writes [report] over whatever was held, pictures and all. */
    fun save(report: PendingBugReport) {
        runCatching {
            dir.mkdirs()
            dir.listFiles()?.forEach { it.delete() }
            report.capture.shots.forEachIndexed { at, shot ->
                val bytes = shot.screenshot ?: return@forEachIndexed
                File(dir, PendingBugReports.screenshotFile(at + 1)).writeBytes(bytes)
            }
            // The manifest lands last and by a rename, so a kill in the
            // middle of a write leaves the previous report readable
            // rather than half of the new one.
            val staging = File(dir, "$MANIFEST.new")
            staging.writeText(PendingBugReports.encode(report))
            staging.renameTo(File(dir, MANIFEST))
        }.onFailure { DiagLog.w(TAG, "the open report could not be kept: ${it.message}") }
    }

    /** Drops the open report and everything captured for it. */
    fun clear() {
        runCatching {
            dir.listFiles()?.forEach { it.delete() }
            dir.delete()
        }
    }

    private companion object {
        const val DIR = "bug-report-pending"
        const val MANIFEST = "report.json"
        const val TAG = "herold.bugreport"
    }
}
