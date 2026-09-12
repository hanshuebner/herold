package com.netzhansa.herold.android.push

import android.graphics.Bitmap
import android.graphics.BitmapShader
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Rect
import android.graphics.RectF
import android.graphics.Shader
import android.graphics.Typeface
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.shared.mail.SenderAvatar

/**
 * The notification's large icon: the sender's picture where herold serves
 * one, otherwise their initials on a colour derived from the address
 * (issue #348, suite `REQ-MAIL-44`).
 *
 * Both come back as a round bitmap of the shade's large-icon size, because
 * that is what the platform draws unclipped in the notification and in the
 * conversation styles that reuse it.
 */
object SenderAvatars {

    /** The icon for a sender; never null, so a notification always has one. */
    fun of(bytes: ByteArray?, name: String?, address: String, sizePx: Int): Bitmap {
        val picture = bytes?.let { ImageScaling.decodeSampled(it, sizePx) }
        return if (picture != null) round(picture, sizePx) else initials(name, address, sizePx)
    }

    /** The picture, centre-cropped into a circle. */
    private fun round(source: Bitmap, sizePx: Int): Bitmap {
        val output = Bitmap.createBitmap(sizePx, sizePx, Bitmap.Config.ARGB_8888)
        val canvas = Canvas(output)
        val edge = minOf(source.width, source.height)
        val crop = Rect(
            (source.width - edge) / 2,
            (source.height - edge) / 2,
            (source.width - edge) / 2 + edge,
            (source.height - edge) / 2 + edge,
        )
        val square = Bitmap.createBitmap(source, crop.left, crop.top, crop.width(), crop.height())
        val scaled = Bitmap.createScaledBitmap(square, sizePx, sizePx, true)
        val paint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            shader = BitmapShader(scaled, Shader.TileMode.CLAMP, Shader.TileMode.CLAMP)
        }
        canvas.drawOval(RectF(0f, 0f, sizePx.toFloat(), sizePx.toFloat()), paint)
        if (square !== source) square.recycle()
        if (scaled !== square) scaled.recycle()
        source.recycle()
        return output
    }

    /** The fallback: one or two letters on the address's colour. */
    private fun initials(name: String?, address: String, sizePx: Int): Bitmap {
        val output = Bitmap.createBitmap(sizePx, sizePx, Bitmap.Config.ARGB_8888)
        val canvas = Canvas(output)
        val background = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            // A payload that names the sender without an address still
            // gets a colour of its own.
            color = SenderAvatar.colourFor(address.ifBlank { name.orEmpty() })
        }
        canvas.drawOval(RectF(0f, 0f, sizePx.toFloat(), sizePx.toFloat()), background)

        val letters = SenderAvatar.initialsFor(name, address)
        val text = Paint(Paint.ANTI_ALIAS_FLAG).apply {
            color = 0xFFFFFFFF.toInt()
            textAlign = Paint.Align.CENTER
            typeface = Typeface.create(Typeface.DEFAULT, Typeface.BOLD)
            textSize = sizePx * if (letters.length > 1) 0.38f else 0.48f
        }
        val metrics = text.fontMetrics
        val baseline = sizePx / 2f - (metrics.ascent + metrics.descent) / 2f
        canvas.drawText(letters, sizePx / 2f, baseline, text)
        return output
    }
}
