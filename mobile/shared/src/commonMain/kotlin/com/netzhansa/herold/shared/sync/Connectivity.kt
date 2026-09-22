package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.transformLatest

/**
 * Whether the device has a usable network. The Android actual watches the
 * platform's default network; a test drives a plain flow.
 */
interface ConnectivityMonitor {
    val online: StateFlow<Boolean>

    /**
     * The client's own traffic just reached the server (issue #479). The
     * platform delivers a reading only on its own callback, which a
     * resolution recovery does not itself prompt, so a request that got
     * through is stronger evidence than a reading that predates it.
     */
    fun noteReachable() {}

    /**
     * A run of the client's own requests could not reach the server on
     * a network the platform still calls validated (issue #479).
     * Nothing here is going to change the platform's reading on its
     * own, so this asks the platform to check the network again rather
     * than wait on a callback nothing prompts.
     */
    fun noteUnreachable() {}
}

/** How long a drop is tolerated before the shell says anything about it. */
const val OFFLINE_GRACE_MS: Long = 4_000

/**
 * The offline indication the shell renders (REQ-AND-SYNC-30): true only
 * once the connection has been gone for [graceMs]. A drop shorter than
 * that - a handover between cells, a moment in a lift - never reaches the
 * UI, so the chip does not flash.
 */
@OptIn(ExperimentalCoroutinesApi::class)
fun Flow<Boolean>.offlineIndication(graceMs: Long = OFFLINE_GRACE_MS): Flow<Boolean> =
    transformLatest { online ->
        if (online) {
            emit(false)
        } else {
            delay(graceMs)
            emit(true)
        }
    }.distinctUntilChanged()
