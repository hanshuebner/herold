package com.netzhansa.herold.android.push

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.netzhansa.herold.android.HeroldApplication
import kotlinx.coroutines.runBlocking

/**
 * The wake channel while the app is backgrounded or killed
 * (`docs/design/android/architecture/04-push.md`). herold sends data-only
 * messages carrying the JMAP `StateChange` envelope in `data.payload`, so
 * the system never renders a notification of its own: this service decides,
 * after reconciling, what the user sees.
 *
 * `onMessageReceived` runs on a background thread the framework keeps alive
 * for the duration of the call, so the delivery work runs to completion
 * under its own timeout rather than being handed to a detached scope.
 */
// Open so the instrumented acceptance run can attach a context and hand
// the handler a RemoteMessage, which is the one part of the delivery path
// Google's SDK would otherwise own.
open class HeroldMessagingService : FirebaseMessagingService() {

    final override fun onMessageReceived(message: RemoteMessage) {
        runBlocking {
            runCatching { PushDelivery(applicationContext).deliver(message.data) }
                .onFailure { Log.w(TAG, "push delivery failed: ${it.message}") }
        }
    }

    /**
     * FCM rotated this install's token: register the new one and let herold
     * destroy the superseded subscription (REQ-AND-PUSH-02).
     */
    override fun onNewToken(token: String) {
        val container = (applicationContext as HeroldApplication).container
        runBlocking {
            runCatching { container.push.register(token) }
                .onFailure { Log.w(TAG, "re-registration after token rotation failed: ${it.message}") }
        }
    }

    private companion object {
        const val TAG = "herold.push"
    }
}
