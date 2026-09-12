package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.android.HeroldApplication
import com.netzhansa.herold.shared.jmap.PushTransport
import com.netzhansa.herold.shared.push.RegistrationOutcome
import com.netzhansa.herold.shared.push.WebPushKeys

/**
 * The herold half of the UnifiedPush transport: what happens to an
 * endpoint the distributor hands out (REQ-AND-PUSH-04). Separate from
 * `PushController` because the broadcast receiver runs in a process that
 * may have no UI and reaches only the container.
 */
class UnifiedPushRegistration(private val context: Context) {

    private val container get() = (context.applicationContext as HeroldApplication).container

    /**
     * This install's RFC 8291 keys, minted on first use and kept in
     * Keystore-backed storage so a woken receiver can decrypt without the
     * user present.
     */
    suspend fun keys(): WebPushKeys? =
        runCatching { container.tokenStore.pushKeysOrGenerate() }
            .onFailure { Log.w(TAG, "push keys unavailable: ${it.message}") }
            .getOrNull()

    /**
     * Registers [endpoint] with herold under this install's keys and
     * remembers it, so a later sign-in or a lost subscription can
     * re-register without waiting for the distributor to speak again.
     */
    suspend fun registerEndpoint(endpoint: String): RegistrationOutcome? {
        container.tokenStore.setPushEndpoint(endpoint)
        return registerStoredEndpoint()
    }

    /** Registers the endpoint already in hand; null when there is none or no session. */
    suspend fun registerStoredEndpoint(): RegistrationOutcome? {
        val endpoint = container.tokenStore.pushEndpoint() ?: return null
        val keys = keys() ?: return null
        container.restore()
        val registrar = container.session.value?.pushRegistrar ?: return null
        val outcome = registrar.registerUnifiedPush(endpoint, keys)
        when (outcome) {
            is RegistrationOutcome.Rejected ->
                Log.w(TAG, "UnifiedPush registration rejected: ${outcome.message}")
            is RegistrationOutcome.Failed ->
                Log.w(TAG, "UnifiedPush registration failed: ${outcome.message}")
            else -> Unit
        }
        return outcome
    }

    /**
     * The endpoint is gone - the distributor took it back or refused the
     * app. The subscription herold holds is destroyed with it; a
     * subscription whose endpoint answers 404 or 410 would be dropped by
     * the server's next delivery anyway.
     */
    suspend fun forgetEndpoint() {
        container.tokenStore.setPushEndpoint(null)
        // A distributor that speaks up after the device moved to FCM must
        // not take the FCM subscription down with it.
        val held = runCatching { container.store.pushRegistration() }.getOrNull() ?: return
        if (held.transport != PushTransport.UNIFIED_PUSH.wire) return
        container.session.value?.pushRegistrar?.unregister()
    }

    private companion object {
        const val TAG = "herold.push"
    }
}
