package com.netzhansa.herold.android

import android.app.Application
import com.netzhansa.herold.android.push.FirebaseSetup
import com.netzhansa.herold.android.push.NotificationChannels

/**
 * Owns the process-wide object graph. The local store and the Keystore-backed
 * token store outlive any activity, so a configuration change or a
 * back-stack pop never rebuilds them, and an instrumented test can reach the
 * same container the UI reads.
 */
class HeroldApplication : Application() {
    val container: AppContainer by lazy { AppContainer(applicationContext) }

    override fun onCreate() {
        super.onCreate()
        // The per-kind channels exist before the first notification, so the
        // user finds them in system settings straight away (REQ-AND-PUSH-10).
        NotificationChannels.create(this)
        // Firebase is initialised from the values the build read, rather
        // than by the google-services plugin, so a build without
        // credentials runs with push switched off.
        FirebaseSetup.initialise(this)
    }
}
