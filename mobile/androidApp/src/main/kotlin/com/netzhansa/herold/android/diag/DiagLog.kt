package com.netzhansa.herold.android.diag

import android.util.Log
import com.netzhansa.herold.shared.diag.LogRing

/**
 * The app's logging front door (REQ-AND-SYS-52). Every call reaches
 * logcat exactly as `android.util.Log` would, and the same line is kept
 * in a bounded in-memory ring a bug report carries - so a report filed
 * minutes after the problem still says what the app was doing, without
 * the maintainer having had a cable attached.
 *
 * The ring redacts on the way in: no credential and no message subject
 * is held (`LogRing`, `Redaction`).
 */
object DiagLog {
    /** What a report reads out of. */
    val ring = LogRing(now = { System.currentTimeMillis() })

    /** Whether the ring keeps what is logged; the settings toggle drives it. */
    var keepLog: Boolean
        get() = ring.enabled
        set(value) {
            ring.enabled = value
        }

    fun i(tag: String, message: String) {
        Log.i(tag, message)
        ring.info(tag, message)
    }

    fun w(tag: String, message: String) {
        Log.w(tag, message)
        ring.warn(tag, message)
    }

    fun e(tag: String, message: String) {
        Log.e(tag, message)
        ring.error(tag, message)
    }
}
