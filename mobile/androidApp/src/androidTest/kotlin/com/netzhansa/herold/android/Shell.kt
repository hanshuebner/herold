package com.netzhansa.herold.android

import android.os.ParcelFileDescriptor
import androidx.test.platform.app.InstrumentationRegistry

/**
 * Runs a command through the instrumentation shell and returns what it
 * printed, for the device-level setup a test needs (a screen lock for
 * the unlock check, the radios, a package clear).
 */
fun shellOut(command: String): String {
    val fd = InstrumentationRegistry.getInstrumentation().uiAutomation.executeShellCommand(command)
    return ParcelFileDescriptor.AutoCloseInputStream(fd).use { it.readBytes().decodeToString() }
}
