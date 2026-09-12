package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.shared.jmap.PushTransport
import com.netzhansa.herold.shared.push.RegistrationOutcome

/**
 * Owns this install's push registration, the transport it is made over,
 * and the memory of a declined notification permission
 * (REQ-AND-PUSH-01/02/03/04/05).
 *
 * Registration needs a session, because the subscription is bound to the
 * authenticated principal, and it needs a transport: FCM where Play
 * Services carries it and the build has a Firebase project, a UnifiedPush
 * distributor otherwise. With neither, the app runs unchanged and reports
 * push unavailable.
 */
class PushController(
    private val context: Context,
    private val container: AppContainer,
) {
    private val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val unifiedPush = UnifiedPushRegistration(context)

    /** True when some transport could carry a push to this device. */
    val available: Boolean get() = transport() != null

    /** True when the build was given a Firebase project and the device can reach FCM. */
    val fcmAvailable: Boolean
        get() = FirebaseSetup.configured && playServicesAvailable()

    /** True when a UnifiedPush distributor is installed. */
    val unifiedPushAvailable: Boolean get() = UnifiedPushTransport.available(context)

    /** What the user asked for; Automatic until they say otherwise. */
    var choice: PushTransportChoice
        get() = PushTransportChoice.fromStored(prefs.getString(KEY_TRANSPORT, null))
        set(value) = prefs.edit().putString(KEY_TRANSPORT, value.stored).apply()

    /** The distributor a UnifiedPush registration would go to. */
    fun distributor(): String? = UnifiedPushTransport.effective(context)

    /** Every installed distributor, for the picker settings shows. */
    fun distributors(): List<String> = UnifiedPushTransport.distributors(context)

    fun distributorLabel(distributor: String): String =
        UnifiedPushTransport.label(context, distributor)

    /**
     * The transport [choice] resolves to on this device, null when the
     * chosen one cannot carry a push here.
     */
    fun transport(): PushTransport? = when (choice) {
        PushTransportChoice.FCM -> PushTransport.FCM.takeIf { fcmAvailable }
        PushTransportChoice.UNIFIED_PUSH -> PushTransport.UNIFIED_PUSH.takeIf { unifiedPushAvailable }
        PushTransportChoice.AUTOMATIC -> when {
            fcmAvailable -> PushTransport.FCM
            unifiedPushAvailable -> PushTransport.UNIFIED_PUSH
            else -> null
        }
    }

    /** The user turned the system permission down; do not ask again unprompted. */
    var permissionDenied: Boolean
        get() = prefs.getBoolean(KEY_DENIED, false)
        set(value) = prefs.edit().putBoolean(KEY_DENIED, value).apply()

    /** The contextual prompt has been shown once. */
    var permissionRequested: Boolean
        get() = prefs.getBoolean(KEY_REQUESTED, false)
        set(value) = prefs.edit().putBoolean(KEY_REQUESTED, value).apply()

    /** Clears the remembered denial, so settings can offer the prompt again. */
    fun forgetDenial() {
        prefs.edit().remove(KEY_DENIED).remove(KEY_REQUESTED).apply()
    }

    /**
     * Registers over the resolved transport. Safe to call on every
     * foreground: an unchanged token or endpoint makes no server call.
     */
    suspend fun registerCurrentTransport(): RegistrationOutcome? = when (transport()) {
        PushTransport.FCM -> registerCurrentToken()
        PushTransport.UNIFIED_PUSH -> registerUnifiedPush()
        null -> null
    }

    /** Kept for the call sites that only ever meant the FCM token. */
    suspend fun registerCurrentToken(): RegistrationOutcome? {
        val token = FirebaseSetup.registrationToken(context) ?: return null
        return register(token)
    }

    /** Registers [token], e.g. the one FCM handed to `onNewToken`. */
    suspend fun register(token: String): RegistrationOutcome? {
        container.restore()
        val registrar = container.session.value?.pushRegistrar ?: return null
        val outcome = registrar.register(token)
        when (outcome) {
            is RegistrationOutcome.Rejected -> Log.w(TAG, "push registration rejected: ${outcome.message}")
            is RegistrationOutcome.Failed -> Log.w(TAG, "push registration failed: ${outcome.message}")
            else -> Unit
        }
        return outcome
    }

    /**
     * Asks the distributor for an endpoint and registers the one already
     * in hand. The distributor's answer arrives as a broadcast, which
     * registers the new endpoint when it differs.
     */
    suspend fun registerUnifiedPush(): RegistrationOutcome? {
        UnifiedPushTransport.register(context)
        return unifiedPush.registerStoredEndpoint()
    }

    /**
     * Moves this install to [next] (REQ-AND-PUSH-05): the subscription
     * held over the old transport is destroyed and a fresh one is
     * registered, so herold is never left pushing to a target the device
     * has stopped listening on.
     */
    suspend fun switchTransport(next: PushTransportChoice): RegistrationOutcome? {
        val previous = transport()
        choice = next
        val resolved = transport()
        if (previous != null && previous != resolved) {
            container.session.value?.pushRegistrar?.unregister()
            if (previous == PushTransport.UNIFIED_PUSH) {
                UnifiedPushTransport.unregister(context)
                container.tokenStore.setPushEndpoint(null)
            }
        }
        return registerCurrentTransport()
    }

    /** Records the distributor the user picked and registers through it. */
    suspend fun selectDistributor(distributor: String): RegistrationOutcome? {
        UnifiedPushTransport.select(context, distributor)
        return registerCurrentTransport()
    }

    /** Drops the subscription, on sign-out. */
    suspend fun unregister() {
        container.session.value?.pushRegistrar?.unregister()
        if (transport() == PushTransport.UNIFIED_PUSH) {
            UnifiedPushTransport.unregister(context)
            container.tokenStore.setPushEndpoint(null)
        }
    }

    /**
     * Whether Google Play Services is on this device at all, which is
     * what decides the automatic transport. The package is declared in
     * `<queries>` so it is visible to this check on Android 11 and later;
     * a de-Googled phone has none and falls through to UnifiedPush.
     */
    private fun playServicesAvailable(): Boolean = runCatching {
        context.packageManager.getApplicationInfo(PLAY_SERVICES, 0).enabled
    }.getOrDefault(false)

    private companion object {
        const val PREFS = "herold-push"
        const val KEY_DENIED = "permission-denied"
        const val KEY_REQUESTED = "permission-requested"
        const val KEY_TRANSPORT = "transport-choice"
        const val PLAY_SERVICES = "com.google.android.gms"
        const val TAG = "herold.push"
    }
}
