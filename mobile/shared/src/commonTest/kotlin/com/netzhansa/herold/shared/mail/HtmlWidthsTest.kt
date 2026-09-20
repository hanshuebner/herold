package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * The widths a desktop template declares, made to yield to the card
 * (issue #430). The card here is 387 CSS pixels wide, what a phone's
 * message card gives the body.
 */
class HtmlWidthsTest {

    private val card = 387

    @Test
    fun aTableWiderThanTheCardLosesItsWidthAttribute() {
        val out = HtmlWidths.neutralise("""<table width="575" cellpadding="0"><tr></tr></table>""", card)

        assertFalse(out.contains("width=\"575\""), out)
        assertContains(out, "cellpadding=\"0\"")
    }

    @Test
    fun aTableThatAlreadyFitsKeepsItsWidth() {
        val out = HtmlWidths.neutralise("""<table width="320"><tr></tr></table>""", card)

        assertContains(out, "width=\"320\"")
    }

    @Test
    fun aPercentageWidthIsLeftAlone() {
        val out = HtmlWidths.neutralise("""<table width="100%"><td width="50%"></td></table>""", card)

        assertContains(out, "width=\"100%\"")
        assertContains(out, "width=\"50%\"")
    }

    @Test
    fun anInlineWidthWiderThanTheCardBecomesAuto() {
        val out = HtmlWidths.neutralise("""<div style="width: 575px; padding: 20px">x</div>""", card)

        assertContains(out, "width:auto")
        assertContains(out, "padding: 20px")
        assertFalse(out.contains("575px"), out)
    }

    @Test
    fun anInlineMinimumWidthWiderThanTheCardIsRemoved() {
        val out = HtmlWidths.neutralise("""<div style="min-width:600px;color:red">x</div>""", card)

        assertFalse(out.contains("min-width"), out)
        assertContains(out, "color:red")
    }

    @Test
    fun anInlineMaximumWidthWiderThanTheCardBecomesRelative() {
        val out = HtmlWidths.neutralise("""<div style="max-width:640px">x</div>""", card)

        assertContains(out, "max-width:100%")
    }

    @Test
    fun aPointLengthIsReadAsPixels() {
        // 480pt is 640px, well past the card; 200pt is 266px, which fits.
        val wide = HtmlWidths.neutralise("""<div style="width:480pt">x</div>""", card)
        val narrow = HtmlWidths.neutralise("""<div style="width:200pt">x</div>""", card)

        assertContains(wide, "width:auto")
        assertContains(narrow, "width:200pt")
    }

    @Test
    fun aFixedHeightGoesWithTheWidthItSized() {
        val out = HtmlWidths.neutralise("""<td width="555" height="60">x</td>""", card)

        assertFalse(out.contains("height=\"60\""), out)
    }

    @Test
    fun anImageThatFitsKeepsItsGeometry() {
        val out = HtmlWidths.neutralise("""<img src="x.png" width="24" height="24">""", card)

        assertContains(out, "width=\"24\"")
        assertContains(out, "height=\"24\"")
    }

    /** The shape of the message in issue #430, start to finish. */
    @Test
    fun theDesktopNewsletterReflows() {
        val out = HtmlWidths.neutralise(DESKTOP_NEWSLETTER, card)

        assertFalse(out.contains("575"), out)
        assertFalse(out.contains("555"), out)
        assertEquals(0, HtmlWidths.minimumWidthCssPx(out))
    }

    @Test
    fun aRowThatRefusesToWrapStatesHowWideItNeedsToBe() {
        val row = "<table><tr style=\"white-space:nowrap\"><td>" +
            (1..40).joinToString("</td><td>") { "column $it" } +
            "</td></tr></table>"

        assertTrue(HtmlWidths.minimumWidthCssPx(row) > card, "a forty-column row fits the card")
    }

    @Test
    fun aShortUnwrappableRunFitsTheCard() {
        val out = "<p><span style=\"white-space:nowrap\">+49 30 123456</span></p>"

        assertTrue(HtmlWidths.minimumWidthCssPx(out) <= card, HtmlWidths.minimumWidthCssPx(out).toString())
    }

    @Test
    fun thePreWrappedPlainTextWrapperDoesNotCountAsUnwrappable() {
        val wrapped = HtmlSanitizer.fromPlainText("a very long line of plain text ".repeat(20))

        assertEquals(0, HtmlWidths.minimumWidthCssPx(wrapped))
    }

    private companion object {
        /**
         * A centred table declared at 575px inside a 20px-padded div,
         * with a nested 555px table and inline images - the layout the
         * message in issue #430 is written in.
         */
        val DESKTOP_NEWSLETTER = """
            <div style="padding:20px">
              <table width="575" align="center" style="width:575px">
                <tr><td><img src="cid:banner" width="555" height="120"></td></tr>
                <tr><td>
                  <table width="555" style="width:555px"><tr>
                    <td width="277">Left column as the template writes it.</td>
                    <td width="278">Right column as the template writes it.</td>
                  </tr></table>
                </td></tr>
              </table>
            </div>
        """.trimIndent()
    }
}
