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
