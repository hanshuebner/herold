package com.netzhansa.herold.shared.compose

/**
 * The display box an inlined image is written with (issue #367).
 *
 * A receiving client that lays a message out without the sender's CSS -
 * Gmail's web view among them - draws an `img` at its intrinsic pixel
 * size, so a phone photo arrives as a wall of image. The tag therefore
 * carries the box the image is meant to be drawn in, which is what Gmail
 * itself emits (`<img src="cid:..." width="562" height="423">`), and a
 * `max-width:100%` so a narrower column still fits it.
 */
object InlineImage {

    /** The widest an inlined image is drawn, in CSS pixels. */
    const val MAX_WIDTH = 562

    /** The width and height attributes an inlined image carries. */
    data class Box(val width: Int, val height: Int)

    /**
     * The display box for an image of [width] x [height] encoded pixels.
     * An image narrower than [maxWidth] keeps its own size; a wider one is
     * bounded to [maxWidth] with its aspect ratio held. Null when the
     * encoded size is unknown, in which case the tag carries no
     * dimensions rather than a guess.
     */
    fun fit(width: Int, height: Int, maxWidth: Int = MAX_WIDTH): Box? {
        if (width <= 0 || height <= 0 || maxWidth <= 0) return null
        if (width <= maxWidth) return Box(width, height)
        val bounded = (height.toLong() * maxWidth / width).toInt().coerceAtLeast(1)
        return Box(maxWidth, bounded)
    }

    /** The `img` element the composer inserts for [src]. */
    fun tag(src: String, box: Box?): String = buildString {
        append("<img src=\"").append(src).append("\"")
        if (box != null) {
            append(" width=\"").append(box.width).append("\"")
            append(" height=\"").append(box.height).append("\"")
        }
        append(" style=\"max-width:100%\">")
    }
}
