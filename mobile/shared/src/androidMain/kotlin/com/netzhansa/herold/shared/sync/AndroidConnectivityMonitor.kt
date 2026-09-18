package com.netzhansa.herold.shared.sync

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.stateIn

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

    override val online: StateFlow<Boolean> = callbackFlow {
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
    }.distinctUntilChanged().stateIn(scope, SharingStarted.Eagerly, hasInternet())

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
