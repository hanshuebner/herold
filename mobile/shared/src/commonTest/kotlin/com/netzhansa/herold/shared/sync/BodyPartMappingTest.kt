package com.netzhansa.herold.shared.sync

import com.netzhansa.herold.shared.jmap.WireBodyPart
import com.netzhansa.herold.shared.jmap.WireBodyValue
import com.netzhansa.herold.shared.jmap.WireEmail
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/**
 * Which parts of a message become its two bodies (issue #430).
 *
 * `textBody` names the HTML part itself when a message carries no
 * text/plain (RFC 8621 4.1.4), so the mapping has to tell a real text
 * alternative from that fallback.
 */
class BodyPartMappingTest {

    @Test
    fun aMultipartAlternativeCarriesBothBodies() {
        val email = wire(
            html = WireBodyPart(partId = "2", type = "text/html"),
            text = WireBodyPart(partId = "1", type = "text/plain"),
            values = mapOf(
                "1" to WireBodyValue("The message as text."),
                "2" to WireBodyValue("<p>The message as HTML.</p>"),
            ),
        ).toStoreRow("a")

        assertEquals("<p>The message as HTML.</p>", email.bodyHtml)
        assertEquals("The message as text.", email.bodyText)
    }

    @Test
    fun anHtmlOnlyMessageHasNoTextAlternative() {
        val part = WireBodyPart(partId = "1", type = "text/html")
        val email = wire(
            html = part,
            text = part,
            values = mapOf("1" to WireBodyValue("<p>Only HTML.</p>")),
        ).toStoreRow("a")

        assertEquals("<p>Only HTML.</p>", email.bodyHtml)
        assertNull(email.bodyText)
    }

    @Test
    fun aTextOnlyMessageKeepsItsText() {
        val part = WireBodyPart(partId = "1", type = "text/plain")
        val email = wire(
            html = null,
            text = part,
            values = mapOf("1" to WireBodyValue("Only text.")),
        ).toStoreRow("a")

        assertNull(email.bodyHtml)
        assertEquals("Only text.", email.bodyText)
    }

    /**
     * The server names a text-only message's single part in BOTH body
     * lists (RFC 8621 4.1.4 symmetric fill, issue #258), so the HTML
     * side has to recognise that the part it was handed is the text one
     * (issue #458).
     */
    @Test
    fun aTextOnlyMessageNamedInBothListsHasNoHtmlBody() {
        val part = WireBodyPart(partId = "1", type = "text/plain")
        val body = "First line.\n\nSecond paragraph.\n> a quoted line\n"
        val email = wire(
            html = part,
            text = part,
            values = mapOf("1" to WireBodyValue(body)),
        ).toStoreRow("a")

        assertNull(email.bodyHtml)
        assertEquals(body, email.bodyText)
    }

    /**
     * A part of neither text type, named in both lists, reads as text:
     * nothing declared its content markup, and rendering it as HTML
     * would lose its line structure and parse whatever angle brackets
     * it happens to carry.
     */
    @Test
    fun aPartOfNeitherTextTypeReadsAsText() {
        val part = WireBodyPart(partId = "1", type = "text/markdown")
        val email = wire(
            html = part,
            text = part,
            values = mapOf("1" to WireBodyValue("# A heading\n\nA paragraph.\n")),
        ).toStoreRow("a")

        assertNull(email.bodyHtml)
        assertEquals("# A heading\n\nA paragraph.\n", email.bodyText)
    }

    private fun wire(
        html: WireBodyPart?,
        text: WireBodyPart?,
        values: Map<String, WireBodyValue>,
    ) = WireEmail(
        id = "e1",
        threadId = "t1",
        htmlBody = html?.let { listOf(it) },
        textBody = text?.let { listOf(it) },
        bodyValues = values,
    )
}
