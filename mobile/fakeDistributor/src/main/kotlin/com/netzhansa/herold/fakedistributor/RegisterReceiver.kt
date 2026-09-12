package com.netzhansa.herold.fakedistributor

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/**
 * The distributor half of the UnifiedPush registration handshake: an app
 * broadcasts REGISTER with its package and an instance token, and the
 * distributor answers with the endpoint pushes for that token arrive on.
 */
class RegisterReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val token = intent.getStringExtra(EXTRA_TOKEN).orEmpty()
        val application = intent.getStringExtra(EXTRA_APPLICATION).orEmpty()
        if (token.isBlank() || application.isBlank()) {
            Log.w(TAG, "ignoring ${intent.action} without a token and application")
            return
        }
        val registrations = Registrations(context)
        when (intent.action) {
            ACTION_REGISTER -> {
                registrations.save(token, application)
                DistributorService.start(context)
                val endpoint = registrations.endpointFor(token)
                context.sendBroadcast(
                    Intent(ACTION_NEW_ENDPOINT).apply {
                        `package` = application
                        putExtra(EXTRA_TOKEN, token)
                        putExtra(EXTRA_ENDPOINT, endpoint)
                    },
                )
                Log.i(TAG, "registered $application at $endpoint")
            }
            ACTION_UNREGISTER -> {
                registrations.forget(token)
                context.sendBroadcast(
                    Intent(ACTION_UNREGISTERED).apply {
                        `package` = application
                        putExtra(EXTRA_TOKEN, token)
                    },
                )
                Log.i(TAG, "unregistered $application")
            }
        }
    }

    private companion object {
        const val TAG = DistributorServer.TAG
        const val ACTION_REGISTER = "org.unifiedpush.android.distributor.REGISTER"
        const val ACTION_UNREGISTER = "org.unifiedpush.android.distributor.UNREGISTER"
        const val ACTION_NEW_ENDPOINT = "org.unifiedpush.android.connector.NEW_ENDPOINT"
        const val ACTION_UNREGISTERED = "org.unifiedpush.android.connector.UNREGISTERED"
        const val EXTRA_APPLICATION = "application"
        const val EXTRA_TOKEN = "token"
        const val EXTRA_ENDPOINT = "endpoint"
    }
}
