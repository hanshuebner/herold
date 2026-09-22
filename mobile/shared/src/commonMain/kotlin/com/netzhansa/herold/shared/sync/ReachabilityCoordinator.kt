package com.netzhansa.herold.shared.sync

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.launchIn
import kotlinx.coroutines.flow.onEach

/**
 * Keeps [connectivity] in step with what the client's own traffic just
 * found (issue #479).
 *
 * The platform delivers a network reading only on its own callback,
 * which a host-resolution failure or its recovery does not itself
 * prompt: a network the platform called validated before the failures
 * started can still read that way after they stop, with nothing to
 * correct it. [Reachability.confirmed] is what a genuine JMAP success -
 * a decoded descriptor or method response - moves, not merely a socket
 * round trip completing; a captive portal's login page does that too,
 * and telling the platform such a network is validated would hide the
 * portal from it. A run of it changing direction is reported here so a
 * confirmed reach clears a stale reading without waiting on a callback
 * that may never come, and a run of failures asks the platform to
 * check the network it still calls up again.
 */
class ReachabilityCoordinator(
    reachability: Reachability,
    connectivity: ConnectivityMonitor,
    scope: CoroutineScope,
) {
    init {
        reachability.confirmed
            .onEach { reached -> if (reached) connectivity.noteReachable() else connectivity.noteUnreachable() }
            .launchIn(scope)
    }
}
