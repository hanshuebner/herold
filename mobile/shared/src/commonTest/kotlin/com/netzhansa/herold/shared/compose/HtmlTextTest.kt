package com.netzhansa.herold.shared.compose

import kotlin.test.Test
import kotlin.test.assertEquals

/** The `text/plain` alternative every send carries alongside the rich body. */
class HtmlTextTest {

    @Test
    fun paragraphsBecomeLines() {
        assertEquals(
            "First paragraph.\nSecond paragraph.",
            HtmlText.toPlainText("<p>First paragraph.</p><p>Second paragraph.</p>"),
        )
    }

    @Test
    fun formattingIsDroppedAndLineBreaksKept() {
        assertEquals(
            "A bold word.\nAfter the break.",
            HtmlText.toPlainText("<p>A <b>bold</b> word.<br>After the break.</p>"),
        )
    }

    @Test
    fun listItemsGetADash() {
        assertEquals(
            "- one\n- two",
            HtmlText.toPlainText("<ul><li>one</li><li>two</li></ul>"),
        )
    }

    @Test
    fun aQuotedOriginalIsPrefixed() {
        assertEquals(
            "My answer.\nBob wrote:\n> The original.\n> Second line.",
            HtmlText.toPlainText(
                "<p>My answer.</p><p>Bob wrote:</p><blockquote><p>The original.</p><p>Second line.</p></blockquote>",
            ),
        )
    }

    @Test
    fun aLinkKeepsItsTarget() {
        assertEquals(
            "the report (https://example.com/r)",
            HtmlText.toPlainText("""<p><a href="https://example.com/r">the report</a></p>"""),
        )
    }

    @Test
    fun anInlineImageIsNamedByItsAltText() {
        assertEquals(
            "before [chart] after",
            HtmlText.toPlainText("""<p>before <img src="cid:x" alt="chart"> after</p>"""),
        )
    }

    @Test
    fun entitiesAreDecoded() {
        assertEquals("a < b & c", HtmlText.toPlainText("<p>a &lt; b &amp; c</p>"))
    }

    @Test
    fun plainTextRoundTripsIntoParagraphs() {
        assertEquals(
            "<p>one<br>two</p><p>three</p>",
            HtmlText.toHtml("one\ntwo\n\nthree"),
        )
    }
}
