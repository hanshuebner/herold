package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.jmap.WireBodyPart
import com.netzhansa.herold.shared.jmap.WireBodyValue
import com.netzhansa.herold.shared.jmap.WireEmail
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * What the reading pane makes of a message that carries only a
 * `text/plain` part (issue #458).
 *
 * The server names that one part in both `textBody` and `htmlBody` (RFC
 * 8621 4.1.4 symmetric fill), so the pane has to recognise it as text:
 * handed to the HTML path its newlines become ordinary whitespace and
 * the message collapses into one block.
 */
class TextOnlyBodyRenderTest {

    /** The reported shape: paragraphs, a signature and a quoted tail. */
    private val plainBody = buildString {
        append("Dear Hans,\n")
        append("\n")
        append("the first paragraph runs over\n")
        append("two source lines.\n")
        append("\n")
        append("Kind regards\n")
        append("Someone\n")
        append("\n")
        append("On Monday, Hans wrote:\n")
        append("> the quoted original\n")
        append("> over two lines\n")
    }

    @Test
    fun aTextOnlyMessageRendersWithItsLineStructure() {
        val rendered = readingPane(textOnlyMessage(plainBody))

        assertTrue(rendered.contains("white-space:pre-wrap"), "plain text renders preformatted: $rendered")
        assertTrue(rendered.contains("Kind regards\nSomeone"), "the signature keeps its own line: $rendered")
        assertTrue(rendered.contains("\n&gt; the quoted original"), "a quoted line starts a line: $rendered")
    }

    @Test
    fun aTextOnlyMessageBodyIsEscapedNotParsed() {
        val rendered = readingPane(
            textOnlyMessage("Comparing 3 < 4 && 5 > 4.\n<b>not bold</b>\n<script>alert(1)</script>\n"),
        )

        assertTrue(rendered.contains("3 &lt; 4 &amp;&amp; 5 &gt; 4."), "angle brackets are escaped: $rendered")
        assertFalse(rendered.contains("<b>"), "text is not parsed as markup: $rendered")
        assertTrue(rendered.contains("&lt;b&gt;not bold&lt;/b&gt;"), "the reader sees the characters typed: $rendered")
        assertFalse(rendered.contains("<script"), "no script element reaches the document: $rendered")
        assertTrue(
            rendered.contains("&lt;script&gt;alert(1)&lt;/script&gt;"),
            "a script-looking line reads as the text it is: $rendered",
        )
    }

    @Test
    fun aTextOnlyMessageFoldsItsQuotedHistory() {
        val rendered = readingPane(textOnlyMessage(plainBody))

        assertTrue(rendered.contains(QuotedHtml.DETAILS_CLASS), "the quoted tail is folded: $rendered")
    }

    @Test
    fun anHtmlMessageStillRendersAsHtml() {
        val html = WireBodyPart(partId = "2", type = "text/html")
        val text = WireBodyPart(partId = "1", type = "text/plain")
        val email = WireEmail(
            id = "e1",
            threadId = "t1",
            htmlBody = listOf(html),
            textBody = listOf(text),
            bodyValues = mapOf(
                "1" to WireBodyValue("The message as text."),
                "2" to WireBodyValue("<p>The message as <b>HTML</b>.</p>"),
            ),
        ).toStoreRow("a")

        val rendered = readingPane(email)

        assertTrue(rendered.contains("<b>HTML</b>"), "the HTML half renders as markup: $rendered")
        assertFalse(rendered.contains("white-space:pre-wrap"), "it is not the text half: $rendered")
    }

    @Test
    fun anHtmlOnlyMessageStillRendersAsHtml() {
        val part = WireBodyPart(partId = "1", type = "text/html")
        val email = WireEmail(
            id = "e1",
            threadId = "t1",
            htmlBody = listOf(part),
            textBody = listOf(part),
            bodyValues = mapOf("1" to WireBodyValue("<p>Only <i>HTML</i>.</p>")),
        ).toStoreRow("a")

        assertEquals("<p>Only <i>HTML</i>.</p>", email.bodyHtml)
        assertTrue(readingPane(email).contains("<i>HTML</i>"))
    }

    /** A message whose single text/plain part is named in both body lists. */
    private fun textOnlyMessage(body: String): Email {
        val part = WireBodyPart(partId = "1", type = "text/plain")
        return WireEmail(
            id = "e1",
            threadId = "t1",
            htmlBody = listOf(part),
            textBody = listOf(part),
            bodyValues = mapOf("1" to WireBodyValue(body)),
        ).toStoreRow("a")
    }

    /**
     * The body markup the thread view hands the WebView, composed the
     * way `ThreadScreen` composes it.
     */
    private fun readingPane(email: Email, contentWidthCssPx: Int = 360): String {
        val htmlBody = email.bodyHtml?.let {
            HtmlSanitizer.sanitize(
                it,
                loadRemoteImages = false,
                collapseQuotes = true,
                fitToWidthCssPx = contentWidthCssPx,
            )
        }
        val choice = BodyPreference.choose(
            hasHtml = htmlBody != null,
            text = email.bodyText,
            minimumWidthCssPx = htmlBody?.minimumWidthCssPx ?: 0,
            contentWidthCssPx = contentWidthCssPx,
        )
        val body = when (choice.show) {
            BodyVariant.Html -> htmlBody?.html.orEmpty()
            BodyVariant.Text -> HtmlSanitizer.sanitize(
                HtmlSanitizer.fromPlainText(email.bodyText.orEmpty(), collapseQuotes = true),
                loadRemoteImages = false,
            ).html
        }
        return body
    }
}
