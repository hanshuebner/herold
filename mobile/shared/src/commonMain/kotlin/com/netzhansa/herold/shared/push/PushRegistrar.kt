package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.jmap.FcmSubscriptionCreate
import com.netzhansa.herold.shared.jmap.JmapApi
import com.netzhansa.herold.shared.jmap.JmapException
import com.netzhansa.herold.shared.jmap.NotificationRules
import com.netzhansa.herold.shared.jmap.PushSubscriptionCreate
import com.netzhansa.herold.shared.jmap.PushTransport
import com.netzhansa.herold.shared.jmap.QuietHours
import com.netzhansa.herold.shared.jmap.UnifiedPushSubscriptionCreate
import com.netzhansa.herold.shared.store.LocalStore
import com.netzhansa.herold.shared.store.PushRegistration
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** What a registration attempt did. */
sealed interface RegistrationOutcome {
    /** herold created the subscription and returned [subscriptionId]. */
    data class Registered(val subscriptionId: String) : RegistrationOutcome

    /** The same transport and the same token or endpoint are already registered; no call was made. */
    data object Unchanged : RegistrationOutcome

    /** The server refused the create. */
    data class Rejected(val message: String) : RegistrationOutcome

    /** The call did not reach the server; the next attempt retries. */
    data class Failed(val message: String) : RegistrationOutcome
}

/**
 * Registers this install with herold and keeps exactly one subscription
 * alive (REQ-AND-PUSH-01/02/04), whichever transport carries it. A
 * rotation - a new FCM token, a new distributor endpoint - and a switch
 * of transport both register anew and destroy the superseded subscription
 * in the same `PushSubscription/set` call, so the server never fans a
 * push out to a dead target.
 *
 * Platform-free: the FCM SDK or the UnifiedPush distributor hands the
 * target in, the local store holds the assigned subscription id, and
 * neither the token nor the endpoint is written anywhere - only a
 * fingerprint of it, which is what rotation detection needs.
 */
class PushRegistrar(
    private val api: JmapApi,
    private val store: LocalStore,
    private val now: () -> Long = { 0L },
    private val newDeviceClientId: () -> String,
) {

    /**
     * Registers the FCM registration [token]. Re-registering the same
     * token is a no-op, so the call is safe to make on every foreground
     * and after every sync.
     *
     * [rules] and [quietHours] are sent only when the user has configured
     * them; omitted, herold applies its REQ-PUSH-81 defaults, which is what
     * the Suite's registration also leaves in place.
     */
    suspend fun register(
        token: String,
        rules: NotificationRules? = null,
        quietHours: QuietHours? = null,
    ): RegistrationOutcome {
        if (token.isBlank()) return RegistrationOutcome.Failed("no FCM registration token")
        return register(PushTransport.FCM, token) { deviceClientId ->
            FcmSubscriptionCreate(
                deviceClientId = deviceClientId,
                fcmToken = token,
                notificationRules = rules,
                quietHours = quietHours,
            )
        }
    }

    /**
     * Registers the [endpoint] a UnifiedPush distributor handed out,
     * together with the RFC 8291 public key and auth secret herold
     * encrypts the envelope to (REQ-AND-PUSH-04). The private half stays
     * on the device.
     */
    suspend fun registerUnifiedPush(
        endpoint: String,
        keys: WebPushKeys,
        rules: NotificationRules? = null,
        quietHours: QuietHours? = null,
    ): RegistrationOutcome {
        if (endpoint.isBlank()) return RegistrationOutcome.Failed("no distributor endpoint")
        return register(PushTransport.UNIFIED_PUSH, "$endpoint|${keys.p256dh}") { deviceClientId ->
            UnifiedPushSubscriptionCreate(
                deviceClientId = deviceClientId,
                endpoint = endpoint,
                p256dh = keys.p256dh,
                auth = keys.auth,
                notificationRules = rules,
                quietHours = quietHours,
            )
        }
    }

    private suspend fun register(
        transport: PushTransport,
        identity: String,
        create: (deviceClientId: String) -> PushSubscriptionCreate,
    ): RegistrationOutcome {
        val fingerprint = fingerprint(identity)
        val existing = store.pushRegistration()
        if (existing != null &&
            existing.transport == transport.wire &&
            existing.tokenFingerprint == fingerprint
        ) {
            return RegistrationOutcome.Unchanged
        }
        val deviceClientId = existing?.deviceClientId ?: newDeviceClientId()
        val outcome = try {
            api.pushSubscriptionSet(
                create = create(deviceClientId),
                destroy = listOfNotNull(existing?.subscriptionId),
            )
        } catch (e: JmapException) {
            return RegistrationOutcome.Failed(e.message ?: "push registration failed")
        } catch (t: Throwable) {
            return RegistrationOutcome.Failed(t.message ?: "push registration failed")
        }
        outcome.notCreated?.let { return RegistrationOutcome.Rejected(it) }
        val id = outcome.createdId ?: return RegistrationOutcome.Rejected("no subscription id returned")
        store.setPushRegistration(
            PushRegistration(
                subscriptionId = id,
                deviceClientId = deviceClientId,
                tokenFingerprint = fingerprint,
                registeredAt = now(),
                transport = transport.wire,
            ),
        )
        // herold delivers nothing to an unverified subscription (RFC 8620
        // section 7.2). The code arrives twice: on the created object and,
        // over the push channel, as a PushVerification handshake. Echo the
        // one already in hand so delivery does not wait on the ping.
        outcome.verificationCode?.let { confirmVerification(id, it) }
        return RegistrationOutcome.Registered(id)
    }

    /**
     * Echoes a verification code back, which is what makes the
     * subscription eligible for delivery (RFC 8620 section 7.2.2). Called
     * with the code the create returned and again when the handshake
     * arrives over the push channel; both are idempotent.
     */
    suspend fun confirmVerification(subscriptionId: String, code: String): Boolean {
        val id = subscriptionId.ifBlank { store.pushRegistration()?.subscriptionId ?: return false }
        return runCatching {
            api.pushSubscriptionSet(
                update = mapOf(id to buildJsonObject { put("verificationCode", code) }),
            )
        }.isSuccess
    }

    /**
     * Destroys the subscription this install holds, on sign-out, when the
     * user turns notifications off, and when the transport changes. The
     * local row is dropped whether or not the call reaches the server;
     * herold prunes a target its transport reports as gone on the next
     * delivery attempt.
     */
    suspend fun unregister() {
        val existing = store.pushRegistration() ?: return
        runCatching { api.pushSubscriptionSet(destroy = listOf(existing.subscriptionId)) }
        store.setPushRegistration(null)
    }

    companion object {
        /**
         * A stable 64-bit FNV-1a digest of the registration token or the
         * distributor endpoint, rendered hex. Enough to notice it changed;
         * carries none of it.
         */
        fun fingerprint(token: String): String {
            var hash = FNV_OFFSET
            token.encodeToByteArray().forEach { byte ->
                hash = hash xor (byte.toLong() and 0xFF)
                hash *= FNV_PRIME
            }
            return hash.toULong().toString(16).padStart(16, '0')
        }

        private const val FNV_OFFSET = -3750763034362895579L // 14695981039346656037 unsigned
        private const val FNV_PRIME = 1099511628211L
    }
}
