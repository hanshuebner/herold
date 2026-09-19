package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * The quoted-history fold (issue #432), on the shapes the Suite's own
 * fixtures cover (`web/apps/suite/src/lib/mail/sanitize.test.ts`,
 * `quoted.test.ts`), so both clients fold the same mail the same way.
 */
class QuotedHistoryTest {

    private fun folded(html: String): String =
        HtmlSanitizer.sanitize(html, collapseQuotes = true).html

    private fun assertInsideFold(html: String, text: String) {
        val start = html.indexOf(DETAILS)
        val end = html.indexOf("</details>")
        assertTrue(start >= 0, "nothing folded in: $html")
        val at = html.indexOf(text)
        assertTrue(at > start && at < end, "\"$text\" folded outside the region: $html")
    }

    private fun assertBeforeFold(html: String, text: String) {
        val start = html.indexOf(DETAILS)
        assertTrue(start >= 0, "nothing folded in: $html")
        val at = html.indexOf(text)
        assertTrue(at in 0 until start, "\"$text\" did not stay ahead of the fold: $html")
    }

    // ---- the HTML path ---------------------------------------------------

    @Test
    fun foldsATrailingBlockquoteBehindTheChip() {
        val html = folded("<p>My reply.</p><blockquote>Original message body.</blockquote>")

        assertContains(html, DETAILS)
        assertContains(html, QuotedHtml.SHOW_LABEL)
        assertContains(html, QuotedHtml.HIDE_LABEL)
        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "Original message body.")
    }

    @Test
    fun foldsAGmailQuoteDiv() {
        val html = folded(
            "<p>My reply.</p>" +
                "<div class=\"gmail_quote_attribution\">On Mon, Alice wrote:</div>" +
                "<div class=\"gmail_quote\">Original quoted text</div>",
        )

        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "On Mon, Alice wrote:")
        assertInsideFold(html, "Original quoted text")
    }

    @Test
    fun leavesABodyWithNoQuoteAlone() {
        val source = "<p>Just my reply.</p>"
        assertEquals(source, folded(source))
    }

    @Test
    fun leavesABottomPostedReplyExpanded() {
        val html = folded(
            "<blockquote><p>On Mon Alice wrote: original message text</p></blockquote>" +
                "<div><p>My bottom-posted reply that must remain visible</p></div>",
        )

        assertFalse(html.contains("<details"), "the quote folded over a bottom-posted reply")
        assertContains(html, "My bottom-posted reply that must remain visible")
    }

    @Test
    fun leavesAnInterleavedReplyExpanded() {
        val html = folded(
            "<p>First reply paragraph.</p><blockquote>First quoted block.</blockquote>" +
                "<p>Second reply paragraph.</p><blockquote>Second quoted block.</blockquote>",
        )

        assertFalse(html.contains("<details"), "an interleaved reply folded")
        assertContains(html, "Second quoted block.")
    }

    @Test
    fun foldsABodyThatIsNothingButAQuote() {
        val html = folded("<blockquote>Only quoted text, no reply at all.</blockquote>")

        assertInsideFold(html, "Only quoted text, no reply at all.")
    }

    @Test
    fun sweepsSeparatorsAndWhitespaceIntoTheFold() {
        val html = folded("<p>My reply.</p><blockquote><p>Quoted</p></blockquote><br>   ")

        assertInsideFold(html, "<br>")
        assertBeforeFold(html, "My reply.")
    }

    @Test
    fun foldsTheCitationIntroducerWithTheQuote() {
        val html = folded(
            "<p>My reply.</p>" +
                "<p>On Mon, 13 Jul 2026, Alice &lt;a@x.test&gt; wrote:</p>" +
                "<blockquote>Original message.</blockquote>",
        )

        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "wrote:")
    }

    @Test
    fun foldsAnIntroducerSeparatedFromTheQuoteByWhitespace() {
        val html = folded(
            "<p>My reply.</p>\n" +
                "<p>On Mon, 13 Jul 2026, Florian &lt;florian@example.local&gt; wrote:</p>\n" +
                "  <blockquote>Original message.</blockquote>\n",
        )

        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "wrote:")
    }

    @Test
    fun foldsAGermanNameFirstIntroducer() {
        val html = folded(
            "<p>Danke!</p>" +
                "<p>Hans Huebner (Vorstand VzEkC e.V.) schrieb am 13.07.26 um 16:53:</p>" +
                "<blockquote>Original.</blockquote>",
        )

        assertBeforeFold(html, "Danke!")
        assertInsideFold(html, "schrieb am 13.07.26")
    }

    @Test
    fun keepsOrdinaryProseAheadOfTheQuoteVisible() {
        val html = folded(
            "<p>My reply.</p>" +
                "<p>Here is some context I want you to read first.</p>" +
                "<blockquote>Original message.</blockquote>",
        )

        assertBeforeFold(html, "Here is some context")
        assertInsideFold(html, "Original message.")
    }

    @Test
    fun doesNotWalkPastProseToFindAnEarlierIntroducer() {
        val html = folded(
            "<p>On Mon, 13 Jul 2026, Alice &lt;a@x.test&gt; wrote:</p>\n" +
                "<p>Here is some unrelated context.</p>\n" +
                "<blockquote>Original message.</blockquote>\n",
        )

        assertBeforeFold(html, "Here is some unrelated context.")
        assertBeforeFold(html, "wrote:")
    }

    @Test
    fun foldsATrailingSignatureWithTheQuote() {
        val html = folded(
            "<p>My reply.</p><blockquote>Original.</blockquote>" +
                "<p></p><p></p><p>-- </p><p>Alice</p><p>Vorstand VzEkC e.V.</p>",
        )

        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "Vorstand VzEkC e.V.")
    }

    @Test
    fun keepsTextAfterTheQuoteFromFolding() {
        val html = folded(
            "<blockquote>Original.</blockquote><p>One more thing I forgot to mention.</p>",
        )

        assertFalse(html.contains("<details"), "text after the quote folded")
    }

    @Test
    fun liftsFreshTextOutOfTheQuoteElementItWasWrittenInto() {
        val html = folded(
            "<blockquote class=\"gmail_quote\">" +
                "<div>Sounds good, let us proceed on Monday.</div>" +
                "<div class=\"gmail_attr\">On Mon, Aug 31, 2026 at 6:48 PM Alice wrote:<br></div>" +
                "<blockquote class=\"gmail_quote\"><div>Original quoted text.</div></blockquote>" +
                "</blockquote>",
        )

        assertBeforeFold(html, "Sounds good, let us proceed on Monday.")
        assertInsideFold(html, "Original quoted text.")
    }

    @Test
    fun foldsAPlainQuoteWholeWhenItCarriesNoMarkerChild() {
        val html = folded(
            "<p>My reply.</p>" +
                "<blockquote><div>Line one of the original.</div>" +
                "<div>Line two of the original.</div></blockquote>",
        )

        assertInsideFold(html, "Line one of the original.")
        assertInsideFold(html, "Line two of the original.")
    }

    @Test
    fun foldsTheThunderbirdAttributionAndQuoteBundledInOneDiv() {
        val html = folded(
            "<p>Hallo Herr Mustermann,</p>" +
                "<p>Mit freundlichen Gruessen<br>Max Mustermann</p>" +
                "<div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></div>",
        )

        assertBeforeFold(html, "Max Mustermann")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
        assertInsideFold(html, "Original original text.")
    }

    @Test
    fun leavesTheComposersBodyUnfolded() {
        val source = "<p>My reply.</p><blockquote>Original message body.</blockquote>"

        assertEquals(source, HtmlSanitizer.sanitize(source).html)
    }

    @Test
    fun keepsTheRestOfTheDocumentAsItWas() {
        val source = "<p>My reply.</p>\n<blockquote type=\"cite\">Quoted &amp; kept.</blockquote>"
        val html = folded(source)

        assertContains(html, "<blockquote type=\"cite\">Quoted &amp; kept.</blockquote>")
        assertContains(html, "<p>My reply.</p>")
    }

    // ---- the plain-text path ---------------------------------------------

    @Test
    fun foldsAPlainTextCitation() {
        val html = HtmlSanitizer.fromPlainText(
            "My reply.\n\nOn Mon, 13 Jul 2026, Alice wrote:\n> The original line.\n>> Nested deeper.\n",
            collapseQuotes = true,
        )

        assertBeforeFold(html, "My reply.")
        assertInsideFold(html, "On Mon, 13 Jul 2026, Alice wrote:")
        assertInsideFold(html, "&gt; The original line.")
        assertInsideFold(html, "&gt;&gt; Nested deeper.")
    }

    @Test
    fun leavesAPlainTextBottomPostedReplyExpanded() {
        val html = HtmlSanitizer.fromPlainText(
            "On Mon, 13 Jul 2026, Alice wrote:\n> The original line.\n\nMy answer below it.\n",
            collapseQuotes = true,
        )

        assertFalse(html.contains("<details"), "a bottom-posted plain-text reply folded")
        assertContains(html, "My answer below it.")
    }

    @Test
    fun foldsAPlainTextSignatureWithTheCitation() {
        val split = QuotedText.split(
            "My reply.\n\nAm 10.09.26 um 18:21 schrieb Jane Doe:\n> Original.\n-- \nMax\n",
        )

        assertEquals("My reply.", split.head)
        assertContains(split.collapsed, "schrieb Jane Doe")
        assertContains(split.collapsed, "-- ")
        assertContains(split.collapsed, "Max")
    }

    @Test
    fun leavesAPlainTextBodyWithNoCitationAlone() {
        val body = "Just a note.\nNothing quoted here.\n"

        assertEquals(BodySplit(body, ""), QuotedText.split(body))
        assertFalse(
            HtmlSanitizer.fromPlainText(body, collapseQuotes = true).contains("<details"),
            "a body with no citation folded",
        )
    }

    @Test
    fun leavesTheComposersPlainTextUnfolded() {
        val body = "My reply.\n\nOn Mon, Alice wrote:\n> Original.\n"

        assertFalse(
            HtmlSanitizer.fromPlainText(body).contains("<details"),
            "the composer's own wrapping folded the quote",
        )
    }

    private companion object {
        const val DETAILS = "<details class=\"herold-quoted\">"
    }
}
