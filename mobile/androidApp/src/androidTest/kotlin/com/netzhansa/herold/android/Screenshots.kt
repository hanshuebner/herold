package com.netzhansa.herold.android

import android.graphics.Bitmap
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.io.FileOutputStream

/**
 * Writes a PNG of the current screen where the acceptance harness pulls it
 * from with `adb pull`. A milestone closes on these images, not on a green
 * build (docs/design/android/implementation-plan.md, "Verification model").
 *
 * The capture goes through the shell's `screencap` rather than Compose's
 * `captureToImage`, for two reasons: the reading pane's body is a WebView,
 * whose surface is composited by the platform and does not appear in a
 * Compose-tree capture; and the file lands in shared storage under
 * [SHOT_DIR], which outlives the instrumented run's uninstall of the app.
 * The short settle before the shot lets a just-decoded inline image reach
 * the frame.
 */
const val SHOT_DIR = "/sdcard/herold-shots"

fun ComposeTestRule.captureScreen(name: String) {
    waitForIdle()
    captureDeviceScreen(name)
}

/** The same capture, for a test that drives the device without a Compose rule. */
fun captureDeviceScreen(name: String) {
    SystemClock.sleep(SETTLE_MS)
    shell("mkdir -p $SHOT_DIR")
    shell("screencap -p $SHOT_DIR/$name.png")
}

/**
 * Captures one composable rather than the screen, for a shot the
 * platform will not let `screencap` take: the device-credential prompt
 * marks its window secure, so a screen capture under it is black,
 * while the composition beneath renders fine. The bitmap goes out
 * through the app's own external files directory - the only place the
 * app process may write - and is copied into [SHOT_DIR] from the
 * shell, where the harness collects it with the rest.
 */
fun ComposeTestRule.captureComposable(tag: String, name: String) {
    waitForIdle()
    val bitmap = onNodeWithTag(tag).captureToImage().asAndroidBitmap()
    val context = InstrumentationRegistry.getInstrumentation().targetContext
    val staged = File(context.getExternalFilesDir(null), "$name.png")
    FileOutputStream(staged).use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    shell("mkdir -p $SHOT_DIR")
    shell("cp ${staged.absolutePath} $SHOT_DIR/$name.png")
}

/** Runs a shell command and waits for it to finish. */
private fun shell(command: String) {
    val fd = InstrumentationRegistry.getInstrumentation().uiAutomation.executeShellCommand(command)
    ParcelFileDescriptor.AutoCloseInputStream(fd).use { stream ->
        while (stream.read() != -1) {
            // Draining the pipe is what makes the command run to completion.
        }
    }
}

private const val SETTLE_MS = 1_500L
