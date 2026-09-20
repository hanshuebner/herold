package com.netzhansa.herold.android.media

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * The scaling and encoding decision (issue #445), on the host JVM: what
 * counts as carrying an alpha channel, what format that leads to, and
 * what the display bound leaves an image at.
 *
 * The decision reads container headers, so the fixtures are container
 * headers: a PNG of a given colour type with the chunks it declares, the
 * three WebP shapes, a GIF, an icon, a JPEG. The pixel data behind them
 * is not read and is not there.
 */
class ImageFormatsTest {

    @Test
    fun `a truecolour png with an alpha channel is written back as png`() {
        val png = png(colourType = 6)

        assertTrue(ImageFormats.carriesAlpha(png))
        assertEquals(ImageEncoding.PNG, ImageFormats.encodingFor(png))
        assertEquals("image/png", ImageFormats.encodingFor(png).type)
    }

    @Test
    fun `a greyscale png with an alpha channel keeps it too`() {
        assertTrue(ImageFormats.carriesAlpha(png(colourType = 4)))
    }

    @Test
    fun `a palette png carries alpha through its tRNS chunk`() {
        assertTrue(ImageFormats.carriesAlpha(png(colourType = 3, chunks = listOf("tRNS"))))
        assertFalse(ImageFormats.carriesAlpha(png(colourType = 3)))
    }

    @Test
    fun `an opaque png takes the lossy path`() {
        val png = png(colourType = 2)

        assertFalse(ImageFormats.carriesAlpha(png))
        assertEquals(ImageEncoding.JPEG, ImageFormats.encodingFor(png))
    }

    @Test
    fun `a photograph takes the lossy path`() {
        assertFalse(ImageFormats.carriesAlpha(jpeg()))
        assertEquals(ImageEncoding.JPEG, ImageFormats.encodingFor(jpeg()))
    }

    @Test
    fun `a gif carries transparency`() {
        assertTrue(ImageFormats.carriesAlpha(gif()))
        assertEquals(ImageEncoding.PNG, ImageFormats.encodingFor(gif()))
    }

    @Test
    fun `a webp carries alpha when its header says so`() {
        assertTrue(ImageFormats.carriesAlpha(webp("VP8X", alpha = true)))
        assertFalse(ImageFormats.carriesAlpha(webp("VP8X", alpha = false)))
        assertTrue(ImageFormats.carriesAlpha(webp("VP8L", alpha = false)))
        assertFalse(ImageFormats.carriesAlpha(webp("VP8 ", alpha = false)))
    }

    @Test
    fun `an icon carries alpha`() {
        assertTrue(ImageFormats.carriesAlpha(byteArrayOf(0, 0, 1, 0, 1, 0)))
    }

    @Test
    fun `pixels the decoder found transparent keep the lossless path`() {
        assertEquals(ImageEncoding.PNG, ImageFormats.encodingFor(jpeg(), decodedHasAlpha = true))
    }

    @Test
    fun `nothing recognisable takes the lossy path`() {
        assertFalse(ImageFormats.carriesAlpha(ByteArray(0)))
        assertFalse(ImageFormats.carriesAlpha("not an image at all".toByteArray()))
        assertEquals(ImageEncoding.JPEG, ImageFormats.encodingFor("not an image at all".toByteArray()))
    }

    @Test
    fun `a truncated png is not read past its end`() {
        assertFalse(ImageFormats.carriesAlpha(png(colourType = 6).copyOf(20)))
    }

    @Test
    fun `the display bound holds on a banner`() {
        val bounded = ImageFormats.boundedTo(1875, 288, 1080)!!

        assertEquals(1080, bounded.first)
        assertEquals(165, bounded.second)
        assertTrue(maxOf(bounded.first, bounded.second) <= 1080)
    }

    @Test
    fun `an image within the bound is left alone`() {
        assertNull(ImageFormats.boundedTo(800, 600, 1080))
        assertNull(ImageFormats.boundedTo(1080, 1080, 1080))
        assertNull(ImageFormats.boundedTo(0, 0, 1080))
    }

    @Test
    fun `an edge never rounds away`() {
        val bounded = ImageFormats.boundedTo(20_000, 3, 1000)!!

        assertEquals(1000, bounded.first)
        assertEquals(1, bounded.second)
    }

    @Test
    fun `the name states the format the bytes are in`() {
        assertEquals("logo.png", ImageFormats.rename("logo.png", ImageEncoding.PNG))
        assertEquals("logo.png", ImageFormats.rename("logo.gif", ImageEncoding.PNG))
        assertEquals("camera.jpg", ImageFormats.rename("camera.jpg", ImageEncoding.JPEG))
        assertEquals("camera.jpeg", ImageFormats.rename("camera.jpeg", ImageEncoding.JPEG))
        assertEquals("logo.jpg", ImageFormats.rename("logo.png", ImageEncoding.JPEG))
        assertEquals("inline.png", ImageFormats.rename("inline", ImageEncoding.PNG))
    }

    // ---- fixtures --------------------------------------------------------

    /**
     * A PNG header: the signature, an IHDR declaring [colourType], the
     * [chunks] that follow it, and the image data the decision stops at.
     */
    private fun png(colourType: Int, chunks: List<String> = emptyList()): ByteArray {
        val out = mutableListOf<Byte>()
        out += listOf(0x89.toByte(), 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
        val ihdr = mutableListOf<Byte>()
        ihdr += int(1875)
        ihdr += int(288)
        ihdr += listOf<Byte>(8, colourType.toByte(), 0, 0, 0)
        out += chunk("IHDR", ihdr)
        chunks.forEach { out += chunk(it, listOf<Byte>(0, 0)) }
        out += chunk("IDAT", List(16) { 0.toByte() })
        out += chunk("IEND", emptyList())
        return out.toByteArray()
    }

    /** One PNG chunk: length, type, payload, and the four CRC bytes. */
    private fun chunk(type: String, payload: List<Byte>): List<Byte> =
        int(payload.size) + type.toByteArray(Charsets.US_ASCII).toList() + payload + int(0)

    private fun int(value: Int): List<Byte> = listOf(
        (value ushr 24).toByte(),
        (value ushr 16).toByte(),
        (value ushr 8).toByte(),
        value.toByte(),
    )

    private fun jpeg(): ByteArray =
        byteArrayOf(0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte()) + ByteArray(64)

    private fun gif(): ByteArray = "GIF89a".toByteArray(Charsets.US_ASCII) + ByteArray(32)

    /** A RIFF container whose first chunk is [chunk], with or without the alpha flag. */
    private fun webp(chunk: String, alpha: Boolean): ByteArray {
        val out = mutableListOf<Byte>()
        out += "RIFF".toByteArray(Charsets.US_ASCII).toList()
        out += int(64)
        out += "WEBP".toByteArray(Charsets.US_ASCII).toList()
        out += chunk.toByteArray(Charsets.US_ASCII).toList()
        out += int(32)
        out += (if (alpha) 0x10 else 0x00).toByte()
        out += List(32) { 0.toByte() }
        return out.toByteArray()
    }
}
