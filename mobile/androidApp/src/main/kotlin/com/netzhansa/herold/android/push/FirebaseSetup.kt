package com.netzhansa.herold.android.push

import android.content.Context
import android.util.Log
import com.google.firebase.FirebaseApp
import com.google.firebase.FirebaseOptions
import com.google.firebase.messaging.FirebaseMessaging
import com.netzhansa.herold.android.BuildConfig
import kotlin.coroutines.resume
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Brings up the Firebase SDK from the values the build read out of
 * `firebase.properties` or `google-services.json` (see
 * `androidApp/build.gradle.kts`). Initialising in code rather than through
 * the google-services Gradle plugin keeps the project buildable without the
 * credentials: a checkout that has none simply reports push unavailable.
 */
object FirebaseSetup {

    private const val TAG = "herold.push"

    /** True when the build carries a Firebase project. */
    val configured: Boolean
        get() = BuildConfig.FIREBASE_APPLICATION_ID.isNotBlank() &&
            BuildConfig.FIREBASE_API_KEY.isNotBlank()

    /**
     * Initialises the default Firebase app, once per process. Returns false
     * when the build has no project configured or the SDK refused the
     * options, in which case the app runs without push.
     */
    fun initialise(context: Context): Boolean {
        if (!configured) return false
        if (FirebaseApp.getApps(context).isNotEmpty()) return true
        val options = FirebaseOptions.Builder()
            .setApplicationId(BuildConfig.FIREBASE_APPLICATION_ID)
            .setApiKey(BuildConfig.FIREBASE_API_KEY)
            .setProjectId(BuildConfig.FIREBASE_PROJECT_ID.ifBlank { null })
            .setGcmSenderId(BuildConfig.FIREBASE_PROJECT_NUMBER.ifBlank { null })
            .build()
        return runCatching { FirebaseApp.initializeApp(context, options) != null }
            .onFailure { Log.w(TAG, "Firebase initialisation failed: ${it.message}") }
            .getOrDefault(false)
    }

    /**
     * The current FCM registration token, or null when Firebase is not
     * running or the device cannot reach Google's registration service.
     */
    suspend fun registrationToken(context: Context): String? {
        if (!initialise(context)) return null
        return suspendCancellableCoroutine { continuation ->
            FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
                if (task.isSuccessful) {
                    continuation.resume(task.result)
                } else {
                    Log.w(TAG, "FCM token request failed: ${task.exception?.message}")
                    continuation.resume(null)
                }
            }
        }
    }
}
