package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * What the client's own traffic says about reaching the server
 * (issue #370).
 *
 * The platform still reports a validated network for a moment after the
 * radio has gone, so a send in that window fails on the wire while
 * [ConnectivityMonitor] says the phone is online. The drain and the sync
 * record what they met here, and the shell's offline indication is the
 * two together - which is what makes the chip say "offline" at the
 * moment the user's own message could not go out.
 */
class Reachability {

    private val _reachable = MutableStateFlow(true)

    /** False once a request failed on the wire, until one gets through. */
    val reachable: StateFlow<Boolean> = _reachable.asStateFlow()

    /** A request reached the server, whatever the server then said. */
    fun reached() {
        _reachable.value = true
    }

    /** A request never got there. */
    fun unreachable() {
        _reachable.value = false
    }
}
