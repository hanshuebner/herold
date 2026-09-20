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
    fun foldsAGermanNameFirstIntroducerSeparatedFromTheQuoteByWhitespace() {
        val html = folded(
            "<p>Danke fuer die Einladung.</p>\n" +
                "<p>Hans Huebner (Vorstand VzEkC e.V.) schrieb am 13.07.26 um 16:53:</p>\n" +
                "<blockquote>Original.</blockquote>\n",
        )

        assertBeforeFold(html, "Danke fuer die Einladung.")
        assertInsideFold(html, "schrieb am 13.07.26")
    }

    @Test
    fun foldsTheIntroducerAndTheTrailingSignatureTogether() {
        val html = folded(
            "<p>Super, bis dann!</p>" +
                "<p>On Mo., 13. Juli 2026, 16:55, Florian &lt;florian@example.local&gt; wrote:</p>" +
                "<blockquote>Original quoted text.</blockquote>" +
                "<p></p><p>-- </p><p>Alice</p>",
        )

        assertBeforeFold(html, "Super, bis dann!")
        assertInsideFold(html, "wrote:")
        assertInsideFold(html, ">Alice<")
    }

    @Test
    fun keepsOrdinaryProseSeparatedFromTheQuoteByWhitespaceVisible() {
        val html = folded(
            "<p>My reply.</p>\n" +
                "<p>Here is some context I want you to read first.</p>\n" +
                "<blockquote>Original message.</blockquote>\n",
        )

        assertBeforeFold(html, "Here is some context")
        assertInsideFold(html, "Original message.")
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
    fun foldsAThunderbirdCitePrefixDivAndItsBlockquote() {
        val html = folded(
            "<p>Hallo Herr Mustermann,</p>" +
                "<p>vielen Dank fuer Ihr Angebot.</p>" +
                "<p>Mit freundlichen Gruessen<br>Max Mustermann</p>" +
                "<div class=\"moz-cite-prefix\">Am 10.09.26 um 18:21 schrieb Jane Doe:<br></div>" +
                "<blockquote type=\"cite\" cite=\"mid:xxxx@xxx\">" +
                "<div class=\"moz-cite-prefix\">Am 09.09.26 um 12:00 schrieb John Doe:<br></div>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></blockquote>",
        )

        assertBeforeFold(html, "Max Mustermann")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
        assertInsideFold(html, "Original original text.")
    }

    @Test
    fun foldsAMozForwardContainersAttributionAndQuote() {
        val html = folded(
            "<p>Hallo Herr Mustermann,</p>" +
                "<p>vielen Dank fuer Ihr Angebot.</p>" +
                "<div class=\"moz-forward-container\">" +
                "<div class=\"moz-cite-prefix\">Am 10.09.26 um 18:21 schrieb Jane Doe:<br></div>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></div>",
        )

        assertBeforeFold(html, "vielen Dank")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
    }

    @Test
    fun liftsFreshTextOutOfAQuoteThatBundlesTheAttributionAndQuoteInADiv() {
        val html = folded(
            "<blockquote type=\"cite\">" +
                "<p>Hallo Herr Mustermann,</p>" +
                "<p>vielen Dank fuer Ihr Angebot.</p>" +
                "<p>Mit freundlichen Gruessen<br>Max Mustermann</p>" +
                "<div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></div></blockquote>",
        )

        assertBeforeFold(html, "Hallo Herr Mustermann,")
        assertBeforeFold(html, "Max Mustermann")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
        assertInsideFold(html, "Original original text.")
    }

    @Test
    fun liftsFreshTextOutOfACitePrefixDivThatBundlesTheAttributionAndQuote() {
        val html = folded(
            "<div class=\"moz-cite-prefix\">" +
                "<p>Hallo Herr Mustermann,</p>" +
                "<p>vielen Dank fuer Ihr Angebot.</p>" +
                "<div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></div></div>",
        )

        assertBeforeFold(html, "vielen Dank")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
    }

    @Test
    fun doesNotMistakeAFreshWrapperDivForAQuoteWrapper() {
        val html = folded(
            "<blockquote type=\"cite\">" +
                "<div>Just an ordinary fresh paragraph, no attribution here.</div>" +
                "<div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
                "<blockquote type=\"cite\">Original.</blockquote></div></blockquote>",
        )

        assertBeforeFold(html, "Just an ordinary fresh paragraph")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
    }

    @Test
    fun keepsASignatureAheadOfTheAttributionVisible() {
        val html = folded(
            "<blockquote type=\"cite\">" +
                "<p>Hallo Herr Mustermann,</p>" +
                "<p>-- </p><p>Max Mustermann</p><p>Musterstrasse 1, 12345 Musterstadt</p>" +
                "<div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
                "<blockquote type=\"cite\">Original.</blockquote></div></blockquote>",
        )

        assertBeforeFold(html, "Musterstrasse 1")
        assertInsideFold(html, "Am 10.09.26 um 18:21 schrieb Jane Doe")
    }

    @Test
    fun leavesSeparatorOnlyLeadingChildrenInsideTheQuote() {
        val html = folded(
            "<blockquote class=\"gmail_quote\"><br>" +
                "<div class=\"gmail_attr\">On Mon, Alice wrote:</div>" +
                "<div>Original quoted text.</div></blockquote>",
        )

        assertInsideFold(html, "Original quoted text.")
        assertInsideFold(html, "On Mon, Alice wrote:")
    }

    // ---- an attribution written across several nodes (issue #432) --------

    @Test
    fun foldsOnlyTheCitationWhenThunderbirdWritesTheReplyIntoTheCitePrefixDiv() {
        // Thunderbird puts a reply composed above the citation into the
        // same div as the attribution line, and writes that line over a
        // text node, a mailto: link and the colon after it.
        val html = folded(
            "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br><br>" +
                "Am 20.09.26 um 14:12 schrieb " +
                "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:jane@example.test\">" +
                "jane@example.test</a>:<br></div>" +
                "<blockquote type=\"cite\" cite=\"mid:abc@example.test\">Original quoted text.</blockquote>",
        )

        assertBeforeFold(html, "Hallo Jane,")
        assertBeforeFold(html, "das passt mir gut.")
        assertInsideFold(html, "Am 20.09.26 um 14:12 schrieb")
        assertInsideFold(html, "Original quoted text.")
    }

    @Test
    fun keepsTheReplyVisibleWhenTheCitePrefixDivCarriesNoRecognisedAttribution() {
        // The fold starts at the citation even when the attribution is
        // in a shape the line heuristic does not know: what the sender
        // wrote stays on screen and the quote alone folds.
        val html = folded(
            "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br><br>" +
                "20.09.2026 14:12 &gt;&gt; jane@example.test<br></div>" +
                "<blockquote type=\"cite\">Original quoted text.</blockquote>",
        )

        assertBeforeFold(html, "das passt mir gut.")
        assertInsideFold(html, "Original quoted text.")
    }

    @Test
    fun keepsAReplyThatOpensWithTheAttributionsOwnWordsVisible() {
        // "Am ..." opens both the reply and the attribution line; the
        // fold starts at the last of the two, not the first.
        val html = folded(
            "<div class=\"moz-cite-prefix\">Am Montag passt es mir gut, danke.<br><br>" +
                "Am 20.09.26 um 14:12 schrieb " +
                "<a href=\"mailto:jane@example.test\">jane@example.test</a>:<br></div>" +
                "<blockquote type=\"cite\">Original quoted text.</blockquote>",
        )

        assertBeforeFold(html, "Am Montag passt es mir gut, danke.")
        assertInsideFold(html, "Original quoted text.")
    }

    @Test
    fun foldsAQuoteWholeWhenItsOwnHeadIsTheAttribution() {
        // The element leads with its attribution, so nothing of the
        // sender's precedes it and the whole element folds - including
        // when a deeper quote ends on an attribution line of its own,
        // which a second reading of the text would cut the element
        // open at.
        val html = folded(
            "<div class=\"gmail_quote\">" +
                "<div class=\"gmail_attr\">On Mon, 15 Sep 2026, Alice wrote:<br></div>" +
                "<blockquote class=\"gmail_quote\">Quoted line.</blockquote>" +
                "<div class=\"moz-cite-prefix\">Am 14.09.26 um 09:00 schrieb bob@example.test:</div>" +
                "</div>",
        )

        assertInsideFold(html, "On Mon, 15 Sep 2026, Alice wrote:")
        assertInsideFold(html, "Quoted line.")
        assertInsideFold(html, "Am 14.09.26 um 09:00 schrieb bob@example.test:")
    }

    @Test
    fun keepsTheSecondRegionsLeadingTextInsideTheFold() {
        // The fold is passed over the citation-prefix div, whose
        // attribution the heuristic does not know, and begins at the
        // blockquote after it. What that blockquote leads with is the
        // correspondent's own reply from the message being quoted, not
        // the sender's: it belongs behind the chip.
        val html = folded(
            "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>Danke fuer die Nachricht.<br><br>" +
                "20.09.2026 14:12 &gt;&gt; jane@example.test<br></div>" +
                "<blockquote type=\"cite\">" +
                "<p>Was written above the quote in the message being answered.</p>" +
                "<div class=\"moz-cite-prefix\">Am 19.09.26 um 09:00 schrieb john@example.test:<br></div>" +
                "<blockquote type=\"cite\">The oldest message.</blockquote></blockquote>",
        )

        assertBeforeFold(html, "Danke fuer die Nachricht.")
        assertInsideFold(html, "Was written above the quote in the message being answered.")
        assertInsideFold(html, "The oldest message.")
    }

    @Test
    fun keepsTheSecondRegionsLeadingTextInsideTheFoldWithASplitAttribution() {
        // The same pass-over, with the quoted message's own attribution
        // written over several nodes: reading it as one line must not
        // turn the line above it into the sender's text.
        val html = folded(
            "<div class=\"moz-cite-prefix\">Reply text in a shape the heuristic does not know.<br></div>" +
                "<blockquote type=\"cite\">Quoted line one.<br>" +
                "Am 19.09.26 um 09:00 schrieb " +
                "<a href=\"mailto:john@example.test\">john@example.test</a>:<br>" +
                "<blockquote type=\"cite\">The oldest message.</blockquote></blockquote>",
        )

        assertBeforeFold(html, "Reply text in a shape the heuristic does not know.")
        assertInsideFold(html, "Quoted line one.")
        assertInsideFold(html, "The oldest message.")
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
    fun splitsAPlainTextBodyAtItsEnglishAttributionLine() {
        val body = "Thanks for the doc.\n\n" +
            "On Mon, Apr 28, 2026 at 9:01 AM, Alice <a@x.test> wrote:\n" +
            "> First, the goals.\n> Second, the approach."
        val split = QuotedText.split(body)

        assertEquals("Thanks for the doc.", split.head)
        assertContains(split.collapsed, "On Mon, Apr 28, 2026")
        assertContains(split.collapsed, "> First, the goals.")
        assertEquals(
            "Thanks for the doc.\n" +
                "On Mon, Apr 28, 2026 at 9:01 AM, Alice <a@x.test> wrote:\n" +
                "> First, the goals.\n> Second, the approach.",
            split.head + "\n" + split.collapsed,
        )
    }

    @Test
    fun splitsAPlainTextBodyAtABareQuotePrefix() {
        val split = QuotedText.split("See my reply inline.\n\n> The original\n> said this.")

        assertEquals("See my reply inline.", split.head)
        assertEquals("> The original\n> said this.", split.collapsed)
    }

    @Test
    fun collapsesADeeplyNestedPlainTextQuotePrefix() {
        val split = QuotedText.split("Reply.\n\n>> earlier\n>> nested")

        assertEquals("Reply.", split.head)
        assertContains(split.collapsed, ">> earlier")
    }

    @Test
    fun collapsesAPlainTextQuoteWithBlankLinesWithinTheRun() {
        val split = QuotedText.split(
            "My text.\n\n> Quoted paragraph 1.\n\n> Quoted paragraph 2.",
        )

        assertEquals("My text.", split.head)
        assertEquals("> Quoted paragraph 1.\n\n> Quoted paragraph 2.", split.collapsed)
    }

    @Test
    fun splitsAPlainTextBodyAtAGermanNameFirstAttributionLine() {
        val split = QuotedText.split(
            "Danke fuer die Einladung.\n\n" +
                "Hans Huebner (Vorstand VzEkC e.V.) schrieb am 13.07.26 um 16:53:\n" +
                "> Liebe Mitglieder,\n> die naechste Versammlung ist am 20. Juli.",
        )

        assertEquals("Danke fuer die Einladung.", split.head)
        assertContains(split.collapsed, "schrieb am 13.07.26 um 16:53:")
        assertContains(split.collapsed, "> Liebe Mitglieder,")
    }

    @Test
    fun keepsAnOrdinaryGermanSentenceMentioningSchriebAmVisible() {
        val split = QuotedText.split(
            "Ich erinnere mich noch, wie er schrieb am Wochenende um die Ecke zu fahren.\n\n" +
                "> Older quoted history.",
        )

        assertContains(split.head, "wie er schrieb am Wochenende um die Ecke zu fahren.")
        assertEquals("> Older quoted history.", split.collapsed)
    }

    @Test
    fun collapsesNothingWhenPlainTextFollowsTheQuotedRun() {
        val body = "Am 2026-07-04 17:07, schrieb Hans Huebner:\n" +
            "> es geht, finally!\n> ...\n>> hjgjh\n\ndas ist noch nicht alles"

        assertEquals(BodySplit(body, ""), QuotedText.split(body))
    }

    @Test
    fun acceptsASignatureDelimiterWithoutATrailingSpace() {
        val split = QuotedText.split("Hello.\n\n> Quoted.\n\n--\nSig.")

        assertContains(split.collapsed, "> Quoted.")
        assertContains(split.collapsed, "--\nSig.")
        assertFalse(split.head.contains("Sig."), "the signature stayed visible")
    }

    @Test
    fun doesNotFoldASignatureWhenRealContentSitsBetweenItAndTheQuote() {
        val body = "> quoted\n\nsome final remark\n\n-- \nSig"

        assertEquals(BodySplit(body, ""), QuotedText.split(body))
    }

    @Test
    fun leavesASignatureWithNoCitationVisible() {
        val body = "Just a note.\n\n-- \nSig only, no quote."

        assertEquals(BodySplit(body, ""), QuotedText.split(body))
    }

    @Test
    fun returnsAnEmptySplitForAnEmptyBody() {
        assertEquals(BodySplit("", ""), QuotedText.split(""))
    }

    @Test
    fun doesNotTreatASigdashAsAQuoteBoundary() {
        val body = "Thanks!\n--\nAlice"

        assertEquals(BodySplit(body, ""), QuotedText.split(body))
    }

    @Test
    fun doesNotSplitOnAGreaterThanInsideASentence() {
        assertEquals("", QuotedText.split("I think a > b in this case.").collapsed)
    }

    @Test
    fun collapsesTheWholePlainTextBodyWhenItIsNothingButACitation() {
        val split = QuotedText.split("On Mon, Alice wrote:\n> Quoted only.")

        assertEquals("", split.head)
        assertContains(split.collapsed, "On Mon, Alice wrote:")
        assertContains(split.collapsed, "> Quoted only.")
    }

    @Test
    fun collapsesOnlyTheLastTrailingPlainTextCitation() {
        val split = QuotedText.split(
            "My reply to paragraph one.\n\n> Original paragraph one.\n\n" +
                "My reply to paragraph two.\n\n> Original paragraph two.",
        )

        assertEquals("> Original paragraph two.", split.collapsed)
        assertContains(split.head, "> Original paragraph one.")
        assertContains(split.head, "My reply to paragraph two.")
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
