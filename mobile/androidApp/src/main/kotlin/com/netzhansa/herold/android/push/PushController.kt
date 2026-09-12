package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.shared.push.RegistrationOutcome

/**
 * Owns this install's push registration and the memory of a declined
 * notification permission (REQ-AND-PUSH-01/02/03).
 *
 * Registration needs a session, because the subscription is bound to the
 * authenticated principal, and it needs a token, which only exists when the
 * build carries a Firebase project. Both absent, the app runs unchanged and
 * reports push unavailable.
 */
class PushController(
    private val context: Context,
    private val container: AppContainer,
) {
    private val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** True when the build was given a Firebase project to register with. */
    val available: Boolean get() = FirebaseSetup.configured

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
     * Fetches the FCM token and registers it. Safe to call on every
     * foreground: an unchanged token makes no server call.
     */
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

    /** Drops the subscription, on sign-out. */
    suspend fun unregister() {
        container.session.value?.pushRegistrar?.unregister()
    }

    private companion object {
        const val PREFS = "herold-push"
        const val KEY_DENIED = "permission-denied"
        const val KEY_REQUESTED = "permission-requested"
        const val TAG = "herold.push"
    }
}
