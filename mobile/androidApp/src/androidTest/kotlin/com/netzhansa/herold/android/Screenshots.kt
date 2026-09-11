package com.netzhansa.herold.android

import android.graphics.Bitmap
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.ComposeTestRule
import androidx.compose.ui.test.onRoot
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/**
 * Writes a PNG of the current screen into the app's external files
 * directory, where the acceptance harness pulls it from with `adb pull`.
 * A milestone closes on these images, not on a green build
 * (docs/design/android/implementation-plan.md, "Verification model").
 */
fun ComposeTestRule.captureScreen(name: String) {
    waitForIdle()
    val bitmap = onRoot().captureToImage().asAndroidBitmap()
    val dir = InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir(null)
        ?: return
    dir.mkdirs()
    File(dir, "$name.png").outputStream().use { out ->
        bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)
    }
}
