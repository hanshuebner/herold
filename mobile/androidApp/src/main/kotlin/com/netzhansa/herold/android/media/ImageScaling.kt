package com.netzhansa.herold.android.media

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import java.io.ByteArrayOutputStream

/**
 * What a photo from the camera is reduced to before it is uploaded, and how
 * a received image is decoded for the screen (issue #341).
 *
 * A phone camera produces a 12-megapixel JPEG of several megabytes. Sent as
 * it is, it inflates the message for every recipient and costs the reader a
 * full-resolution decode to paint a view a few hundred pixels wide. Both
 * ends are handled here: [scale] re-encodes an outgoing image at the size
 * the sender picked, and [decodeSampled] / [thumbnail] decode an incoming
 * one at roughly the size it is displayed at.
 */
object ImageScaling {

    /**
     * Above this an image attachment is worth asking about. A megabyte is
     * the point where a camera photo is unmistakably a camera photo and a
     * screenshot or a logo still passes through untouched.
     */
    const val OFFER_THRESHOLD_BYTES = 1_000_000

    /** JPEG quality for a re-encoded attachment: visually clean, a fraction of the bytes. */
    private const val JPEG_QUALITY = 85

    fun isImage(type: String): Boolean = type.startsWith("image/", ignoreCase = true)

    /** The image's pixel dimensions without decoding it, or null when it is not one. */
    fun dimensions(bytes: ByteArray): Pair<Int, Int>? {
        val options = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, options)
        if (options.outWidth <= 0 || options.outHeight <= 0) return null
        return options.outWidth to options.outHeight
    }

    /**
     * [bytes] re-encoded so its longer edge is at most [size]'s bound.
     * `ORIGINAL`, an image already within the bound, and anything that does
     * not decode come back untouched.
     */
    fun scale(name: String, type: String, bytes: ByteArray, size: ImageSize): ScaledImage {
        val bound = size.maxEdgePx ?: return ScaledImage(name, type, bytes)
        return scaleTo(name, type, bytes, bound)
    }

    /** The same, bounded by an explicit pixel edge. */
    fun scaleTo(name: String, type: String, bytes: ByteArray, bound: Int): ScaledImage {
        val (width, height) = dimensions(bytes) ?: return ScaledImage(name, type, bytes)
        if (maxOf(width, height) <= bound) return ScaledImage(name, type, bytes)

        val decoded = decodeSampled(bytes, bound) ?: return ScaledImage(name, type, bytes)
        val factor = bound.toFloat() / maxOf(decoded.width, decoded.height)
        val target = if (factor < 1f) {
            Bitmap.createScaledBitmap(
                decoded,
                (decoded.width * factor).toInt().coerceAtLeast(1),
                (decoded.height * factor).toInt().coerceAtLeast(1),
                true,
            )
        } else {
            decoded
        }
        val out = ByteArrayOutputStream()
        target.compress(Bitmap.CompressFormat.JPEG, JPEG_QUALITY, out)
        if (target !== decoded) target.recycle()
        decoded.recycle()
        val encoded = out.toByteArray()
        if (encoded.isEmpty() || encoded.size >= bytes.size) return ScaledImage(name, type, bytes)
        return ScaledImage(jpegName(name), "image/jpeg", encoded)
    }

    /**
     * Decodes [bytes] with the smallest sample step that still covers
     * [maxEdgePx], so a multi-megapixel image never occupies its full
     * bitmap in memory to be drawn small.
     */
    fun decodeSampled(bytes: ByteArray, maxEdgePx: Int): Bitmap? {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= maxEdgePx) sample *= 2
        val options = BitmapFactory.Options().apply { inSampleSize = sample }
        return BitmapFactory.decodeByteArray(bytes, 0, bytes.size, options)
    }

    /** A bounded thumbnail of [bytes] for an attachment chip. */
    fun thumbnail(bytes: ByteArray, maxEdgePx: Int): ImageBitmap? =
        decodeSampled(bytes, maxEdgePx)?.asImageBitmap()

    /**
     * [bytes] re-encoded for display at [maxEdgePx] when it is larger than
     * that, for the reading pane's inline images: the WebView bounds them
     * to the column width anyway, and decoding the original is what makes
     * a thread with a camera photo in it stall on open.
     */
    fun forDisplay(bytes: ByteArray, maxEdgePx: Int): ByteArray {
        val (width, height) = dimensions(bytes) ?: return bytes
        if (maxOf(width, height) <= maxEdgePx) return bytes
        return scaleTo("inline.jpg", "image/jpeg", bytes, maxEdgePx).bytes
    }

    private fun jpegName(name: String): String {
        val stem = name.substringBeforeLast('.', name)
        return if (name.endsWith(".jpg", true) || name.endsWith(".jpeg", true)) name else "$stem.jpg"
    }
}

/** An image as it will be uploaded. */
data class ScaledImage(val name: String, val type: String, val bytes: ByteArray) {
    override fun equals(other: Any?): Boolean =
        other is ScaledImage && name == other.name && type == other.type && bytes.contentEquals(other.bytes)

    override fun hashCode(): Int = 31 * (31 * name.hashCode() + type.hashCode()) + bytes.contentHashCode()
}

/**
 * The sizes an image attachment is offered at, the choice Gmail presents
 * when a photo goes out. [maxEdgePx] bounds the longer edge; null sends the
 * file as it was picked.
 */
enum class ImageSize(val label: String, val maxEdgePx: Int?) {
    SMALL("Small", 1024),
    MEDIUM("Medium", 1600),
    LARGE("Large", 2048),
    ORIGINAL("Original", null),
    ;

    companion object {
        /** The default offer: large enough to look like the photo, small enough to send. */
        val DEFAULT = LARGE

        fun named(name: String?): ImageSize = entries.firstOrNull { it.name == name } ?: DEFAULT
    }
}

/**
 * The size the user picked last, so the choice is made once and then
 * remembered (issue #341). It is a UI preference, not a credential, so it
 * lives in ordinary preferences.
 */
object ImageSizePreference {
    private const val FILE = "herold-ui"
    private const val KEY = "attachment_image_size"

    fun last(context: Context): ImageSize =
        ImageSize.named(
            context.getSharedPreferences(FILE, Context.MODE_PRIVATE).getString(KEY, null),
        )

    fun remember(context: Context, size: ImageSize) {
        context.getSharedPreferences(FILE, Context.MODE_PRIVATE).edit().putString(KEY, size.name).apply()
    }
}
