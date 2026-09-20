package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertEquals

/** Which half of a `multipart/alternative` the pane reads (issue #430). */
class BodyPreferenceTest {

    private val card = 387

    @Test
    fun htmlThatFitsIsShownWithNothingOffered() {
        val choice = BodyPreference.choose(
            hasHtml = true,
            text = "The same message as text.",
            minimumWidthCssPx = 0,
            contentWidthCssPx = card,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Html, null), choice)
    }

    @Test
    fun htmlThatCannotFitGivesWayToTheTextAlternative() {
        val choice = BodyPreference.choose(
            hasHtml = true,
            text = "The same message as text.",
            minimumWidthCssPx = 900,
            contentWidthCssPx = card,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Text, BodyVariant.Html), choice)
    }

    @Test
    fun theReaderCanAskForTheHtmlAnyway() {
        val choice = BodyPreference.choose(
            hasHtml = true,
            text = "The same message as text.",
            minimumWidthCssPx = 900,
            contentWidthCssPx = card,
            readerChose = BodyVariant.Html,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Html, BodyVariant.Text), choice)
    }

    @Test
    fun htmlWithNoTextAlternativeIsShownHoweverWideItIs() {
        val choice = BodyPreference.choose(
            hasHtml = true,
            text = null,
            minimumWidthCssPx = 900,
            contentWidthCssPx = card,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Html, null), choice)
    }

    @Test
    fun anEmptyTextAlternativeIsNoAlternative() {
        val choice = BodyPreference.choose(
            hasHtml = true,
            text = "   \n ",
            minimumWidthCssPx = 900,
            contentWidthCssPx = card,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Html, null), choice)
    }

    @Test
    fun aTextOnlyMessageReadsAsText() {
        val choice = BodyPreference.choose(
            hasHtml = false,
            text = "Just text.",
            minimumWidthCssPx = 0,
            contentWidthCssPx = card,
        )

        assertEquals(BodyPreference.Choice(BodyVariant.Text, null), choice)
    }

    /** The decision the reading pane actually makes, from the markup up. */
    @Test
    fun aDesktopNewsletterWithATextAlternativeStillReadsAsHtml() {
        val content = HtmlSanitizer.contentWidthCssPx(411)
        val sanitized = HtmlSanitizer.sanitize(
            "<div style=\"padding:20px\"><table width=\"575\" style=\"width:575px\">" +
                "<tr><td>Newsletter copy that reflows.</td></tr></table></div>",
            fitToWidthCssPx = content,
        )

        val choice = BodyPreference.choose(
            hasHtml = true,
            text = "Newsletter copy that reflows.",
            minimumWidthCssPx = sanitized.minimumWidthCssPx,
            contentWidthCssPx = content,
        )

        assertEquals(BodyVariant.Html, choice.show)
    }
}
