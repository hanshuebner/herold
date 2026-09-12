package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.shared.push.PushDecryptionException
import com.netzhansa.herold.shared.push.PushEnvelope
import com.netzhansa.herold.shared.push.PushVerification
import com.netzhansa.herold.shared.push.WebPushEnvelope
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeoutOrNull
import org.unifiedpush.android.connector.MessagingReceiver

/**
 * What the distributor delivers, on the way to the same seam the FCM
 * service feeds (`PushDelivery`). herold POSTs the RFC 8291 envelope to
 * the endpoint; the distributor hands those bytes here unchanged, so the
 * decryption happens on the device and the distributor learns nothing
 * about the mail (architecture `04-push.md`).
 *
 * The connector holds a wake lock across `onReceive`, so the work runs to
 * completion inside its own timeout rather than being handed to a
 * detached scope.
 */
class HeroldUnifiedPushReceiver : MessagingReceiver() {

    /**
     * The distributor handed out an endpoint - on first registration and
     * again whenever it rotates (REQ-AND-PUSH-02 parity). Registering the
     * new one destroys the superseded subscription.
     */
    override fun onNewEndpoint(context: Context, endpoint: String, instance: String) {
        runBlocking {
            withTimeoutOrNull(WORK_BUDGET_MS) {
                runCatching { controller(context).registerEndpoint(endpoint) }
                    .onFailure { Log.w(TAG, "UnifiedPush endpoint registration failed: ${it.message}") }
            }
        }
    }

    /**
     * One push. The envelope is either the RFC 8620 section 7.2.2
     * verification handshake or a `StateChange`; both are the same JSON
     * the FCM transport carries in its data map, so they route through
     * the one delivery path.
     */
    override fun onMessage(context: Context, message: ByteArray, instance: String) {
        runBlocking {
            val keys = controller(context).keys()
            if (keys == null) {
                Log.w(TAG, "push arrived before this install held subscription keys")
                return@runBlocking
            }
            val json = try {
                WebPushEnvelope.decryptToText(message, keys)
            } catch (e: PushDecryptionException) {
                Log.w(TAG, "push envelope rejected: ${e.message}")
                return@runBlocking
            }
            runCatching { PushDelivery(context.applicationContext).deliver(dataMap(json)) }
                .onFailure { Log.w(TAG, "push delivery failed: ${it.message}") }
        }
    }

    /** The distributor gave the endpoint back: herold's subscription goes with it. */
    override fun onUnregistered(context: Context, instance: String) {
        runBlocking {
            withTimeoutOrNull(WORK_BUDGET_MS) {
                runCatching { controller(context).forgetEndpoint() }
                    .onFailure { Log.w(TAG, "dropping the UnifiedPush subscription failed: ${it.message}") }
            }
        }
    }

    /** The distributor refused to register this app; settings shows the transport as unavailable. */
    override fun onRegistrationFailed(context: Context, instance: String) {
        Log.w(TAG, "the distributor refused to register this app")
        runBlocking { runCatching { controller(context).forgetEndpoint() } }
    }

    private fun controller(context: Context) =
        UnifiedPushRegistration(context.applicationContext)

    private companion object {
        const val TAG = "herold.push"

        /** A manifest receiver's window; the connector holds a wake lock over it. */
        const val WORK_BUDGET_MS = 20_000L

        /**
         * Routes a decrypted envelope into the FCM transport's data-map
         * shape, so both transports reach `PushDelivery` the same way.
         */
        fun dataMap(json: String): Map<String, String> =
            if (PushVerification.parse(json) != null) {
                mapOf(PushVerification.DATA_KEY to json)
            } else {
                mapOf(PushEnvelope.DATA_KEY to json)
            }
    }
}
