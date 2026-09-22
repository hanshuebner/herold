package com.netzhansa.herold.shared.sync

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch

/**
 * The platform's view of connectivity, as a flow the shell and the sync
 * engine read (architecture `03-sync-and-state.md` § Connectivity and
 * background). A network the system says is validated counts as online;
 * a captive portal or a network without internet does not, because a
 * drain against one only produces failures.
 */
class AndroidConnectivityMonitor(
    context: Context,
    scope: CoroutineScope,
) : ConnectivityMonitor {

    private val manager =
        context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager

    private val _online = MutableStateFlow(hasInternet())
    override val online: StateFlow<Boolean> = _online.asStateFlow()

    init {
        scope.launch {
            callbackFlow {
                val callback = object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(network: Network) {
                        trySend(usable(network))
                    }

                    override fun onLost(network: Network) {
                        trySend(hasInternet())
                    }

                    override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
                        trySend(usable(capabilities))
                    }
                }
                trySend(hasInternet())
                manager.registerDefaultNetworkCallback(callback)
                awaitClose { runCatching { manager.unregisterNetworkCallback(callback) } }
            }.distinctUntilChanged().collect { _online.value = it }
        }
    }

    /**
     * The client's own traffic just confirmed the server (issue #479):
     * a reading the platform delivered before this is overruled by
     * stronger evidence, and the platform is told the network it
     * reported is working so a validation that has gone stale is
     * corrected rather than left for a callback that may not come.
     *
     * [ReachabilityCoordinator] calls this from a `StateFlow` that only
     * emits on a change of direction, so one confirmed run reports
     * once here, not once per request in it; [manager]'s own call has
     * no visible effect for a network already reported the same way.
     * `activeNetwork` answers null between networks - a captive-portal
     * sign-out, a SIM swap - and the report is skipped rather than
     * made against nothing.
     */
    override fun noteReachable() {
        _online.value = true
        manager.activeNetwork?.let { manager.reportNetworkConnectivity(it, true) }
    }

    /**
     * A run of the client's own requests could not reach the server on
     * a network the platform still calls validated (issue #479): asked
     * to check again, rather than left to a callback nothing here
     * prompts. The same once-per-run and null-`activeNetwork` handling
     * as [noteReachable] applies.
     */
    override fun noteUnreachable() {
        manager.activeNetwork?.let { manager.reportNetworkConnectivity(it, false) }
    }

    /**
     * The default network as the platform last reported it. The
     * callback names the network, and that is what is read: the
     * process-wide `activeNetwork` answers null while the app's own
     * network access is blocked - a restricted standby bucket, data
     * saver - which would leave the shell saying "offline" about a
     * connection its own requests are getting through on.
     */
    private fun usable(network: Network): Boolean =
        manager.getNetworkCapabilities(network)?.let(::usable) ?: false

    private fun usable(capabilities: NetworkCapabilities): Boolean =
        capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
            capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)

    /** What the platform says before the first callback arrives. */
    private fun hasInternet(): Boolean {
        val active = manager.activeNetwork ?: return false
        return usable(active)
    }
}
