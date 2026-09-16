package com.netzhansa.herold.shared.diag

/**
 * The mutual exclusion the diagnostic ring needs. Its writers are the
 * app's log calls, which arrive on whatever thread did the logging, and
 * its reader is the bug reporter; a plain lock is enough and a coroutine
 * mutex is not usable, because a log call does not suspend.
 */
internal expect class DiagLock() {
    fun <T> withLock(block: () -> T): T
}
