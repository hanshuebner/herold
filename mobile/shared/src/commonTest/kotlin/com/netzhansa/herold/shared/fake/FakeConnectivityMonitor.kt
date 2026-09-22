package com.netzhansa.herold.shared.fake

import com.netzhansa.herold.shared.sync.ConnectivityMonitor
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * A platform connectivity reading a test drives by hand (issue #479):
 * [set] is what a fresh platform callback would have delivered, and
 * [reachableCalls] / [unreachableCalls] count what
 * [ConnectivityMonitor.noteReachable] / [ConnectivityMonitor.noteUnreachable]
 * were told without one.
 */
class FakeConnectivityMonitor(initial: Boolean = true) : ConnectivityMonitor {
    private val _online = MutableStateFlow(initial)
    override val online: StateFlow<Boolean> = _online.asStateFlow()

    var reachableCalls = 0
        private set
    var unreachableCalls = 0
        private set

    /** What a fresh platform callback would have delivered. */
    fun set(up: Boolean) {
        _online.value = up
    }

    override fun noteReachable() {
        reachableCalls++
        _online.value = true
    }

    override fun noteUnreachable() {
        unreachableCalls++
    }
}
