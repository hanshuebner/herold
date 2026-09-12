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
