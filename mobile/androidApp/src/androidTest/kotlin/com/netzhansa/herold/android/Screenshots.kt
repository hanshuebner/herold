package com.netzhansa.herold.android

import android.graphics.Bitmap
import android.os.SystemClock
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/**
 * Writes a PNG of the current screen into the app's external files
 * directory, where the acceptance harness pulls it from with `adb pull`.
 * A milestone closes on these images, not on a green build
 * (docs/design/android/implementation-plan.md, "Verification model").
 *
 * The capture goes through UiAutomation rather than Compose's
 * `captureToImage`, because the reading pane's body is a WebView: its
 * surface is composited by the platform and does not appear in a
 * Compose-tree capture. The short settle before the shot lets a
 * just-decoded inline image reach the frame.
 */
fun ComposeTestRule.captureScreen(name: String) {
    waitForIdle()
    SystemClock.sleep(SETTLE_MS)
    val instrumentation = InstrumentationRegistry.getInstrumentation()
    val bitmap = instrumentation.uiAutomation.takeScreenshot() ?: return
    val dir = instrumentation.targetContext.getExternalFilesDir(null) ?: return
    dir.mkdirs()
    File(dir, "$name.png").outputStream().use { out ->
        bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)
    }
}

private const val SETTLE_MS = 1_500L
