package com.netzhansa.herold.shared.compose

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

class InlineImageTest {

    @Test
    fun aCameraPhotoIsBoundedToTheDisplayWidth() {
        val box = InlineImage.fit(4000, 3000)
        assertEquals(InlineImage.Box(562, 421), box)
    }

    @Test
    fun anImageNarrowerThanTheBoxKeepsItsOwnSize() {
        assertEquals(InlineImage.Box(320, 240), InlineImage.fit(320, 240))
    }

    @Test
    fun aPortraitPhotoKeepsItsAspectRatio() {
        val box = InlineImage.fit(3000, 4000)!!
        assertEquals(562, box.width)
        assertEquals(749, box.height)
    }

    @Test
    fun anUnknownSizeProducesNoBox() {
        assertNull(InlineImage.fit(0, 0))
        assertNull(InlineImage.fit(-1, 10))
    }

    @Test
    fun theTagCarriesGmailsShape() {
        val tag = InlineImage.tag("cid:abc@herold.local", InlineImage.fit(4000, 3000))
        assertEquals(
            "<img src=\"cid:abc@herold.local\" width=\"562\" height=\"421\" style=\"max-width:100%\">",
            tag,
        )
    }

    @Test
    fun aTagWithoutABoxStillBoundsItselfToTheColumn() {
        val tag = InlineImage.tag("cid:abc@herold.local", null)
        assertTrue(tag.contains("max-width:100%"), tag)
        assertTrue(!tag.contains("width=\"5"), tag)
    }
}
