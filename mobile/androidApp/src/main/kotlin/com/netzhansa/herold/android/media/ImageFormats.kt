package com.netzhansa.herold.android.media

/**
 * What a re-encoded image is written as (issue #445).
 *
 * JPEG has no alpha channel, and the platform encoder writes a
 * transparent pixel as black, so an image that carries transparency is
 * written losslessly instead. [type] is the content type of the bytes
 * that come out, which is what a WebView or a recipient is told.
 */
enum class ImageEncoding(val type: String, val extension: String) {
    /** A photograph, where a smaller file is worth the loss. */
    JPEG("image/jpeg", "jpg"),

    /** Artwork with transparency, kept exactly and with its alpha channel. */
    PNG("image/png", "png"),
}

/**
 * The scaling and encoding decision, taken on the bytes alone.
 *
 * It is separate from [ImageScaling] - which decodes, scales and
 * encodes through the platform - so the decision itself is a plain
 * Kotlin function with a host-JVM test over it: whether an image
 * carries an alpha channel, what format it is therefore re-encoded in,
 * and what size the bound leaves it at.
 */
object ImageFormats {

    /**
     * Whether [bytes] carries transparency, read from the container's
     * own header.
     *
     * The answer is conservative: a format that can carry alpha counts
     * as carrying it, since writing a PNG for an image that turned out
     * to be opaque costs bytes, while writing a JPEG for one that was
     * not costs the picture. Formats with no alpha at all - JPEG above
     * all - answer false and take the lossy path.
     */
    fun carriesAlpha(bytes: ByteArray): Boolean = when {
        isPng(bytes) -> pngCarriesAlpha(bytes)
        isGif(bytes) -> true
        isWebp(bytes) -> webpCarriesAlpha(bytes)
        // A cursor or icon file is RGBA throughout.
        isIco(bytes) -> true
        else -> false
    }

    /**
     * How [bytes] is written when it is re-encoded. [decodedHasAlpha] is
     * what the platform decoder reported about the same image, so a
     * container this does not recognise is still written losslessly when
     * the pixels turn out to have an alpha channel.
     */
    fun encodingFor(bytes: ByteArray, decodedHasAlpha: Boolean = false): ImageEncoding =
        if (decodedHasAlpha || carriesAlpha(bytes)) ImageEncoding.PNG else ImageEncoding.JPEG

    /**
     * The size an image of [width] by [height] takes to fit inside
     * [bound] on its longer edge, or null when it is already within it
     * and is left as it was. The aspect ratio is kept, and an edge never
     * rounds down to nothing.
     */
    fun boundedTo(width: Int, height: Int, bound: Int): Pair<Int, Int>? {
        if (width <= 0 || height <= 0 || bound <= 0) return null
        val longer = maxOf(width, height)
        if (longer <= bound) return null
        val factor = bound.toFloat() / longer
        return (width * factor).toInt().coerceAtLeast(1) to (height * factor).toInt().coerceAtLeast(1)
    }

    /** [name] with the extension the [encoding] it was written in implies. */
    fun rename(name: String, encoding: ImageEncoding): String {
        val current = name.substringAfterLast('.', "").lowercase()
        if (current == encoding.extension || (encoding == ImageEncoding.JPEG && current == "jpeg")) return name
        val stem = name.substringBeforeLast('.', name)
        return "${stem.ifBlank { name }}.${encoding.extension}"
    }

    // ---- containers ------------------------------------------------------

    private val PNG_SIGNATURE = byteArrayOf(-119, 80, 78, 71, 13, 10, 26, 10)

    private fun isPng(bytes: ByteArray): Boolean =
        bytes.size > 33 && PNG_SIGNATURE.indices.all { bytes[it] == PNG_SIGNATURE[it] }

    /**
     * A PNG's IHDR names its colour type at a fixed offset: 4 is grey
     * with alpha and 6 is truecolour with alpha. The other three carry
     * transparency only through a tRNS chunk, so those are looked for.
     */
    private fun pngCarriesAlpha(bytes: ByteArray): Boolean {
        val colourType = bytes[25].toInt() and 0xFF
        if (colourType == 4 || colourType == 6) return true
        return hasChunk(bytes, "tRNS")
    }

    /** Whether the PNG holds a chunk of [name], walking the chunk list. */
    private fun hasChunk(bytes: ByteArray, name: String): Boolean {
        var offset = 8
        while (offset + 8 <= bytes.size) {
            val length = readInt(bytes, offset)
            if (length < 0) return false
            val type = String(bytes, offset + 4, 4, Charsets.US_ASCII)
            if (type == name) return true
            if (type == "IDAT" || type == "IEND") return false
            offset += 12 + length
        }
        return false
    }

    private fun isGif(bytes: ByteArray): Boolean =
        bytes.size > 6 && String(bytes, 0, 3, Charsets.US_ASCII) == "GIF"

    private fun isWebp(bytes: ByteArray): Boolean =
        bytes.size > 16 &&
            String(bytes, 0, 4, Charsets.US_ASCII) == "RIFF" &&
            String(bytes, 8, 4, Charsets.US_ASCII) == "WEBP"

    /**
     * An extended WebP declares alpha in its VP8X flags; a lossless one
     * (VP8L) may carry it in its own header, so it counts. Plain lossy
     * VP8 has none.
     */
    private fun webpCarriesAlpha(bytes: ByteArray): Boolean {
        val chunk = String(bytes, 12, 4, Charsets.US_ASCII)
        return when (chunk) {
            "VP8X" -> bytes.size > 20 && (bytes[20].toInt() and 0x10) != 0
            "VP8L" -> true
            else -> false
        }
    }

    private fun isIco(bytes: ByteArray): Boolean =
        bytes.size > 4 && bytes[0].toInt() == 0 && bytes[1].toInt() == 0 &&
            (bytes[2].toInt() == 1 || bytes[2].toInt() == 2) && bytes[3].toInt() == 0

    private fun readInt(bytes: ByteArray, offset: Int): Int {
        if (offset + 4 > bytes.size) return -1
        return ((bytes[offset].toInt() and 0xFF) shl 24) or
            ((bytes[offset + 1].toInt() and 0xFF) shl 16) or
            ((bytes[offset + 2].toInt() and 0xFF) shl 8) or
            (bytes[offset + 3].toInt() and 0xFF)
    }
}
