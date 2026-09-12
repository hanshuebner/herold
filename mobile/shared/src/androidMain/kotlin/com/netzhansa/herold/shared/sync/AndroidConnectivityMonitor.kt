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
                trySend(hasInternet())
            }

            override fun onLost(network: Network) {
                trySend(hasInternet())
            }

            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) {
                trySend(hasInternet())
            }
        }
        trySend(hasInternet())
        manager.registerDefaultNetworkCallback(callback)
        awaitClose { runCatching { manager.unregisterNetworkCallback(callback) } }
    }.distinctUntilChanged().stateIn(scope, SharingStarted.Eagerly, hasInternet())

    private fun hasInternet(): Boolean {
        val active = manager.activeNetwork ?: return false
        val capabilities = manager.getNetworkCapabilities(active) ?: return false
        return capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
            capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
    }
}
