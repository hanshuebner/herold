package com.netzhansa.herold.android

import android.app.Application

/**
 * Owns the process-wide object graph. The local store and the Keystore-backed
 * token store outlive any activity, so a configuration change or a
 * back-stack pop never rebuilds them, and an instrumented test can reach the
 * same container the UI reads.
 */
class HeroldApplication : Application() {
    val container: AppContainer by lazy { AppContainer(applicationContext) }
}
