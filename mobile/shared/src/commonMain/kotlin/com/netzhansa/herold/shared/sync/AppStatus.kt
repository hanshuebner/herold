package com.netzhansa.herold.shared.sync

/**
 * What the shell's status indicator says about the client's dealings
 * with the server (REQ-AND-SYNC-30). One value covers connectivity, the
 * reconciler and the queue, because the indicator is one dot.
 */
enum class AppStatus {
    /** Connected, nothing in flight, nothing waiting. */
    IDLE,

    /** A sync or an outbox drain is running. */
    BUSY,

    /** The phone has been off the network longer than the grace period. */
    OFFLINE,

    /** The last sync failed, or an entry in the queue was refused. */
    FAILED,
}

/**
 * The indicator's state from what the app knows.
 *
 * Offline outranks a failure because a failure met with no network is
 * the same fact told twice, and the one the user can act on is the
 * missing connection. A failure outranks work in flight, so a refusal
 * stays visible while the next pass runs. The detail behind whichever
 * it is belongs to the diagnostics screen.
 */
fun appStatus(
    offline: Boolean,
    sync: SyncStatus,
    pendingOutbox: Int,
    failedOutbox: Int,
): AppStatus = when {
    offline -> AppStatus.OFFLINE
    sync is SyncStatus.Failed || failedOutbox > 0 -> AppStatus.FAILED
    sync is SyncStatus.Syncing || pendingOutbox > 0 -> AppStatus.BUSY
    else -> AppStatus.IDLE
}
