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
