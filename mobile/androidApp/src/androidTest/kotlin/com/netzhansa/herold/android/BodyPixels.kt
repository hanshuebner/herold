package com.netzhansa.herold.android

import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.Rect
import android.os.SystemClock
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertTrue
import java.io.ByteArrayOutputStream

/**
 * Reading the pixels a message body put on screen (issue #445).
 *
 * A screenshot shows a reviewer what happened; these helpers let a check
 * assert it. The body is a WebView, so what a test has to find its
 * images by is the accessibility tree, where an `alt` text arrives as
 * the node's description, and what it reads the colours from is a
 * capture of the device screen.
 */
private const val PIXELS_TAG = "HeroldBodyPixels"

/** How far apart two colours may be and still be the same colour. */
private const val TOLERANCE = 12

/** How much bluer than red the fixture's artwork has to read to be itself. */
private const val ARTWORK_MARGIN = 40

/** Below this width the tree has a box but the body has not laid out. */
private const val MIN_BOX_PX = 100

private const val BOUNDS_TIMEOUT_MS = 20_000L
private const val POLL_MS = 100L

/**
 * A banner in the shape the reported message carried: wide enough to
 * cross the pane's display bound, transparent over white on its left
 * half, noisy artwork on its right. The noise is what keeps the encoder
 * from shrinking the banner below the size where scaling it is worth
 * doing.
 */
fun transparentBannerPng(width: Int = 1875, height: Int = 288): ByteArray {
    val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
    bitmap.eraseColor(Color.TRANSPARENT)
    val half = width / 2
    val artwork = IntArray(half * height)
    var state = 0x5eed
    for (index in artwork.indices) {
        state = state * 1103515245 + 12345
        val jitter = state ushr 16 and 0x3F
        artwork[index] = Color.rgb(0x10 + jitter, 0x50 + jitter, 0xd0 + (jitter and 0x1F))
    }
    bitmap.setPixels(artwork, 0, half, half, 0, half, height)
    val out = ByteArrayOutputStream()
    bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)
    bitmap.recycle()
    return out.toByteArray()
}

/** The IHDR colour type of a PNG: 6 is truecolour with alpha. */
fun pngColourType(bytes: ByteArray): Int = bytes[25].toInt() and 0xFF

/** Waits for the image a document named [alt] to take a box on screen. */
fun awaitBodyImage(alt: String): Rect {
    val deadline = SystemClock.uptimeMillis() + BOUNDS_TIMEOUT_MS
    var bounds = bodyImageBounds(alt)
    while ((bounds == null || bounds.width() < MIN_BOX_PX) && SystemClock.uptimeMillis() < deadline) {
        SystemClock.sleep(POLL_MS)
        bounds = bodyImageBounds(alt)
    }
    return bounds?.takeIf { it.width() >= MIN_BOX_PX }
        ?: error("the body never drew the image \"$alt\" (saw $bounds)")
}

/** Where the document drew the image it named [alt], from the accessibility tree. */
fun bodyImageBounds(alt: String): Rect? {
    val automation = InstrumentationRegistry.getInstrumentation().uiAutomation
    val root = automation.rootInActiveWindow ?: return null
    val node = firstAccessibilityNode(root) { it.contentDescription?.toString() == alt } ?: return null
    return Rect().also { node.getBoundsInScreen(it) }
}

fun firstAccessibilityNode(
    node: AccessibilityNodeInfo,
    match: (AccessibilityNodeInfo) -> Boolean,
): AccessibilityNodeInfo? {
    if (match(node)) return node
    for (i in 0 until node.childCount) {
        val child = node.getChild(i) ?: continue
        firstAccessibilityNode(child, match)?.let { return it }
    }
    return null
}

/** What the device currently shows, WebView surface and all. */
fun deviceScreen(): Bitmap =
    InstrumentationRegistry.getInstrumentation().uiAutomation.takeScreenshot()
        ?: error("the device produced no screenshot")

/**
 * The transparent half of the banner at [image] reads as the colour
 * [behind] it, and its artwork half reads as artwork: an image
 * composited onto a flat colour fails the first, an image that never
 * loaded fails the second.
 *
 * @param what names the surface behind the image, for the failure
 * @param where names the flow the check is in, for the failure
 */
fun assertShowsThrough(screen: Bitmap, image: Rect, behind: Int, what: String, where: String) {
    val y = image.centerY().coerceIn(0, screen.height - 1)
    val clear = screen.getPixel((image.left + image.width() / 4).coerceIn(0, screen.width - 1), y)
    val artwork = screen.getPixel((image.left + 3 * image.width() / 4).coerceIn(0, screen.width - 1), y)
    Log.i(
        PIXELS_TAG,
        "$where $what: through ${hexColor(clear)} expecting ${hexColor(behind)}, " +
            "artwork ${hexColor(artwork)}, box $image",
    )
    assertTrue(
        "the transparent half of the image reads ${hexColor(clear)} where $what is " +
            "${hexColor(behind)} ($where, box $image)",
        nearColor(clear, behind),
    )
    assertTrue(
        "the artwork half of the image reads ${hexColor(artwork)}, so the image did not render " +
            "($where, box $image)",
        Color.blue(artwork) > Color.red(artwork) + ARTWORK_MARGIN,
    )
}

fun nearColor(seen: Int, expected: Int): Boolean =
    kotlin.math.abs(Color.red(seen) - Color.red(expected)) <= TOLERANCE &&
        kotlin.math.abs(Color.green(seen) - Color.green(expected)) <= TOLERANCE &&
        kotlin.math.abs(Color.blue(seen) - Color.blue(expected)) <= TOLERANCE

fun hexColor(color: Int): String =
    "#%02x%02x%02x".format(Color.red(color), Color.green(color), Color.blue(color))
