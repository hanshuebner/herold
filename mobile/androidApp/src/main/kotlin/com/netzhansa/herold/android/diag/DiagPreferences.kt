package com.netzhansa.herold.android.diag

import android.content.Context

/**
 * The two settings the in-app reporter carries (REQ-AND-SYS-51/52):
 * whether shaking the phone opens the report sheet, and whether the
 * diagnostic ring keeps anything. Both are on out of the box, because
 * the builds this reporter exists for are the ones the maintainer
 * installs; both are remembered once turned off.
 */
object DiagPreferences {
    private const val FILE = "herold-diag"
    private const val KEY_SHAKE = "shake_to_report"
    private const val KEY_KEEP_LOG = "keep_diagnostic_log"

    fun shakeToReport(context: Context): Boolean =
        prefs(context).getBoolean(KEY_SHAKE, true)

    fun setShakeToReport(context: Context, enabled: Boolean) {
        prefs(context).edit().putBoolean(KEY_SHAKE, enabled).apply()
    }

    fun keepLog(context: Context): Boolean =
        prefs(context).getBoolean(KEY_KEEP_LOG, true)

    fun setKeepLog(context: Context, enabled: Boolean) {
        prefs(context).edit().putBoolean(KEY_KEEP_LOG, enabled).apply()
        DiagLog.keepLog = enabled
    }

    /** Applies the stored setting to the ring at process start. */
    fun apply(context: Context) {
        DiagLog.keepLog = keepLog(context)
    }

    private fun prefs(context: Context) =
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE)
}
