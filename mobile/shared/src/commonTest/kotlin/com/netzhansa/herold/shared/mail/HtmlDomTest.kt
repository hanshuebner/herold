package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The tree the quoted-history fold walks (issue #432). A body it does
 * not change comes back as it went in, whatever shape it arrived in,
 * so the fold can be attempted on any mail.
 */
class HtmlDomTest {

    private fun roundTrip(html: String) =
        assertEquals(html, HtmlDom.render(HtmlDom.parse(html)), "the tree did not render its source")

    @Test
    fun rendersWellFormedMarkupUnchanged() {
        roundTrip("<p>One</p>\n<div class=\"x\"><blockquote>Two &amp; three</blockquote></div>")
    }

    @Test
    fun rendersUnclosedAndStrayTagsUnchanged() {
        roundTrip("<p>One<p>Two<div>Three</b></div>")
        roundTrip("<ul><li>One<li>Two</ul>")
        roundTrip("<table><tr><td>One<td>Two</table>")
    }

    @Test
    fun rendersCommentsDoctypesAndAttributeAngleBracketsUnchanged() {
        roundTrip("<!-- a note --><p title=\"a > b\">One</p>")
        roundTrip("<!DOCTYPE html><html><body><p>One</p></body></html>")
        roundTrip("<style>p { color: red }</style><p>One</p>")
        roundTrip("<p>5 < 6 and 7 > 6</p>")
    }

    @Test
    fun readsAnElementsClassAndText() {
        val root = HtmlDom.parse("<div class=\"gmail_quote\">Am 1.1.26 <b>schrieb</b> Jane:</div>")
        val div = root.children.filterIsInstance<HtmlElement>().single()

        assertEquals("gmail_quote", div.classes)
        assertEquals("Am 1.1.26 schrieb Jane:", div.text())
    }

    @Test
    fun decodesTheEntitiesAnAttributionLineCarries() {
        val root = HtmlDom.parse("<p>On Mon, Alice &lt;a@x.test&gt; wrote:</p>")

        assertEquals("On Mon, Alice <a@x.test> wrote:", root.children.single().text())
    }

    @Test
    fun keepsAStyleElementsContentOutOfTheText() {
        val root = HtmlDom.parse("<div><style>p { color: red }</style>Hello</div>")

        assertTrue(
            root.children.single().text().trim() == "Hello",
            "the stylesheet reached the element's text",
        )
    }
}
