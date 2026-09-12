package com.netzhansa.herold.android

import android.os.Build
import androidx.test.platform.app.InstrumentationRegistry

/**
 * Grants POST_NOTIFICATIONS before a test drives the app.
 *
 * The shell grants it rather than the app asking, because the app asks
 * contextually - after the first successful sync (REQ-AND-PUSH-03) - and
 * the system dialog would otherwise cover the screen the test is reading.
 * The permission prompt itself is exercised in its own check.
 */
fun grantNotificationPermission() {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    instrumentation.uiAutomation.executeShellCommand(
        "pm grant ${instrumentation.targetContext.packageName} android.permission.POST_NOTIFICATIONS",
    ).close()
}
