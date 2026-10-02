package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.jmap.WireBodyPart
import com.netzhansa.herold.shared.jmap.WireBodyValue
import com.netzhansa.herold.shared.jmap.WireEmail
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * Which parts the reading pane lists as attachments (issue #503).
 *
 * A part belongs to the body when its disposition says `inline`, or when
 * the HTML draws it as `cid:`. A Content-ID on its own says nothing:
 * Gmail stamps one on every attachment it sends, and a reader that reads
 * that as "the body holds this" loses the file altogether.
 */
class AttachmentMappingTest {

    @Test
    fun anAttachedPartCarryingAContentIdIsNotInline() {
        val email = wire(
            html = "<html><body><p>The file is attached.</p></body></html>",
            part = WireBodyPart(
                partId = "2",
                blobId = "blob-pdf",
                type = "application/pdf",
                name = "invoice.pdf",
                disposition = "attachment",
                cid = "f_munsrmn90",
            ),
        ).toStoreRow("a")

        val attachment = email.attachments.single()
        assertEquals("invoice.pdf", attachment.name)
        assertFalse(attachment.isInline, "an attached file is listed whatever Content-ID it carries")
    }

    @Test
    fun anInlinePartCarryingAContentIdIsInline() {
        val email = wire(
            html = "<html><body><p>Hello.</p></body></html>",
            part = WireBodyPart(
                partId = "2",
                blobId = "blob-png",
                type = "image/png",
                name = "dot.png",
                disposition = "inline",
                cid = "logo@example",
            ),
        ).toStoreRow("a")

        assertTrue(email.attachments.single().isInline)
    }

    @Test
    fun anAttachedPartTheBodyDrawsIsInline() {
        val email = wire(
            html = "<html><body><p><img src=\"cid:logo@example\"></p></body></html>",
            part = WireBodyPart(
                partId = "2",
                blobId = "blob-png",
                type = "image/png",
                name = "logo.png",
                disposition = "attachment",
                cid = "<logo@example>",
            ),
        ).toStoreRow("a")

        assertTrue(email.attachments.single().isInline, "the body draws this part, so it is not listed")
    }

    /**
     * A part with no disposition at all and a Content-ID nothing names is
     * a file the sender attached; the reader is shown it.
     */
    @Test
    fun aPartWithNoDispositionTheBodyIgnoresIsNotInline() {
        val email = wire(
            html = "<html><body><p>Nothing drawn here.</p></body></html>",
            part = WireBodyPart(
                partId = "2",
                blobId = "blob-png",
                type = "image/png",
                name = "spare.png",
                cid = "spare@example",
            ),
        ).toStoreRow("a")

        assertFalse(email.attachments.single().isInline)
    }

    /** A message read without its body still lists what it carries. */
    @Test
    fun aPartOfAMessageWithNoHtmlBodyIsNotInline() {
        val email = WireEmail(
            id = "e1",
            threadId = "t1",
            attachments = listOf(
                WireBodyPart(
                    partId = "2",
                    blobId = "blob-pdf",
                    type = "application/pdf",
                    name = "invoice.pdf",
                    disposition = "attachment",
                    cid = "f_munsrmn90",
                ),
            ),
        ).toStoreRow("a")

        assertFalse(email.attachments.single().isInline)
    }

    private fun wire(html: String, part: WireBodyPart) = WireEmail(
        id = "e1",
        threadId = "t1",
        htmlBody = listOf(WireBodyPart(partId = "1", type = "text/html")),
        textBody = listOf(WireBodyPart(partId = "0", type = "text/plain")),
        bodyValues = mapOf(
            "0" to WireBodyValue("The message as text."),
            "1" to WireBodyValue(html),
        ),
        attachments = listOf(part),
    )
}
