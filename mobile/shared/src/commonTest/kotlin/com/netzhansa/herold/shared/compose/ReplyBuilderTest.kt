package com.netzhansa.herold.shared.compose

import com.netzhansa.herold.shared.domain.Attachment
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.domain.MailAddress
import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * The reply derivations, which decide who a reply reaches and how it
 * threads. They mirror the suite's rules
 * (`web/apps/suite/src/lib/compose/compose.svelte.ts`), so the cases here
 * are the suite's cases.
 */
class ReplyBuilderTest {

    private val self = setOf("alice@example.local", "vorsitz@classic-computing.example")

    private fun message(
        from: MailAddress = MailAddress("Bob", "bob@example.local"),
        to: List<MailAddress> = listOf(MailAddress(null, "alice@example.local")),
        cc: List<MailAddress> = emptyList(),
        deliveredTo: String? = "alice@example.local",
        subject: String = "Quarterly report",
        messageId: List<String> = listOf("parent@example.local"),
        references: List<String> = emptyList(),
    ) = Email(
        accountId = "a2",
        id = "1",
        threadId = "t1",
        fromName = from.name.orEmpty(),
        fromEmail = from.email,
        toAddresses = to,
        ccAddresses = cc,
        deliveredTo = deliveredTo,
        subject = subject,
        messageId = messageId,
        references = references,
        bodyText = "The original body.",
    )

    /** The same parent, with an HTML body as a formatted mail carries one. */
    private fun htmlMessage(html: String) = message().copy(bodyHtml = html)

    @Test
    fun replyGoesToTheSender() {
        val parent = message()
        assertEquals(listOf(MailAddress("Bob", "bob@example.local")), ReplyBuilder.replyTo(parent, self))
    }

    @Test
    fun replyToAMessageTheUserSentGoesToItsRecipients() {
        val parent = message(
            from = MailAddress("Alice", "alice@example.local"),
            to = listOf(MailAddress("Bob", "bob@example.local")),
            deliveredTo = null,
        )
        assertTrue(ReplyBuilder.isOwnMessage(parent, self))
        assertEquals(listOf(MailAddress("Bob", "bob@example.local")), ReplyBuilder.replyTo(parent, self))
    }

    @Test
    fun aDeliveredMessageFromOneOfTheUsersAddressesIsNotOwnSent() {
        // herold stamps X-Herold-Recipient on everything it delivers, so a
        // message whose From happens to be one of the user's addresses but
        // which arrived through delivery replies to the sender.
        val parent = message(from = MailAddress("Alice", "alice@example.local"))
        assertFalse(ReplyBuilder.isOwnMessage(parent, self))
        assertEquals(listOf(MailAddress("Alice", "alice@example.local")), ReplyBuilder.replyTo(parent, self))
    }

    @Test
    fun replyAllCarriesTheOtherRecipientsAndDropsTheUserAndTheSender() {
        val parent = message(
            to = listOf(
                MailAddress(null, "alice@example.local"),
                MailAddress("Carol", "carol@example.com"),
            ),
            cc = listOf(
                MailAddress("Dan", "dan@example.com"),
                MailAddress("Bob", "bob@example.local"),
                MailAddress("Carol", "carol@example.com"),
            ),
        )
        assertEquals(
            listOf(MailAddress("Carol", "carol@example.com"), MailAddress("Dan", "dan@example.com")),
            ReplyBuilder.replyAllCc(parent, self),
        )
    }

    @Test
    fun replyAllOnAnOwnMessageKeepsOnlyItsCc() {
        val parent = message(
            from = MailAddress("Alice", "alice@example.local"),
            to = listOf(MailAddress("Bob", "bob@example.local")),
            cc = listOf(MailAddress("Carol", "carol@example.com"), MailAddress(null, "alice@example.local")),
            deliveredTo = null,
        )
        assertEquals(listOf(MailAddress("Carol", "carol@example.com")), ReplyBuilder.replyAllCc(parent, self))
    }

    @Test
    fun subjectMarkersCollapseIntoOne() {
        assertEquals("Re: Quarterly report", ReplyBuilder.replySubject("Quarterly report"))
        assertEquals("Re: Quarterly report", ReplyBuilder.replySubject("Re: Quarterly report"))
        assertEquals("Re: Quarterly report", ReplyBuilder.replySubject("AW: Re: Quarterly report"))
        assertEquals("Fwd: Quarterly report", ReplyBuilder.forwardSubject("WG: Fwd: Quarterly report"))
        // A subject that merely contains a colon keeps its first word.
        assertEquals("Re: Bugfix: the parser", ReplyBuilder.replySubject("Bugfix: the parser"))
    }

    @Test
    fun referencesAppendTheParentsMessageId() {
        val parent = message(messageId = listOf("parent@example.local"), references = listOf("root@example.local"))
        assertEquals(listOf("root@example.local", "parent@example.local"), ReplyBuilder.references(parent))
        assertEquals(listOf("parent@example.local"), ReplyBuilder.inReplyTo(parent))
    }

    @Test
    fun theQuoteCarriesTheAttributionAndTheOriginalBody() {
        val quote = ReplyBuilder.replyQuote(message(), "Fri, 11 Sep 2026, 22:30")
        assertContains(quote, "On Fri, 11 Sep 2026, 22:30, Bob &lt;bob@example.local&gt; wrote:")
        assertContains(quote, "<blockquote><p>The original body.</p></blockquote>")
    }

    @Test
    fun theForwardQuoteCarriesTheOriginalHeaders() {
        val quote = ReplyBuilder.forwardQuote(message(), "Fri, 11 Sep 2026, 22:30")
        assertContains(quote, "---------- Forwarded message ----------")
        assertContains(quote, "Subject: Quarterly report")
        assertContains(quote, "To: alice@example.local")
    }

    /**
     * The HTML original is quoted as markup (issue #431): a forward of a
     * formatted message carries its layout, not a text rendering of it.
     */
    @Test
    fun anHtmlParentIsQuotedAsMarkup() {
        val parent = htmlMessage(
            "<html><body><h1>Release notes</h1>" +
                "<table><tr><td><strong>Build</strong></td><td>2026.9</td></tr></table>" +
                "</body></html>",
        )
        listOf(
            ReplyBuilder.replyQuote(parent, "Fri, 11 Sep 2026, 22:30"),
            ReplyBuilder.forwardQuote(parent, "Fri, 11 Sep 2026, 22:30"),
        ).forEach { quote ->
            assertContains(quote, "<h1>Release notes</h1>")
            assertContains(quote, "<strong>Build</strong>")
            assertContains(quote, "<table>")
            // The text rendering the quote used to be is gone from the
            // body; the composer derives it for the text alternative.
            assertFalse(quote.contains("<p>Release notes</p>"), "the quote is markup, not a re-wrapped rendering")
            // The document's own wrappers do not nest inside a blockquote.
            assertFalse(quote.contains("<html", ignoreCase = true))
            assertFalse(quote.contains("<body", ignoreCase = true))
        }
    }

    /** A parent with text and no HTML keeps the paragraph form it always had. */
    @Test
    fun aTextOnlyParentIsStillQuotedAsParagraphs() {
        val parent = message().copy(bodyText = "First line.\n\nSecond line.")
        val quote = ReplyBuilder.replyQuote(parent, null)
        assertContains(quote, "<blockquote><p>First line.</p><p>Second line.</p></blockquote>")
        assertContains(ReplyBuilder.forwardQuote(parent, null), "<p>Second line.</p>")
    }

    /** A parent whose body never reached the device quotes the preview, then nothing. */
    @Test
    fun aParentWithNeitherBodyKeepsTheFallback() {
        val previewOnly = message().copy(bodyText = null, bodyHtml = null, preview = "Only the preview.")
        assertContains(ReplyBuilder.replyQuote(previewOnly, null), "<p>Only the preview.</p>")

        val empty = message().copy(bodyText = null, bodyHtml = null, preview = "")
        assertContains(ReplyBuilder.replyQuote(empty, null), "<blockquote><p>(no quoted body)</p></blockquote>")
        assertContains(ReplyBuilder.forwardQuote(empty, null), "<blockquote><p>(no quoted body)</p></blockquote>")
    }

    /** An HTML body with nothing in it falls through to the text the parent has. */
    @Test
    fun anEmptyHtmlBodyFallsBackToTheText() {
        val parent = message().copy(bodyHtml = "<html><head><style>p { color: red }</style></head><body></body></html>")
        assertContains(ReplyBuilder.replyQuote(parent, null), "<p>The original body.</p>")
    }

    /**
     * What the parent's markup may not smuggle into the outgoing message:
     * a script, a style rule, an event handler or a `javascript:` target.
     */
    @Test
    fun theQuotedMarkupCarriesNothingActive() {
        val parent = htmlMessage(
            "<html><head><style>body { display: none }</style></head><body>" +
                "<script>alert(1)</script>" +
                "<p onclick=\"steal()\">Text</p>" +
                "<a href=\"javascript:steal()\">link</a>" +
                "<img src=\"https://tracker.example/pixel.gif\">" +
                "</body></html>",
        )
        val quote = ReplyBuilder.forwardQuote(parent, null)
        assertFalse(quote.contains("script", ignoreCase = true), "no script reaches the quote: $quote")
        assertFalse(quote.contains("<style", ignoreCase = true), "no style block reaches the quote: $quote")
        assertFalse(quote.contains("onclick", ignoreCase = true), "no event handler reaches the quote: $quote")
        assertFalse(quote.contains("javascript:", ignoreCase = true), "no javascript: target reaches the quote")
        assertContains(quote, "<p>Text</p>")
        // The remote image keeps the URL the original carried; holding it
        // back is the renderer's job (HtmlSanitizer.sanitize).
        assertContains(quote, "https://tracker.example/pixel.gif")
    }

    /**
     * An inline part the quoted body points at is carried as an inline
     * entry on the parent's own blob, so the `cid:` in the quote names a
     * part the outgoing message holds.
     */
    @Test
    fun theQuotedInlinePartsAreCarriedOnTheParentsBlobs() {
        val parent = htmlMessage("<p>Logo: <img src=\"cid:logo@x\"></p>").copy(
            attachments = listOf(
                Attachment("blob-logo", "logo.png", "image/png", 256, cid = "<logo@x>", isInline = true),
                Attachment("blob-unused", "spare.png", "image/png", 128, cid = "<spare@x>", isInline = true),
                Attachment("blob-a", "report.pdf", "application/pdf", 1024),
            ),
        )
        val quote = ReplyBuilder.forwardQuote(parent, null)
        assertContains(quote, "src=\"cid:logo@x\"")

        val carried = ReplyBuilder.quotedInlineAttachments(parent, quote)
        assertEquals(1, carried.size, "only the part the quote references is carried")
        assertEquals("blob-logo", carried[0].blobId)
        assertEquals("logo@x", carried[0].cid, "the part's cid matches the reference in the body")
        assertTrue(carried[0].inline)
        assertTrue(carried[0].isReady)

        // A reference the parent has no part for carries nothing.
        val dangling = htmlMessage("<p><img src=\"cid:missing@x\"></p>").copy(attachments = parent.attachments)
        assertEquals(
            emptyList(),
            ReplyBuilder.quotedInlineAttachments(dangling, ReplyBuilder.forwardQuote(dangling, null)),
        )
    }

    @Test
    fun forwardCarriesTheParentsFilesAndLeavesItsInlineImages() {
        val parent = message().copy(
            attachments = listOf(
                Attachment("blob-a", "report.pdf", "application/pdf", 1024),
                Attachment("blob-b", "logo.png", "image/png", 256, cid = "logo@x", isInline = true),
            ),
        )
        val carried = ReplyBuilder.forwardAttachments(parent)
        assertEquals(1, carried.size)
        assertEquals("report.pdf", carried[0].name)
        assertEquals("blob-a", carried[0].blobId)
        assertTrue(carried[0].isReady)
    }
}
