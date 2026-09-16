package com.netzhansa.herold.android.diag

import android.app.Activity
import android.graphics.Bitmap
import android.os.Handler
import android.os.Looper
import android.view.PixelCopy
import android.view.View
import java.io.ByteArrayOutputStream
import kotlin.coroutines.resume
import kotlin.coroutines.suspendCoroutine

/**
 * A PNG of the window as it stands (REQ-AND-SYS-51). `PixelCopy` reads
 * the composited surface, so what the report carries is what the user
 * was looking at - the reading pane's WebView included, which a
 * view-tree capture would leave blank.
 *
 * The bitmap is scaled down before encoding: a report is mail, and a
 * phone-resolution screenshot is several megabytes of it.
 */
object WindowCapture {

    /** How long the longer edge of the captured image may be. */
    const val MAX_EDGE_PX = 1080

    /** What the PNG is compressed at; PNG ignores it, kept for clarity. */
    private const val QUALITY = 100

    /** The window's pixels as a PNG, or null when the platform refused. */
    suspend fun png(activity: Activity): ByteArray? {
        val bitmap = capture(activity) ?: return null
        val scaled = scale(bitmap)
        return ByteArrayOutputStream().use { out ->
            scaled.compress(Bitmap.CompressFormat.PNG, QUALITY, out)
            if (scaled !== bitmap) scaled.recycle()
            bitmap.recycle()
            out.toByteArray()
        }
    }

    private suspend fun capture(activity: Activity): Bitmap? {
        val view: View = activity.window?.decorView ?: return null
        val width = view.width
        val height = view.height
        if (width <= 0 || height <= 0) return null
        val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
        return suspendCoroutine { continuation ->
            runCatching {
                PixelCopy.request(
                    activity.window,
                    bitmap,
                    { result ->
                        if (result == PixelCopy.SUCCESS) {
                            continuation.resume(bitmap)
                        } else {
                            bitmap.recycle()
                            continuation.resume(null)
                        }
                    },
                    Handler(Looper.getMainLooper()),
                )
            }.onFailure {
                bitmap.recycle()
                continuation.resume(null)
            }
        }
    }

    private fun scale(bitmap: Bitmap): Bitmap {
        val longer = maxOf(bitmap.width, bitmap.height)
        if (longer <= MAX_EDGE_PX) return bitmap
        val factor = MAX_EDGE_PX.toFloat() / longer
        return Bitmap.createScaledBitmap(
            bitmap,
            (bitmap.width * factor).toInt().coerceAtLeast(1),
            (bitmap.height * factor).toInt().coerceAtLeast(1),
            true,
        )
    }
}
