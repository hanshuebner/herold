package com.netzhansa.herold.android.auth

import android.content.Context
import androidx.biometric.BiometricManager
import com.netzhansa.herold.shared.auth.UnlockGate
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first

/**
 * App-launch and idle unlock (REQ-AND-AUTH-11). While the app is
 * locked no bearer token is handed to the network layer: the sync
 * engine, the outbox drain and the EventSource all take their token
 * from the [com.netzhansa.herold.shared.auth.SessionAuthenticator],
 * which waits on this gate.
 *
 * Off by default. Turning it on locks the app on every cold start and
 * whenever it returns to the foreground after the idle period, which
 * the user picks in settings.
 */
class UnlockController(context: Context) : UnlockGate {

    private val appContext = context.applicationContext
    private val preferences = appContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _locked = MutableStateFlow(false)

    /** True while the user has to unlock before anything reaches the network. */
    val locked: StateFlow<Boolean> = _locked.asStateFlow()

    /** When the app last had the foreground, for the idle comparison. */
    private var backgroundedAt: Long = 0L

    var enabled: Boolean
        get() = preferences.getBoolean(KEY_ENABLED, false)
        set(value) {
            preferences.edit().putBoolean(KEY_ENABLED, value).apply()
            if (!value) _locked.value = false
        }

    /** How long the app may sit in the background before it locks again. */
    var idlePeriod: IdlePeriod
        get() = IdlePeriod.ofMinutes(preferences.getInt(KEY_IDLE_MINUTES, IdlePeriod.DEFAULT.minutes))
        set(value) {
            preferences.edit().putInt(KEY_IDLE_MINUTES, value.minutes).apply()
        }

    /**
     * Whether this device can actually challenge the user. With neither
     * a biometric nor a device credential enrolled there is nothing to
     * unlock with, so the toggle stays unavailable rather than locking
     * the user out of their own mail.
     */
    fun available(): Boolean =
        BiometricManager.from(appContext).canAuthenticate(authenticators()) ==
            BiometricManager.BIOMETRIC_SUCCESS

    /** Called once per process, before the session is restored. */
    fun lockIfEnabled() {
        if (enabled && available()) _locked.value = true
    }

    fun onBackgrounded(nowMillis: Long) {
        backgroundedAt = nowMillis
    }

    fun onForegrounded(nowMillis: Long) {
        if (!enabled || !available()) return
        if (backgroundedAt == 0L) return
        if (nowMillis - backgroundedAt >= idlePeriod.millis) _locked.value = true
    }

    fun unlocked() {
        _locked.value = false
        backgroundedAt = 0L
    }

    override fun isUnlocked(): Boolean = !_locked.value

    override suspend fun awaitUnlocked() {
        locked.first { !it }
    }

    companion object {
        private const val PREFS = "herold-unlock"
        private const val KEY_ENABLED = "biometric_unlock_enabled"
        private const val KEY_IDLE_MINUTES = "idle_minutes"

        /**
         * A device credential is accepted alongside a biometric, per
         * platform convention (REQ-AND-AUTH-11): a user without a
         * fingerprint enrolled unlocks with their PIN or pattern.
         */
        fun authenticators(): Int =
            BiometricManager.Authenticators.BIOMETRIC_WEAK or
                BiometricManager.Authenticators.DEVICE_CREDENTIAL
    }
}

/** How long the app may be away before it asks again. */
enum class IdlePeriod(val minutes: Int, val label: String) {
    IMMEDIATELY(0, "Every time"),
    ONE_MINUTE(1, "After 1 minute"),
    FIVE_MINUTES(5, "After 5 minutes"),
    FIFTEEN_MINUTES(15, "After 15 minutes"),
    ;

    val millis: Long get() = minutes * 60_000L

    companion object {
        val DEFAULT = ONE_MINUTE

        fun ofMinutes(minutes: Int): IdlePeriod =
            entries.firstOrNull { it.minutes == minutes } ?: DEFAULT
    }
}
